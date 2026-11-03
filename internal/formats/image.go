// Package formats reads what is written to the flash of an ESP chip: the
// application and bootloader images, the partition table, merged images
// and the build folders of ESP-IDF, PlatformIO and Arduino. It also tells
// the data partitions apart (NVS, OTA data, LittleFS, SPIFFS, FAT).
//
// Sources: ESP-IDF (Apache-2.0) headers esp_app_format.h, esp_app_desc.h,
// esp_bootloader_desc.h, esp_flash_partitions.h, esp_partition.h, the NVS
// and wear levelling headers, and the Programming Guide (partition tables,
// NVS, Secure Boot v2); the esptool documentation "Firmware Image Format";
// the littlefs SPEC.md. The image parser is adapted from AltHex
// (core/fw/esp.go, same author, MIT). No esptool code is used.
package formats

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// The image format: esp_app_format.h of ESP-IDF master (Nov 2026) and the
// esptool documentation "Firmware Image Format". A header of 8 bytes, the
// extended header of 16, segments (load address, length, data), zeros to
// the last byte of a 16-byte block, the checksum byte there and, when the
// extended header says so, the SHA-256 of all that.
const (
	imageMagic     = 0xE9 // ESP_IMAGE_HEADER_MAGIC
	headerLen      = 8
	extHeaderLen   = 16
	imageHeaderLen = headerLen + extHeaderLen // sizeof(esp_image_header_t) == 24
	segHeaderLen   = 8                        // esp_image_segment_header_t
	maxSegments    = 16                       // ESP_IMAGE_MAX_SEGMENTS
	checksumSeed   = 0xEF
)

// The application description, esp_app_desc_t of esp_app_desc.h (ESP-IDF
// master, Nov 2026): 256 bytes at the start of the first segment
const (
	appDescMagic = 0xABCD5432 // ESP_APP_DESC_MAGIC_WORD
	appDescLen   = 256
)

// AppHeadSize is how much of an application image ParseAppHead needs: the
// headers, the first segment header and the app description
const AppHeadSize = imageHeaderLen + segHeaderLen + appDescLen // 0x120

// bootDescMagic is the first byte of esp_bootloader_desc_t
// (esp_bootloader_desc.h, ESP-IDF master, Nov 2026), in the bootloader's
// DRAM since IDF v5.2
const bootDescMagic = 80 // ESP_BOOTLOADER_DESC_MAGIC_BYTE

// ErrFormat is data that is not what it was parsed as
var ErrFormat = errors.New("not a valid image")

// Segment is a segment of an image: its load address, length and the
// offset of its data in the image
type Segment struct{ Addr, Size, Offset uint32 }

// AppDesc is the application description (esp_app_desc_t) of an image
type AppDesc struct {
	ProjectName, Version, IDFVersion, Date, Time string
	ELFSHA256                                    [32]byte
	SecureVersion                                uint32
}

// Image is a parsed application or bootloader image
type Image struct {
	ChipID                   uint16 // from the extended header
	MinRevFull, MaxRevFull   uint16 // major*100+minor; 0 / 0xFFFF when not set
	Entry                    uint32
	FlashMode, FlashFreqSize byte // header bytes 2 and 3
	Segments                 []Segment
	HashAppended             bool
	ChecksumOK               bool
	HashOK                   bool     // false when no hash appended
	Size                     int      // bytes of the image incl. checksum/hash padding
	Signed                   bool     // a secure boot signature block follows (best effort)
	App                      *AppDesc // nil when not an application
	IsBootloader             bool
}

// chip is what this package needs to know of a chip
type chip struct {
	id   uint16
	key  string
	boot uint32      // where the bootloader is in the flash
	maps [][2]uint32 // the address ranges the flash is mapped to
}

