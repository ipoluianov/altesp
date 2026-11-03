package ops

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/formats/fatfs"
	"github.com/ipoluianov/altesp/internal/formats/littlefs"
	"github.com/ipoluianov/altesp/internal/formats/spiffs"
	"github.com/ipoluianov/altesp/internal/loader"
)

// otaBoard is a simulated C3 with two OTA slots and an erased otadata
func otaBoard(t *testing.T) loader.SimBoard {
	t.Helper()
	b := c3Board()
	parts := []Partition{
		{Name: "nvs", Type: formats.PartData, SubType: formats.SubDataNVS, Offset: 0x9000, Size: 0x4000},
		{Name: "otadata", Type: formats.PartData, SubType: formats.SubDataOTA, Offset: 0xD000, Size: 0x2000},
		{Name: "phy_init", Type: formats.PartData, SubType: 0x01, Offset: 0xF000, Size: 0x1000},
		{Name: "ota_0", Type: formats.PartApp, SubType: formats.SubOTAMin, Offset: 0x10000, Size: 0x100000},
		{Name: "ota_1", Type: formats.PartApp, SubType: formats.SubOTAMin + 1, Offset: 0x110000, Size: 0x100000},
	}
	table, err := formats.BuildPartitionTable(parts)
	if err != nil {
		t.Fatal(err)
	}
	copy(b.Flash[0x8000:], table)
	copy(b.Flash[0x110000:], appHead(5, "second", "2.0"))
	return b
}

