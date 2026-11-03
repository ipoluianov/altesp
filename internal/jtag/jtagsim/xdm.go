package jtagsim

import "fmt"

// The Xtensa side of the simulator: a chain of Xtensa TAPs, each with the
// XDM (Xtensa Debug Module) registers reached through NAR/NDR, written
// from xdm-regs.h (Cadence, MIT license; in ESP-IDF
// components/xtensa/include/xtensa/xdm-regs.h): the 5-bit IR and its
// instructions, the 8-bit NAR (bit 0 write, bits 7..1 the register),
// the 32-bit NDR, the BUSY and ERROR bits a NAR scan captures, the 8-bit
// power control and status registers and their J_* bits, and the OCD
// register numbers.
//
// What the header does not say is modeled the simplest way and is
// unverified on hardware (TZ.md "Hardware checks"): a NAR scan captures
// the status of the access before it; a NAR scan with the write bit clear
// reads the register at its Update-DR, and the NDR scan that follows
// captures the value; a NAR scan with the write bit set is followed by an
// NDR scan whose Update-DR writes; selecting the NAR_SEL instruction makes
// the next scan a NAR scan. Execution of instructions (DIR0EXEC), halting
// and resuming are not modeled: the Tensilica On-Chip Debugging Guide
// that describes them is not public, and the host does not use them.

// XtensaConfig makes the device an Xtensa chip
type XtensaConfig struct {
	// Cores is the number of TAPs in the chain, 1 when 0. TAP 0 is the
	// one nearest TDO.
	Cores int
	// OCDID is what the OCD_ID register reads
	OCDID uint32
}

// XDM register numbers as NAR addresses and bits (xdm-regs.h)
const (
	xirPWRCTL  = 0x08
	xirPWRSTAT = 0x09
	xirNARSEL  = 0x1C
	xirIDCODE  = 0x1E
	xirBYPASS  = 0x1F

	narOCDID    = 0x40
	narDCRCLR   = 0x42
	narDCRSET   = 0x43
	narDSR      = 0x44
	narDDR      = 0x45
	narDDREXEC  = 0x46
	narDIR0EXEC = 0x47
	narPWRCTL   = 0x58
	narPWRSTAT  = 0x59

	narBusy  = 0x02
	narError = 0x01

	jPwrCoreOn       = 0x01
	jPwrMemOn        = 0x02
	jPwrDebugOn      = 0x04
	jPwrCoreWasReset = 0x10
	jPwrDbgWasReset  = 0x40

	dsrCoreOn  = 0x40000000
	dsrDebugOn = 0x80000000
)

// xtTap is one Xtensa TAP with its XDM
type xtTap struct {
	ir      uint32
	shift   uint64
	n       int
	narNext bool
	nar     uint8
	write   bool
	errSt   bool
	rdata   uint32
	pwrctl  uint8
	pwrstat uint8
	dcr     uint32
	ddr     uint32
	ocdID   uint32
	idcode  uint32
}

type xtChain struct {
	taps []*xtTap
	// Accesses counts the XDM register accesses by NAR address
	accesses map[uint8]int
}

func newXtChain(cfg Config) *xtChain {
	n := cfg.Xtensa.Cores
	if n <= 0 {
		n = 1
	}
	c := &xtChain{accesses: map[uint8]int{}}
	for i := 0; i < n; i++ {
		c.taps = append(c.taps, &xtTap{
			ir: xirIDCODE, narNext: true, idcode: cfg.IDCode, ocdID: cfg.Xtensa.OCDID,
			pwrstat: jPwrCoreOn | jPwrMemOn | jPwrDebugOn | jPwrCoreWasReset | jPwrDbgWasReset,
		})
	}
	return c
}

func (t *xtTap) drLen() int {
	switch t.ir {
	case xirIDCODE:
		return 32
	case xirPWRCTL, xirPWRSTAT:
		return 8
	case xirNARSEL:
		if t.narNext {
			return 8
		}
		return 32
	}
	return 1
}

func (t *xtTap) captureDR() uint64 {
	switch t.ir {
	case xirIDCODE:
		return uint64(t.idcode)
	case xirPWRCTL:
		return uint64(t.pwrctl)
	case xirPWRSTAT:
		return uint64(t.pwrstat)
	case xirNARSEL:
		if t.narNext {
			if t.errSt {
				return narError
			}
			return 0
		}
		return uint64(t.rdata)
	}
	return 0
}

