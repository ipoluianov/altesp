package chips

// field is a field of the eFuse: bits [bit, bit+n) of a block as
// esp_efuse_table.csv counts them. n == 0 is a field the chip doesn't
// have.
type field struct{ blk, bit, n int }

// chipRegs is what ReadInfo needs of a chip
type chipRegs struct {
	// efuse is the address of the first read register of BLK0, BLK1 and
	// BLK2 (efuse_reg.h); the words of a block follow each other
	efuse [3]uint32
	// mac is the factory MAC: 48 bits, its first byte in the highest
	mac field
	// major and minor are the wafer version, made of parts: the first is
	// the most significant
	major, minor []field

	flashCap, psramCap field
	// flashMB and psramMB are the sizes by the codes of flashCap and
	// psramCap, where the table documents them; nil otherwise
	flashMB, psramMB map[uint32]int

	spi spiRegs

	// xtalMHz is the crystal when the chip supports only one (soc_caps.h
	// SOC_XTAL_SUPPORT_*); 0 when the board decides
	xtalMHz int
	// features are what the chip always has (chip_info.c, soc_caps.h)
	features []string
	// extra reads what is particular to the chip: revision bits outside
	// the eFuse, the package, the crystal
	extra func(e *efuseReader, in *Info) error
}

// spiRegs is the SPI controller of the flash with its user-command
// registers: SPI1 of the ESP32 (spi_reg.h), SPI_MEM/SPIMEM1 of the others
// (spi_mem_reg.h, spi1_mem_reg.h, spi1_mem_c_reg.h). CMD is at +0x0 and
// CTRL at +0x8 on all of them.
type spiRegs struct {
	base                         uint32
	user, user2, misoDLen, dataW uint32
	// lines are the bits of CTRL that select dual, quad and octal
	// transfers: cleared while the ID is read
	lines uint32
}

// The user-command bits, the same on all chips (spi_reg.h of the ESP32,
// spi_mem_reg.h of the others): CMD.USR, USER.USR_COMMAND, USER.USR_MISO,
// USER2.USR_COMMAND_BITLEN at 28
const (
	spiCmdUSR      uint32 = 1 << 18
	spiUserCommand uint32 = 1 << 31
	spiUserMISO    uint32 = 1 << 28
	spiCmdBitlenS         = 28
	spiCmdReg      uint32 = 0x0
	spiCtrlReg     uint32 = 0x8
	spiFreadQIO    uint32 = 1 << 24
	spiFreadDIO    uint32 = 1 << 23
	spiFreadQuad   uint32 = 1 << 20
	spiFreadDual   uint32 = 1 << 14
	spiFcmdOct     uint32 = 1 << 9
	spiFcmdQuad    uint32 = 1 << 8
	spiFcmdDual    uint32 = 1 << 7
	spiFaddrOct    uint32 = 1 << 6
	spiFdinOct     uint32 = 1 << 5
	spiFdoutOct    uint32 = 1 << 4
	spiLinesESP32         = spiFreadQIO | spiFreadDIO | spiFreadQuad | spiFreadDual
	spiLinesC3            = spiLinesESP32 | spiFcmdQuad | spiFcmdDual
	spiLinesS3            = spiLinesC3 | spiFcmdOct | spiFaddrOct | spiFdinOct | spiFdoutOct
	spiLinesC6            = spiLinesESP32 | spiFcmdQuad | spiFcmdOct | spiFaddrOct | spiFdinOct | spiFdoutOct
)

// spi1 is the SPI1 of the ESP32: USER +0x1C, USER2 +0x24, MISO_DLEN +0x2C,
// W0 +0x80 (soc/esp32/register/soc/spi_reg.h)
func spi1(base uint32) spiRegs {
	return spiRegs{base: base, user: 0x1C, user2: 0x24, misoDLen: 0x2C, dataW: 0x80, lines: spiLinesESP32}
}

// spiMem is the SPI_MEM controller of the later chips: USER +0x18,
// USER2 +0x20, MISO_DLEN +0x28, W0 +0x58 (spi_mem_reg.h, spi1_mem_reg.h,
// spi1_mem_c_reg.h of each chip)
func spiMem(base, lines uint32) spiRegs {
	return spiRegs{base: base, user: 0x18, user2: 0x20, misoDLen: 0x28, dataW: 0x58, lines: lines}
}

