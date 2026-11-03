package riscv_test

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
	"github.com/ipoluianov/altesp/internal/jtag/jtagsim"
	"github.com/ipoluianov/altesp/internal/jtag/riscv"
)

// ramBus is 64 KB of RAM at 0x40800000
type ramBus struct{ ram [0x10000]byte }

const ramBase = 0x40800000

func (b *ramBus) off(a uint32) (int, bool) {
	if a%4 == 0 && a >= ramBase && a+4 <= ramBase+uint32(len(b.ram)) {
		return int(a - ramBase), true
	}
	return 0, false
}
func (b *ramBus) Read32(a uint32) (uint32, bool) {
	o, ok := b.off(a)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b.ram[o:]), true
}
func (b *ramBus) Write32(a, v uint32) bool {
	o, ok := b.off(a)
	if ok {
		binary.LittleEndian.PutUint32(b.ram[o:], v)
	}
	return ok
}
func (b *ramBus) Fetch(pc uint32) (uint32, bool)        { return b.Read32(pc) }
func (b *ramBus) Call(*jtagsim.Hart) jtagsim.CallResult { return jtagsim.NotROM }
func (b *ramBus) Reset()                                {}

func connect(t *testing.T, cfg jtagsim.Config) (*riscv.DM, *jtagsim.Device, *ramBus) {
	t.Helper()
	bus := &ramBus{}
	cfg.IDCode = 0xDC25
	cfg.AppPC = ramBase
	dev := jtagsim.New(cfg, bus)
	tap := espjtag.New(dev)
	tap.Reset()
	dtm, err := riscv.NewDTM(tap)
	if err != nil {
		t.Fatal(err)
	}
	dm, err := riscv.NewDM(dtm)
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.Halt(); err != nil {
		t.Fatal(err)
	}
	return dm, dev, bus
}

func TestMemoryPaths(t *testing.T) {
	cases := []struct {
		cfg  jtagsim.Config
		want riscv.MemPath
	}{
		{jtagsim.Config{SBA: true, ProgBufSize: 16, BusyCycles: 20}, riscv.MemSysBus},
		{jtagsim.Config{AbstractMem: true, ProgBufSize: 2, ImpEbreak: true, BusyCycles: 3}, riscv.MemAbstract},
		{jtagsim.Config{ProgBufSize: 16, BusyCycles: 11}, riscv.MemProgBuf},
		{jtagsim.Config{ProgBufSize: 2, ImpEbreak: true}, riscv.MemProgBuf},
		{jtagsim.Config{ProgBufSize: 2}, riscv.MemNone},
	}
	for _, c := range cases {
		dm, dev, bus := connect(t, c.cfg)
		if got := dm.ProbeMemory(ramBase); got != c.want {
			t.Fatalf("%+v: path %v, want %v", c.cfg, got, c.want)
		}
		if c.want == riscv.MemNone {
			continue
		}
		words := make([]uint32, 300)
		for i := range words {
			words[i] = uint32(i)*0x01010101 ^ 0xA5A5A5A5
		}
		if err := dm.WriteMem(ramBase+0x100, words); err != nil {
			t.Fatalf("%v: write %v", c.want, err)
		}
		if v, _ := bus.Read32(ramBase + 0x100 + 4*299); v != words[299] {
			t.Fatalf("%v: ram %08x", c.want, v)
		}
		got, err := dm.ReadMem(ramBase+0x100, len(words))
		if err != nil {
			t.Fatalf("%v: read %v", c.want, err)
		}
		for i := range words {
			if got[i] != words[i] {
				t.Fatalf("%v: word %d %08x", c.want, i, got[i])
			}
		}
		if _, err := dm.ReadMem(0x10, 2); err == nil {
			t.Fatalf("%v: a bad address read worked", c.want)
		}
		// still usable after the error
		if got, err := dm.ReadMem(ramBase+0x100, 1); err != nil || got[0] != words[0] {
			t.Fatalf("%v: after the error: %v", c.want, err)
		}
		if c.cfg.BusyCycles > 4 && dev.DMIBusyHits == 0 {
			t.Fatalf("%v: busy never seen", c.want)
		}
	}
}

func TestRegisters(t *testing.T) {
	dm, dev, _ := connect(t, jtagsim.Config{ProgBufSize: 16, BusyCycles: 8})
	if err := dm.WriteReg(riscv.GPR(10), 0x12345678); err != nil {
		t.Fatal(err)
	}
	if v, err := dm.ReadReg(riscv.GPR(10)); err != nil || v != 0x12345678 {
		t.Fatalf("a0 %08x %v", v, err)
	}
	if v, err := dm.ReadReg(riscv.CSR(riscv.CSRDcsr)); err != nil || v>>riscv.DcsrCauseShift&7 != riscv.CauseHaltReq {
		t.Fatalf("dcsr %08x %v", v, err)
	}
	if dev.Hart().Dpc != ramBase {
		t.Fatal("dpc")
	}
	_, err := dm.ReadReg(riscv.CSR(0x7C0))
	var ce *riscv.CmdError
	if !errors.As(err, &ce) || ce.Code != 3 {
		t.Fatalf("unknown CSR: %v", err)
	}
	// a function that is just an ebreak: resume, it halts again
	dm.ProbeMemory(ramBase)
	dm.WriteMem(ramBase+0x40, []uint32{riscv.InsnEbreak})
	dm.WriteReg(riscv.CSR(riscv.CSRDcsr), riscv.DcsrEbreakM)
	dm.WriteReg(riscv.CSR(riscv.CSRDpc), ramBase+0x40)
	if err := dm.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := dm.WaitHalted(1e9, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := dm.ReadReg(riscv.CSR(riscv.CSRDcsr)); v>>riscv.DcsrCauseShift&7 != riscv.CauseEbreak {
		t.Fatalf("cause %d", v>>riscv.DcsrCauseShift&7)
	}
}

func TestNoDTM(t *testing.T) {
	dev := jtagsim.New(jtagsim.Config{IDCode: 0xDC25, NoDTM: true}, &ramBus{})
	tap := espjtag.New(dev)
	tap.Reset()
	if _, err := riscv.NewDTM(tap); !errors.Is(err, riscv.ErrNoDTM) {
		t.Fatal(err)
	}
}
