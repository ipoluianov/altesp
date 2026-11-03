package efuse

import "github.com/ipoluianov/altesp/internal/chips"

// The eFuse controllers.
//
// Two kinds: the ESP32's, with write registers per block (BLKn_WDATA),
// one PGM command for all of them, the 3/4 and repeat coding of BLOCK1-3
// and DEC_STATUS for its warnings; and the controller of the later chips,
// with one set of programming registers (PGM_DATA0-7 and
// PGM_CHECK_VALUE0-2 right after them), the block number in CMD, BLOCK0
// stored four times (RD_REPEAT_ERR) and RS(44,32) on BLOCK1 and up
// (RD_RS_ERR).
//
// Addresses: components/soc/<chip>/register/soc/efuse_reg.h and reg_base.h
// (DR_REG_EFUSE_BASE), the block ranges of range_read_addr_blocks in
// components/efuse/<chip>/esp_efuse_utility.c, the timing of
// efuse_hal_set_timing in components/hal/<chip>/efuse_hal.c; ESP-IDF
// master at 185194117695 (2026-11-02). The op codes 0x5A5A (program) and
// 0x5AA5 (read): soc/esp32/include/soc/efuse_defs.h and the ESP32-C3 TRM
// v1.4, 4.3.2 and 4.3.3.

type ctrlKind int

const (
	kindESP32 ctrlKind = iota
	kindRS
)

const (
	opWrite uint32 = 0x5A5A
	opRead  uint32 = 0x5AA5

	cmdRead    uint32 = 1 << 0
	cmdPgm     uint32 = 1 << 1
	cmdBlkNumS        = 2
)

// blockLayout is where a block is read (and on the ESP32 written)
type blockLayout struct {
	rd    uint32
	words int
	wr    uint32 // the ESP32's BLKn_WDATA0
}

type timingKind int

const (
	// timingNone: the controller keeps its defaults (ESP32-C5, -P4, -S31:
	// "keep timing settings by default")
	timingNone timingKind = iota
	// timingFixed: DAC_NUM 0xFF, DAC_CLK_DIV 0x28, PWR_ON_NUM 0x3000,
	// PWR_OFF_NUM 0x190 (ESP32-S3, -C3, -C6, -C61, -H2, -H21, -H4)
	timingFixed
	// timingC2: timingFixed and TPGM_INACTIVE = crystal MHz * 5
	timingC2
	// timingS2: by the APB clock
	timingS2
	// timingESP32: CLK_SEL0, CLK_SEL1 and DAC_CLK_DIV by the APB clock
	timingESP32
)

// rsErrKind is how RD_RS_ERR reports a block
type rsErrKind int

const (
	// rsErrNibble: four bits per block from BLOCK1, the error count in
	// [2:0] and the fail bit in [3] (efuse_hal.c of the later chips:
	// ESP_EFUSE_BLOCK_ERROR_BITS with 0x08 and _NUM_BITS with 0x07; the
	// ESP32-S2, -S3 and -C2 test all four bits, 0x0F)
	rsErrNibble rsErrKind = iota
	// rsErrC3: the ESP32-C3's, whose fail bits are shifted by a block
	// (efuse_hal.c of the ESP32-C3 and the TRM 4.3.2)
	rsErrC3
)

type layout struct {
	kind      ctrlKind
	base      uint32
	blocks    []blockLayout
	conf, cmd uint32
	timing    timingKind

	// kindRS
	pgmData   uint32 // PGM_DATA0; PGM_CHECK_VALUE0 is at +0x20
	repeatErr []uint32
	rsErr     []uint32
	rsErrKind rsErrKind
	dacConf   uint32
	wrTim0    uint32
	wrTim1    uint32
	wrTim2    uint32
	rdTim     uint32
	blkNumMax int // the largest block number CMD.BLK_NUM takes

	// kindESP32
	clk, decStatus uint32
}

const pgmCheckOff = 0x20

// keyBlocks: the blocks that hold keys (esp_efuse_chip.h: EFUSE_BLK_KEY0
// to EFUSE_BLK_KEY_MAX-1)
func (l *layout) keyBlock(chipKey string, b int) bool {
	switch chipKey {
	case "esp32":
		return b >= 1 && b <= 3
	case "esp32c2":
		return b == 3
	case "esp32s31":
		return b >= 4 && b <= 8
	}
	return b >= 4 && b <= 9
}

func blocks(base uint32, words []int, offs ...uint32) []blockLayout {
	bl := make([]blockLayout, len(offs))
	for i, o := range offs {
		bl[i] = blockLayout{rd: base + o, words: words[i]}
	}
	return bl
}

