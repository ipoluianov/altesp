// Package jtagsim is the other end of the JTAG transport for the tests: a
// simulated Espressif USB-JTAG controller (the command protocol of the
// TRM), an IEEE 1149.1 TAP, a RISC-V JTAG DTM and Debug Module (debug
// spec 0.13.2) and a hart that runs a few RV32I instructions. The memory
// and the ROM behind the hart are a Bus, supplied by the caller (the
// chip model of package jtag). It is written from the same documents as
// the host side, independently of it, so the tests check the host
// against the documents rather than against itself.
package jtagsim

import (
	"errors"
	"sync"
)

// ErrNoData is what Read returns when the controller has nothing to send
var ErrNoData = errors.New("jtagsim: no TDO data pending")

// ErrGone is returned after a reset that re-enumerated the USB device,
// until Reopen
var ErrGone = errors.New("jtagsim: the USB device is gone")

// CallResult is what a Bus did at the pc of the hart
type CallResult int

const (
	// NotROM: no modeled function there, the instruction is fetched
	NotROM CallResult = iota
	// Returned: the function ran, a0 is set, the hart goes on at ra
	Returned
	// ChipReset: the chip reset itself (a watchdog)
	ChipReset
)

// Bus is the memory, the registers and the ROM the hart sees
type Bus interface {
	Read32(addr uint32) (uint32, bool)
	Write32(addr, v uint32) bool
	// Fetch reads an instruction at a pc (the instruction bus)
	Fetch(pc uint32) (uint32, bool)
	// Call runs the ROM function at h.PC, if one is modeled there
	Call(h *Hart) CallResult
	// Reset is a system reset of the chip (ndmreset, a watchdog)
	Reset()
}

// Config is the debug hardware of the chip
type Config struct {
	IDCode uint32
	// Disabled: the TAP doesn't answer, TDO stays 1 (JTAG disabled by
	// eFuse)
	Disabled bool
	// NoDTM: the TAP answers but dtmcs reads 0 (JTAG soft-disabled)
	NoDTM bool
	Abits int
	// IdleHint is dtmcs.idle
	IdleHint int
	// BusyCycles is how many TCK cycles a DMI access takes; a scan that
	// comes sooner sees busy
	BusyCycles int
	SBA        bool
	// AbstractMem: the DM supports abstract memory access
	AbstractMem bool
	ProgBufSize int
	ImpEbreak   bool
	// HaltPolls: after the hart halts by itself, dmstatus shows it
	// running for this many reads (the ROM function takes time)
	HaltPolls int
	// MaxSteps bounds a run of the hart; beyond it the hart is lost in
	// code the model doesn't know and keeps running
	MaxSteps int
	// Reenumerate: a system reset drops the USB device until Reopen
	Reenumerate bool
	// AppPC is where a running application is found when halted
	AppPC uint32
	// Xtensa makes the chip an Xtensa one: a chain of Xtensa TAPs with
	// their XDM instead of the RISC-V DTM and DM (xdm.go)
	Xtensa *XtensaConfig
}

// Hart is the state of the RISC-V hart
type Hart struct {
	X       [32]uint32
	PC      uint32
	Dcsr    uint32
	Dpc     uint32
	Mstatus uint32
	Halted  bool
	// Lost: the hart runs code the model can't run; only a halt request
	// or a reset brings it back
	Lost bool
}

// TAP states, IEEE 1149.1 figure 6-1
type tapState int

const (
	sTLR tapState = iota
	sRTI
	sSelDR
	sCapDR
	sShiftDR
	sExit1DR
	sPauseDR
	sExit2DR
	sUpdDR
	sSelIR
	sCapIR
	sShiftIR
	sExit1IR
	sPauseIR
	sExit2IR
	sUpdIR
)

// next[state][tms]
var next = [16][2]tapState{
	sTLR:     {sRTI, sTLR},
	sRTI:     {sRTI, sSelDR},
	sSelDR:   {sCapDR, sSelIR},
	sCapDR:   {sShiftDR, sExit1DR},
	sShiftDR: {sShiftDR, sExit1DR},
	sExit1DR: {sPauseDR, sUpdDR},
	sPauseDR: {sPauseDR, sExit2DR},
	sExit2DR: {sShiftDR, sUpdDR},
	sUpdDR:   {sRTI, sSelDR},
	sSelIR:   {sCapIR, sTLR},
	sCapIR:   {sShiftIR, sExit1IR},
	sShiftIR: {sShiftIR, sExit1IR},
	sExit1IR: {sPauseIR, sUpdIR},
	sPauseIR: {sPauseIR, sExit2IR},
	sExit2IR: {sShiftIR, sUpdIR},
	sUpdIR:   {sRTI, sSelDR},
}

