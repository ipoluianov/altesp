package xtensa

import (
	"errors"
	"strings"
)

// Special registers (specreg.h)
const (
	SRWindowBase  = 72
	SRWindowStart = 73
	SRDDR         = 104
	SREPC6        = 182
	SREPS6        = 198
	SRExcSave6    = 214
	SRPS          = 230
	SRDebugCause  = 233
)

// Configuration of the ESP32 and ESP32-S3 cores (core-isa.h)
const (
	DebugLevel = 6
	ExcmLevel  = 3
	NumARegs   = 64
)

// PS fields (corebits.h)
const (
	PSIntLevelMask = 0x0000000F
	PSExcm         = 0x00000010
	PSUM           = 0x00000020
	PSCallIncShift = 16
	PSCallIncMask  = 0x00030000
	PSWOE          = 0x00040000
)

// DEBUGCAUSE fields (ISA RM table 4-123, corebits.h)
const (
	DebugCauseICount   = 0x01
	DebugCauseIBreak   = 0x02
	DebugCauseDBreak   = 0x04
	DebugCauseBreak    = 0x08
	DebugCauseBreakN   = 0x10
	DebugCauseDebugInt = 0x20
)

// Instruction encodings as 24-bit little-endian words (the ISA RM
// chapter 6, cross-checked with xtensa-esp32-elf-as; see insn_test.go)

// RSR is "rsr at, sr": AR[t] <- SR[sr]
func RSR(sr, t uint8) uint32 { return 0x030000 | uint32(sr)<<8 | uint32(t&15)<<4 }

// WSR is "wsr at, sr": SR[sr] <- AR[t]
func WSR(sr, t uint8) uint32 { return 0x130000 | uint32(sr)<<8 | uint32(t&15)<<4 }

// L32I is "l32i at, as, off" (off a multiple of 4, up to 1020)
func L32I(t, s uint8, off uint32) uint32 {
	return (off/4&0xFF)<<16 | 0x2<<12 | uint32(s&15)<<8 | uint32(t&15)<<4 | 0x2
}

// S32I is "s32i at, as, off"
func S32I(t, s uint8, off uint32) uint32 {
	return (off/4&0xFF)<<16 | 0x6<<12 | uint32(s&15)<<8 | uint32(t&15)<<4 | 0x2
}

// LDDR32P is "lddr32.p as": DDR <- mem[AR[s]], AR[s] += 4 (OCD only;
// XCHAL_HAVE_OCD_LS32DDR)
func LDDR32P(s uint8) uint32 { return 0x0070E0 | uint32(s&15)<<8 }

// SDDR32P is "sddr32.p as": mem[AR[s]] <- DDR, AR[s] += 4 (OCD only)
func SDDR32P(s uint8) uint32 { return 0x0070F0 | uint32(s&15)<<8 }

// Fixed instructions
const (
	InsnRFDO0  = 0xF1E000 // rfdo 0: return from debug operation (OCD only)
	InsnBreak  = 0x0041F0 // break 1, 15
	InsnISync  = 0x002000
	InsnRSync  = 0x002010
	InsnDSync  = 0x002030
	InsnNop    = 0x0020F0
	InsnCallX4 = 0x0000D0 // callx4 a0 (| s<<8 for as)
	InsnRetW   = 0x000090
)

// ErrUnavailable is the error of everything the OCD can't do yet
var ErrUnavailable = errors.New("Xtensa JTAG is not available yet")

// Missing lists the facts that the public documents don't give and that
// halting, running instructions on and resuming an Xtensa core over JTAG
// need. The Xtensa ISA Reference Manual (2010, sections 4.7.6 and the
// RFDO/RFDD pages) defers all of them to the "Tensilica On-Chip
// Debugging Guide", which Cadence gives only to licensees; xdm-regs.h
// names the registers and bits but not the protocol. Each would come
// from that guide, or from Espressif publishing the OCD chapter for the
// ESP32/ESP32-S3 cores.
var Missing = []string{
	"how a halt is requested and acknowledged: whether setting DCR.DebugInterrupt (with DCR.EnableOCD) makes the core enter OCD halt mode rather than take InterruptVector[DEBUGLEVEL], and which DSR bits (InOCDMode, Stopped/StopCause) confirm it",
	"the instruction-execution handshake: writing DIR0EXEC (and DIR0 for instructions longer than 32 bits) and how DSR.ExecDone/ExecException/ExecBusy/ExecOverrun are set and cleared (write-1-to-clear or not), and whether DDREXEC re-runs the last DIR",
	"what RFDO does: where the core resumes (EPC[DEBUGLEVEL]?), which PS it restores (EPS[DEBUGLEVEL]?), and whether DCR.DebugInterrupt must be cleared first",
	"which registers the halted core exposes: that EPC6/EPS6 hold the PC and PS at the halt, and that instructions run in OCD mode see the live a0..a15 window",
	"whether a BREAK instruction executed with DCR.EnableOCD set enters OCD halt mode (needed to return from a ROM function call to the debugger)",
	"the NAR/NDR scan protocol in detail: whether a NAR scan captures the status of the previous or the current access, and when the read data is captured",
	"how the chip is reset with the core halted at the reset vector (PWRCTL CoreReset with DCR.DebugInterrupt held?), needed to reach the ROM's download mode",
	"the JTAG chain of the ESP32 and ESP32-S3: how many TAPs (one per core?), their order and IDCODEs (xdm-regs.h gives 0x120034E5 as a FIXME)",
}

// MissingText is Missing as one paragraph
func MissingText() string { return strings.Join(Missing, "; ") }
