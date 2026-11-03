package formats

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// oldApp is an application built before ESP-IDF v3.3: no description, a
// segment mapped from the flash (DROM of the ESP32)
func oldApp() []byte {
	img := make([]byte, 24+8+64)
	img[0], img[1], img[2], img[3] = 0xE9, 1, 2, 0x20
	binary.LittleEndian.PutUint32(img[4:], 0x40080400)
	binary.LittleEndian.PutUint16(img[17:], 0xFFFF)
	binary.LittleEndian.PutUint32(img[24:], 0x3F400020)
	binary.LittleEndian.PutUint32(img[28:], 64)
	sum := byte(0xEF)
	for _, b := range img[32:] {
		sum ^= b
	}
	for (len(img)+1)%16 != 0 {
		img = append(img, 0)
	}
	return append(img, sum)
}

// An application without a description is not a bootloader; the build's
// table tells the kinds of the data files, a table inside at_customize is
// data
func TestBuildKindsByTable(t *testing.T) {
	if k := Detect(oldApp()); k != App {
		t.Fatalf("an old application is %v", k)
	}
	table, err := BuildPartitionTable([]Partition{
		{Name: "phy_init", Type: PartData, SubType: 0x01, Offset: 0xF000, Size: 0x1000},
		{Name: "at_customize", Type: 0x40, SubType: 0, Offset: 0x20000, Size: 0xE0000},
		{Name: "ota_0", Type: PartApp, SubType: SubOTAMin, Offset: 0x100000, Size: 0x180000},
	})
	if err != nil {
		t.Fatal(err)
	}
	inner, _ := BuildPartitionTable([]Partition{{Name: "x", Type: PartData, SubType: 0x81, Offset: 0x21000, Size: 0x1000}})
	dir := t.TempDir()
	files := map[string][]byte{"partition-table.bin": table, "phy_init.bin": {1, 2, 3, 4}, "at_customize.bin": inner, "ota_0.bin": oldApp()}
	for n, d := range files {
		os.WriteFile(filepath.Join(dir, n), d, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "flasher_args.json"), []byte(`{"flash_files": {"0x8000": "partition-table.bin", "0xf000": "phy_init.bin", "0x20000": "at_customize.bin", "0x100000": "ota_0.bin"}}`), 0o644)
	b, err := LoadBuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32]Kind{0x8000: PartitionTable, 0xF000: PHYInit, 0x20000: Data, 0x100000: App}
	for _, f := range b.Files {
		if f.Kind != want[f.Addr] {
			t.Errorf("0x%X: %v, want %v", f.Addr, f.Kind, want[f.Addr])
		}
	}
}