// chips: the IDs are esp_chip_id_t of esp_app_format.h; the bootloader
// offsets ESP_ROM_BOOTLOADER_OFFSET_FLASH of components/esp_rom/<chip>/
// Kconfig.soc_caps.in; the flash-mapped ranges SOC_DROM_LOW..HIGH and
// SOC_IROM_LOW..HIGH of components/soc/<chip>/include/soc/soc.h (all ESP-IDF
// master, Nov 2026). The C6 and H2 map up to 256 MMU pages; their largest
// (64 KB) page is taken.
var chips = []chip{
	{0x0000, "esp32", 0x1000, [][2]uint32{{0x3F400000, 0x3F800000}, {0x400D0000, 0x40400000}}},
	{0x0002, "esp32s2", 0x1000, [][2]uint32{{0x3F000000, 0x3FF80000}, {0x40080000, 0x40800000}}},
	{0x0005, "esp32c3", 0, [][2]uint32{{0x3C000000, 0x3C800000}, {0x42000000, 0x42800000}}},
	{0x0009, "esp32s3", 0, [][2]uint32{{0x3C000000, 0x3E000000}, {0x42000000, 0x44000000}}},
	{0x000C, "esp32c2", 0, [][2]uint32{{0x3C000000, 0x3C400000}, {0x42000000, 0x42400000}}},
	{0x000D, "esp32c6", 0, [][2]uint32{{0x42000000, 0x43000000}}},
	{0x0010, "esp32h2", 0, [][2]uint32{{0x42000000, 0x43000000}}},
	{0x0012, "esp32p4", 0x2000, [][2]uint32{{0x40000000, 0x44000000}}},
	{0x0014, "esp32c61", 0, [][2]uint32{{0x42000000, 0x44000000}}},
	{0x0017, "esp32c5", 0x2000, [][2]uint32{{0x42000000, 0x44000000}}},
	{0x0019, "esp32h21", 0, [][2]uint32{{0x42000000, 0x43000000}}},
	{0x001C, "esp32h4", 0x2000, [][2]uint32{{0x42000000, 0x44000000}}},
	{0x0020, "esp32s31", 0x2000, [][2]uint32{{0x40000000, 0x44000000}}},
}

func chipByID(id uint16) (chip, bool) {
	for _, c := range chips {
		if c.id == id {
			return c, true
		}
	}
	return chip{}, false
}

func chipByKey(key string) (chip, bool) {
	for _, c := range chips {
		if c.key == key {
			return c, true
		}
	}
	return chip{}, false
}

// ChipKey is the chip key ("esp32s3") of a chip ID of the image header, ""
// for an ID this package doesn't know
func ChipKey(id uint16) string {
	c, _ := chipByID(id)
	return c.key
}

