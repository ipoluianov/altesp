package formats

import (
	"bytes"
	"crypto/md5"
	"errors"
	"fmt"
)

// The partition table: esp_partition_info_t of esp_flash_partitions.h
// (ESP-IDF master, Nov 2026) and the Programming Guide "Partition Tables".
// Entries of 32 bytes: magic 0x50AA, type, subtype, offset, size, label
// (16 bytes), flags; up to 0xC00 bytes, ended by an erased entry. gen_
// esp32part adds an entry with magic 0xEBEB whose bytes 16..31 are the
// MD5 of the entries before it.
const (
	partMagic    = 0x50AA // ESP_PARTITION_MAGIC
	partMagicMD5 = 0xEBEB // ESP_PARTITION_MAGIC_MD5
	partEntryLen = 32
	partMD5Off   = 16    // ESP_PARTITION_MD5_OFFSET
	partTableMax = 0xC00 // ESP_PARTITION_TABLE_MAX_LEN
	sectorSize   = 0x1000
)

// Partition types and subtypes: esp_partition.h (ESP-IDF master, Nov 2026)
const (
	PartApp            = 0x00
	PartData           = 0x01
	PartBootloader     = 0x02
	PartPartitionTable = 0x03

	SubFactory = 0x00
	SubOTAMin  = 0x10 // ota_0..ota_15
	SubOTAMax  = 0x1F
	SubTest    = 0x20

	SubDataOTA      = 0x00
	SubDataNVS      = 0x02
	SubDataFAT      = 0x81
	SubDataSPIFFS   = 0x82
	SubDataLittleFS = 0x83
)

// Partition flags: PART_FLAG_* of esp_flash_partitions.h
const (
	FlagEncrypted = 1 << 0
	FlagReadOnly  = 1 << 1
)

// Partition is an entry of the partition table
type Partition struct {
	Name          string
	Type, SubType byte
	Offset, Size  uint32
	Flags         uint32
}

// TypeName is the type as in a partition CSV
func (p Partition) TypeName() string {
	switch p.Type {
	case PartApp:
		return "app"
	case PartData:
		return "data"
	case PartBootloader:
		return "bootloader"
	case PartPartitionTable:
		return "partition_table"
	}
	return fmt.Sprintf("0x%02X", p.Type)
}

// dataSubtypes: esp_partition_subtype_t of esp_partition.h; the names are
// those of the partition CSV (Programming Guide "Partition Tables")
var dataSubtypes = map[byte]string{
	0x00: "ota",
	0x01: "phy",
	0x02: "nvs",
	0x03: "coredump",
	0x04: "nvs_keys",
	0x05: "efuse",
	0x06: "undefined",
	0x80: "esphttpd",
	0x81: "fat",
	0x82: "spiffs",
	0x83: "littlefs",
	0x90: "tee_ota",
}

// SubTypeName is the subtype as in a partition CSV
func (p Partition) SubTypeName() string {
	s := p.SubType
	switch p.Type {
	case PartApp:
		switch {
		case s == SubFactory:
			return "factory"
		case s >= SubOTAMin && s <= SubOTAMax:
			return fmt.Sprintf("ota_%d", s-SubOTAMin)
		case s == SubTest:
			return "test"
		case s == 0x30 || s == 0x31:
			return fmt.Sprintf("tee_%d", s-0x30)
		}
	case PartData:
		if n, ok := dataSubtypes[s]; ok {
			return n
		}
	case PartBootloader:
		switch s {
		case 0:
			return "primary"
		case 1:
			return "ota"
		case 2:
			return "recovery"
		}
	case PartPartitionTable:
		switch s {
		case 0:
			return "primary"
		case 1:
			return "ota"
		}
	}
	return fmt.Sprintf("0x%02X", s)
}

// IsApp tells an application partition
func (p Partition) IsApp() bool { return p.Type == PartApp }

// IsOTA tells an OTA application slot
func (p Partition) IsOTA() bool {
	return p.Type == PartApp && p.SubType >= SubOTAMin && p.SubType <= SubOTAMax
}

// ErrNoTable is data without a partition table
var ErrNoTable = errors.New("no partition table")

// ParsePartitionTable reads the partition table at the start of data. The
// MD5 entry, when there is one, must match.
func ParsePartitionTable(data []byte) ([]Partition, error) {
	var parts []Partition
	n := min(len(data), partTableMax) / partEntryLen
	for i := 0; i < n; i++ {
		e := data[i*partEntryLen : (i+1)*partEntryLen]
		switch le16(e) {
		case partMagic:
			parts = append(parts, Partition{
				Type:    e[2],
				SubType: e[3],
				Offset:  le32(e[4:]),
				Size:    le32(e[8:]),
				Name:    cString(e[12:28]),
				Flags:   le32(e[28:]),
			})
			continue
		case partMagicMD5:
			sum := md5.Sum(data[:i*partEntryLen])
			if !bytes.Equal(sum[:], e[partMD5Off:]) {
				return nil, errors.New("the MD5 of the partition table does not match")
			}
		case 0xFFFF:
			if !allFF(e) {
				return nil, fmt.Errorf("partition table entry %d is damaged", i)
			}
		default:
			if len(parts) == 0 {
				return nil, ErrNoTable
			}
			return nil, fmt.Errorf("partition table entry %d is damaged", i)
		}
		break
	}
	if len(parts) == 0 {
		return nil, ErrNoTable
	}
	return parts, nil
}

// FindPartitionTable finds the table in flash data read from 0 (a merged
// image or a flash dump) without assuming 0x8000: CONFIG_PARTITION_TABLE_
// OFFSET is any sector after the bootloader. Returns its offset.
func FindPartitionTable(flash []byte) (uint32, bool) {
	for off := sectorSize; off+partEntryLen <= len(flash); off += sectorSize {
		if le16(flash[off:]) != partMagic {
			continue
		}
		if _, err := ParsePartitionTable(flash[off:min(off+partTableMax, len(flash))]); err == nil {
			return uint32(off), true
		}
	}
	return 0, false
}

// AppPartition is where a single application image goes: factory, else
// the first OTA app partition (ota_0 normally)
func AppPartition(parts []Partition) (Partition, bool) {
	var ota *Partition
	for i, p := range parts {
		if p.Type != PartApp {
			continue
		}
		if p.SubType == SubFactory {
			return p, true
		}
		if p.IsOTA() && (ota == nil || p.SubType < ota.SubType) {
			ota = &parts[i]
		}
	}
	if ota != nil {
		return *ota, true
	}
	return Partition{}, false
}

// FindPartition is the first partition of a type and subtype
func FindPartition(parts []Partition, typ, sub byte) (Partition, bool) {
	for _, p := range parts {
		if p.Type == typ && p.SubType == sub {
			return p, true
		}
	}
	return Partition{}, false
}
