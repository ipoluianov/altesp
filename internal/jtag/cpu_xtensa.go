package jtag

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ipoluianov/altesp/internal/jtag/chain"
	"github.com/ipoluianov/altesp/internal/jtag/xtensa"
)

// XtensaError is the answer for an Xtensa chip: its debug module was
// found and probed, but halting the core, running instructions on it and
// resuming it are not documented publicly (xtensa.Missing)
type XtensaError struct {
	// TAP is the TAP of the core (0: nearest TDO), TAPs the length of the
	// chain
	TAP, TAPs int
	Probe     xtensa.Probe
	Missing   []string
}

func (e *XtensaError) Error() string {
	return fmt.Sprintf("%v: the Xtensa On-Chip Debug protocol (halting the core, running instructions on it, resuming it) "+
		"is described only in the Tensilica On-Chip Debugging Guide, which is not public. "+
		"The debug module answers (TAP %d of %d: %v); flash this chip over the serial port", ErrXtensaUnavailable, e.TAP, e.TAPs, e.Probe)
}

func (e *XtensaError) Unwrap() error { return ErrXtensaUnavailable }

// xtensaCPU is the cpu of an Xtensa chip. What the documents give is
// done: the TAP chain, the XDM register access, the power-up of the debug
// domain, the OCD registers. The rest returns XtensaError; each method
// says what it would do and which fact it lacks.
type xtensaCPU struct {
	xdm   *xtensa.XDM
	tap   int
	taps  int
	probe xtensa.Probe
}

// attachXtensa finds the chain and the XDM of the first core that answers
func attachXtensa(tap chain.Resetter, t *target) (*xtensaCPU, error) {
	ids, err := chain.ScanIDCodes(tap)
	if err != nil {
		return nil, err
	}
	lens := make([]int, len(ids))
	for i := range lens {
		// TODO(stage 7): a TAP without IDCODE may have another IR length;
		// the Espressif chains are expected to hold Xtensa TAPs only
		lens[i] = xtensa.IRLen
	}
	var last error
	for i := range ids {
		if ids[i] == 0 {
			continue
		}
		c, err := chain.New(tap, lens, i)
		if err != nil {
			return nil, err
		}
		x := xtensa.New(c)
		p, err := x.ProbeCore()
		if err != nil {
			last = err
			continue
		}
		return &xtensaCPU{xdm: x, tap: i, taps: len(ids), probe: p}, nil
	}
	return nil, &DisabledError{IDCode: ids[0], Detail: fmt.Sprintf("no Xtensa debug module answers in the chain of %d TAPs: %v", len(ids), last)}
}

func (c *xtensaCPU) unavailable() error {
	return &XtensaError{TAP: c.tap, TAPs: c.taps, Probe: c.probe, Missing: xtensa.Missing}
}

// halt would set DCR.EnableOCD and DCR.DebugInterrupt (DCR_SET) and wait
// for DSR.InOCDMode.
// TODO(stage 7): xtensa.Missing[0], how the halt is acknowledged.
func (c *xtensaCPU) halt() error { return c.unavailable() }

// resetSystem would assert PWRCTL CoreReset with the debug interrupt
// pending so the core halts at its reset vector.
// TODO(stage 7): xtensa.Missing[6].
func (c *xtensaCPU) resetSystem(halt bool) error { return c.unavailable() }

// ackReset clears the sticky "was reset" bits of PWRSTAT
func (c *xtensaCPU) ackReset() error {
	_, err := c.xdm.PowerStatus(xtensa.PwrStatCoreWasReset | xtensa.PwrStatDebugWasReset)
	return err
}

// readMem would run, for each word: DDR <- address (NAR DDR),
// "rsr.ddr a2" and then "lddr32.p a2" (DIR0EXEC), and read DDR, with a2
// saved and restored around it.
// TODO(stage 7): xtensa.Missing[1], the execution handshake.
func (c *xtensaCPU) readMem(addr uint32, n int) ([]uint32, error) { return nil, c.unavailable() }

// writeMem would do the same with "sddr32.p a2" and DDREXEC.
// TODO(stage 7): xtensa.Missing[1].
func (c *xtensaCPU) writeMem(addr uint32, words []uint32) error { return c.unavailable() }

// call would run a windowed ROM function as CALLX4 does (ISA RM,
// Windowed Register Option): PS.CALLINC = 1, PS.WOE = 1, PS.EXCM = 0,
// PS.INTLEVEL = EXCMLEVEL (above that the debug exception of the
// returning BREAK still is taken, as DEBUGLEVEL 6 > INTLEVEL), a4 = the
// return address with 01 in its top two bits, a6..a11 = the arguments
// (at most 6), a1 = sp, WINDOWSTART = 1 << WINDOWBASE, EPC6 = fn, then
// RFDO; the BREAK at ret (DEBUGCAUSE.BI) halts it and the result is a6.
// The stack keeps the extra save area ESP-IDF's xt_instr_macros.h
// (SET_STACK, Apache-2.0) reserves below the stack pointer.
// TODO(stage 7): xtensa.Missing[2..4], what RFDO restores and whether the
// BREAK enters OCD halt mode.
func (c *xtensaCPU) call(ctx context.Context, fn, ret, sp uint32, timeout time.Duration, args ...uint32) (uint32, error) {
	if len(args) > 6 {
		return 0, errors.New("a windowed call takes at most 6 arguments in registers")
	}
	return 0, c.unavailable()
}

func (c *xtensaCPU) describe() string {
	return fmt.Sprintf("Xtensa debug module, TAP %d of %d, %v", c.tap, c.taps, c.probe)
}

func (c *xtensaCPU) memPath() string { return "Xtensa OCD (not available)" }

func (c *xtensaCPU) breakpoint() uint32 { return xtensa.InsnBreak }
