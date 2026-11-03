package jtag

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/internal/jtag/riscv"
)

func newSim(t *testing.T, b SimBoard) *Sim {
	t.Helper()
	s, err := NewSim(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func openSim(t *testing.T, s *Sim, key string) *Session {
	t.Helper()
	sess, err := OpenLink(s.Link(), key, s.Options())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func pattern(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// The whole path but the OS USB layer: connect, identify, write, read
// back, erase, for each chip and each way the debug module reaches
// memory
func TestEndToEnd(t *testing.T) {
	cases := []struct {
		name  string
		board SimBoard
		path  string
	}{
		{"c3 progbuf", SimBoard{Chip: "esp32c3", BusyCycles: 9, HaltPolls: 3}, "program buffer"},
		{"c3 progbuf impebreak", SimBoard{Chip: "esp32c3", ProgBufSize: 2, ImpEbreak: true, BusyCycles: 6}, "program buffer"},
		{"c6 sysbus", SimBoard{Chip: "esp32c6", SBA: true, BusyCycles: 12, HaltPolls: 2}, "system bus"},
		{"h2 abstract", SimBoard{Chip: "esp32h2", AbstractMem: true, BusyCycles: 5}, "abstract memory access"},
		// the same chips through ESP-Prog on their JTAG pins
		{"c3 progbuf esp-prog", SimBoard{Chip: "esp32c3", BusyCycles: 9, HaltPolls: 3, Adapter: AdapterFTDI}, "program buffer"},
		{"c6 sysbus esp-prog", SimBoard{Chip: "esp32c6", SBA: true, BusyCycles: 12, Adapter: AdapterFTDI}, "system bus"},
		{"h2 abstract esp-prog", SimBoard{Chip: "esp32h2", AbstractMem: true, BusyCycles: 5, Adapter: AdapterFTDI}, "abstract memory access"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.board.MAC = [6]byte{0x84, 0xF7, 0x03, 0x12, 0x34, 0x56}
			old := pattern(64<<10, 1)
			c.board.Flash = bytes.Repeat([]byte{0xFF}, 4<<20)
			copy(c.board.Flash[0x10000:], old)
			sim := newSim(t, c.board)
			s := openSim(t, sim, c.board.Chip)
			ctx := context.Background()

			in, err := s.Info()
			if err != nil {
				t.Fatal(err)
			}
			if in.Key != c.board.Chip || !in.HasMAC || in.MAC != c.board.MAC {
				t.Fatalf("info: %+v", in)
			}
			if in.FlashSize != 4<<20 || in.FlashID != 0xC84016 {
				t.Fatalf("flash: size %d id %06x", in.FlashSize, in.FlashID)
			}
			if in.MemPath != c.path {
				t.Fatalf("memory path %q, want %q", in.MemPath, c.path)
			}
			if len(in.Warnings) != 0 {
				t.Fatalf("warnings: %v", in.Warnings)
			}
			if sim.BootMode != "download" {
				t.Fatalf("boot mode %s", sim.BootMode)
			}

			// a write that starts and ends inside sectors keeps their
			// other bytes
			data := pattern(40000, 2)
			addr := uint32(0x10000 + 0x123)
			var last, total int
			if err := s.WriteFlash(ctx, addr, data, func(d, t int) { last, total = d, t }); err != nil {
				t.Fatalf("write: %v", err)
			}
			if last != total || total == 0 {
				t.Fatalf("progress %d/%d", last, total)
			}
			exp := bytes.Repeat([]byte{0xFF}, 4<<20)
			copy(exp[0x10000:], old)
			copy(exp[addr:], data)
			if !bytes.Equal(sim.Flash(), exp) {
				t.Fatal("flash content differs after the write")
			}
			got, err := s.ReadFlash(ctx, addr-3, len(data)+5, nil)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, exp[addr-3:int(addr)+len(data)+2]) {
				t.Fatal("read back differs")
			}
			sum, err := s.FlashMD5(ctx, 0x10000, 0x20000)
			if err != nil || sum != md5Of(exp[0x10000:0x30000]) {
				t.Fatalf("md5: %v", err)
			}

			if err := s.EraseRegion(ctx, 0x10000, 0x11000); err != nil {
				t.Fatal(err)
			}
			for i := 0x10000; i < 0x21000; i++ {
				exp[i] = 0xFF
			}
			if !bytes.Equal(sim.Flash(), exp) {
				t.Fatal("flash content differs after the region erase")
			}
			if err := s.EraseRegion(ctx, 0x10010, 0x1000); err == nil {
				t.Fatal("unaligned erase accepted")
			}
			if err := s.EraseFlash(ctx); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sim.Flash(), bytes.Repeat([]byte{0xFF}, 4<<20)) {
				t.Fatal("flash not blank after the chip erase")
			}
			if sim.WatchdogResets != 0 {
				t.Fatalf("%d watchdog resets", sim.WatchdogResets)
			}

			// into the application, then another operation brings it
			// back to download mode
			if err := s.Reset(true); err != nil {
				t.Fatal(err)
			}
			if sim.BootMode != "app" {
				t.Fatalf("boot mode after Reset(true): %s", sim.BootMode)
			}
			if err := s.WriteFlash(ctx, 0x1000, []byte("hello"), nil); err != nil {
				t.Fatal(err)
			}
			if sim.BootMode != "download" || !bytes.Equal(sim.Flash()[0x1000:0x1005], []byte("hello")) {
				t.Fatalf("after re-entering download mode: %s", sim.BootMode)
			}
			if len(sim.Errors) != 0 {
				t.Fatalf("protocol errors: %v", sim.Errors)
			}
			if f := sim.FTDI; f != nil {
				if len(f.Errors) != 0 {
					t.Fatalf("FTDI errors: %v", f.Errors)
				}
				if f.TCKHz() != 5000000 {
					t.Fatalf("TCK %d Hz", f.TCKHz())
				}
			}
		})
	}
}

func TestDetectByIDCode(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32c6", SBA: true})
	s := openSim(t, sim, "")
	in, _ := s.Info()
	if in.Key != "esp32c6" || in.Chip != "ESP32-C6" {
		t.Fatalf("%+v", in)
	}
}

func TestIDCodeMismatchWarns(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32c3", IDCode: 0x0000DC25})
	s := openSim(t, sim, "esp32c3")
	in, _ := s.Info()
	if len(in.Warnings) != 1 {
		t.Fatalf("warnings %v", in.Warnings)
	}
}

func TestJTAGDisabled(t *testing.T) {
	for _, b := range []SimBoard{
		{Chip: "esp32c3", JTAGDisabled: true},
		{Chip: "esp32c6", DMDisabled: true},
	} {
		sim := newSim(t, b)
		_, err := OpenLink(sim.Link(), b.Chip, Options{BootWait: time.Millisecond})
		var de *DisabledError
		if !errors.Is(err, ErrJTAGDisabled) || !errors.As(err, &de) {
			t.Fatalf("%+v: %v", b, err)
		}
	}
}

func TestUnsupportedChip(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32c3"})
	if _, err := OpenLink(sim.Link(), "esp32s3", Options{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("%v", err)
	}
	if Supported("esp32s3") || Supported("esp32") || !Supported("esp32h2") {
		t.Fatal("Supported")
	}
}

// A reset that re-enumerates the USB device: the session reopens it
func TestReenumerate(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32h2", SBA: true, Reenumerate: true})
	s := openSim(t, sim, "esp32h2")
	if sim.Resets != 1 || sim.BootMode != "download" {
		t.Fatalf("resets %d, mode %s", sim.Resets, sim.BootMode)
	}
	if err := s.WriteFlash(context.Background(), 0, []byte{1, 2, 3, 4}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(true); err != nil {
		t.Fatal(err)
	}
	if sim.BootMode != "app" {
		t.Fatal(sim.BootMode)
	}
}

// Without the watchdog sequence the first ROM call resets the chip; the
// session sees the call fail
func TestWatchdogsModeled(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32c3"})
	saved := targetByKey("esp32c3").wdts
	targetByKey("esp32c3").wdts = nil
	defer func() { targetByKey("esp32c3").wdts = saved }()
	_, err := OpenLink(sim.Link(), "esp32c3", Options{BootWait: time.Millisecond})
	if !errors.Is(err, riscv.ErrReset) || sim.WatchdogResets == 0 {
		t.Fatalf("err %v, watchdog resets %d", err, sim.WatchdogResets)
	}
}

func TestNoReset(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32c6", SBA: true})
	s, err := OpenLink(sim.Link(), "esp32c6", Options{NoReset: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if sim.Resets != 0 || sim.BootMode != "app" {
		t.Fatalf("resets %d", sim.Resets)
	}
	if _, err := s.ReadFlash(context.Background(), 0, 16, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCancel(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32c6", SBA: true})
	s := openSim(t, sim, "esp32c6")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadFlash(ctx, 0, 1<<20, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// An Xtensa chip: the chain is found, the debug module of a core probed
// through the documented XDM registers, and the session stops there with
// ErrXtensaUnavailable, having changed nothing but the power control
func TestXtensaProbe(t *testing.T) {
	cases := []struct {
		board SimBoard
		key   string
		want  string
	}{
		{SimBoard{Chip: "esp32s3", OCDID: 0x12345678}, "esp32s3", "esp32s3"},
		{SimBoard{Chip: "esp32s3", OCDID: 0x12345678}, "", "esp32s3"},
		{SimBoard{Chip: "esp32", OCDID: 0x0BADC0DE, Adapter: AdapterFTDI}, "esp32", "esp32"},
		{SimBoard{Chip: "esp32", OCDID: 0x0BADC0DE, Adapter: AdapterFTDI}, "", "esp32"},
		{SimBoard{Chip: "esp32s3", OCDID: 0x0BADC0DE, Adapter: AdapterFTDI}, "esp32s3", "esp32s3"},
	}
	for _, c := range cases {
		sim := newSim(t, c.board)
		_, err := OpenLink(sim.Link(), c.key, sim.Options())
		var xe *XtensaError
		if !errors.Is(err, ErrXtensaUnavailable) || !errors.As(err, &xe) {
			t.Fatalf("%+v %q: %v", c.board, c.key, err)
		}
		if xe.Probe.OCDID != c.board.OCDID || xe.TAPs != 2 || xe.TAP != 0 || len(xe.Missing) == 0 {
			t.Fatalf("%+v: %+v", c.board, xe)
		}
		if xe.Probe.DSR&(1<<31) == 0 || xe.Probe.PwrStat&4 == 0 {
			t.Fatalf("probe %v", xe.Probe)
		}
		if !strings.Contains(err.Error(), "Xtensa JTAG is not available yet: ") {
			t.Fatalf("message: %v", err)
		}
		dcr, pwr := sim.XDMState(0)
		if dcr != 0 || pwr != 0x87 {
			t.Fatalf("DCR 0x%x PWRCTL 0x%x", dcr, pwr)
		}
		if len(sim.Errors) != 0 {
			t.Fatalf("protocol errors: %v", sim.Errors)
		}
		if sim.FTDI != nil && len(sim.FTDI.Errors) != 0 {
			t.Fatalf("FTDI errors: %v", sim.FTDI.Errors)
		}
	}
}

// The IDCODE of a RISC-V chip for an Xtensa key, and the other way round
func TestArchMismatch(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32s3"})
	if _, err := OpenLink(sim.Link(), "esp32c3", sim.Options()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("%v", err)
	}
}

func TestXtensaDisabled(t *testing.T) {
	sim := newSim(t, SimBoard{Chip: "esp32", JTAGDisabled: true, Adapter: AdapterFTDI})
	if _, err := OpenLink(sim.Link(), "esp32", sim.Options()); !errors.Is(err, ErrJTAGDisabled) {
		t.Fatalf("%v", err)
	}
}

func TestDeviceKind(t *testing.T) {
	if (Device{Adapter: AdapterFTDI}).Kind() != "ESP-Prog" || (Device{}).Kind() != "USB-JTAG" {
		t.Fatal("Kind")
	}
}