// stdBlocks: BLOCK0 from RD_WR_DIS (+0x2C) with RD_REPEAT_DATA0-4, BLOCK1
// RD_MAC_SPI_SYS_0-5 / RD_MAC_SYS_0-5 (+0x44), BLOCK2 RD_SYS_PART1_DATA0-7
// (+0x5C), BLOCK3 RD_USR_DATA0-7 (+0x7C), BLOCK4-9 RD_KEY0-5_DATA0-7
// (+0x9C, step 0x20), BLOCK10 RD_SYS_PART2_DATA0-7 (+0x15C)
func stdBlocks(base uint32) []blockLayout {
	return blocks(base, []int{6, 6, 8, 8, 8, 8, 8, 8, 8, 8, 8},
		0x2C, 0x44, 0x5C, 0x7C, 0x9C, 0xBC, 0xDC, 0xFC, 0x11C, 0x13C, 0x15C)
}

func regs(base uint32, offs ...uint32) []uint32 {
	r := make([]uint32, len(offs))
	for i, o := range offs {
		r[i] = base + o
	}
	return r
}

// std builds the layout shared by most of the later chips
func std(base, conf, cmd uint32, repeatErr, rsErr []uint32, dac, tim1, tim2 uint32, t timingKind) *layout {
	return &layout{kind: kindRS, base: base, blocks: stdBlocks(base), pgmData: base,
		conf: base + conf, cmd: base + cmd, repeatErr: repeatErr, rsErr: rsErr,
		dacConf: base + dac, wrTim1: base + tim1, wrTim2: base + tim2, timing: t, blkNumMax: 15}
}

// The repeat and RS error registers of the ESP32-S2, -S3, -C3, -C6:
// RD_REPEAT_ERR0-3 at 0x17C-0x188, RD_REPEAT_ERR4 at 0x190, RD_RS_ERR0-1 at
// 0x1C0; of the ESP32-H2 and -P4: RD_REPEAT_ERR0-4 at 0x17C-0x18C; of the
// ESP32-C5, -C61, -H21, -H4: RD_REPEAT_DATA_ERR0-4 at 0x17C-0x18C,
// RD_RS_DATA_ERR0-1 at 0x190
func errA(b uint32) ([]uint32, []uint32) {
	return regs(b, 0x17C, 0x180, 0x184, 0x188, 0x190), regs(b, 0x1C0, 0x1C4)
}
func errB(b uint32) ([]uint32, []uint32) {
	return regs(b, 0x17C, 0x180, 0x184, 0x188, 0x18C), regs(b, 0x1C0, 0x1C4)
}
func errC(b uint32) ([]uint32, []uint32) {
	return regs(b, 0x17C, 0x180, 0x184, 0x188, 0x18C), regs(b, 0x190, 0x194)
}

