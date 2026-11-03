package jtag

import (
	"fmt"
	"strings"

	"github.com/ipoluianov/altesp/internal/chips"
)

// arch is the debug architecture of a chip. Stage 6 has the RISC-V debug
// module; the Xtensa chips (ESP32-S3, ESP32: stage 7) have the Xtensa
// OCD over a TAP and get their own cpu implementation (see cpu.go) and
// an entry here with archXtensa.
type arch int

const (
	archRISCV arch = iota
	archXtensa
)

// romFuncs are the addresses of the ROM functions used as the flash
// routine, from ESP-IDF (Apache-2.0) at commit 185194117695:
// components/esp_rom/<chip>/ld/<chip>.rom.ld and <chip>.rom.api.ld.
// The signatures are those of components/esp_rom/include/esp_rom_spiflash.h
// and esp_rom_md5.h (the MD5Context variant for these chips):
//
//	void     esp_rom_spiflash_attach(uint32_t ishspi, bool legacy)
//	result_t esp_rom_spiflash_config_param(uint32_t deviceId, uint32_t chip_size,
//	             uint32_t block_size, uint32_t sector_size, uint32_t page_size,
//	             uint32_t status_mask)
//	result_t esp_rom_spiflash_unlock(void)             (components/esp_rom/<chip>/include/<chip>/rom/spi_flash.h)
//	result_t esp_rom_spiflash_erase_chip(void)
//	result_t esp_rom_spiflash_erase_block(uint32_t block_num)   64 KB blocks
//	result_t esp_rom_spiflash_erase_sector(uint32_t sector_num) 4 KB sectors
//	result_t esp_rom_spiflash_write(uint32_t dest, const uint32_t *src, int32_t len)
//	result_t esp_rom_spiflash_read(uint32_t src, uint32_t *dest, int32_t len)
//	void     MD5Init(md5_context_t *ctx)                (esp_rom_md5_init)
//	void     MD5Update(md5_context_t *ctx, const void *buf, uint32_t len)
//	void     MD5Final(uint8_t digest[16], md5_context_t *ctx)
//
// result_t is ESP_ROM_SPIFLASH_RESULT_OK (0), _ERR (1), _TIMEOUT (2).
// On C6 and H2 esp_rom_spiflash_attach is spi_flash_attach (rom.api.ld).
type romFuncs struct {
	attach, configParam, unlock        uint32
	eraseChip, eraseBlock, eraseSector uint32
	write, read                        uint32
	md5Init, md5Update, md5Final       uint32
}

// wdt is a watchdog that is disabled by writing key to its write-protect
// register, 0 to its config register (clears the enable bit, bit 31 on
// all of them), and 0 to the protect register again
type wdt struct {
	name            string
	protect, config uint32
	key             uint32
}

// swd is the super watchdog: it can't be disabled, only fed
// automatically by setting autoFeed in its config register (under its
// own write protection)
type swd struct {
	protect, config, key, autoFeed uint32
}

// regBit is one bit of a register
type regBit struct{ addr, bit uint32 }

// target is the JTAG side of a chip
type target struct {
	key  string
	arch arch
	// idcode is the expected IDCODE of the TAP. unverified: the values
	// aren't in the TRMs; the low 12 bits 0xC25 are Espressif's JEDEC
	// manufacturer code (bank 13, 0x12) with the mandatory bit 0. A
	// mismatch only adds a warning.
	idcode uint32
	// sram is the internal SRAM as the data bus sees it, and iOffset is
	// what is added to reach the same byte through the instruction bus
	// (components/soc/<chip>/include/soc/soc.h: SOC_DRAM_LOW/HIGH,
	// SOC_I_D_OFFSET)
	sramLow, sramHigh uint32
	iOffset           uint32
	// work is the RAM area the flash routine uses (data bus address):
	// the ebreak the ROM functions return to, the MD5 context, a stack and
	// the data buffer. unverified: that the ROM in download mode leaves
	// it alone. It is chosen below the ROM's own data and stack (the top
	// of SRAM: SOC_ROM_STACK_START in soc.h, rom_spiflash_legacy_data in
	// rom.ld) and away from where esp-flasher-stub loads (its text_start
	// and data_start).
	work uint32
	rom  romFuncs
	// forceDownload makes the ROM enter download mode after a reset
	// without the strapping pins (C3: RTC_CNTL_OPTION1_REG
	// RTC_CNTL_FORCE_DOWNLOAD_BOOT in soc/esp32c3/register/soc/rtc_cntl_reg.h;
	// C6, H2: LP_AON_SYS_CFG_REG LP_AON_FORCE_DOWNLOAD_BOOT in
	// soc/<chip>/register/soc/lp_aon_reg.h)
	forceDownload regBit
	// wdts are disabled after the halt: the RTC/LP watchdog
	// (rtc_cntl_reg.h, lp_wdt_reg.h) and the two timer group watchdogs
	// (timer_group_reg.h: TIMG_WDTCONFIG0_REG +0x48, TIMG_WDTWPROTECT_REG
	// +0x64, bases in reg_base.h). The keys: esp_hal_wdt/<chip>/include/
	// hal/rwdt_ll.h, lpwdt_ll.h and mwdt_ll.h (0x50D83AA1; the C3 super
	// watchdog 0x8F1D312A).
	wdts []wdt
	swd  swd
	// Xtensa only: taps is the number of TAPs in the chain (one per core,
	// unverified), brk the instruction bus address of the BREAK the ROM
	// functions return to when it is not work+workEbreak+iOffset (the
	// ESP32's instruction RAM is not an alias of its data RAM)
	taps int
	brk  uint32
}