const (
	irLen    = 5
	irIDCODE = 0x01
	irDTMCS  = 0x10
	irDMI    = 0x11
)

// Device is the simulated USB-JTAG device with the chip behind it
type Device struct {
	cfg Config
	bus Bus

	mu   sync.Mutex
	gone bool
	// controller
	out     []byte
	capBits int
	capByte byte
	srst    bool
	// Errors records protocol misuse the host must not do
	Errors []string
	// TAP
	state  tapState
	ir     uint32
	shift  uint64
	shiftN int
	cycles int64
	// DTM
	dmiBusyUntil        int64
	dmiSticky           bool
	dmiData             uint32
	dmiStatus           uint32
	dmiAddr             uint32
	DMIBusyHits, Resets int
	// DM
	dmactive          bool
	haltreq, ndmreset bool
	resumeAck         bool
	haveReset         bool
	cmdErr            uint32
	data              [2]uint32
	progbuf           [16]uint32
	sbcs              uint32
	sbaddr, sbdata    uint32
	hart              Hart
	pendingPolls      int
	xt                *xtChain
}

// New makes a device in front of a bus; the hart runs the application
func New(cfg Config, bus Bus) *Device {
	if cfg.Abits == 0 {
		cfg.Abits = 7
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 10000
	}
	d := &Device{cfg: cfg, bus: bus}
	if cfg.Xtensa != nil {
		d.xt = newXtChain(cfg)
	}
	d.hart.Lost = true
	d.hart.PC = cfg.AppPC
	return d
}

// Hart returns a copy of the hart state
func (d *Device) Hart() Hart {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hart
}

// Reopen brings the device back after a re-enumeration
func (d *Device) Reopen() {
	d.mu.Lock()
	d.gone = false
	d.out = nil
	d.capBits = 0
	d.mu.Unlock()
}

// Gone tells whether the device is off the bus
func (d *Device) Gone() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.gone
}

// Write takes commands from the bulk OUT endpoint: two per byte, the
// high nibble first
func (d *Device) Write(p []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone {
		return ErrGone
	}
	for _, b := range p {
		for _, c := range []byte{b >> 4, b & 0xF} {
			d.command(c)
			if d.gone {
				return nil
			}
		}
	}
	return nil
}

// Read gives the captured TDO bytes from the bulk IN endpoint
func (d *Device) Read(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone {
		return 0, ErrGone
	}
	if len(d.out) == 0 {
		return 0, ErrNoData
	}
	n := copy(p, d.out)
	d.out = d.out[n:]
	return n, nil
}

func (d *Device) command(c byte) {
	switch {
	case c&0x8 == 0: // CMD_CLK
		capture, tms, tdi := c&4 != 0, c&2 != 0, c&1 != 0
		if capture {
			d.pushTDO(d.tdo())
		}
		d.clock(tms, tdi)
	case c == 0x8 || c == 0x9: // CMD_RST
		d.srst = c&1 != 0
	case c == 0xA: // CMD_FLUSH
		if d.capBits > 0 {
			d.out = append(d.out, d.capByte)
			d.capBits, d.capByte = 0, 0
		}
	case c == 0xB:
	default:
		d.Errors = append(d.Errors, "CMD_REP is not modeled")
	}
}

func (d *Device) pushTDO(bit bool) {
	if bit {
		d.capByte |= 1 << uint(d.capBits)
	}
	d.capBits++
	if d.capBits == 8 {
		d.out = append(d.out, d.capByte)
		d.capBits, d.capByte = 0, 0
	}
}

func (d *Device) tdo() bool {
	if d.cfg.Disabled {
		return true
	}
	if d.xt != nil {
		return d.xt.tdo(d)
	}
	if d.state == sShiftDR || d.state == sShiftIR {
		return d.shift&1 != 0
	}
	return false
}

