package formats

import (
	"bytes"
	"strings"
	"testing"
)

// The single-app table of ESP-IDF (partitions_singleapp.csv)
const singleApp = `# Name,   Type, SubType, Offset,  Size, Flags
nvs,      data, nvs,     ,        0x6000,
phy_init, data, phy,     ,        0x1000,
factory,  app,  factory, ,        1M,
`

func TestPartitionsCSV(t *testing.T) {
	parts, err := ReadPartitionsCSV(strings.NewReader(singleApp), 0x8000)
	if err != nil {
		t.Fatal(err)
	}
	want := []Partition{
		{Name: "nvs", Type: PartData, SubType: SubDataNVS, Offset: 0x9000, Size: 0x6000},
		{Name: "phy_init", Type: PartData, SubType: 0x01, Offset: 0xF000, Size: 0x1000},
		{Name: "factory", Type: PartApp, SubType: SubFactory, Offset: 0x10000, Size: 1 << 20},
	}
	if len(parts) != len(want) {
		t.Fatalf("%d partitions", len(parts))
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, parts[i], want[i])
		}
	}
	if p := CheckPartitions(parts, 0x8000, 4<<20); len(p) != 0 {
		t.Errorf("problems with the default table: %v", p)
	}
	// Binary and CSV round trips
	bin, err := BuildPartitionTable(parts)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParsePartitionTable(bin)
	if err != nil || len(back) != 3 || back[2] != want[2] {
		t.Fatalf("parsed back %v, %v", back, err)
	}
	if Detect(bin) != PartitionTable {
		t.Fatalf("detected as %v", Detect(bin))
	}
	var csv bytes.Buffer
	if err := WritePartitionsCSV(&csv, parts); err != nil {
		t.Fatal(err)
	}
	again, err := ReadPartitionsCSV(&csv, 0x8000)
	if err != nil || len(again) != 3 || again[0] != want[0] {
		t.Fatalf("CSV round trip: %v, %v\n%s", again, err, csv.String())
	}
}

func TestCheckPartitions(t *testing.T) {
	parts := []Partition{
		{Name: "nvs", Type: PartData, SubType: SubDataNVS, Offset: 0x8000, Size: 0x6000},   // over the table
		{Name: "app", Type: PartApp, SubType: SubFactory, Offset: 0x11000, Size: 0x100000}, // misaligned
		{Name: "app", Type: PartApp, SubType: SubOTAMin, Offset: 0x100000, Size: 0x400000}, // same name, overlaps, too big
	}
	text := strings.Join(CheckPartitions(parts, 0x8000, 4<<20), "\n")
	for _, want := range []string{"overlaps the partition table", "not aligned", "used twice", "app overlaps app", "past the end"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
}

func TestOTASelect(t *testing.T) {
	data := bytes.Repeat([]byte{0xFF}, 2*sectorSize)
	cur := ParseOTAData(data)
	if _, ok := cur.Slot(2); ok {
		t.Fatal("an erased otadata selects a slot")
	}
	// Select slot 1, then slot 0, then slot 0 again
	for _, slot := range []int{1, 0, 0} {
		sector, content, err := SelectOTA(cur, slot, 2)
		if err != nil {
			t.Fatal(err)
		}
		copy(data[sector*sectorSize:], content)
		cur = ParseOTAData(data)
		if got, ok := cur.Slot(2); !ok || got != slot {
			t.Fatalf("selected %d, ok %v; want %d", got, ok, slot)
		}
		if Detect(data) != OTAData {
			t.Fatalf("the otadata is detected as %v", Detect(data))
		}
	}
	if _, _, err := SelectOTA(cur, 2, 2); err == nil {
		t.Fatal("slot 2 of 2 selected")
	}
}