// The work area layout, offsets from target.work
const (
	workEbreak = 0x0000 // one ebreak: the return address of the calls
	workMD5    = 0x0040 // md5_context_t: 88 bytes
	workDigest = 0x00C0 // 16 bytes
	workStack  = 0x1000 // the top of the stack, which grows down to 0x100
	workBuf    = 0x1000 // the data buffer
	bufSize    = 0x4000 // 16 KB
	workSize   = workBuf + bufSize
)

const (
	timg0C3, timg1C3 = 0x6001F000, 0x60020000
	timg0C6, timg1C6 = 0x60008000, 0x60009000
	timg0H2, timg1H2 = 0x60009000, 0x6000A000
	wdtKey           = 0x50D83AA1
)

func timgWDTs(base0, base1 uint32) []wdt {
	return []wdt{
		{"TIMG0 watchdog", base0 + 0x64, base0 + 0x48, wdtKey},
		{"TIMG1 watchdog", base1 + 0x64, base1 + 0x48, wdtKey},
	}
}

var targets = []*target{
	{
		key: "esp32c3", arch: archRISCV, idcode: 0x00005C25,
		sramLow: 0x3FC80000, sramHigh: 0x3FCE0000, iOffset: 0x700000,
		work: 0x3FCA0000,
		rom: romFuncs{
			attach: 0x40000164, configParam: 0x40000134, unlock: 0x40000140,
			eraseChip: 0x40000120, eraseBlock: 0x40000124, eraseSector: 0x40000128,
			write: 0x4000012C, read: 0x40000130,
			md5Init: 0x40000614, md5Update: 0x40000618, md5Final: 0x4000061C,
		},
		// DR_REG_RTCCNTL_BASE 0x60008000 + 0xF4, bit 0
		forceDownload: regBit{0x600080F4, 1 << 0},
		wdts: append([]wdt{
			// RTC_CNTL_WDTWPROTECT_REG +0xA8, RTC_CNTL_WDTCONFIG0_REG +0x90
			{"RTC watchdog", 0x600080A8, 0x60008090, wdtKey},
		}, timgWDTs(timg0C3, timg1C3)...),
		// RTC_CNTL_SWD_WPROTECT_REG +0xB0, RTC_CNTL_SWD_CONF_REG +0xAC,
		// RTC_CNTL_SWD_AUTO_FEED_EN bit 31
		swd: swd{protect: 0x600080B0, config: 0x600080AC, key: 0x8F1D312A, autoFeed: 1 << 31},
	},
	{
		key: "esp32c6", arch: archRISCV, idcode: 0x0000DC25,
		sramLow: 0x40800000, sramHigh: 0x40880000,
		work: 0x40820000,
		rom: romFuncs{
			attach: 0x400001DC, configParam: 0x40000160, unlock: 0x40000154,
			eraseChip: 0x40000124, eraseBlock: 0x40000148, eraseSector: 0x40000144,
			write: 0x4000014C, read: 0x40000150,
			md5Init: 0x4000074C, md5Update: 0x40000750, md5Final: 0x40000754,
		},
		// DR_REG_LP_AON_BASE 0x600B1000 + 0x34, bit 30
		forceDownload: regBit{0x600B1034, 1 << 30},
		wdts: append([]wdt{
			// DR_REG_LP_WDT_BASE 0x600B1C00: WPROTECT +0x18, CONFIG0 +0x0
			{"LP watchdog", 0x600B1C18, 0x600B1C00, wdtKey},
		}, timgWDTs(timg0C6, timg1C6)...),
		// LP_WDT_SWD_WPROTECT_REG +0x20, LP_WDT_SWD_CONFIG_REG +0x1C,
		// LP_WDT_SWD_AUTO_FEED_EN bit 18
		swd: swd{protect: 0x600B1C20, config: 0x600B1C1C, key: wdtKey, autoFeed: 1 << 18},
	},
	{
		key: "esp32h2", arch: archRISCV, idcode: 0x00010C25,
		sramLow: 0x40800000, sramHigh: 0x40850000,
		work: 0x40820000,
		rom: romFuncs{
			attach: 0x400001D4, configParam: 0x40000158, unlock: 0x4000014C,
			eraseChip: 0x4000011C, eraseBlock: 0x40000140, eraseSector: 0x4000013C,
			write: 0x40000144, read: 0x40000148,
			md5Init: 0x40000718, md5Update: 0x4000071C, md5Final: 0x40000720,
		},
		forceDownload: regBit{0x600B1034, 1 << 30},
		wdts: append([]wdt{
			// DR_REG_LP_WDT_BASE 0x600B1C00: WPROTECT +0x1C, CONFIG0 +0x0
			{"LP watchdog", 0x600B1C1C, 0x600B1C00, wdtKey},
		}, timgWDTs(timg0H2, timg1H2)...),
		// LP_WDT_SWD_WPROTECT_REG +0x24, LP_WDT_SWD_CONFIG_REG +0x20
		swd: swd{protect: 0x600B1C24, config: 0x600B1C20, key: wdtKey, autoFeed: 1 << 18},
	},
}

