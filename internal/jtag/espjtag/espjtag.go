// Package espjtag speaks the JTAG command protocol of the Espressif
// USB-Serial-JTAG controller and drives the TAP state machine over it.
//
// The protocol (ESP32-C3/C6/H2 Technical Reference Manuals, chapter "USB
// Serial/JTAG Controller", section "JTAG Command Processor"): the host
// writes 4-bit commands to the bulk OUT endpoint of the JTAG interface,
// two per byte; the controller clocks the TAP and returns the TDO bits it
// was asked to capture on the bulk IN endpoint, eight per byte.
//
//	CMD_CLK   0 c s d  one TCK cycle with TMS=s, TDI=d; c: capture TDO
//	CMD_RST   1 0 0 r  set the SRST line to r
//	CMD_FLUSH 1 0 1 0  send the captured bits now, the last byte padded
//	CMD_RSV   1 0 1 1  reserved
//	CMD_REP   1 1 x x  repeat the previous command (count in base 4)
//
// unverified (TZ.md "Hardware checks"): the order of the two commands in
// a byte (here the high nibble first, NibbleHighFirst), the order of the
// TDO bits in a byte (here the first captured bit in bit 0), that TDO is
// sampled before the TCK edge of the same CMD_CLK, and how many captured
// bytes the controller buffers before the host must read
// (MaxCaptureBits). CMD_REP is not used: every clock is sent as its own
// command, which needs no knowledge of the repeat count encoding.
//
// The TAP state machine is that of IEEE 1149.1, figure 6-1.
package espjtag

import (
	"errors"
	"fmt"
	"time"
)

// Link is the pair of bulk endpoints of the JTAG interface
type Link interface {
	// Write sends the commands
	Write(p []byte) error
	// Read returns captured bytes as they come, at least one, or an
	// error when none came in time
	Read(p []byte) (int, error)
}

// The commands (TRM, "JTAG Command Processor")
const (
	CmdClk   byte = 0x0
	CmdRst   byte = 0x8
	CmdFlush byte = 0xA
	CmdRsv   byte = 0xB
	CmdRep   byte = 0xC

	clkCapture byte = 1 << 2
	clkTMS     byte = 1 << 1
	clkTDI     byte = 1 << 0
)

// NibbleHighFirst: the first of the two commands of a byte is its high
// nibble. unverified, see the package comment.
const NibbleHighFirst = true

// Clk is CMD_CLK
func Clk(capture, tms, tdi bool) byte {
	c := CmdClk
	if capture {
		c |= clkCapture
	}
	if tms {
		c |= clkTMS
	}
	if tdi {
		c |= clkTDI
	}
	return c
}

// Pack packs commands two per byte; an odd count is an error of the
// caller (the TAP pads before it packs)
func Pack(cmds []byte) []byte {
	out := make([]byte, (len(cmds)+1)/2)
	for i, c := range cmds {
		shift := uint(0)
		if (i%2 == 0) == NibbleHighFirst {
			shift = 4
		}
		out[i/2] |= (c & 0xF) << shift
	}
	return out
}

// Unpack is the inverse of Pack
func Unpack(b []byte) []byte {
	out := make([]byte, 0, 2*len(b))
	for _, x := range b {
		if NibbleHighFirst {
			out = append(out, x>>4, x&0xF)
		} else {
			out = append(out, x&0xF, x>>4)
		}
	}
	return out
}

// TDOBit is bit i of the captured bytes: the first captured bit is bit 0
// of the first byte
func TDOBit(b []byte, i int) bool { return b[i/8]>>(uint(i)%8)&1 != 0 }

// Result is a captured DR or IR scan, valid after Run
type Result struct {
	n     int
	off   int // bit offset in the captured stream of its Run
	v     uint64
	valid bool
	// parent: a Sub result is bits [off, off+n) of its parent
	parent *Result
}

// NewResult is a result of n bits for another TAP driver (the FTDI
// adapter) to fill with Set
func NewResult(n int) *Result { return &Result{n: n} }

// Set fills the result: v holds the captured bits, the first in bit 0
func (r *Result) Set(v uint64) {
	if r.n < 64 {
		v &= 1<<uint(r.n) - 1
	}
	r.v, r.valid = v, true
}

// Sub is the n bits of r from bit off, valid when r is: the bits of one
// TAP in a scan through a chain of TAPs
func (r *Result) Sub(off, n int) *Result { return &Result{parent: r, off: off, n: n} }

// Value is the captured bits, the first one in bit 0
func (r *Result) Value() uint64 {
	if r.parent != nil {
		v := r.parent.Value() >> uint(r.off)
		if r.n < 64 {
			v &= 1<<uint(r.n) - 1
		}
		return v
	}
	return r.v
}

// Valid tells that Run filled it
func (r *Result) Valid() bool {
	if r.parent != nil {
		return r.parent.Valid()
	}
	return r.valid
}

type segment struct {
	cmds    []byte
	capBits int
}

// TAP drives the TAP of the chip through the command protocol. Scans are
// queued and sent by Run; between scans the TAP rests in Run-Test/Idle.
type TAP struct {
	link Link
	// MaxCaptureBits is how many TDO bits one write may ask for before the
	// host reads them back: the controller's IN buffer must not fill up
	// while the host is still writing
	MaxCaptureBits int
	// Timeout is how long Run waits for captured bits
	Timeout time.Duration

	cur     segment
	segs    []segment
	results []*Result
	capOff  int
	// Stats
	Writes, BytesOut, BytesIn int
}

// New makes a TAP over a link; call Reset before the first scan
func New(l Link) *TAP {
	return &TAP{link: l, MaxCaptureBits: 512, Timeout: 2 * time.Second}
}

