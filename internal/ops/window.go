package ops

import (
	"bytes"
	"io"

	"github.com/ipoluianov/altesp/internal/formats"
)

// What the window needs besides the operations on the board: the image
// files shown without a board, the partition table as CSV, and a board
// with OTA slots for its tests.

// Summary is the partition table and the applications of the image in
// words, as Board.Summary has them
func (im *Image) Summary() Summary {
	s := Board{Partitions: im.Partitions, OTA: im.OTA, Apps: im.Apps}.Summary()
	return Summary{Partitions: s.Partitions, Apps: s.Apps}
}

// ActivePartition is the app partition the bootloader of the image starts
func (im *Image) ActivePartition() (Partition, bool) {
	return ActivePartition(im.Partitions, im.OTA)
}

// ReadPartitionsCSV reads a partition table in the CSV of ESP-IDF; the
// partitions without an offset are placed after the table at tableAddr
func ReadPartitionsCSV(r io.Reader, tableAddr uint32) ([]Partition, error) {
	return formats.ReadPartitionsCSV(r, tableAddr)
}

// WritePartitionsCSV writes the partition table in the CSV of ESP-IDF
func WritePartitionsCSV(w io.Writer, parts []Partition) error {
	return formats.WritePartitionsCSV(w, parts)
}

// CheckPartitions lists what is wrong with a partition table: overlaps,
// alignment, the table itself covered, partitions past the flash
func CheckPartitions(parts []Partition, tableAddr uint32, flashSize int) []string {
	return formats.CheckPartitions(parts, tableAddr, flashSize)
}

// SimulateEmptyBoard is SimulateBoard with a new board: its flash is
// erased
func SimulateEmptyBoard() (end func()) {
	b := c3Board()
	b.Flash = bytes.Repeat([]byte{0xFF}, 4<<20)
	return simulate(b)
}

// SimulateOTABoard is SimulateBoard with a board of two OTA slots: ota_0
// runs "sensor-node 1.4.2", ota_1 holds "sensor-node 1.5.0", the otadata
// is erased (so ota_0 boots)
func SimulateOTABoard() (end func()) {
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
		panic(err)
	}
	copy(b.Flash[0x8000:], table)
	copy(b.Flash[0x110000:], appHead(5, "sensor-node", "1.5.0"))
	return simulate(b)
}