// The Xtensa chips. Their debug module is reached and probed, the flash
// routine is not run yet (xtensa.Missing); the data is here for when it
// is. Sources: ESP-IDF v4.4.7 (38eeba213a, Apache-2.0) as shipped in the
// Arduino-ESP32 SDK: components/esp_rom/<chip>/ld/<chip>.rom*.ld,
// components/soc/<chip>/include/soc/soc.h, reg_base.h / soc.h,
// rtc_cntl_reg.h, timer_group_reg.h, hal/rwdt_ll.h and mwdt_ll.h.
//
// IDCODE: xdm-regs.h (Cadence, MIT) gives XTENSA_IDCODE 0x120034E5 with
// a FIXME; unverified that the ESP32 and ESP32-S3 TAPs answer it, and
// with both chips on the same value the chip can't be told by its IDCODE.
const xtensaIDCode = 0x120034E5

var xtensaTargets = []*target{
	{
		key: "esp32s3", arch: archXtensa, idcode: xtensaIDCode, taps: 2,
		// SOC_DRAM_LOW..HIGH; SOC_I_D_OFFSET = SOC_DIRAM_IRAM_LOW -
		// SOC_DIRAM_DRAM_LOW = 0x40378000 - 0x3FC88000
		sramLow: 0x3FC88000, sramHigh: 0x3FD00000, iOffset: 0x6F0000,
		// below SOC_ROM_STACK_START 0x3FCEB710 and the ROM data, inside
		// the DIRAM so that the BREAK is reachable at work+iOffset
		work: 0x3FCA0000,
		rom: romFuncs{
			// esp32s3.rom.api.ld: esp_rom_spiflash_attach = spi_flash_attach
			// (esp32s3.rom.ld 0x40000aec), the others from esp32s3.rom.api.ld;
			// MD5Init/Update/Final from esp32s3.rom.ld
			attach: 0x40000AEC, configParam: 0x40000A50, unlock: 0x40000A2C,
			eraseChip: 0x4000099C, eraseBlock: 0x40000A08, eraseSector: 0x400009FC,
			write: 0x40000A14, read: 0x40000A20,
			md5Init: 0x40001C5C, md5Update: 0x40001C68, md5Final: 0x40001C74,
		},
		// RTC_CNTL_OPTION1_REG (DR_REG_RTCCNTL_BASE 0x60008000 + 0x12C),
		// RTC_CNTL_FORCE_DOWNLOAD_BOOT bit 0
		forceDownload: regBit{0x6000812C, 1 << 0},
		wdts: append([]wdt{
			// RTC_CNTL_WDTWPROTECT_REG +0xB0, RTC_CNTL_WDTCONFIG0_REG +0x98
			{"RTC watchdog", 0x600080B0, 0x60008098, wdtKey},
		}, timgWDTs(0x6001F000, 0x60020000)...),
		// RTC_CNTL_SWD_WPROTECT_REG +0xB8, RTC_CNTL_SWD_CONF_REG +0xB4,
		// RTC_CNTL_SWD_AUTO_FEED_EN bit 31, key RTC_CNTL_SWD_WKEY_VALUE
		swd: swd{protect: 0x600080B8, config: 0x600080B4, key: 0x8F1D312A, autoFeed: 1 << 31},
	},
	{
		key: "esp32", arch: archXtensa, idcode: xtensaIDCode, taps: 2,
		// SOC_DRAM_LOW..HIGH: data RAM only; the instruction RAM is
		// SOC_IRAM_LOW..HIGH 0x40080000..0x400A0000
		sramLow: 0x3FFAE000, sramHigh: 0x40000000,
		// in SRAM2, away from the ROM's data and stack at 0x3FFE0000..
		// (SOC_ROM_STACK_START 0x3FFE3F20)
		work: 0x3FFC0000,
		brk:  0x40090000,
		rom: romFuncs{
			// esp32.rom.ld (attach, config_param), esp32.rom.spiflash.ld
			// (the others; esp_rom_spiflash_unlock is not in the ROM:
			// its address there is "0x400?????"), esp32.rom.redefined.ld
			// (MD5)
			attach: 0x40062A6C, configParam: 0x40063238, unlock: 0,
			eraseChip: 0x40062C14, eraseBlock: 0x40062C4C, eraseSector: 0x40062CCC,
			write: 0x40062D50, read: 0x40062ED8,
			md5Init: 0x4005DA7C, md5Update: 0x4005DA9C, md5Final: 0x4005DB1C,
		},
		// no force-download bit: the ESP32 enters download mode only by
		// its strapping pins (GPIO0 low at reset)
		wdts: append([]wdt{
			// DR_REG_RTCCNTL_BASE 0x3FF48000: RTC_CNTL_WDTWPROTECT_REG
			// +0xA4, RTC_CNTL_WDTCONFIG0_REG +0x8C
			{"RTC watchdog", 0x3FF480A4, 0x3FF4808C, wdtKey},
		}, timgWDTs(0x3FF5F000, 0x3FF60000)...),
	},
}

