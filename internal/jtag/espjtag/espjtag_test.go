package espjtag_test

import (
	"bytes"
	"testing"

	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
	"github.com/ipoluianov/altesp/internal/jtag/jtagsim"
)

func TestPack(t *testing.T) {
	cmds := []byte{espjtag.Clk(true, false, true), espjtag.Clk(false, true, false), espjtag.CmdFlush, espjtag.CmdRst | 1}
	b := espjtag.Pack(cmds)
	// CLK(cap, tdi) = 0101, CLK(tms) = 0010, FLUSH = 1010, RST(1) = 1001
	if !bytes.Equal(b, []byte{0x52, 0xA9}) {
		t.Fatalf("% x", b)
	}
	if !bytes.Equal(espjtag.Unpack(b), cmds) {
		t.Fatal("unpack")
	}
}

type nullBus struct{}

func (nullBus) Read32(uint32) (uint32, bool)          { return 0, false }
func (nullBus) Write32(uint32, uint32) bool           { return false }
func (nullBus) Fetch(uint32) (uint32, bool)           { return 0, false }
func (nullBus) Call(*jtagsim.Hart) jtagsim.CallResult { return jtagsim.NotROM }
func (nullBus) Reset()                                {}

// byteLink hands the TDO bytes over one at a time
type byteLink struct{ *jtagsim.Device }

func (l byteLink) Read(p []byte) (int, error) { return l.Device.Read(p[:1]) }

func TestIDCodeAndScans(t *testing.T) {
	dev := jtagsim.New(jtagsim.Config{IDCode: 0x00005C25}, nullBus{})
	tap := espjtag.New(byteLink{dev})
	tap.MaxCaptureBits = 40 // several segments
	id, err := tap.ReadIDCode()
	if err != nil || id != 0x00005C25 {
		t.Fatalf("idcode %08x %v", id, err)
	}
	// BYPASS: one bit of delay, so the bits come back shifted by one
	tap.IR(5, 0x1F)
	r1 := tap.DR(33, 0x1_2345_6789<<1>>1, 33, 0)
	tap.IR(5, 0x01)
	r2 := tap.DR(32, 0, 32, 0)
	r3 := tap.DR(32, 0, 16, 3)
	if err := tap.Run(); err != nil {
		t.Fatal(err)
	}
	if r1.Value()>>1 != 0x1_2345_6789&(1<<32-1) {
		t.Fatalf("bypass %x", r1.Value())
	}
	if r2.Value() != 0x5C25 || r3.Value() != 0x5C25&0xFFFF {
		t.Fatalf("%x %x", r2.Value(), r3.Value())
	}
	if len(dev.Errors) != 0 {
		t.Fatal(dev.Errors)
	}
}

func TestDisabledTAP(t *testing.T) {
	dev := jtagsim.New(jtagsim.Config{IDCode: 0x5C25, Disabled: true}, nullBus{})
	id, err := espjtag.New(dev).ReadIDCode()
	if err != nil || id != 0xFFFFFFFF {
		t.Fatalf("%08x %v", id, err)
	}
}

func TestNoTDO(t *testing.T) {
	// a link that loses everything
	tap := espjtag.New(lost{})
	tap.Reset()
	tap.DR(32, 0, 32, 0)
	if err := tap.Run(); err == nil {
		t.Fatal("no error without TDO")
	}
}

type lost struct{}

func (lost) Write(p []byte) error       { return nil }
func (lost) Read(p []byte) (int, error) { return 0, jtagsim.ErrNoData }