// newEFuse is the usual layout of the eFuse read registers after the
// ESP32: BLK0 from EFUSE_RD_WR_DIS_REG (+0x2C), BLK1 from
// EFUSE_RD_MAC_SPI_SYS_0_REG / EFUSE_RD_MAC_SYS_0_REG (+0x44), BLK2 from
// EFUSE_RD_SYS_PART1_DATA0_REG (+0x5C)
func newEFuse(base uint32) [3]uint32 {
	return [3]uint32{base + 0x2C, base + 0x44, base + 0x5C}
}

// Feature names
const (
	featWiFi    = "Wi-Fi"
	featBT      = "Bluetooth Classic"
	featBLE     = "BLE"
	feat802154  = "IEEE 802.15.4"
	featSingle  = "Single core"
	featDual    = "Dual core"
	featUSBJTAG = "USB-Serial-JTAG"
	featUSBOTG  = "USB OTG"
)

// The chip table.
//
//   - ChipID: esp_chip_id_t, esp_app_format.h.
//   - BootloaderAddr: ESP_ROM_BOOTLOADER_OFFSET_FLASH of
//     components/esp_rom/<chip>/Kconfig.soc_caps.in (the value of the
//     ESP-IDF "Bootloader" guides: 0x1000 on ESP32 and S2, 0x2000 on C5,
//     P4, H4 and S31, 0x0 on the others).
//   - USB: ESP_ROM_USB_SERIAL_DEVICE_NUM and ESP_ROM_USB_OTG_NUM of the same
//     file (the ROM can download through it); a chip with both is listed
//     with its USB-Serial-JTAG, as TZ.md does.
//   - Arch: the esp-flasher-stub targets (cmake/esp-targets.cmake).
//   - eFuse fields: esp_efuse_table.csv; their registers: efuse_reg.h and
//     reg_base.h; the composition of the revision: efuse_ll.h and
//     efuse_hal.c.
//   - SPI: reg_base.h (DR_REG_SPI1_BASE, DR_REG_SPIMEM1_BASE,
//     DR_REG_MSPI1_BASE, DR_REG_FLASH_SPI1_BASE) and the register headers.
//   - features: chip_info.c and soc_caps.h.
//
// All of ESP-IDF master at 185194117695 (2026-11-02).
var table = []*Chip{
	{
		Name: "ESP32", Key: "esp32", ChipID: 0x0000, Arch: Xtensa, Stage: 1,
		BootloaderAddr: 0x1000, ROMEncryptWord: false, USB: USBNone,
		// unverified: the ROM word at MagicRegAddr
		magic: 0x00F01D83,
		regs: chipRegs{
			// EFUSE_BLK0_RDATA0_REG, EFUSE_BLK1_RDATA0_REG (+0x38),
			// EFUSE_BLK2_RDATA0_REG (+0x58); DR_REG_EFUSE_BASE 0x3FF5A000
			efuse: [3]uint32{0x3FF5A000, 0x3FF5A038, 0x3FF5A058},
			mac:   field{0, 32, 48},
			// the major revision is CHIP_VER_REV1, CHIP_VER_REV2 and a bit
			// of APB_CTRL_DATE: esp32Extra
			minor: []field{{0, 184, 2}},
			spi:   spi1(0x3FF42000),
			extra: esp32Extra,
		},
	},
	{
		Name: "ESP32-S3", Key: "esp32s3", ChipID: 0x0009, Arch: Xtensa, Stage: 1,
		BootloaderAddr: 0x0, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			efuse:    newEFuse(0x60007000),
			mac:      field{1, 0, 48},
			major:    []field{{1, 184, 2}},
			minor:    []field{{1, 183, 1}, {1, 114, 3}},
			flashCap: field{1, 123, 3},
			// PSRAM_CAP is 2 bits in the table although its value list
			// goes to 4
			psramCap: field{1, 131, 2},
			// the value lists of FLASH_CAP and PSRAM_CAP in the table
			flashMB:  map[uint32]int{1: 8, 2: 4},
			psramMB:  map[uint32]int{1: 8, 2: 2, 3: 16, 4: 4},
			spi:      spiMem(0x60002000, spiLinesS3),
			xtalMHz:  40,
			features: []string{featWiFi, featBLE, featDual, featUSBJTAG, featUSBOTG},
		},
	},
	{
		Name: "ESP32-C3", Key: "esp32c3", ChipID: 0x0005, Arch: RISCV, Stage: 1,
		BootloaderAddr: 0x0, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			efuse:    newEFuse(0x60008800),
			mac:      field{1, 0, 48},
			major:    []field{{1, 184, 2}},
			minor:    []field{{1, 183, 1}, {1, 114, 3}},
			flashCap: field{1, 123, 3},
			flashMB:  map[uint32]int{1: 4, 2: 2, 3: 1, 4: 8},
			spi:      spiMem(0x60002000, spiLinesC3),
			xtalMHz:  40,
			features: []string{featWiFi, featBLE, featSingle, featUSBJTAG},
		},
	},
	{
		Name: "ESP32-S2", Key: "esp32s2", ChipID: 0x0002, Arch: Xtensa, Stage: 2,
		BootloaderAddr: 0x1000, ROMEncryptWord: true, USB: USBOTG,
		// unverified: the ROM word at MagicRegAddr
		magic: 0x000007C6,
		regs: chipRegs{
			efuse: newEFuse(0x3F41A000),
			mac:   field{1, 0, 48},
			major: []field{{1, 114, 2}},
			minor: []field{{1, 116, 1}, {1, 132, 3}},
			// FLASH_VERSION and PSRAM_VERSION: the table lists no sizes
			flashCap: field{1, 117, 4},
			psramCap: field{1, 124, 4},
			spi:      spiMem(0x3F402000, spiLinesS3),
			xtalMHz:  40,
			features: []string{featWiFi, featSingle, featUSBOTG},
			extra:    esp32s2Extra,
		},
	},
	{
		Name: "ESP32-C6", Key: "esp32c6", ChipID: 0x000D, Arch: RISCV, Stage: 2,
		BootloaderAddr: 0x0, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			efuse:    newEFuse(0x600B0800),
			mac:      field{1, 0, 48},
			major:    []field{{1, 118, 2}},
			minor:    []field{{1, 114, 4}},
			flashCap: field{1, 128, 3},
			spi:      spiMem(0x60003000, spiLinesC6),
			xtalMHz:  40,
			features: []string{featWiFi, featBLE, feat802154, featSingle, featUSBJTAG},
		},
	},
	{
		Name: "ESP32-H2", Key: "esp32h2", ChipID: 0x0010, Arch: RISCV, Stage: 3,
		BootloaderAddr: 0x0, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			efuse:    newEFuse(0x600B0800),
			mac:      field{1, 0, 48},
			major:    []field{{1, 117, 2}},
			minor:    []field{{1, 114, 3}},
			flashCap: field{1, 120, 3},
			spi:      spiMem(0x60003000, spiLinesC6),
			xtalMHz:  32,
			features: []string{featBLE, feat802154, featSingle, featUSBJTAG},
		},
	},
	{
		Name: "ESP32-C2", Key: "esp32c2", ChipID: 0x000C, Arch: RISCV, Stage: 3,
		BootloaderAddr: 0x0, ROMEncryptWord: true, USB: USBNone,
		regs: chipRegs{
			// BLK0 from EFUSE_RD_WR_DIS_REG (+0x2C), BLK1 from
			// EFUSE_RD_BLK1_DATA0_REG (+0x34), BLK2 from
			// EFUSE_RD_BLK2_DATA0_REG (+0x40)
			efuse:    [3]uint32{0x6000882C, 0x60008834, 0x60008840},
			mac:      field{2, 0, 48},
			major:    []field{{2, 52, 2}},
			minor:    []field{{2, 48, 4}},
			flashCap: field{2, 253, 3},
			spi:      spiMem(0x60002000, spiLinesC3),
			// 26 or 40 MHz: the board decides
			features: []string{featWiFi, featBLE, featSingle},
		},
	},
	{
		Name: "ESP32-C5", Key: "esp32c5", ChipID: 0x0017, Arch: RISCV, Stage: 5,
		BootloaderAddr: 0x2000, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			efuse:    newEFuse(0x600B4800),
			mac:      field{1, 0, 48},
			major:    []field{{1, 68, 2}},
			minor:    []field{{1, 64, 4}},
			flashCap: field{1, 77, 3},
			psramCap: field{1, 83, 3},
			spi:      spiMem(0x60003000, spiLinesC6),
			// 40 or 48 MHz: c5Extra
			features: []string{featWiFi, featBLE, feat802154, featSingle, featUSBJTAG},
			extra:    esp32c5Extra,
		},
	},
	{
		Name: "ESP32-P4", Key: "esp32p4", ChipID: 0x0012, Arch: RISCV, Stage: 5,
		BootloaderAddr: 0x2000, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			// DR_REG_EFUSE_BASE = DR_REG_LPPERIPH_BASE + 0xD000; the same
			// in hw_ver1 and hw_ver3
			efuse: newEFuse(0x5012D000),
			mac:   field{1, 0, 48},
			// WAFER_VERSION_MAJOR_HI, WAFER_VERSION_MAJOR_LO
			major:    []field{{1, 87, 1}, {1, 68, 2}},
			minor:    []field{{1, 64, 4}},
			psramCap: field{1, 77, 3},
			// DR_REG_FLASH_SPI1_BASE = DR_REG_HPPERIPH0_BASE + 0x8D000
			spi:      spiMem(0x5008D000, spiLinesC6),
			xtalMHz:  40,
			features: []string{featDual, featUSBJTAG, featUSBOTG},
		},
	},
	{
		Name: "ESP32-C61", Key: "esp32c61", ChipID: 0x0014, Arch: RISCV, Stage: 5,
		BootloaderAddr: 0x0, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			efuse:    newEFuse(0x600B4800), // DR_REG_EFUSE0_BASE
			mac:      field{1, 0, 48},
			major:    []field{{1, 68, 2}},
			minor:    []field{{1, 64, 4}},
			flashCap: field{1, 77, 3},
			psramCap: field{1, 83, 3},
			spi:      spiMem(0x60003000, spiLinesC6), // DR_REG_MSPI1_BASE
			xtalMHz:  40,
			features: []string{featWiFi, featBLE, featSingle, featUSBJTAG},
		},
	},
	{
		Name: "ESP32-H4", Key: "esp32h4", ChipID: 0x001C, Arch: RISCV, Stage: 5,
		BootloaderAddr: 0x2000, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			efuse:    newEFuse(0x600B1800),
			mac:      field{1, 0, 48},
			major:    []field{{1, 118, 2}},
			minor:    []field{{1, 114, 4}},
			flashCap: field{1, 127, 3},
			psramCap: field{1, 133, 3},
			spi:      spiMem(0x60099000, spiLinesC6), // DR_REG_SPIMEM1_BASE
			xtalMHz:  32,
			features: []string{featBLE, feat802154, featDual, featUSBJTAG},
		},
	},
	{
		Name: "ESP32-H21", Key: "esp32h21", ChipID: 0x0019, Arch: RISCV, Stage: 5,
		BootloaderAddr: 0x0, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			efuse:    newEFuse(0x600B4000),
			mac:      field{1, 0, 48},
			major:    []field{{1, 168, 2}},
			minor:    []field{{1, 164, 4}},
			flashCap: field{1, 120, 3},
			spi:      spiMem(0x60003000, spiLinesC6), // DR_REG_SPIMEM1_BASE
			xtalMHz:  32,
			features: []string{featBLE, feat802154, featSingle, featUSBJTAG},
		},
	},
	{
		Name: "ESP32-S31", Key: "esp32s31", ChipID: 0x0020, Arch: RISCV, Stage: 5,
		BootloaderAddr: 0x2000, ROMEncryptWord: true, USB: USBSerialJTAG,
		regs: chipRegs{
			// BLK0 from EFUSE_RD_WR_DIS_REG (+0x2C), BLK1 from
			// EFUSE_RD_MAC_SYS0_REG (+0x50), BLK2 from
			// EFUSE_RD_SYS_PART1_DATA0_REG (+0x68)
			efuse:    [3]uint32{0x2071502C, 0x20715050, 0x20715068},
			mac:      field{1, 0, 48},
			major:    []field{{1, 118, 2}},
			minor:    []field{{1, 114, 4}},
			psramCap: field{1, 127, 3},
			spi:      spiMem(0x20501000, spiLinesC6), // DR_REG_FLASH_SPI1_BASE
			xtalMHz:  40,
			features: []string{featWiFi, featBT, featBLE, feat802154, featDual, featUSBJTAG, featUSBOTG},
		},
	},
}