func (c *xtChain) updateDR(d *Device, t *xtTap) {
	v := uint32(t.shift)
	switch t.ir {
	case xirPWRCTL:
		t.pwrctl = uint8(v)
	case xirPWRSTAT:
		t.pwrstat &^= uint8(v) & (jPwrCoreWasReset | jPwrDbgWasReset)
	case xirNARSEL:
		if t.narNext {
			t.nar, t.write = uint8(v>>1)&0x7F, v&1 != 0
			t.errSt = false
			if !t.write {
				v, ok := t.readReg(d, t.nar)
				t.rdata, t.errSt = v, !ok
				c.accesses[t.nar]++
			}
			t.narNext = false
			return
		}
		if t.write {
			if !t.writeReg(d, t.nar, v) {
				t.errSt = true
			}
			c.accesses[t.nar]++
		}
		t.narNext = true
	}
}

func (t *xtTap) dsr() uint32 {
	v := uint32(0)
	if t.pwrstat&jPwrCoreOn != 0 {
		v |= dsrCoreOn
	}
	if t.pwrstat&jPwrDebugOn != 0 {
		v |= dsrDebugOn
	}
	return v
}

func (t *xtTap) readReg(d *Device, nar uint8) (uint32, bool) {
	switch nar {
	case narOCDID:
		return t.ocdID, true
	case narDCRSET, narDCRCLR:
		return t.dcr, true
	case narDSR:
		return t.dsr(), true
	case narDDR:
		return t.ddr, true
	case narPWRCTL:
		return uint32(t.pwrctl), true
	case narPWRSTAT:
		return uint32(t.pwrstat), true
	}
	d.Errors = append(d.Errors, fmt.Sprintf("XDM read of NAR 0x%02x is not modeled", nar))
	return 0, false
}

func (t *xtTap) writeReg(d *Device, nar uint8, v uint32) bool {
	switch nar {
	case narDCRSET:
		t.dcr |= v
	case narDCRCLR:
		t.dcr &^= v
	case narDDR:
		t.ddr = v
	case narDSR:
		// the sticky bits: how they clear is not documented
	case narDIR0EXEC, narDDREXEC:
		d.Errors = append(d.Errors, "instruction execution through DIR0EXEC/DDREXEC is not modeled (undocumented)")
		return false
	default:
		d.Errors = append(d.Errors, fmt.Sprintf("XDM write of NAR 0x%02x is not modeled", nar))
		return false
	}
	return true
}

// clock is one TCK cycle through the whole chain: TDI enters the last
// TAP, TDO leaves TAP 0
func (c *xtChain) clock(d *Device, tms, tdi bool) {
	switch d.state {
	case sCapDR:
		for _, t := range c.taps {
			t.n = t.drLen()
			t.shift = t.captureDR()
		}
	case sCapIR:
		for _, t := range c.taps {
			t.n = 5
			t.shift = 1 // IEEE 1149.1 7.1.1: the low bits 01
		}
	case sShiftDR, sShiftIR:
		in := tdi
		for i := len(c.taps) - 1; i >= 0; i-- {
			t := c.taps[i]
			out := t.shift&1 != 0
			t.shift >>= 1
			if in {
				t.shift |= 1 << uint(t.n-1)
			}
			in = out
		}
	}
	s := 0
	if tms {
		s = 1
	}
	d.state = next[d.state][s]
	switch d.state {
	case sTLR:
		for _, t := range c.taps {
			t.ir, t.narNext = xirIDCODE, true
		}
	case sUpdIR:
		for _, t := range c.taps {
			t.ir = uint32(t.shift) & 0x1F
			if t.ir == xirNARSEL {
				t.narNext = true
			}
		}
	case sUpdDR:
		for _, t := range c.taps {
			c.updateDR(d, t)
		}
	}
}

func (c *xtChain) tdo(d *Device) bool {
	if d.state == sShiftDR || d.state == sShiftIR {
		return c.taps[0].shift&1 != 0
	}
	return false
}

// XDMAccesses counts the XDM register accesses of TAP chain by NAR
// address
func (d *Device) XDMAccesses() map[uint8]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[uint8]int{}
	if d.xt != nil {
		for k, v := range d.xt.accesses {
			out[k] = v
		}
	}
	return out
}

// XDMState returns DCR and PWRCTL of TAP i
func (d *Device) XDMState(i int) (dcr uint32, pwrctl uint8) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.xt.taps[i]
	return t.dcr, t.pwrctl
}
