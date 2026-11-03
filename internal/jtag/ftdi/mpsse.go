package ftdi

import (
	"errors"
	"fmt"
	"time"

	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
)

// MPSSE opcodes (AN_108 sections 3.2-3.8). JTAG shifts LSB first, the
// adapter drives TDI and TMS on the falling edge of TCK and samples TDO
// on the rising edge (the TAP samples on the rising edge and changes TDO
// on the falling one, IEEE 1149.1).
const (
	OpBytesOut       = 0x19 // bytes out, -ve edge, LSB first: len-1 (2 bytes), data
	OpBitsOut        = 0x1B // bits out, -ve edge, LSB first: len-1, byte
	OpBytesInOut     = 0x39 // bytes out -ve, in +ve, LSB first
	OpBitsInOut      = 0x3B // bits out -ve, in +ve, LSB first
	OpTMSOut         = 0x4B // TMS bits out, -ve edge: len-1, byte (bit 7 is TDI)
	OpTMSInOut       = 0x6B // TMS bits out -ve, TDO in +ve
	OpSetLow         = 0x80 // value, direction of ADBUS
	OpSetHigh        = 0x82 // value, direction of ACBUS
	OpLoopbackOff    = 0x85
	OpClockDivisor   = 0x86 // low, high
	OpSendImmediate  = 0x87
	OpDiv5Off        = 0x8A // 60 MHz master clock (H series)
	OpDiv5On         = 0x8B
	OpThreePhaseOff  = 0x8D
	OpAdaptiveOff    = 0x97
	BadCommand       = 0xFA // the answer to an invalid opcode, then the opcode
	syncOpcode       = 0xAA // an invalid opcode, for synchronization
	pinTCK           = 1 << 0
	pinTDI           = 1 << 1
	pinTDO           = 1 << 2
	pinTMS           = 1 << 3
	masterClockHz    = 60000000
	maxSegmentOut    = 2048 // well inside the 4 KB buffers of a channel
	maxSegmentIn     = 2048
	maxScanCmdBytes  = 32 // the commands of one scan of up to 64 bits
	defaultClockHz   = 5000000
	defaultTimeoutMs = 2000
)

// ClockDivisor is the divisor for TCK = 60 MHz / (2 * (1 + divisor))
// (AN_108 section 3.8.1, divide-by-5 off)
func ClockDivisor(hz int) uint16 {
	if hz <= 0 {
		hz = defaultClockHz
	}
	d := (masterClockHz/2+hz-1)/hz - 1
	return uint16(max(0, min(d, 0xFFFF)))
}

// read is where the bits of a capture come back in the answer of a
// segment: n bits at byte off; bytes holds 8 bits per byte (a bytes
// command), otherwise one byte with the n bits in its top bits (a bits
// or TMS command)
type read struct {
	off   int
	n     int
	bytes bool
}

type pendingCapture struct {
	r     *espjtag.Result
	reads []read
}

type segment struct {
	cmds []byte
	in   int
	caps []pendingCapture
}

// TAP drives a TAP through the MPSSE. Like espjtag.TAP, scans are queued
// and sent by Run, and between scans the TAP rests in Run-Test/Idle.
type TAP struct {
	link espjtag.Link
	// ClockHz is the TCK frequency; 5 MHz when 0
	ClockHz int
	Timeout time.Duration
	ready   bool
	cur     segment
	segs    []segment
	// Stats
	Writes, BytesOut, BytesIn int
}

// NewTAP makes a TAP over an MPSSE link; the MPSSE is set up at the
// first Run
func NewTAP(l espjtag.Link) *TAP {
	return &TAP{link: l, Timeout: defaultTimeoutMs * time.Millisecond}
}

// SetLink replaces the link (after the device came back); the MPSSE is
// set up again
func (t *TAP) SetLink(l espjtag.Link) {
	t.link = l
	t.ready = false
	t.Discard()
}

// Discard drops what is queued
func (t *TAP) Discard() {
	t.cur = segment{}
	t.segs = nil
}