func (d *Device) drLen() int {
	switch d.ir {
	case irIDCODE, irDTMCS:
		return 32
	case irDMI:
		return d.cfg.Abits + 34
	}
	return 1
}

// clock is one rising edge of TCK: the action of the current state, then
// the transition
func (d *Device) clock(tms, tdi bool) {
	d.cycles++
	if d.cfg.Disabled {
		return
	}
	if d.xt != nil {
		d.xt.clock(d, tms, tdi)
		return
	}
	switch d.state {
	case sCapDR:
		d.shiftN = d.drLen()
		d.shift = d.captureDR()
	case sShiftDR, sShiftIR:
		d.shift >>= 1
		if tdi {
			d.shift |= 1 << uint(d.shiftN-1)
		}
	case sCapIR:
		d.shiftN = irLen
		d.shift = 1 // the low bits 01, IEEE 1149.1 7.1.1
	}
	t := 0
	if tms {
		t = 1
	}
	d.state = next[d.state][t]
	switch d.state {
	case sTLR:
		d.ir = irIDCODE
	case sUpdIR:
		d.ir = uint32(d.shift) & (1<<irLen - 1)
	case sUpdDR:
		d.updateDR()
	}
}

func (d *Device) captureDR() uint64 {
	switch d.ir {
	case irIDCODE:
		return uint64(d.cfg.IDCode)
	case irDTMCS:
		if d.cfg.NoDTM {
			return 0
		}
		st := uint32(0)
		if d.dmiSticky {
			st = 3
		}
		return uint64(1 | uint32(d.cfg.Abits)<<4 | st<<10 | uint32(d.cfg.IdleHint)<<12)
	case irDMI:
		if d.cfg.NoDTM {
			return 0
		}
		if !d.dmiSticky && d.cycles < d.dmiBusyUntil {
			d.dmiSticky = true
			d.DMIBusyHits++
		}
		st := d.dmiStatus
		if d.dmiSticky {
			st = 3
		}
		return uint64(d.dmiAddr)<<34 | uint64(d.dmiData)<<2 | uint64(st)
	}
	return 0
}

func (d *Device) updateDR() {
	switch d.ir {
	case irDTMCS:
		v := uint32(d.shift)
		if v&(1<<16) != 0 || v&(1<<17) != 0 {
			d.dmiSticky = false
			d.dmiStatus = 0
		}
	case irDMI:
		if d.cfg.NoDTM || d.dmiSticky {
			return
		}
		op := uint32(d.shift & 3)
		data := uint32(d.shift >> 2)
		addr := uint32(d.shift>>34) & (1<<uint(d.cfg.Abits) - 1)
		if op == 0 {
			return
		}
		d.dmiAddr = addr
		d.dmiStatus = 0
		if op == 1 {
			d.dmiData = d.dmRead(addr)
		} else if op == 2 {
			d.dmWrite(addr, data)
			d.dmiData = data
		} else {
			d.dmiStatus = 2
		}
		d.dmiBusyUntil = d.cycles + int64(d.cfg.BusyCycles)
	}
}

// The DM registers, debug spec 3.12
const (
	rData0     = 0x04
	rData1     = 0x05
	rDMControl = 0x10
	rDMStatus  = 0x11
	rHartInfo  = 0x12
	rAbsCS     = 0x16
	rCommand   = 0x17
	rAbsAuto   = 0x18
	rProgBuf0  = 0x20
	rSBCS      = 0x38
	rSBAddr0   = 0x39
	rSBData0   = 0x3C
)

func (d *Device) dmRead(addr uint32) uint32 {
	switch {
	case addr == rData0 || addr == rData1:
		return d.data[addr-rData0]
	case addr == rDMControl:
		v := uint32(0)
		if d.dmactive {
			v |= 1
		}
		if d.ndmreset {
			v |= 2
		}
		return v
	case addr == rDMStatus:
		return d.dmstatus()
	case addr == rAbsCS:
		return uint32(d.cfg.ProgBufSize)<<24 | d.cmdErr<<8 | 2
	case addr >= rProgBuf0 && addr < rProgBuf0+16:
		return d.progbuf[addr-rProgBuf0]
	case addr == rSBCS:
		if !d.cfg.SBA {
			return 0
		}
		return 1<<29 | d.sbcs | 32<<5 | 1<<2
	case addr == rSBAddr0:
		return d.sbaddr
	case addr == rSBData0:
		if !d.cfg.SBA {
			return 0
		}
		v := d.sbdata
		if d.sbcs&(1<<15) != 0 {
			d.sbAccess(false)
		}
		return v
	}
	return 0
}