func init() { targets = append(targets, xtensaTargets...) }

func targetByKey(key string) *target {
	for _, t := range targets {
		if t.key == key {
			return t
		}
	}
	return nil
}

// targetByIDCode is the chip of an IDCODE: want when it has that IDCODE,
// else the first one; among the Xtensa chips that share theirs, the
// ESP32-S3 behind a built-in USB-JTAG (the ESP32 has none)
func targetByIDCode(id uint32, want *target, a Adapter) *target {
	var found []*target
	for _, t := range targets {
		if t.idcode == id {
			if t == want {
				return t
			}
			found = append(found, t)
		}
	}
	if len(found) == 0 {
		return nil
	}
	if len(found) > 1 && a == AdapterUSBJTAG {
		for _, t := range found {
			if t.key == "esp32s3" {
				return t
			}
		}
	}
	return found[0]
}

// ambiguous warns when the IDCODE is that of several chips
func ambiguous(id uint32) string {
	var keys []string
	for _, t := range targets {
		if t.idcode == id {
			keys = append(keys, t.key)
		}
	}
	if len(keys) < 2 {
		return ""
	}
	return fmt.Sprintf("the IDCODE 0x%08x is that of %s; choose the chip to be sure", id, strings.Join(keys, " and "))
}

// Supported tells whether flashing over JTAG is implemented for the
// chip: the RISC-V chips; the Xtensa ones are not yet (xtensa.Missing)
func Supported(chipKey string) bool {
	t := targetByKey(chipKey)
	return t != nil && t.arch == archRISCV
}

// retAddr is the instruction address of the breakpoint the ROM functions
// return to
func (t *target) retAddr() uint32 {
	if t.brk != 0 {
		return t.brk
	}
	return t.iaddr(workEbreak)
}

func (t *target) chip() *chips.Chip { return chips.ByKey(t.key) }

// iaddr is the instruction bus address of a work area offset
func (t *target) iaddr(off uint32) uint32 { return t.work + off + t.iOffset }
