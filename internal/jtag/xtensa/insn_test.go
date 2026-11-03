package xtensa

import "testing"

// The words xtensa-esp32-elf-as (Espressif GCC 8.4 toolchain) produced
// for these instructions, read back with objdump -d
func TestEncodings(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"rsr.ddr a2", RSR(SRDDR, 2), 0x036820},
		{"wsr.ddr a2", WSR(SRDDR, 2), 0x136820},
		{"rsr.ddr a3", RSR(SRDDR, 3), 0x036830},
		{"rsr.epc6 a2", RSR(SREPC6, 2), 0x03b620},
		{"wsr.epc6 a2", WSR(SREPC6, 2), 0x13b620},
		{"rsr.eps6 a2", RSR(SREPS6, 2), 0x03c620},
		{"wsr.eps6 a2", WSR(SREPS6, 2), 0x13c620},
		{"rsr.ps a2", RSR(SRPS, 2), 0x03e620},
		{"wsr.ps a2", WSR(SRPS, 2), 0x13e620},
		{"rsr.windowbase a2", RSR(SRWindowBase, 2), 0x034820},
		{"wsr.windowstart a2", WSR(SRWindowStart, 2), 0x134920},
		{"rsr.debugcause a2", RSR(SRDebugCause, 2), 0x03e920},
		{"wsr.excsave6 a2", WSR(SRExcSave6, 2), 0x13d620},
		{"l32i a3, a2, 0", L32I(3, 2, 0), 0x002232},
		{"s32i a3, a2, 0", S32I(3, 2, 0), 0x006232},
		{"lddr32.p a2", LDDR32P(2), 0x0072e0},
		{"sddr32.p a2", SDDR32P(2), 0x0072f0},
		{"rfdo 0", InsnRFDO0, 0xf1e000},
		{"break 1, 15", InsnBreak, 0x0041f0},
		{"isync", InsnISync, 0x002000},
		{"rsync", InsnRSync, 0x002010},
		{"dsync", InsnDSync, 0x002030},
		{"nop", InsnNop, 0x0020f0},
		{"callx4 a0", InsnCallX4, 0x0000d0},
		{"retw", InsnRetW, 0x000090},
		{"l32i a3, a2, 8", L32I(3, 2, 8), 0x022232},
		{"s32i a3, a2, 1020", S32I(3, 2, 1020), 0xff6232},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: 0x%06x, want 0x%06x", c.name, c.got, c.want)
		}
	}
}