func TestOTAAndPartitions(t *testing.T) {
	s, sim := connectSim(t, otaBoard(t), c3Port, Options{})
	ctx := context.Background()
	b := s.Board()
	if act, ok := ActivePartition(b.Partitions, b.OTA); !ok || act.Name != "ota_0" {
		t.Fatalf("active %v %v", act.Name, ok)
	}
	if err := s.SetActiveOTA(ctx, 1); err != nil {
		t.Fatal(err)
	}
	b = s.Board()
	if act, _ := ActivePartition(b.Partitions, b.OTA); act.Name != "ota_1" {
		t.Fatalf("after the switch the active is %s", act.Name)
	}
	sum := b.Summary()
	for _, p := range sum.Partitions {
		if p.Active != (p.Name == "ota_1") {
			t.Errorf("%s active %v", p.Name, p.Active)
		}
	}

	// A partition written, read back, erased
	data := bytes.Repeat([]byte{0xA5}, 0x1800)
	if err := s.WritePartition(ctx, "nvs", data, true, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadPartition(ctx, "nvs", nil)
	if err != nil || !bytes.Equal(got[:len(data)], data) || got[len(data)] != 0xFF {
		t.Fatalf("ReadPartition: %v", err)
	}
	if err := s.WritePartition(ctx, "phy_init", make([]byte, 0x2000), false, nil); err == nil {
		t.Fatal("a file larger than the partition was written")
	}
	if err := s.ErasePartition(ctx, "nvs"); err != nil {
		t.Fatal(err)
	}
	if f := sim.FlashData(); f[0x9000] != 0xFF {
		t.Fatal("nvs not erased")
	}

	// Compare finds exactly the changed block
	img := sim.FlashData()[0x110000:0x118000]
	changed := append([]byte(nil), img...)
	changed[0x2345] ^= 0xFF
	diff, err := s.Compare(ctx, 0x110000, changed, nil)
	if err != nil || len(diff) != 1 || diff[0] != (Range{0x112000, 0x1000}) {
		t.Fatalf("Compare = %v, %v", diff, err)
	}
	// WriteBytes changes one byte and keeps the sector
	if err := s.WriteBytes(ctx, 0x112345, []byte{0x42}); err != nil {
		t.Fatal(err)
	}
	f := sim.FlashData()
	if f[0x112345] != 0x42 || !bytes.Equal(f[0x112000:0x112345], img[0x2000:0x2345]) {
		t.Fatal("WriteBytes broke the sector")
	}

	// A new table: one factory app
	parts := []Partition{{Name: "factory", Type: formats.PartApp, Offset: 0x10000, Size: 0x200000}}
	if err := s.WritePartitionTable(ctx, parts); err != nil {
		t.Fatal(err)
	}
	if b := s.Board(); len(b.Partitions) != 1 || b.Partitions[0].Name != "factory" {
		t.Fatalf("table after writing: %v", b.Partitions)
	}
	if err := s.WritePartitionTable(ctx, []Partition{{Name: "x", Type: formats.PartApp, Offset: 0x11000, Size: 0x1000}}); err == nil {
		t.Fatal("a misaligned table was written")
	}

	// The flash as a file opens like a board
	path := filepath.Join(t.TempDir(), "dump.bin")
	os.WriteFile(path, sim.FlashData(), 0o644)
	im, err := OpenImage(path)
	if err != nil || im.PartitionTableAddr != 0x8000 || len(im.Partitions) != 1 || im.Apps["factory"] == nil {
		t.Fatalf("OpenImage: %+v, %v", im, err)
	}
}

func TestFileSystemOnBoard(t *testing.T) {
	b := otaBoard(t)
	// The nvs partition holds a LittleFS image for this test
	img := make([]byte, 0x4000)
	for i := range img {
		img[i] = 0xFF
	}
	f, err := littlefs.Format(img, littlefs.Config{BlockSize: 4096, BlockCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/config.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	copy(b.Flash[0x9000:], f.Image())
	s, sim := connectSim(t, b, c3Port, Options{})
	ctx := context.Background()
	fs, err := s.ReadFS(ctx, "nvs", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fs.Kind() != "LittleFS" {
		t.Fatalf("kind %s", fs.Kind())
	}
	if err := fs.WriteFile("/config.txt", []byte("changed on the board")); err != nil {
		t.Fatal(err)
	}
	before := sim.Commands[0x11]
	if err := s.WriteFS(ctx, "nvs", fs, nil); err != nil {
		t.Fatal(err)
	}
	if sim.Commands[0x11] == before {
		t.Fatal("nothing was written")
	}
	back, err := s.ReadFS(ctx, "nvs", nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := back.ReadFile("/config.txt"); string(data) != "changed on the board" {
		t.Fatalf("read back %q", data)
	}
}

func TestOpenFSKinds(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		build func(dir string) ([]byte, error)
	}{
		{"SPIFFS", func(dir string) ([]byte, error) { return spiffs.FromDir(dir, 0x40000, spiffs.IDF) }},
		{"FAT", func(dir string) ([]byte, error) { return fatfs.FromDir(dir, 0x100000, fatfs.IDF) }},
		{"LittleFS", func(dir string) ([]byte, error) { return littlefs.FromDir(dir, 0x40000, 4096) }},
	} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one"), 0o644)
		img, err := tc.build(dir)
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		fs, err := OpenFS(img)
		if err != nil || fs.Kind() != tc.kind {
			t.Fatalf("%s opened as %v, %v", tc.kind, fs, err)
		}
		if err := fs.WriteFile("/b.txt", []byte("two")); err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		again, err := OpenFS(fs.Image())
		if err != nil {
			t.Fatal(err)
		}
		if d, _ := again.ReadFile("/a.txt"); string(d) != "one" {
			t.Errorf("%s: a.txt = %q", tc.kind, d)
		}
		if d, _ := again.ReadFile("/b.txt"); string(d) != "two" {
			t.Errorf("%s: b.txt = %q", tc.kind, d)
		}
	}
}

func TestFlashAddrOf(t *testing.T) {
	s, _ := connectSim(t, c3Board(), c3Port, Options{})
	// appHead: one segment loaded at 0x3C000020, 0x100 bytes, data at
	// 0x10000 + 24 + 8 in the factory partition
	got, err := s.FlashAddrOf(context.Background(), 0x3C000030)
	if err != nil || got != 0x10000+32+0x10 {
		t.Fatalf("FlashAddrOf = 0x%X, %v", got, err)
	}
	if _, err := s.FlashAddrOf(context.Background(), 0x40000000); err != ErrNotInApp {
		t.Fatalf("an address outside: %v", err)
	}
}