// tms clocks TMS bits (first in bit 0) with TDI held at tdi; a read
// returns the TDO of the first clock only when capture
func (t *TAP) tms(bits uint8, n int, tdi bool, capture bool) *read {
	b := bits & 0x7F
	if tdi {
		b |= 0x80
	}
	op := byte(OpTMSOut)
	if capture {
		op = OpTMSInOut
	}
	t.cur.cmds = append(t.cur.cmds, op, byte(n-1), b)
	if !capture {
		return nil
	}
	r := &read{off: t.cur.in, n: n}
	t.cur.in++
	return r
}

// reserve closes the segment unless extra command bytes and a scan's
// answer fit in it
func (t *TAP) reserve(extra int) {
	if len(t.cur.cmds)+maxScanCmdBytes+extra > maxSegmentOut || t.cur.in+10 > maxSegmentIn {
		t.closeSegment()
	}
}

func (t *TAP) closeSegment() {
	if len(t.cur.cmds) == 0 {
		return
	}
	if t.cur.in > 0 {
		t.cur.cmds = append(t.cur.cmds, OpSendImmediate)
	}
	t.segs = append(t.segs, t.cur)
	t.cur = segment{}
}

// Reset goes to Test-Logic-Reset by five TMS=1 clocks, then to
// Run-Test/Idle
func (t *TAP) Reset() {
	t.reserve(0)
	t.tms(0x1F, 6, false, false)
}

// Idle clocks n times in Run-Test/Idle
func (t *TAP) Idle(n int) {
	for n > 0 {
		t.reserve(0)
		k := min(n, 7*64)
		t.idle(k)
		n -= k
	}
}

// idle queues n Run-Test/Idle clocks in the current segment
func (t *TAP) idle(n int) {
	for n > 0 {
		k := min(n, 7)
		t.tms(0, k, false, false)
		n -= k
	}
}

func idleBytes(n int) int { return 3 * ((n + 6) / 7) }

func (t *TAP) shift(ir bool, n int, v uint64, capture int, idle int) *espjtag.Result {
	if n <= 0 || n > 64 || capture > n {
		panic(fmt.Sprintf("ftdi: scan of %d bits, %d captured", n, capture))
	}
	if idle > 7*64 {
		defer t.Idle(idle - 7*64)
		idle = 7 * 64
	}
	t.reserve(idleBytes(idle))
	rd := capture > 0
	var c pendingCapture
	// Run-Test/Idle -> Shift-xR: TMS 1,0,0 for DR, 1,1,0,0 for IR
	if ir {
		t.tms(0x03, 4, false, false)
	} else {
		t.tms(0x01, 3, false, false)
	}
	body := n - 1
	if nb := body / 8; nb > 0 {
		op := byte(OpBytesOut)
		if rd {
			op = OpBytesInOut
		}
		t.cur.cmds = append(t.cur.cmds, op, byte(nb-1), byte((nb-1)>>8))
		for i := 0; i < nb; i++ {
			t.cur.cmds = append(t.cur.cmds, byte(v>>uint(8*i)))
		}
		if rd {
			c.reads = append(c.reads, read{off: t.cur.in, n: 8 * nb, bytes: true})
			t.cur.in += nb
		}
	}
	if rem := body % 8; rem > 0 {
		op := byte(OpBitsOut)
		if rd {
			op = OpBitsInOut
		}
		t.cur.cmds = append(t.cur.cmds, op, byte(rem-1), byte(v>>uint(body-rem)))
		if rd {
			c.reads = append(c.reads, read{off: t.cur.in, n: rem})
			t.cur.in++
		}
	}
	// the last bit leaves Shift-xR for Exit1-xR
	if r := t.tms(0x01, 1, v>>uint(n-1)&1 != 0, rd); r != nil {
		c.reads = append(c.reads, *r)
	}
	// Exit1-xR -> Update-xR -> Run-Test/Idle
	t.tms(0x01, 2, false, false)
	t.idle(idle)
	if !rd {
		return nil
	}
	c.r = espjtag.NewResult(capture)
	t.cur.caps = append(t.cur.caps, c)
	return c.r
}