func (d *Device) dmstatus() uint32 {
	if !d.dmactive {
		return 0
	}
	v := uint32(2 | 1<<7) // version 0.13, authenticated
	if d.cfg.ImpEbreak {
		v |= 1 << 22
	}
	halted := d.hart.Halted
	if halted && d.pendingPolls > 0 {
		d.pendingPolls--
		halted = false
	}
	if halted {
		v |= 1<<9 | 1<<8
	} else {
		v |= 1<<11 | 1<<10
	}
	if d.resumeAck {
		v |= 1<<17 | 1<<16
	}
	if d.haveReset {
		v |= 1<<19 | 1<<18
	}
	return v
}

func (d *Device) dmWrite(addr, v uint32) {
	switch {
	case addr == rData0 || addr == rData1:
		d.data[addr-rData0] = v
	case addr == rDMControl:
		d.dmcontrol(v)
	case addr == rAbsCS:
		d.cmdErr &^= v >> 8 & 7
	case addr == rCommand:
		if d.cmdErr == 0 {
			d.command32(v)
		}
	case addr >= rProgBuf0 && addr < rProgBuf0+uint32(d.cfg.ProgBufSize):
		d.progbuf[addr-rProgBuf0] = v
	case addr == rSBCS && d.cfg.SBA:
		keep := v & (1<<20 | 7<<17 | 1<<16 | 1<<15)
		errs := d.sbcs & (1<<22 | 7<<12)
		errs &^= v & (1<<22 | 7<<12)
		d.sbcs = keep | errs
	case addr == rSBAddr0 && d.cfg.SBA:
		d.sbaddr = v
		if d.sbcs&(1<<20) != 0 {
			d.sbAccess(false)
		}
	case addr == rSBData0 && d.cfg.SBA:
		d.sbdata = v
		d.sbAccess(true)
	}
}

func (d *Device) sbAccess(write bool) {
	if d.sbcs&(7<<12) != 0 {
		return
	}
	if d.sbcs>>17&7 != 2 {
		d.sbcs |= 4 << 12 // unsupported size
		return
	}
	ok := false
	if write {
		ok = d.bus.Write32(d.sbaddr, d.sbdata)
	} else {
		d.sbdata, ok = d.bus.Read32(d.sbaddr)
	}
	if !ok {
		d.sbcs |= 2 << 12 // bad address
		return
	}
	if d.sbcs&(1<<16) != 0 {
		d.sbaddr += 4
	}
}

func (d *Device) dmcontrol(v uint32) {
	if v&1 == 0 {
		d.dmactive = false
		d.cmdErr, d.data, d.sbcs = 0, [2]uint32{}, 0
		return
	}
	d.dmactive = true
	d.haltreq = v&(1<<31) != 0
	if v&(1<<28) != 0 {
		d.haveReset = false
	}
	nd := v&2 != 0
	if d.ndmreset && !nd {
		d.systemReset()
	}
	d.ndmreset = nd
	if d.ndmreset {
		return
	}
	if d.haltreq && !d.hart.Halted {
		d.halt(3)
	}
	if v&(1<<30) != 0 && !d.haltreq && d.hart.Halted {
		d.resumeAck = false
		d.resume()
		d.resumeAck = true
	}
}

// systemReset resets the chip but the DM; the hart halts as it comes out
// of reset when a halt is requested
func (d *Device) systemReset() {
	d.Resets++
	d.bus.Reset()
	d.hart = Hart{PC: d.cfg.AppPC, Lost: true}
	d.haveReset = true
	d.resumeAck = false
	d.pendingPolls = 0
	if d.haltreq {
		d.halt(3)
	}
	if d.cfg.Reenumerate {
		d.gone = true
		d.out = nil
		d.capBits = 0
	}
}

func (d *Device) halt(cause uint32) {
	h := &d.hart
	h.Halted, h.Lost = true, false
	h.Dpc = h.PC
	h.Dcsr = h.Dcsr&^(7<<6) | cause<<6 | 3 // prv M
}

