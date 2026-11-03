// Package xtensa is the host side of the Xtensa Debug Module (XDM) of
// the Xtensa cores of the ESP32 and ESP32-S3, reached through their JTAG
// TAP, and the pieces of the Xtensa ISA a debugger needs.
//
// Sources and their licenses:
//
//   - xdm-regs.h, "Common register and related definitions for the XDM
//     (Xtensa Debug Module)", Copyright (c) 2016 Cadence Design Systems
//     Inc., MIT license; shipped in ESP-IDF as
//     components/xtensa/include/xtensa/xdm-regs.h: the TAP instructions
//     (5-bit IR: PWRCTL 0x08, PWRSTAT 0x09, NAR_SEL 0x1C, IDCODE 0x1E,
//     BYPASS 0x1F), the NAR format (bit 0 write, bits 7..1 the register),
//     the NAR status bits (BUSY 0x02, ERROR 0x01), the NAR numbers of the
//     OCD registers (OCD_ID 0x40, DCR_CLR 0x42, DCR_SET 0x43, DSR 0x44,
//     DDR 0x45, DDREXEC 0x46, DIR0EXEC 0x47, DIR0 0x48, PWRCTL 0x58,
//     PWRSTAT 0x59), the DCR and DSR bits and the J_PWRCTL/J_PWRSTAT bits.
//   - specreg.h and corebits.h, Copyright (c) 2005-2011 Tensilica Inc.,
//     MIT license, in ESP-IDF components/xtensa/include/xtensa: the
//     special register numbers (DDR 104, EPC_6 182, EPS_6 198, PS 230,
//     DEBUGCAUSE 233, WINDOWBASE 72, WINDOWSTART 73) and the PS and
//     DEBUGCAUSE fields.
//   - core-isa.h of the esp32 and esp32s3 configurations (Tensilica,
//     MIT license, in ESP-IDF components/xtensa/<chip>/include/xtensa/
//     config): XCHAL_DEBUGLEVEL 6, XCHAL_EXCM_LEVEL 3, XCHAL_HAVE_OCD 1,
//     XCHAL_HAVE_OCD_LS32DDR 1, 64 physical address registers.
//   - Xtensa Instruction Set Architecture (ISA) Reference Manual,
//     Tensilica 2010 (PD-09-0801-10-01; "provided solely to enable system
//     and software developers to use Tensilica processors"): the Debug
//     Option (section 4.7.6: DEBUGCAUSE, DDR, BREAK raising a DEBUGLEVEL
//     exception, EPC[DEBUGLEVEL]/EPS[DEBUGLEVEL]), the Windowed Register
//     Option (CALLn/ENTRY/RETW) and the instruction encodings.
//   - Espressif xtensa-isa-doc (github.com/espressif/xtensa-isa-doc,
//     CC BY-NC-SA 3.0; consulted for facts only, no text taken): RSR/WSR,
//     L32I/S32I, CALLX4, RETW, ENTRY encodings and the window ABI.
//   - The encodings were cross-checked by assembling with the Espressif
//     xtensa-esp32-elf-as and reading the bytes back (the tool's output,
//     not its source).
//
// What is NOT in any of these and keeps the OCD from being driven: see
// Missing.
package xtensa

import (
	"errors"
	"fmt"

	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
)

// TAP instructions (xdm-regs.h)
const (
	IRLen     = 5
	IRPwrCtl  = 0x08
	IRPwrStat = 0x09
	IRNARSel  = 0x1C
	IRIDCODE  = 0x1E
	IRBypass  = 0x1F
)

// NAR addresses of the XDM registers (xdm-regs.h)
const (
	NARTraxID   = 0x00
	NAROCDID    = 0x40
	NARDCRClr   = 0x42
	NARDCRSet   = 0x43
	NARDSR      = 0x44
	NARDDR      = 0x45
	NARDDRExec  = 0x46
	NARDIR0Exec = 0x47
	NARDIR0     = 0x48
	NARPwrCtl   = 0x58
	NARPwrStat  = 0x59
	NARERIStat  = 0x5A

	narWrite      = 0x01
	narAddrShift  = 1
	NARStatBusy   = 0x02
	NARStatError  = 0x01
	narScanBits   = 8
	ndrScanBits   = 32
	pwrScanBits   = 8
	maxBusyRetry  = 100
	busyIdleClock = 16
)

// DCR bits (xdm-regs.h)
const (
	DCREnableOCD   = 0x0000001
	DCRDebugInt    = 0x0000002
	DCRDebugOvr    = 0x0000004
	DCRStepEnable  = 0x0000040
	DCRBreakInEn   = 0x0010000
	DCRBreakOutEn  = 0x0020000
	DCRDebugIntEn  = 0x0040000
	DCRDbgSWActive = 0x0100000
	DCRStallInEn   = 0x0200000
	DCRDebugOutEn  = 0x0400000
)

// DSR bits (xdm-regs.h, "DOSR")
const (
	DSRExecDone     = 0x01
	DSRException    = 0x02
	DSRBusy         = 0x04
	DSROverrun      = 0x08
	DSRInOCDMode    = 0x10
	DSRStopCauseSh  = 5
	DSRStopCauseMsk = 0xF
	DSRDebugPendBin = 0x10000
	DSRDebugPendHst = 0x20000
	DSRDebugStall   = 0x1000000
	DSRCoreOn       = 0x40000000
	DSRDebugOn      = 0x80000000
)