func layoutFor(chip *chips.Chip) *layout {
	if chip == nil {
		return nil
	}
	switch chip.Key {
	case "esp32":
		// DR_REG_EFUSE_BASE 0x3FF5A000; BLK0_RDATA0-6 +0x00, BLK1_RDATA0-7
		// +0x38, BLK2 +0x58, BLK3 +0x78; BLK0_WDATA0-6 +0x1C, BLK1_WDATA0-7
		// +0x98, BLK2 +0xB8, BLK3 +0xD8; CLK +0xF8, CONF +0xFC, CMD +0x104,
		// DAC_CONF +0x118, DEC_STATUS +0x11C
		const b = 0x3FF5A000
		return &layout{kind: kindESP32, base: b,
			blocks: []blockLayout{
				{rd: b + 0x00, words: 7, wr: b + 0x1C},
				{rd: b + 0x38, words: 8, wr: b + 0x98},
				{rd: b + 0x58, words: 8, wr: b + 0xB8},
				{rd: b + 0x78, words: 8, wr: b + 0xD8},
			},
			clk: b + 0xF8, conf: b + 0xFC, cmd: b + 0x104, dacConf: b + 0x118,
			decStatus: b + 0x11C, timing: timingESP32}
	case "esp32s2":
		const b = 0x3F41A000
		re, rs := errA(b)
		l := std(b, 0x1CC, 0x1D4, re, rs, 0x1E8, 0x1F4, 0x1F8, timingS2)
		l.wrTim0, l.rdTim = b+0x1F0, b+0x1EC
		return l
	case "esp32s3":
		const b = 0x60007000
		re, rs := errA(b)
		return std(b, 0x1CC, 0x1D4, re, rs, 0x1E8, 0x1F4, 0x1F8, timingFixed)
	case "esp32c3":
		const b = 0x60008800
		re, rs := errA(b)
		l := std(b, 0x1CC, 0x1D4, re, rs, 0x1E8, 0x1F0, 0x1F4, timingFixed)
		l.rsErrKind = rsErrC3
		return l
	case "esp32c6":
		const b = 0x600B0800
		re, rs := errA(b)
		return std(b, 0x1CC, 0x1D4, re, rs, 0x1E8, 0x1F0, 0x1F4, timingFixed)
	case "esp32h2":
		const b = 0x600B0800
		re, rs := errB(b)
		return std(b, 0x1CC, 0x1D4, re, rs, 0x1E8, 0x1F0, 0x1F4, timingFixed)
	case "esp32p4":
		// DR_REG_LPPERIPH_BASE + 0xD000, the same in hw_ver1 and hw_ver3
		const b = 0x5012D000
		re, rs := errB(b)
		return std(b, 0x1CC, 0x1D4, re, rs, 0, 0, 0, timingNone)
	case "esp32c5":
		const b = 0x600B4800
		re, rs := errC(b)
		return std(b, 0x1CC, 0x1D8, re, rs, 0, 0, 0, timingNone)
	case "esp32c61":
		const b = 0x600B4800 // DR_REG_EFUSE0_BASE
		re, rs := errC(b)
		return std(b, 0x1CC, 0x1D4, re, rs, 0x1E8, 0x1F0, 0x1F4, timingFixed)
	case "esp32h21":
		const b = 0x600B4000
		re, rs := errC(b)
		return std(b, 0x1CC, 0x1D4, re, rs, 0x1E8, 0x1F0, 0x1F4, timingFixed)
	case "esp32h4":
		const b = 0x600B1800
		re, rs := errC(b)
		return std(b, 0x1CC, 0x1D8, re, rs, 0x1EC, 0x1F4, 0x1F8, timingFixed)
	case "esp32c2":
		// BLOCK0 RD_WR_DIS and RD_REPEAT_DATA0 (+0x2C), BLOCK1
		// RD_BLK1_DATA0-2 (+0x34), BLOCK2 RD_BLK2_DATA0-7 (+0x40), BLOCK3
		// RD_BLK3_DATA0-7 (+0x60); RD_REPEAT_ERR +0x80, RD_RS_ERR +0x84,
		// CONF +0x8C, CMD +0x94 (BLK_NUM is 2 bits), DAC_CONF +0x108,
		// WR_TIM_CONF0-2 +0x110
		const b = 0x60008800
		return &layout{kind: kindRS, base: b, pgmData: b,
			blocks: blocks(b, []int{2, 3, 8, 8}, 0x2C, 0x34, 0x40, 0x60),
			conf:   b + 0x8C, cmd: b + 0x94,
			repeatErr: regs(b, 0x80), rsErr: regs(b, 0x84),
			dacConf: b + 0x108, wrTim0: b + 0x110, wrTim1: b + 0x114, wrTim2: b + 0x118,
			timing: timingC2, blkNumMax: 3}
	case "esp32s31":
		// BLOCK0 RD_WR_DIS and RD_REPEAT_DATA0-7 (+0x2C, 9 words), BLOCK1
		// RD_MAC_SYS0-5 (+0x50), BLOCK2 +0x68, BLOCK3 +0x88, BLOCK4-8 the
		// keys (+0xA8, step 0x20), BLOCK9 RD_SYS_PART2 (+0x148);
		// RD_REPEAT_DATA_ERR0-7 +0x168, RD_RS_DATA_ERR0-1 +0x188
		const b = 0x20715000
		return &layout{kind: kindRS, base: b, pgmData: b,
			blocks: blocks(b, []int{9, 6, 8, 8, 8, 8, 8, 8, 8, 8},
				0x2C, 0x50, 0x68, 0x88, 0xA8, 0xC8, 0xE8, 0x108, 0x128, 0x148),
			conf: b + 0x1CC, cmd: b + 0x1D8,
			repeatErr: regs(b, 0x168, 0x16C, 0x170, 0x174, 0x178, 0x17C, 0x180, 0x184),
			rsErr:     regs(b, 0x188, 0x18C),
			timing:    timingNone, blkNumMax: 15}
	}
	return nil
}

// rsBlockError is what RD_RS_ERR says of a block: the bytes corrected and
// whether decoding failed. bad is what ESP-IDF's
// efuse_hal_is_coding_error_in_block tests.
func (l *layout) rsBlockError(errs []uint32, b int) (num int, fail, bad bool) {
	get := func(i int) uint32 {
		if i < len(errs) {
			return errs[i]
		}
		return 0
	}
	if b < 1 {
		return 0, false, false
	}
	if l.rsErrKind == rsErrC3 {
		failReg := get(b / 8)
		numReg := get((b - 1) / 8)
		fail = failReg>>(4*uint(b%8)+3)&1 != 0
		num = int(numReg >> (4 * uint((b-1)%8)) & 7)
		return num, fail, fail || num != 0
	}
	n := get((b-1)/8) >> (4 * uint((b-1)%8)) & 0xF
	return int(n & 7), n&8 != 0, n != 0
}
