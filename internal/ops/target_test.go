package ops

import (
	"bytes"
	"context"
	"testing"

	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/jtag"
)

// TestJTAGSession runs the operations over the simulated USB-JTAG of an
// ESP32-C6: the card, the partition table, a verified write, compare,
// erase
func TestJTAGSession(t *testing.T) {
	flash := bytes.Repeat([]byte{0xFF}, 4<<20)
	copy(flash[0x8000:], partitionTable())
	copy(flash[0x10000:], appHead(13, "c6-app", "3.1"))
	sim, err := jtag.NewSim(jtag.SimBoard{Chip: "esp32c6", Flash: flash, SBA: true, MAC: [6]byte{0x40, 0x4c, 0xca, 1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	old := openJTAG
	openJTAG = func(d JTAGDevice, key string, opt jtag.Options) (*jtag.Session, error) {
		opt.Reopen = sim.Reopen
		return jtag.OpenLink(sim.Link(), key, opt)
	}
	defer func() { openJTAG = old }()

	ctx := context.Background()
	s, err := ConnectJTAG(ctx, JTAGDevice{VID: 0x303a, PID: 0x1001, Serial: "C6"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	b := s.Board()
	if b.Chip.Name != "ESP32-C6" || !b.JTAG || b.Summary().MAC != "40:4C:CA:01:02:03" {
		t.Fatalf("card %+v", b.Summary())
	}
	if len(b.Partitions) != 3 || b.Apps["factory"] == nil || b.Apps["factory"].App.ProjectName != "c6-app" {
		t.Fatalf("partitions %v apps %v", b.Partitions, b.Apps)
	}
	data := bytes.Repeat([]byte{0x3C}, 10000)
	if err := s.Flash(ctx, []Item{{Name: "x", Addr: 0x200100, Data: data}}, FlashOptions{Verify: true}, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sim.Flash()[0x200100:0x200100+len(data)], data) || sim.Flash()[0x2000FF] != 0xFF {
		t.Fatal("the write is wrong")
	}
	if diff, err := s.Compare(ctx, 0x200100, data, nil); err != nil || len(diff) != 0 {
		t.Fatalf("Compare: %v %v", diff, err)
	}
	if err := s.ErasePartition(ctx, "nvs"); err != nil {
		t.Fatal(err)
	}
	if p, _ := formats.FindPartition(b.Partitions, formats.PartData, formats.SubDataNVS); sim.Flash()[p.Offset] != 0xFF {
		t.Fatal("nvs not erased")
	}
	if err := s.Close(ctx, true); err != nil {
		t.Fatal(err)
	}
}
