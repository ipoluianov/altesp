package ops

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ipoluianov/altesp/internal/formats"
)

// The parts saved are a build folder: flashed back onto an erased board
// they boot the same partitions
func TestExportParts(t *testing.T) {
	s, sim := connectSim(t, c3Board(), c3Port, Options{})
	ctx := context.Background()
	files, _, err := s.Export(ctx, ExportOptions{Kind: ExportParts, UserData: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]uint32{}
	for _, f := range files {
		names[f.Name] = f.Addr
	}
	for name, addr := range map[string]uint32{"bootloader.bin": 0, "partition-table.bin": 0x8000, "factory.bin": 0x10000} {
		if got, ok := names[name]; !ok || got != addr {
			t.Errorf("%s: 0x%X %v (files %v)", name, got, ok, names)
		}
	}
	// An erased data partition is left out
	if _, ok := names["nvs.bin"]; ok {
		t.Error("the erased nvs is saved")
	}
	dir, err := s.WriteExport(files, ExportParts, t.TempDir(), "esp32-c3_board")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != "esp32-c3_board" {
		t.Error("folder", dir)
	}
	b, err := formats.LoadBuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	var items []Item
	for _, f := range b.Files {
		data, _ := os.ReadFile(f.Path)
		items = append(items, Item{Name: filepath.Base(f.Path), Addr: f.Addr, Data: data})
	}
	orig := sim.FlashData()
	if err := s.EraseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Flash(ctx, items, FlashOptions{Verify: true}, nil); err != nil {
		t.Fatal(err)
	}
	got := sim.FlashData()
	for _, r := range [][2]int{{0x8000, 0xC00}, {0x10000, 0x120}} {
		if !bytes.Equal(got[r[0]:r[0]+r[1]], orig[r[0]:r[0]+r[1]]) {
			t.Errorf("0x%X differs after flashing the parts back", r[0])
		}
	}
	if s.Board().Firmware != FirmwarePresent {
		t.Error("firmware", s.Board().Firmware)
	}
	// A second export goes into name-2
	dir2, err := s.WriteExport(files, ExportParts, filepath.Dir(dir), "esp32-c3_board")
	if err != nil || filepath.Base(dir2) != "esp32-c3_board-2" {
		t.Fatal("second folder", dir2, err)
	}
}

// Trimmed: from 0x0 to the last byte that isn't FF; App: the application
// that boots, cut to its image
func TestExportTrimmedAndApp(t *testing.T) {
	s, sim := connectSim(t, c3Board(), c3Port, Options{})
	ctx := context.Background()
	files, _, err := s.Export(ctx, ExportOptions{Kind: ExportTrimmed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	flash := sim.FlashData()
	last := len(flash) - 1
	for last >= 0 && flash[last] == 0xFF {
		last--
	}
	if len(files) != 1 || len(files[0].Data) != last+1 || !bytes.Equal(files[0].Data, flash[:last+1]) {
		t.Fatalf("trimmed: %d bytes, want %d", len(files[0].Data), last+1)
	}
	files, _, err = s.Export(ctx, ExportOptions{Kind: ExportApp}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Name != "factory.bin" || files[0].Addr != 0x10000 || files[0].Data[0] != 0xE9 || len(files[0].Data) > 0x1000 {
		t.Fatalf("app: %s at 0x%X, %d bytes", files[0].Name, files[0].Addr, len(files[0].Data))
	}
}