func (d *Device) resume() {
	h := &d.hart
	h.Halted = false
	h.PC = h.Dpc
	for step := 0; step < d.cfg.MaxSteps; step++ {
		switch d.bus.Call(h) {
		case Returned:
			h.PC = h.X[1]
			continue
		case ChipReset:
			d.systemReset()
			return
		}
		insn, ok := d.bus.Fetch(h.PC)
		if !ok {
			break
		}
		r := d.exec(insn)
		if r == execEbreak {
			if h.Dcsr&(1<<15) != 0 {
				d.halt(1)
				d.pendingPolls = d.cfg.HaltPolls
				return
			}
			break
		}
		if r != execOK {
			break
		}
		h.PC += 4
	}
	h.Lost = true
}

type execResult int

const (
	execOK execResult = iota
	execEbreak
	execFault
)

// exec runs lw, sw, addi and ebreak (RV32I)
func (d *Device) exec(insn uint32) execResult {
	h := &d.hart
	if insn == 0x00100073 {
		return execEbreak
	}
	op := insn & 0x7F
	rd := insn >> 7 & 31
	f3 := insn >> 12 & 7
	rs1 := insn >> 15 & 31
	rs2 := insn >> 20 & 31
	immI := uint32(int32(insn) >> 20)
	immS := uint32(int32(insn)>>25<<5) | insn>>7&31
	set := func(r, v uint32) {
		if r != 0 {
			h.X[r] = v
		}
	}
	switch {
	case op == 0x03 && f3 == 2: // lw
		v, ok := d.bus.Read32(h.X[rs1] + immI)
		if !ok {
			return execFault
		}
		set(rd, v)
	case op == 0x23 && f3 == 2: // sw
		if !d.bus.Write32(h.X[rs1]+immS, h.X[rs2]) {
			return execFault
		}
	case op == 0x13 && f3 == 0: // addi
		set(rd, h.X[rs1]+immI)
	default:
		return execFault
	}
	return execOK
}

// command32 runs an abstract command (debug spec 3.6)
func (d *Device) command32(cmd uint32) {
	h := &d.hart
	switch cmd >> 24 {
	case 0: // access register
		if !h.Halted {
			d.cmdErr = 4
			return
		}
		if cmd>>16&1 != 0 || cmd>>17&1 != 0 {
			if cmd>>20&7 != 2 {
				d.cmdErr = 2
				return
			}
		}
		if cmd>>17&1 != 0 {
			regno := cmd & 0xFFFF
			p := d.reg(regno)
			if p == nil {
				d.cmdErr = 3
				return
			}
			if cmd>>16&1 != 0 {
				if regno != 0x1000 {
					*p = d.data[0]
				}
			} else {
				d.data[0] = *p
			}
		}
		if cmd>>18&1 != 0 {
			d.runProgBuf()
		}
	case 2: // access memory
		if !d.cfg.AbstractMem {
			d.cmdErr = 2
			return
		}
		if !h.Halted {
			d.cmdErr = 4
			return
		}
		if cmd>>20&7 != 2 {
			d.cmdErr = 2
			return
		}
		ok := false
		if cmd>>16&1 != 0 {
			ok = d.bus.Write32(d.data[1], d.data[0])
		} else {
			d.data[0], ok = d.bus.Read32(d.data[1])
		}
		if !ok {
			d.cmdErr = 5
			return
		}
		if cmd>>19&1 != 0 {
			d.data[1] += 4
		}
	default:
		d.cmdErr = 2
	}
}

func (d *Device) reg(regno uint32) *uint32 {
	h := &d.hart
	switch {
	case regno >= 0x1000 && regno < 0x1020:
		return &h.X[regno-0x1000]
	case regno == 0x7B0:
		return &h.Dcsr
	case regno == 0x7B1:
		return &h.Dpc
	case regno == 0x300:
		return &h.Mstatus
	}
	return nil
}

// runProgBuf runs the program buffer on the halted hart; the end of the
// buffer is an ebreak when the DM has an implicit one
func (d *Device) runProgBuf() {
	for i := 0; i < d.cfg.ProgBufSize; i++ {
		switch d.exec(d.progbuf[i]) {
		case execEbreak:
			return
		case execFault:
			d.cmdErr = 3
			return
		}
	}
	if !d.cfg.ImpEbreak {
		// ran off the end without an ebreak
		d.cmdErr = 3
	}
}
