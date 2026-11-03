package ops

import (
	"fmt"
	"sort"

	"github.com/ipoluianov/altesp/internal/formats"
)

// The flash mode, frequency and size the bootloader's image header tells
// the ROM (byte 2: the mode; byte 3: the size in the high four bits, the
// frequency in the low four): esptool documentation "Firmware Image
// Format" of each chip, Nov 2026.

// flashModes are the codes of byte 2, the same on every chip
var flashModes = map[string]byte{"QIO": 0, "QOUT": 1, "DIO": 2, "DOUT": 3}

// flashFreqs are the frequency codes by chip
var flashFreqs = map[string]map[string]byte{
	"esp32":    {"40MHz": 0x0, "26MHz": 0x1, "20MHz": 0x2, "80MHz": 0xF},
	"esp32s2":  {"40MHz": 0x0, "26MHz": 0x1, "20MHz": 0x2, "80MHz": 0xF},
	"esp32s3":  {"40MHz": 0x0, "26MHz": 0x1, "20MHz": 0x2, "80MHz": 0xF},
	"esp32c3":  {"40MHz": 0x0, "26MHz": 0x1, "20MHz": 0x2, "80MHz": 0xF},
	"esp32p4":  {"40MHz": 0x0, "26MHz": 0x1, "20MHz": 0x2, "80MHz": 0xF},
	"esp32c2":  {"30MHz": 0x0, "20MHz": 0x1, "15MHz": 0x2, "60MHz": 0xF},
	"esp32c6":  {"80MHz/40MHz": 0x0, "20MHz": 0x2}, // 0 is 80 or 40 MHz by the MSPI clock source
	"esp32c5":  {"40MHz": 0x0, "20MHz": 0x2, "80MHz": 0xF},
	"esp32c61": {"40MHz": 0x0, "20MHz": 0x2, "80MHz": 0xF},
	"esp32h2":  {"24MHz": 0x0, "16MHz": 0x1, "12MHz": 0x2, "48MHz": 0xF},
	"esp32h4":  {"24MHz": 0x0, "48MHz": 0xF},
	"esp32h21": {"24MHz": 0x0, "48MHz": 0xF},
}

// flashSizeMax is the largest size code of each chip (0 = 1MB, 1 = 2MB ...
// 7 = 128MB)
var flashSizeMax = map[string]byte{
	"esp32": 4, "esp32c3": 4, "esp32c2": 4, "esp32c6": 4, "esp32h2": 4, "esp32h21": 4,
	"esp32c5": 5, "esp32c61": 5, "esp32h4": 5,
	"esp32p4": 6,
	"esp32s2": 7, "esp32s3": 7,
}

// FlashModes are the SPI flash modes
func FlashModes() []string { return []string{"QIO", "QOUT", "DIO", "DOUT"} }

// FlashFreqs are the flash frequencies a chip's header can tell
func FlashFreqs(chipKey string) []string {
	var names []string
	for n := range flashFreqs[chipKey] {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// FlashSizes are the flash sizes a chip's header can tell
func FlashSizes(chipKey string) []string {
	max, ok := flashSizeMax[chipKey]
	if !ok {
		return nil
	}
	var sizes []string
	for c := byte(0); c <= max; c++ {
		sizes = append(sizes, fmt.Sprintf("%dMB", 1<<c))
	}
	return sizes
}

// FlashParams overrides what the bootloader's header tells; "" keeps it
type FlashParams struct{ Mode, Freq, Size string }

// Empty: nothing is overridden
func (p FlashParams) Empty() bool { return p == FlashParams{} }

// PatchFlashParams rewrites the header of the bootloader among the items:
// the item at the chip's bootloader address, or a merged image at 0. Its
// appended SHA-256 is recalculated; a bootloader signed for Secure Boot
// can't be patched. The other items are kept as they are.
func PatchFlashParams(b Board, items []Item, p FlashParams) ([]Item, error) {
	if p.Empty() {
		return items, nil
	}
	if b.Chip == nil {
		return nil, fmt.Errorf("the chip must be known to override the flash parameters")
	}
	key := b.Chip.Key
	var mode, freqSize *byte
	if p.Mode != "" {
		m, ok := flashModes[p.Mode]
		if !ok {
			return nil, fmt.Errorf("unknown flash mode %s", p.Mode)
		}
		mode = &m
	}
	out := append([]Item(nil), items...)
	for i, it := range out {
		bootloader := it.Addr == b.Chip.BootloaderAddr && formats.Detect(it.Data) == formats.Bootloader
		merged := it.Addr == 0 && formats.Detect(it.Data) == formats.Merged
		if !bootloader && !merged {
			continue
		}
		if p.Freq != "" || p.Size != "" {
			img, err := bootloaderHeader(it.Data, merged, b.Chip.BootloaderAddr)
			if err != nil {
				return nil, err
			}
			fs := img
			if p.Freq != "" {
				f, ok := flashFreqs[key][p.Freq]
				if !ok {
					return nil, fmt.Errorf("%s doesn't take the flash frequency %s", b.Chip.Name, p.Freq)
				}
				fs = fs&0xF0 | f
			}
			if p.Size != "" {
				c, ok := sizeCode(key, p.Size)
				if !ok {
					return nil, fmt.Errorf("%s doesn't take the flash size %s", b.Chip.Name, p.Size)
				}
				fs = fs&0x0F | c<<4
			}
			freqSize = &fs
		}
		data, err := formats.PatchHeader(it.Data, mode, freqSize)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", it.Name, err)
		}
		out[i].Data = data
		return out, nil
	}
	return nil, fmt.Errorf("no bootloader among the files: the flash parameters are told by its header")
}

// bootloaderHeader is byte 3 of the bootloader's header
func bootloaderHeader(data []byte, merged bool, addr uint32) (byte, error) {
	off := 0
	if merged {
		off = int(addr)
	}
	if len(data) < off+4 || data[off] != 0xE9 {
		return 0, fmt.Errorf("no bootloader header at 0x%X", off)
	}
	return data[off+3], nil
}

func sizeCode(key, size string) (byte, bool) {
	max, ok := flashSizeMax[key]
	if !ok {
		return 0, false
	}
	for c := byte(0); c <= max; c++ {
		if size == fmt.Sprintf("%dMB", 1<<c) {
			return c, true
		}
	}
	return 0, false
}