// flashMapped tells whether a load address is in the flash mapped into the
// address space of the chip: only applications have such segments
func (c chip) flashMapped(addr uint32) bool {
	for _, r := range c.maps {
		if addr >= r[0] && addr < r[1] {
			return true
		}
	}
	return false
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

// parseHeader checks the header and the extended header
func parseHeader(data []byte) (*Image, chip, error) {
	if len(data) < imageHeaderLen || data[0] != imageMagic {
		return nil, chip{}, ErrFormat
	}
	n := int(data[1])
	if n == 0 || n > maxSegments {
		return nil, chip{}, fmt.Errorf("%w: %d segments", ErrFormat, n)
	}
	x := data[headerLen:imageHeaderLen]
	c, ok := chipByID(le16(x[4:]))
	if !ok {
		return nil, chip{}, fmt.Errorf("%w: chip ID 0x%04X", ErrFormat, le16(x[4:]))
	}
	for _, b := range x[11:15] {
		if b != 0 {
			return nil, chip{}, fmt.Errorf("%w: reserved bytes of the extended header", ErrFormat)
		}
	}
	if x[15] > 1 {
		return nil, chip{}, fmt.Errorf("%w: hash_appended %d", ErrFormat, x[15])
	}
	im := &Image{
		ChipID:        c.id,
		MinRevFull:    le16(x[7:]),
		MaxRevFull:    le16(x[9:]),
		Entry:         le32(data[4:]),
		FlashMode:     data[2],
		FlashFreqSize: data[3],
		HashAppended:  x[15] == 1,
	}
	return im, c, nil
}

// ParseImage parses an application or bootloader image at the start of
// data; what follows the image (a signature, the rest of a flash dump)
// is allowed
func ParseImage(data []byte) (*Image, error) {
	im, c, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	off := imageHeaderLen
	sum := byte(checksumSeed)
	for i := 0; i < int(data[1]); i++ {
		if len(data)-off < segHeaderLen {
			return nil, fmt.Errorf("%w: segment %d is cut", ErrFormat, i)
		}
		addr, n := le32(data[off:]), le32(data[off+4:])
		off += segHeaderLen
		if uint64(n) > uint64(len(data)-off) {
			return nil, fmt.Errorf("%w: segment %d of %d bytes goes beyond the data", ErrFormat, i, n)
		}
		im.Segments = append(im.Segments, Segment{Addr: addr, Size: n, Offset: uint32(off)})
		for _, b := range data[off : off+int(n)] {
			sum ^= b
		}
		off += int(n)
	}
	// Zeros to the last byte of a 16-byte block, the checksum there
	csum := off | 15
	if csum >= len(data) {
		return nil, fmt.Errorf("%w: no checksum", ErrFormat)
	}
	im.ChecksumOK = data[csum] == sum
	im.Size = csum + 1
	if im.HashAppended {
		if len(data)-im.Size < sha256.Size {
			return nil, fmt.Errorf("%w: the SHA-256 is cut", ErrFormat)
		}
		h := sha256.Sum256(data[:im.Size])
		im.HashOK = bytes.Equal(h[:], data[im.Size:im.Size+sha256.Size])
		im.Size += sha256.Size
	}
	im.Signed = signed(data, im.Size)
	if s := im.Segments[0]; s.Size >= appDescLen {
		im.App = parseAppDesc(data[s.Offset : s.Offset+appDescLen])
	}
	if im.App == nil {
		im.IsBootloader = isBootloader(data, im, c)
	}
	return im, nil
}

// ParseAppHead reads the image header, the extended header and the
// esp_app_desc_t from the first bytes of an application image (at least
// AppHeadSize). ok is false when it is not an application image (erased
// flash, encrypted data, a bootloader); App is nil for an application
// without a description. Segments, checksum and hash are left zero.
func ParseAppHead(head []byte) (img *Image, ok bool) {
	if len(head) < AppHeadSize {
		return nil, false
	}
	im, c, err := parseHeader(head)
	if err != nil {
		return nil, false
	}
	d := imageHeaderLen + segHeaderLen
	if le32(head[imageHeaderLen+4:]) >= appDescLen {
		im.App = parseAppDesc(head[d : d+appDescLen])
	}
	// An application built before ESP-IDF v3.3 has no esp_app_desc_t: it
	// is still an application, with App nil, if its first segment loads
	// into the flash-mapped range (a bootloader loads into RAM)
	if im.App == nil && !c.flashMapped(le32(head[imageHeaderLen:])) {
		return nil, false
	}
	return im, true
}

// parseAppDesc reads esp_app_desc_t; nil without its magic word
func parseAppDesc(b []byte) *AppDesc {
	if len(b) < appDescLen || le32(b) != appDescMagic {
		return nil
	}
	a := &AppDesc{
		SecureVersion: le32(b[4:]),
		Version:       cString(b[16:48]),
		ProjectName:   cString(b[48:80]),
		Time:          cString(b[80:96]),
		Date:          cString(b[96:112]),
		IDFVersion:    cString(b[112:144]),
	}
	copy(a.ELFSHA256[:], b[144:176])
	return a
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// isBootloader tells an image without an app description: a bootloader
// runs from RAM only, so none of its segments is in the mapped flash; an
// IDF v5.2+ bootloader also starts a segment with esp_bootloader_desc_t
func isBootloader(data []byte, im *Image, c chip) bool {
	if _, ok := HasBootloaderDesc(data, im); ok {
		return true
	}
	for _, s := range im.Segments {
		if c.flashMapped(s.Addr) {
			return false
		}
	}
	return true
}

// HasBootloaderDesc tells whether a segment of the bootloader image starts
// with esp_bootloader_desc_t (IDF v5.2 and later), and gives its IDF
// version: magic byte 80, then at offset 8 "v..." (idf_ver[32])
func HasBootloaderDesc(data []byte, im *Image) (idfVer string, ok bool) {
	for _, s := range im.Segments {
		if s.Size < 80 {
			continue
		}
		b := data[s.Offset : s.Offset+80]
		if b[0] == bootDescMagic && b[8] == 'v' {
			return cString(b[8:40]), true
		}
	}
	return "", false
}

// Secure Boot v2 (Programming Guide, "Secure Boot v2" → "Signature Block
// Format", ESP-IDF master, Nov 2026): the signature block starts on the
// 4 KB boundary after the image with the magic byte 0xE7, the version
// (0x02 RSA, 0x03 ECDSA) and two zero bytes. Secure Boot v1 (ESP32
// apps): 68 bytes after the image, a version word 0 and the signature;
// that is told only when they end the data exactly.
const (
	sigBlockAlign = 0x1000
	sigBlockMagic = 0xE7
	sigV1Len      = 68
)

func signed(data []byte, size int) bool {
	at := (size + sigBlockAlign - 1) &^ (sigBlockAlign - 1)
	if at+4 <= len(data) {
		b := data[at:]
		if b[0] == sigBlockMagic && (b[1] == 2 || b[1] == 3) && b[2] == 0 && b[3] == 0 {
			return true
		}
	}
	return len(data) == size+sigV1Len && le32(data[size:]) == 0
}

// bootloaderAt finds the bootloader in flash data read from 0 (or in a
// merged image): at 0 or, after erased flash, at the offset of the chips
// whose ROM loads it from 0x1000 or 0x2000
func bootloaderAt(data []byte) (int, *Image) {
	for _, off := range []int{0, 0x1000, 0x2000} {
		if off >= len(data) || data[off] != imageMagic || !allFF(data[:off]) {
			continue
		}
		// An application built before ESP-IDF v3.3 has no description
		// either: a bootloader also maps nothing from the flash
		if im, err := ParseImage(data[off:]); err == nil && im.App == nil && im.IsBootloader {
			return off, im
		}
	}
	return -1, nil
}

// MergedAddr is the flash address a merged image is written at: 0 for the
// output of a merge that starts at 0, else the bootloader offset of the
// chip less where the bootloader is in the file
func MergedAddr(data []byte) uint32 {
	off, im := bootloaderAt(data)
	if im == nil {
		return 0
	}
	c, _ := chipByID(im.ChipID)
	if int(c.boot) > off {
		return c.boot - uint32(off)
	}
	return 0
}

// PatchHeader rewrites flash mode / freq+size (bytes 2 and 3 of the
// header) of a bootloader image, or of the bootloader in a merged image,
// and recalculates the appended SHA-256. The checksum covers only the
// segment data, so it stays. A signed image is refused: its signature
// covers the header. So is a damaged one, whose hash would be made valid.
// nil arguments keep the byte. The result is a copy.
func PatchHeader(img []byte, mode, freqSize *byte) ([]byte, error) {
	off, im := bootloaderAt(img)
	if im == nil {
		var err error
		if im, err = ParseImage(img); err != nil {
			return nil, err
		}
		off = 0
	}
	if im.Signed {
		return nil, errors.New("the image is signed for Secure Boot: changing its header would break the signature")
	}
	if !im.ChecksumOK || im.HashAppended && !im.HashOK {
		return nil, errors.New("the image is damaged: its checksum or SHA-256 does not match")
	}
	out := append([]byte(nil), img...)
	b := out[off:]
	if mode != nil {
		b[2] = *mode
	}
	if freqSize != nil {
		b[3] = *freqSize
	}
	if im.HashAppended {
		end := im.Size - sha256.Size
		h := sha256.Sum256(b[:end])
		copy(b[end:], h[:])
	}
	return out, nil
}

func allFF(b []byte) bool {
	for _, c := range b {
		if c != 0xFF {
			return false
		}
	}
	return true
}
