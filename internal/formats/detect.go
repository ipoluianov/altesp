package formats

import (
	"bytes"
	"hash/crc32"
)

// Kind is what a file or a partition holds
type Kind int

const (
	Unknown Kind = iota
	Bootloader
	App
	PartitionTable
	Merged
	OTAData
	NVS
	LittleFS
	SPIFFS
	FAT
	// PHYInit and Data are told by the partition a file is for, not by
	// its content: the radio calibration, other data of the firmware
	PHYInit
	Data
)

var kindNames = [...]string{
	Unknown:        "Unknown",
	Bootloader:     "Bootloader",
	App:            "Application",
	PartitionTable: "Partition table",
	Merged:         "Merged image",
	OTAData:        "OTA data",
	NVS:            "NVS",
	LittleFS:       "LittleFS",
	SPIFFS:         "SPIFFS",
	FAT:            "FAT",
	PHYInit:        "PHY calibration",
	Data:           "Data",
}

func (k Kind) String() string {
	if k >= 0 && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "Unknown"
}

// Detect tells what a file is by its content (magic bytes, headers), not
// its name. A merged image (or a flash dump) is Merged.
func Detect(data []byte) Kind {
	if len(data) == 0 {
		return Unknown
	}
	if off, im := bootloaderAt(data); im != nil {
		// A bootloader with a partition table after it is a merged image;
		// one after erased flash is too, whatever follows
		if off > 0 {
			return Merged
		}
		if t, ok := FindPartitionTable(data); ok && int(t) >= im.Size {
			return Merged
		}
		return Bootloader
	}
	if data[0] == imageMagic {
		if _, err := ParseImage(data); err == nil {
			return App
		}
	}
	switch {
	case isPartitionTable(data):
		return PartitionTable
	case isOTAData(data):
		return OTAData
	case isNVS(data):
		return NVS
	case isLittleFS(data):
		return LittleFS
	case isFAT(data):
		return FAT
	case isSPIFFS(data):
		return SPIFFS
	}
	return Unknown
}

func isPartitionTable(data []byte) bool {
	if len(data) < partEntryLen || le16(data) != partMagic {
		return false
	}
	_, err := ParsePartitionTable(data)
	return err == nil
}

// espCRC32 is esp_rom_crc32_le(0xFFFFFFFF, ...) of ESP-IDF: the helpers
// invert the register before and after (esp_rom_crc.h, ESP-IDF master,
// Nov 2026), so the register starts at 0, unlike the usual CRC-32
func espCRC32(b []byte) uint32 {
	return crc32.Update(0xFFFFFFFF, crc32.IEEETable, b)
}

// OTA data: two sectors, each starting with esp_ota_select_entry_t
// (esp_flash_partitions.h): ota_seq, seq_label[20], ota_state and the
// CRC of ota_seq. ESP-IDF writes no label; the rest of the sector stays
// erased. The initial image of a build is all erased.
const otaDataLen = 2 * sectorSize

func isOTAData(data []byte) bool {
	if len(data) != otaDataLen {
		return false
	}
	for s := 0; s < otaDataLen; s += sectorSize {
		sec := data[s : s+sectorSize]
		if !allFF(sec[4:24]) || !allFF(sec[32:]) {
			return false
		}
	}
	return true
}

// OTAEntryValid tells whether an otadata entry (32 bytes) has the right
// CRC, i.e. selects an OTA slot
func OTAEntryValid(e []byte) bool {
	return len(e) >= 32 && le32(e) != 0xFFFFFFFF && espCRC32(e[:4]) == le32(e[28:])
}

// NVS: pages of 4096 bytes with a 32-byte header (nvs_page.hpp and
// nvs_constants.h, ESP-IDF master, Nov 2026): the state, the sequence
// number, the version (0xFE for v2, 0xFF for v1), 19 reserved bytes of
// 0xFF and a CRC. The states clear one bit after another from the erased
// 0xFFFFFFFF.
const (
	nvsPageSize = 4096
	nvsVersion2 = 0xFE
	nvsVersion1 = 0xFF
)

var nvsStates = []uint32{0xFFFFFFFE, 0xFFFFFFFC, 0xFFFFFFF8, 0xFFFFFFF0} // active, full, freeing, corrupt

func isNVS(data []byte) bool {
	if len(data) < nvsPageSize || len(data)%nvsPageSize != 0 {
		return false
	}
	used := 0
	for p := 0; p < len(data); p += nvsPageSize {
		h := data[p : p+32]
		st := le32(h)
		if st == 0xFFFFFFFF {
			if !allFF(h) {
				return false
			}
			continue
		}
		known := false
		for _, s := range nvsStates {
			known = known || st == s
		}
		if !known || (h[8] != nvsVersion2 && h[8] != nvsVersion1) || !allFF(h[9:28]) {
			return false
		}
		used++
	}
	return used > 0
}

// LittleFS (littlefs SPEC.md): the superblock is the first entry of the
// metadata pair in blocks 0 and 1, so "littlefs" is at offset 8 of a
// block. The block size is not known before; the common ones are tried.
var littlefsMagic = []byte("littlefs")

func isLittleFS(data []byte) bool {
	for _, bs := range []int{0, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536} {
		if bs+16 <= len(data) && bytes.Equal(data[bs+8:bs+16], littlefsMagic) {
			return true
		}
	}
	return false
}

// FAT: a boot sector (jump, bytes per sector, sectors per cluster, FAT
// count, 0x55AA at 510). Under the ESP-IDF wear levelling layer the
// sectors are shifted by the moving dummy sector (wear_levelling,
// WL_Flash), so the boot sector is looked for at every 4 KB.
func isFAT(data []byte) bool {
	for off := 0; off+512 <= len(data); off += sectorSize {
		if isFATBoot(data[off : off+512]) {
			return true
		}
	}
	return false
}

func isFATBoot(b []byte) bool {
	if b[510] != 0x55 || b[511] != 0xAA {
		return false
	}
	if !(b[0] == 0xEB && b[2] == 0x90) && b[0] != 0xE9 {
		return false
	}
	switch le16(b[11:]) {
	case 512, 1024, 2048, 4096:
	default:
		return false
	}
	spc := b[13]
	return spc != 0 && spc&(spc-1) == 0 && le16(b[14:]) >= 1 && (b[16] == 1 || b[16] == 2)
}

// SPIFFS has no superblock (spiffs, MIT, docs/TECH_SPEC): best effort for
// the ESP-IDF and Arduino geometry, 4 KB blocks of 256-byte pages. The
// first page of a block is the lookup table, a 16-bit object ID per page
// of the block after it (0xFFFF free, 0 deleted); a page in use starts
// with the same object ID in its header.
const (
	spiffsPage  = 256
	spiffsBlock = 4096
)

func isSPIFFS(data []byte) bool {
	if len(data) < spiffsBlock || len(data)%spiffsBlock != 0 {
		return false
	}
	used := 0
	for blk := 0; blk < len(data); blk += spiffsBlock {
		lu := data[blk : blk+spiffsPage]
		for j := 0; j < spiffsBlock/spiffsPage-1; j++ {
			id := le16(lu[2*j:])
			if id == 0xFFFF || id == 0 {
				continue
			}
			if le16(data[blk+(j+1)*spiffsPage:]) != id {
				return false
			}
			used++
		}
	}
	return used > 0
}