// Power control and status as the JTAG PWRCTL/PWRSTAT scans see them
// (xdm-regs.h, the J_* values)
const (
	PwrCtlCoreWakeup  = 0x01
	PwrCtlMemWakeup   = 0x02
	PwrCtlDebugWakeup = 0x04
	PwrCtlCoreReset   = 0x10
	PwrCtlDebugReset  = 0x40
	PwrCtlDebugUse    = 0x80
	PwrCtlAllOn       = PwrCtlDebugUse | PwrCtlCoreWakeup | PwrCtlMemWakeup | PwrCtlDebugWakeup

	PwrStatCoreOn        = 0x01
	PwrStatMemOn         = 0x02
	PwrStatDebugOn       = 0x04
	PwrStatCoreStill     = 0x08
	PwrStatCoreWasReset  = 0x10
	PwrStatDebugWasReset = 0x40
	PwrStatAllOn         = PwrStatCoreOn | PwrStatMemOn | PwrStatDebugOn
)

// Scanner is a TAP driver (espjtag.TAP, ftdi.TAP, or a chain.Chain in
// front of one)
type Scanner interface {
	IR(n int, v uint64)
	DR(n int, v uint64, capture int, idle int) *espjtag.Result
	Run() error
}

// Errors of the XDM access
var (
	ErrNARError = errors.New("the XDM reported an error for the register access")
	ErrNARBusy  = errors.New("the XDM stayed busy")
)

// XDM reaches the debug module of one Xtensa core
type XDM struct {
	tap Scanner
	// Retries counts the busy retries, for the tests
	Retries int
}

// New makes an XDM over a TAP (a chain.Chain when there are several)
func New(tap Scanner) *XDM { return &XDM{tap: tap} }

// access is one XDM register access through NAR/NDR. unverified (TZ.md
// "Hardware checks"): xdm-regs.h says only that NAR_SEL selects "8-bit
// NAR / 32-bit NDR" alternately; here the NAR scan captures the status
// (BUSY, ERROR) and the NDR scan that follows it carries the data, read
// or written, and a busy status is retried.
func (x *XDM) access(nar uint8, write bool, v uint32) (uint32, error) {
	a := uint64(nar) << narAddrShift
	if write {
		a |= narWrite
	}
	for try := 0; ; try++ {
		x.tap.IR(IRLen, IRNARSel)
		st := x.tap.DR(narScanBits, a, narScanBits, 0)
		d := x.tap.DR(ndrScanBits, uint64(v), ndrScanBits, 0)
		// the status of this access, as the next NAR scan sees it
		x.tap.IR(IRLen, IRNARSel)
		after := x.tap.DR(narScanBits, uint64(NAROCDID)<<narAddrShift, narScanBits, 0)
		x.tap.DR(ndrScanBits, 0, 0, 0)
		if err := x.tap.Run(); err != nil {
			return 0, err
		}
		s := st.Value() | after.Value()
		switch {
		case s&NARStatError != 0:
			return 0, fmt.Errorf("%w (NAR 0x%02x, status 0x%02x)", ErrNARError, nar, s)
		case s&NARStatBusy != 0:
			if try >= maxBusyRetry {
				return 0, fmt.Errorf("%w (NAR 0x%02x)", ErrNARBusy, nar)
			}
			x.Retries++
			continue
		}
		return uint32(d.Value()), nil
	}
}

// Read reads an XDM register by its NAR address
func (x *XDM) Read(nar uint8) (uint32, error) { return x.access(nar, false, 0) }

// Write writes an XDM register by its NAR address
func (x *XDM) Write(nar uint8, v uint32) error {
	_, err := x.access(nar, true, v)
	return err
}

// pwr shifts v through the 8-bit PWRCTL or PWRSTAT register and returns
// what it held
func (x *XDM) pwr(ir uint64, v uint8) (uint8, error) {
	x.tap.IR(IRLen, ir)
	r := x.tap.DR(pwrScanBits, uint64(v), pwrScanBits, 0)
	if err := x.tap.Run(); err != nil {
		return 0, err
	}
	return uint8(r.Value()), nil
}

// PowerControl writes the power and reset control register and returns
// its old value
func (x *XDM) PowerControl(v uint8) (uint8, error) { return x.pwr(IRPwrCtl, v) }

// PowerStatus reads the power and reset status; the sticky "was reset"
// bits set in clear are written back to clear them. unverified: that
// writing 1 clears them (xdm-regs.h names them sticky but doesn't say
// how they clear).
func (x *XDM) PowerStatus(clear uint8) (uint8, error) { return x.pwr(IRPwrStat, clear) }

// Probe is what the documented part of the XDM tells about a core
type Probe struct {
	PwrStat uint8
	OCDID   uint32
	DSR     uint32
	DCR     uint32
}

func (p Probe) String() string {
	return fmt.Sprintf("OCD_ID 0x%08x, DSR 0x%08x, DCR 0x%08x, PWRSTAT 0x%02x", p.OCDID, p.DSR, p.DCR, p.PwrStat)
}

// ProbeCore keeps the debug domain of the core powered for the debugger
// (J_PWRCTL_ALL_ON: the debug, memory and core wake-up bits and
// J_DEBUG_USE) and reads its power status and the documented OCD
// registers. It changes nothing in the run state of the core.
func (x *XDM) ProbeCore() (Probe, error) {
	var p Probe
	if _, err := x.PowerControl(PwrCtlAllOn); err != nil {
		return p, err
	}
	st, err := x.PowerStatus(PwrStatCoreWasReset | PwrStatDebugWasReset)
	if err != nil {
		return p, err
	}
	p.PwrStat = st
	if st&PwrStatDebugOn == 0 {
		return p, fmt.Errorf("the debug domain of the core is off (PWRSTAT 0x%02x)", st)
	}
	if p.OCDID, err = x.Read(NAROCDID); err != nil {
		return p, err
	}
	if p.DSR, err = x.Read(NARDSR); err != nil {
		return p, err
	}
	if p.DCR, err = x.Read(NARDCRSet); err != nil {
		return p, err
	}
	return p, nil
}
