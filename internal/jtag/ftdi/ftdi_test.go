package ftdi

import (
	"errors"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/internal/jtag/jtagsim"
)

func simPort(t *testing.T) (*jtagsim.Device, *jtagsim.FTDI) {
	t.Helper()
	d := jtagsim.New(jtagsim.Config{IDCode: 0x00005C25}, nil)
	return d, d.FTDI(512)
}

func TestOpenRequests(t *testing.T) {
	_, f := simPort(t)
	if _, err := Open(f, JTAGInterface, 512); err != nil {
		t.Fatal(err)
	}
	if f.Requests[ReqReset] != 3 || f.Requests[ReqSetLatency] != 1 || f.Requests[ReqSetBitmode] != 2 || len(f.Errors) != 0 {
		t.Fatalf("requests %v, errors %v", f.Requests, f.Errors)
	}
}

func TestScans(t *testing.T) {
	_, f := simPort(t)
	l, err := Open(f, JTAGInterface, 512)
	if err != nil {
		t.Fatal(err)
	}
	tap := NewTAP(l)
	tap.ClockHz = 10000000
	id, err := tap.ReadIDCode()
	if err != nil || id != 0x00005C25 {
		t.Fatalf("IDCODE 0x%08x %v", id, err)
	}
	if f.TCKHz() != 10000000 {
		t.Fatalf("TCK %d", f.TCKHz())
	}
	// dtmcs of the RISC-V DTM (IR 0x10): abits 7, version 1; then a
	// 41-bit dmi scan, 64-bit and 1-bit scans in one Run
	tap.IR(5, 0x10)
	r := tap.DR(32, 0, 32, 3)
	tap.IR(5, 0x01)
	r64 := tap.DR(64, 0, 64, 0)
	tap.IR(5, 0x1F)
	r1 := tap.DR(1, 1, 1, 9)
	if err := tap.Run(); err != nil {
		t.Fatal(err)
	}
	if v := r.Value(); v&0xF != 1 || v>>4&0x3F != 7 {
		t.Fatalf("dtmcs 0x%x", v)
	}
	// the IDCODE register is 32 bits long: the 32 bits after it are the
	// zeros shifted in through TDI
	if uint32(r64.Value()) != 0x00005C25 || r64.Value()>>32 != 0 {
		t.Fatalf("64-bit scan 0x%x", r64.Value())
	}
	if r1.Value() != 0 {
		t.Fatalf("bypass 0x%x", r1.Value())
	}
	if len(f.Errors) != 0 {
		t.Fatal(f.Errors)
	}
}

// Many scans in one Run are split into segments that fit the buffers
func TestLongQueue(t *testing.T) {
	_, f := simPort(t)
	l, _ := Open(f, JTAGInterface, 512)
	tap := NewTAP(l)
	var rs []interface{ Value() uint64 }
	for i := 0; i < 500; i++ {
		tap.Reset()
		rs = append(rs, tap.DR(32, 0, 32, 20))
	}
	if err := tap.Run(); err != nil {
		t.Fatal(err)
	}
	for i, r := range rs {
		if r.Value() != 0x5C25 {
			t.Fatalf("scan %d: 0x%x", i, r.Value())
		}
	}
	if tap.Writes < 3 {
		t.Fatalf("%d writes", tap.Writes)
	}
}

// stalePort answers first with bytes left from an earlier session
type stalePort struct {
	Port
	sent bool
}

func (p *stalePort) Read(b []byte) (int, error) {
	if !p.sent {
		p.sent = true
		return copy(b, []byte{0x32, 0x60, 0x12, BadCommand, 0x34}), nil
	}
	return p.Port.Read(b)
}

func TestSyncSkipsStale(t *testing.T) {
	_, f := simPort(t)
	p := &stalePort{Port: f}
	l, _ := Open(p, JTAGInterface, 512)
	id, err := NewTAP(l).ReadIDCode()
	if err != nil || id != 0x5C25 {
		t.Fatalf("0x%x %v", id, err)
	}
}

// statusOnly is an FTDI that is not in MPSSE mode: it only sends status
type statusOnly struct{}

func (statusOnly) Write([]byte) error                  { return nil }
func (statusOnly) Read(b []byte) (int, error)          { return copy(b, []byte{0x32, 0x60}), nil }
func (statusOnly) Control(uint8, uint16, uint16) error { return nil }

func TestNoMPSSE(t *testing.T) {
	l, _ := Open(statusOnly{}, JTAGInterface, 512)
	l.Timeout = 20 * time.Millisecond
	tap := NewTAP(l)
	tap.Timeout = 50 * time.Millisecond
	if _, err := tap.ReadIDCode(); !errors.Is(err, ErrNoSync) {
		t.Fatalf("%v", err)
	}
}

type packets struct{ data []byte }

func (p *packets) Write([]byte) error                  { return nil }
func (p *packets) Control(uint8, uint16, uint16) error { return nil }
func (p *packets) Read(b []byte) (int, error) {
	n := copy(b, p.data)
	p.data = p.data[n:]
	if n == 0 {
		return copy(b, []byte{1, 2}), nil
	}
	return n, nil
}

func TestStatusStripped(t *testing.T) {
	p := &packets{data: []byte{0x32, 0x60, 'a', 'b', 'c', 'd', 'e', 'f', 0x32, 0x60, 'g', 'h'}}
	l, _ := Open(p, JTAGInterface, 8)
	buf := make([]byte, 16)
	n, err := l.Read(buf)
	if err != nil || string(buf[:n]) != "abcdefgh" {
		t.Fatalf("%q %v", buf[:n], err)
	}
}

func TestClockDivisor(t *testing.T) {
	for hz, want := range map[int]uint16{5000000: 5, 30000000: 0, 1000: 29999, 100: 0xFFFF, 0: 5, 7000000: 4} {
		if got := ClockDivisor(hz); got != want {
			t.Errorf("%d Hz: %d, want %d", hz, got, want)
		}
	}
}
