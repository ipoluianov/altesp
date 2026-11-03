package jtag

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ipoluianov/altesp/internal/jtag/riscv"
)

// cpu is the debug side of one chip architecture: what the flash routine
// needs of it. riscvCPU is the RISC-V debug module (stage 6); the Xtensa
// OCD of stage 7 implements the same interface.
type cpu interface {
	halt() error
	// reset resets the chip; halt asks the hart to halt as it comes out
	resetSystem(halt bool) error
	ackReset() error
	readMem(addr uint32, n int) ([]uint32, error)
	writeMem(addr uint32, words []uint32) error
	// call runs a function on the halted hart with up to 8 arguments; it
	// returns to ret (an address holding a breakpoint instruction) with
	// the stack at sp, and gives back the return value
	call(ctx context.Context, fn, ret, sp uint32, timeout time.Duration, args ...uint32) (uint32, error)
	describe() string
	memPath() string
	// breakpoint is the instruction word that halts the hart when a
	// called function returns to it
	breakpoint() uint32
}

type riscvCPU struct {
	tap riscv.Scanner
	dtm *riscv.DTM
	dm  *riscv.DM
}

// attachRISCV finds the DTM and the DM behind the TAP
func attachRISCV(tap riscv.Scanner) (*riscvCPU, error) {
	dtm, err := riscv.NewDTM(tap)
	if err != nil {
		return nil, err
	}
	dm, err := riscv.NewDM(dtm)
	if err != nil {
		return nil, err
	}
	return &riscvCPU{tap: tap, dtm: dtm, dm: dm}, nil
}

func (c *riscvCPU) halt() error { return c.dm.Halt() }

func (c *riscvCPU) resetSystem(halt bool) error { return c.dm.ResetSystem(halt) }

func (c *riscvCPU) ackReset() error { return c.dm.AckReset() }

func (c *riscvCPU) readMem(addr uint32, n int) ([]uint32, error) { return c.dm.ReadMem(addr, n) }

func (c *riscvCPU) writeMem(addr uint32, words []uint32) error { return c.dm.WriteMem(addr, words) }

func (c *riscvCPU) describe() string {
	return fmt.Sprintf("RISC-V debug module v0.%d, program buffer %d words, system bus %v",
		11+c.dm.Version, c.dm.ProgBufSize, c.dm.SBA)
}

func (c *riscvCPU) memPath() string { return c.dm.Mem.String() }

func (c *riscvCPU) breakpoint() uint32 { return riscv.InsnEbreak }

// call sets a0.., ra = ret, sp, clears mstatus.MIE so that no interrupt
// handler of the application runs while the flash is busy, makes ebreak
// in M-mode enter debug mode (dcsr.ebreakm), sets dpc = fn and resumes.
// The function returns to ret, where the ebreak halts the hart again.
func (c *riscvCPU) call(ctx context.Context, fn, ret, sp uint32, timeout time.Duration, args ...uint32) (uint32, error) {
	if len(args) > 8 {
		return 0, errors.New("more than 8 arguments")
	}
	for i, a := range args {
		if err := c.dm.WriteReg(riscv.GPR(riscv.RegA0+i), a); err != nil {
			return 0, err
		}
	}
	if err := c.dm.WriteReg(riscv.GPR(riscv.RegRA), ret); err != nil {
		return 0, err
	}
	if err := c.dm.WriteReg(riscv.GPR(riscv.RegSP), sp); err != nil {
		return 0, err
	}
	ms, err := c.dm.ReadReg(riscv.CSR(riscv.CSRMstatus))
	if err != nil {
		return 0, err
	}
	if ms&riscv.MstatusMIE != 0 {
		if err := c.dm.WriteReg(riscv.CSR(riscv.CSRMstatus), ms&^riscv.MstatusMIE); err != nil {
			return 0, err
		}
	}
	dcsr, err := c.dm.ReadReg(riscv.CSR(riscv.CSRDcsr))
	if err != nil {
		return 0, err
	}
	dcsr = dcsr&^riscv.DcsrStep | riscv.DcsrEbreakM
	if err := c.dm.WriteReg(riscv.CSR(riscv.CSRDcsr), dcsr); err != nil {
		return 0, err
	}
	if err := c.dm.WriteReg(riscv.CSR(riscv.CSRDpc), fn); err != nil {
		return 0, err
	}
	if err := c.dm.Resume(); err != nil {
		return 0, err
	}
	err = c.dm.WaitHalted(timeout, func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(time.Millisecond)
		return nil
	})
	if err != nil {
		// leave the hart halted whatever happened
		c.dm.Halt()
		if errors.Is(err, riscv.ErrTimeout) {
			return 0, fmt.Errorf("the ROM function at 0x%08x did not return: %w", fn, err)
		}
		return 0, err
	}
	pc, err := c.dm.ReadReg(riscv.CSR(riscv.CSRDpc))
	if err != nil {
		return 0, err
	}
	if dcsr, err = c.dm.ReadReg(riscv.CSR(riscv.CSRDcsr)); err != nil {
		return 0, err
	}
	cause := dcsr >> riscv.DcsrCauseShift & riscv.DcsrCauseMask
	if pc != ret || cause != riscv.CauseEbreak {
		return 0, fmt.Errorf("the ROM function at 0x%08x stopped at 0x%08x (dcsr.cause %d) instead of returning", fn, pc, cause)
	}
	return c.dm.ReadReg(riscv.GPR(riscv.RegA0))
}