// IR shifts n bits into the instruction register
func (t *TAP) IR(n int, v uint64) { t.shift(true, n, v, 0, 0) }

// DR shifts n bits through the selected data register and returns the
// first capture bits that came out (nil when 0)
func (t *TAP) DR(n int, v uint64, capture int, idle int) *espjtag.Result {
	return t.shift(false, n, v, capture, idle)
}

// ErrNoSync: the adapter did not answer the bad-command check, so it is
// not in MPSSE mode or not an MPSSE at all
var ErrNoSync = errors.New("the FTDI adapter does not answer as an MPSSE")

func (t *TAP) readN(n int) ([]byte, error) {
	buf := make([]byte, n)
	got := 0
	deadline := time.Now().Add(t.Timeout)
	for got < n {
		k, err := t.link.Read(buf[got:])
		if err != nil {
			return nil, fmt.Errorf("%w (%d of %d bytes): %v", espjtag.ErrNoTDO, got, n, err)
		}
		got += k
		if got < n && time.Now().After(deadline) {
			return nil, fmt.Errorf("%w (%d of %d bytes)", espjtag.ErrNoTDO, got, n)
		}
	}
	t.BytesIn += n
	return buf, nil
}

// setup synchronizes with the MPSSE by an invalid opcode (AN_108 3.1:
// the answer is 0xFA and the opcode; what comes before it is stale) and
// sets the clock and the pins: TCK, TDI, TMS outputs, TMS high, TCK low
func (t *TAP) setup() error {
	if err := t.link.Write([]byte{syncOpcode, OpSendImmediate}); err != nil {
		return err
	}
	var last byte
	for i := 0; ; i++ {
		b, err := t.readN(1)
		if err != nil || i > 4096 {
			return fmt.Errorf("%w: %v", ErrNoSync, err)
		}
		if last == BadCommand && b[0] == syncOpcode {
			break
		}
		last = b[0]
	}
	div := ClockDivisor(t.ClockHz)
	cmds := []byte{
		OpDiv5Off, OpAdaptiveOff, OpThreePhaseOff, OpLoopbackOff,
		OpClockDivisor, byte(div), byte(div >> 8),
		OpSetLow, pinTMS, pinTCK | pinTDI | pinTMS,
	}
	if err := t.link.Write(cmds); err != nil {
		return err
	}
	t.Writes += 2
	t.ready = true
	return nil
}

// Run sends what is queued and fills the results
func (t *TAP) Run() error {
	t.closeSegment()
	segs := t.segs
	t.Discard()
	if !t.ready {
		if err := t.setup(); err != nil {
			return err
		}
	}
	for _, s := range segs {
		if err := t.link.Write(s.cmds); err != nil {
			return err
		}
		t.Writes++
		t.BytesOut += len(s.cmds)
		if s.in == 0 {
			continue
		}
		ans, err := t.readN(s.in)
		if err != nil {
			return err
		}
		for _, c := range s.caps {
			var v uint64
			pos := 0
			for _, r := range c.reads {
				var bits uint64
				if r.bytes {
					for i := 0; i < r.n/8; i++ {
						bits |= uint64(ans[r.off+i]) << uint(8*i)
					}
				} else {
					bits = uint64(ans[r.off] >> uint(8-r.n))
				}
				if pos < 64 {
					v |= bits << uint(pos)
				}
				pos += r.n
			}
			c.r.Set(v)
		}
	}
	return nil
}

// ReadIDCode resets the TAP and reads the 32-bit IDCODE that
// Test-Logic-Reset selects (IEEE 1149.1, 12.1.1)
func (t *TAP) ReadIDCode() (uint32, error) {
	t.Reset()
	r := t.DR(32, 0, 32, 0)
	if err := t.Run(); err != nil {
		return 0, err
	}
	return uint32(r.Value()), nil
}
