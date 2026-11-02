// Package chips is the table of the ESP chips: how a chip is detected,
// where its bootloader goes, which stub it runs, and how its eFuses and
// SPI flash registers are read for the chip card. A new chip is an entry
// in table.go.
//
// Sources: ESP-IDF (Apache-2.0) at commit 185194117695 of master
// (2026-11-02): components/soc/<chip>/register/soc/*.h,
// components/efuse/<chip>/esp_efuse_table.csv,
// components/bootloader_support/include/esp_app_format.h,
// components/esp_rom/<chip>/Kconfig.soc_caps.in,
// components/soc/<chip>/include/soc/soc_caps.h,
// components/esp_hw_support/port/<chip>/chip_info.c,
// components/hal/<chip>/efuse_hal.c and efuse_ll.h; esp-flasher-stub
// (MIT) v1.4.1; the ESP32 datasheet v5.3; the esptool documentation
// "Serial Protocol". Each table names its own source. Facts that these
// don't settle are marked "unverified:" and listed in TZ.md under
// "Hardware checks".
package chips

import "fmt"

// MagicRegAddr is the address of a ROM word that tells the chips without
// a chip ID in GET_SECURITY_INFO apart (ESP32, ESP32-S2). The esptool
// documentation "Serial Protocol" (chip type detection) names the method;
// the address and the values are unverified: they aren't in the
// documentation.
const MagicRegAddr uint32 = 0x40001000

// Arch is the CPU architecture of a chip
type Arch int

const (
	Xtensa Arch = iota
	RISCV
)

func (a Arch) String() string {
	if a == RISCV {
		return "RISC-V"
	}
	return "Xtensa"
}

// USBKind is the built-in USB of a chip that the ROM loader can be
// reached through
type USBKind int

const (
	// USBNone: only through a USB-UART bridge
	USBNone USBKind = iota
	// USBSerialJTAG is the USB-Serial-JTAG controller (a CDC-ACM port)
	USBSerialJTAG
	// USBOTG is the USB OTG controller with the ROM's CDC
	USBOTG
)

func (u USBKind) String() string {
	switch u {
	case USBSerialJTAG:
		return "USB-Serial-JTAG"
	case USBOTG:
		return "USB OTG"
	}
	return "none"
}

// Revision is a chip revision, "v0.2"
type Revision struct{ Major, Minor int }

func (r Revision) String() string { return fmt.Sprintf("v%d.%d", r.Major, r.Minor) }

// Full is the revision as the image header stores it in
// min_chip_rev_full and max_chip_rev_full: major*100+minor
func (r Revision) Full() int { return r.Major*100 + r.Minor }

// RevisionFromFull is the inverse of Full
func RevisionFromFull(full int) Revision { return Revision{full / 100, full % 100} }

// Chip is an entry of the chip table
type Chip struct {
	Name           string // "ESP32-S3"
	Key            string // "esp32s3", also the base name of its stub
	ChipID         uint16 // esp_chip_id_t of the image header and GET_SECURITY_INFO
	Arch           Arch
	Stage          int    // the stage of TZ.md that adds the chip
	BootloaderAddr uint32 // where the second stage bootloader is flashed
	ROMEncryptWord bool   // the ROM's FLASH_BEGIN takes the fifth word
	USB            USBKind

	// magic is the word at MagicRegAddr, 0 when the chip isn't detected
	// by it
	magic uint32
	regs  chipRegs
}

func (c *Chip) String() string { return c.Name }

// All lists the chips of the table in the order of the table
func All() []*Chip {
	all := make([]*Chip, len(table))
	copy(all, table)
	return all
}

// ByKey finds a chip by its key, "esp32c3"; nil if unknown
func ByKey(key string) *Chip {
	for _, c := range table {
		if c.Key == key {
			return c
		}
	}
	return nil
}

// ByChipID finds a chip by its esp_chip_id_t; nil if unknown
func ByChipID(id uint32) *Chip {
	for _, c := range table {
		if uint32(c.ChipID) == id {
			return c
		}
	}
	return nil
}

// ByMagic finds a chip by the word read at MagicRegAddr: only the ESP32
// and the ESP32-S2 are detected this way; nil if unknown
func ByMagic(v uint32) *Chip {
	if v == 0 {
		return nil
	}
	for _, c := range table {
		if c.magic == v {
			return c
		}
	}
	return nil
}

// StubName is the name of the stub of esp-flasher-stub for the chip of
// that revision. The ESP32-P4 before v3.0 has its own stub "esp32p4-rev1":
// esp-flasher-stub v0.2.0 added "a separate >=ECO5 target" for the newer
// ones (its CHANGELOG.md and src/ld/esp32p4*.ld: the RAM of rev1 starts
// at 0x4FF10000, of ECO5 at 0x4FF50000), and ECO5 is v3.0 (the ESP-IDF
// soc/esp32p4/register/hw_ver1 and hw_ver3 split, efuse table v3.0).
func (c *Chip) StubName(rev Revision) string {
	if c.Key == "esp32p4" && rev.Major < 3 {
		return "esp32p4-rev1"
	}
	return c.Key
}

// ProbeAddr is a register that exists on the chip and is harmless to read:
// the first eFuse word. Secure Download Mode refuses READ_REG, so a read
// of it tells that mode; MagicRegAddr is in the ROM of the ESP32 and the
// ESP32-S2 only.
func (c *Chip) ProbeAddr() uint32 { return c.regs.efuse[0] }