// SetLink replaces the link (after the device came back)
func (t *TAP) SetLink(l Link) {
	t.link = l
	t.Discard()
}

// Discard drops what is queued
func (t *TAP) Discard() {
	t.cur = segment{}
	t.segs = nil
	t.results = nil
	t.capOff = 0
}

func (t *TAP) clk(capture, tms, tdi bool) {
	t.cur.cmds = append(t.cur.cmds, Clk(capture, tms, tdi))
	if capture {
		t.cur.capBits++
	}
}

// closeSegment ends the current segment in Run-Test/Idle: an even number
// of commands, the last one CMD_FLUSH when bits were captured
func (t *TAP) closeSegment() {
	if len(t.cur.cmds) == 0 {
		return
	}
	if t.cur.capBits > 0 {
		if len(t.cur.cmds)%2 == 0 {
			t.clk(false, false, false)
		}
		t.cur.cmds = append(t.cur.cmds, CmdFlush)
	} else if len(t.cur.cmds)%2 != 0 {
		t.clk(false, false, false)
	}
	t.segs = append(t.segs, t.cur)
	t.cur = segment{}
}

func (t *TAP) reserve(bits int) {
	if t.cur.capBits > 0 && t.cur.capBits+bits > t.MaxCaptureBits {
		t.closeSegment()
	}
}

// Reset goes to Test-Logic-Reset by five TMS=1 clocks, then to
// Run-Test/Idle
func (t *TAP) Reset() {
	for i := 0; i < 5; i++ {
		t.clk(false, true, false)
	}
	t.clk(false, false, false)
}

// Idle clocks n times in Run-Test/Idle
func (t *TAP) Idle(n int) {
	for i := 0; i < n; i++ {
		t.clk(false, false, false)
	}
}

// SRST sets the system reset line of the controller (CMD_RST)
func (t *TAP) SRST(assert bool) {
	c := CmdRst
	if assert {
		c |= 1
	}
	t.cur.cmds = append(t.cur.cmds, c)
}

// shift goes from Run-Test/Idle to Shift-xR (ir selects the IR column),
// shifts n bits of v (bit 0 first) capturing the first capture bits that
// come out, and comes back to Run-Test/Idle through Update-xR, then
// clocks idle times
func (t *TAP) shift(ir bool, n int, v uint64, capture int, idle int) *Result {
	if n <= 0 || n > 64 || capture > n {
		panic(fmt.Sprintf("espjtag: scan of %d bits, %d captured", n, capture))
	}
	var r *Result
	if capture > 0 {
		t.reserve(capture)
		r = &Result{n: capture, off: t.capOff + t.queuedCapture()}
		t.results = append(t.results, r)
	}
	t.clk(false, true, false) // Select-DR-Scan
	if ir {
		t.clk(false, true, false) // Select-IR-Scan
	}
	t.clk(false, false, false) // Capture-xR
	t.clk(false, false, false) // Shift-xR
	for i := 0; i < n; i++ {
		// the last bit leaves Shift-xR for Exit1-xR
		t.clk(i < capture, i == n-1, v>>uint(i)&1 != 0)
	}
	t.clk(false, true, false)  // Update-xR
	t.clk(false, false, false) // Run-Test/Idle
	t.Idle(idle)
	return r
}

// queuedCapture is the number of capture bits queued since the last Run
func (t *TAP) queuedCapture() int {
	n := t.cur.capBits
	for _, s := range t.segs {
		n += s.capBits
	}
	return n
}

// IR shifts n bits into the instruction register
func (t *TAP) IR(n int, v uint64) { t.shift(true, n, v, 0, 0) }

// DR shifts n bits through the selected data register and returns the
// first capture bits that came out (nil when 0); idle clocks follow in
// Run-Test/Idle
func (t *TAP) DR(n int, v uint64, capture int, idle int) *Result {
	return t.shift(false, n, v, capture, idle)
}

// ErrNoTDO is returned when the controller didn't send the captured bits
var ErrNoTDO = errors.New("the USB-JTAG controller sent no TDO data")

// Run sends what is queued and fills the results
func (t *TAP) Run() error {
	t.closeSegment()
	segs, results := t.segs, t.results
	t.Discard()
	var tdo []byte
	nbits := 0
	for _, s := range segs {
		if err := t.link.Write(Pack(s.cmds)); err != nil {
			return err
		}
		t.Writes++
		t.BytesOut += len(s.cmds) / 2
		if s.capBits == 0 {
			continue
		}
		need := (s.capBits + 7) / 8
		buf := make([]byte, need)
		got := 0
		deadline := time.Now().Add(t.Timeout)
		for got < need {
			n, err := t.link.Read(buf[got:])
			if err != nil {
				return fmt.Errorf("%w (%d of %d bytes): %v", ErrNoTDO, got, need, err)
			}
			got += n
			if got < need && time.Now().After(deadline) {
				return fmt.Errorf("%w (%d of %d bytes)", ErrNoTDO, got, need)
			}
		}
		t.BytesIn += need
		// the bits of each segment start at a byte: the flush pads
		for i := 0; i < s.capBits; i++ {
			if nbits%8 == 0 {
				tdo = append(tdo, 0)
			}
			if TDOBit(buf, i) {
				tdo[nbits/8] |= 1 << uint(nbits%8)
			}
			nbits++
		}
	}
	for _, r := range results {
		var v uint64
		for i := 0; i < r.n; i++ {
			if TDOBit(tdo, r.off+i) {
				v |= 1 << uint(i)
			}
		}
		r.v, r.valid = v, true
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
