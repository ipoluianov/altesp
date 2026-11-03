package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestFirmwareState(t *testing.T) {
	erased := func() []byte { return bytes.Repeat([]byte{0xFF}, 4<<20) }
	empty := c3Board()
	empty.Flash = erased()
	noTable := c3Board()
	noTable.Flash = erased()
	copy(noTable.Flash, bootloaderHead(5))
	noApp := c3Board()
	copy(noApp.Flash[0x10000:], bytes.Repeat([]byte{0xFF}, 0x1000))
	for _, c := range []struct {
		name string
		opt  Options
		want Firmware
	}{
		{"present", Options{}, FirmwarePresent},
		{"empty", Options{}, FirmwareEmpty},
		{"empty, ROM only", Options{NoStub: true}, FirmwareEmpty},
		{"no table", Options{}, FirmwareNoTable},
		{"no table, ROM only", Options{NoStub: true}, FirmwareUnknown},
		{"no app", Options{}, FirmwareNoApp},
	} {
		b := c3Board()
		switch {
		case strings.HasPrefix(c.name, "empty"):
			b = empty
		case strings.HasPrefix(c.name, "no table"):
			b = noTable
		case c.name == "no app":
			b = noApp
		}
		s, _ := connectSim(t, b, c3Port, c.opt)
		if got := s.Board().Firmware; got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if got := s.Board().Summary().Firmware; (got != "") != (c.want >= FirmwareEmpty) {
			t.Errorf("%s: summary %q", c.name, got)
		}
		s.Close(context.Background(), false)
	}
}

// TestFirmwareAfterWrites: erasing tells an empty board, flashing a
// bootloader, a table and an app tells firmware again
func TestFirmwareAfterWrites(t *testing.T) {
	s, _ := connectSim(t, c3Board(), c3Port, Options{})
	ctx := context.Background()
	if err := s.EraseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.Board().Firmware; got != FirmwareEmpty {
		t.Fatalf("after erasing: %v", got)
	}
	items := []Item{
		{Name: "bootloader.bin", Addr: 0, Data: bootloaderHead(5)},
		{Name: "partition-table.bin", Addr: 0x8000, Data: partitionTable()},
		{Name: "app.bin", Addr: 0x10000, Data: appImage(5)},
	}
	if err := s.Flash(ctx, items, FlashOptions{Verify: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Board().Firmware; got != FirmwarePresent {
		t.Fatalf("after flashing: %v", got)
	}
}

func TestErased(t *testing.T) {
	s, _ := connectSim(t, c3Board(), c3Port, Options{})
	ctx := context.Background()
	for _, c := range []struct {
		addr uint32
		size int
		want bool
	}{{0x8000, 0x1000, false}, {0x20000, 0x10000, true}, {0x10000, 0x10000, false}, {0x3FF000, 0x1000, true}} {
		got, err := s.Erased(ctx, c.addr, c.size)
		if err != nil || got != c.want {
			t.Errorf("0x%X: %v %v, want %v", c.addr, got, err, c.want)
		}
	}
}
