// Package riscv is the host side of the RISC-V External Debug Support
// (specification version 0.13.2): the JTAG Debug Transport Module (DTM)
// and the Debug Module (DM) behind it. It halts and resumes a hart, reads
// and writes its registers by abstract commands, and reads and writes
// memory by system bus access, abstract memory access or the program
// buffer, whichever the DM has.
//
// Register numbers and fields are from the specification: chapter 6.1
// (JTAG DTM: IR values, dtmcs, dmi), chapter 3.12 (DM registers),
// 3.6 (abstract commands), 4.8 (dcsr, dpc).
package riscv

import (
	"errors"
	"fmt"

	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
)

// JTAG instructions of the DTM (spec 6.1.2, table 6.1). The IR is 5 bits
// long on the Espressif RISC-V chips, the length the spec suggests.
const (
	IRLen    = 5
	IRIDCODE = 0x01
	IRDTMCS  = 0x10
	IRDMI    = 0x11
	IRBYPASS = 0x1F
)

// dtmcs fields (spec 6.1.4)
const (
	dtmcsVersionMask = 0xF
	dtmcsAbitsShift  = 4
	dtmcsAbitsMask   = 0x3F
	dtmcsDmistatS    = 10
	dtmcsIdleShift   = 12
	dtmcsIdleMask    = 0x7
	dtmcsDmireset    = 1 << 16
	dtmcsDmihardrst  = 1 << 17
)

// dmi op and status (spec 6.1.5)
const (
	opNop   = 0
	opRead  = 1
	opWrite = 2

	statusOK     = 0
	statusFailed = 2
	statusBusy   = 3
)

// Errors of the DTM
var (
	ErrNoDTM   = errors.New("no RISC-V debug transport behind the TAP")
	ErrBusy    = errors.New("the debug module stayed busy")
	ErrDMIFail = errors.New("a debug module access failed")
)

// Scanner is what the DTM needs of a TAP: queued IR and DR scans that
// Run sends. espjtag.TAP is one; an FT2232 adapter (stage 7) is another.
type Scanner interface {
	IR(n int, v uint64)
	DR(n int, v uint64, capture int, idle int) *espjtag.Result
	Run() error
}

// DTM is the JTAG Debug Transport Module
type DTM struct {
	tap     Scanner
	Version int
	Abits   int
	// Idle is the number of Run-Test/Idle cycles after each dmi scan; it
	// starts at the hint of dtmcs and grows when the DM reports busy
	Idle    int
	MaxIdle int
	ir      int
	// Retries counts the busy recoveries, for the tests
	Retries int
}

// NewDTM reads dtmcs. A TAP without a DTM (JTAG disabled, a TAP that
// isn't RISC-V) returns ErrNoDTM.
func NewDTM(tap Scanner) (*DTM, error) {
	d := &DTM{tap: tap, MaxIdle: 64, ir: -1}
	v, err := d.dtmcs(0)
	if err != nil {
		return nil, err
	}
	d.Version = int(v & dtmcsVersionMask)
	d.Abits = int(v >> dtmcsAbitsShift & dtmcsAbitsMask)
	d.Idle = int(v >> dtmcsIdleShift & dtmcsIdleMask)
	if v == 0 || v == 0xFFFFFFFF || d.Version != 1 || d.Abits < 7 || d.Abits > 30 {
		return nil, fmt.Errorf("%w (dtmcs 0x%08x)", ErrNoDTM, v)
	}
	return d, nil
}

func (d *DTM) selectIR(ir int) {
	if d.ir != ir {
		d.tap.IR(IRLen, uint64(ir))
		d.ir = ir
	}
}

// dtmcs writes w to dtmcs and returns what it read
func (d *DTM) dtmcs(w uint32) (uint32, error) {
	d.selectIR(IRDTMCS)
	r := d.tap.DR(32, uint64(w), 32, 0)
	if err := d.tap.Run(); err != nil {
		d.ir = -1
		return 0, err
	}
	return uint32(r.Value()), nil
}

// Invalidate forgets the IR (after the TAP was reset)
func (d *DTM) Invalidate() { d.ir = -1 }

// Op is one DMI access
type Op struct {
	Write bool
	Addr  uint32
	Data  uint32
}

// R and W make ops
func R(addr uint32) Op       { return Op{Addr: addr} }
func W(addr, data uint32) Op { return Op{Write: true, Addr: addr, Data: data} }
func (d *DTM) scanBits() int { return d.Abits + 34 }
func (d *DTM) encode(o Op) uint64 {
	op := uint64(opRead)
	if o.Write {
		op = opWrite
	}
	return uint64(o.Addr)<<34 | uint64(o.Data)<<2 | op
}

// Exec runs the ops in one round trip and returns the data of each (the
// value read for reads). The result of a dmi scan comes with the next
// scan, so a nop scan ends the batch. When the DM was busy the batch is
// abandoned: the DTM is reset (dmireset), the idle cycles grow and
// ErrBusy is returned; the caller repeats its whole operation, which
// must therefore be idempotent as a whole.
func (d *DTM) Exec(ops []Op) ([]uint32, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	d.selectIR(IRDMI)
	// each scan captures the status of the op before it (2 bits), and
	// its data (32 more) when that was a read
	res := make([]*espjtag.Result, len(ops)+1)
	capture := 2
	for i, o := range ops {
		res[i] = d.tap.DR(d.scanBits(), d.encode(o), capture, d.Idle)
		capture = 2
		if !o.Write {
			capture = 34
		}
	}
	res[len(ops)] = d.tap.DR(d.scanBits(), opNop, capture, d.Idle)
	if err := d.tap.Run(); err != nil {
		d.ir = -1
		return nil, err
	}
	out := make([]uint32, len(ops))
	for i := range ops {
		v := res[i+1].Value()
		switch v & 3 {
		case statusOK:
		case statusBusy:
			return nil, d.busy()
		default:
			d.dtmcs(dtmcsDmireset)
			return nil, fmt.Errorf("%w: dmi op %d at 0x%02x", ErrDMIFail, i, ops[i].Addr)
		}
		out[i] = uint32(v >> 2)
	}
	return out, nil
}

// busy clears the sticky busy of the DTM and slows down
func (d *DTM) busy() error {
	d.Retries++
	if _, err := d.dtmcs(dtmcsDmireset); err != nil {
		return err
	}
	if d.Idle < d.MaxIdle {
		d.Idle = d.Idle*2 + 1
		if d.Idle > d.MaxIdle {
			d.Idle = d.MaxIdle
		}
	}
	return ErrBusy
}

// Retry runs f again while it fails with ErrBusy, a few times
func (d *DTM) Retry(f func() error) error {
	var err error
	for i := 0; i < 8; i++ {
		if err = f(); !errors.Is(err, ErrBusy) {
			return err
		}
	}
	return err
}

// Read reads one DM register
func (d *DTM) Read(addr uint32) (uint32, error) {
	var v uint32
	err := d.Retry(func() error {
		r, err := d.Exec([]Op{R(addr)})
		if err == nil {
			v = r[0]
		}
		return err
	})
	return v, err
}

// Write writes one DM register
func (d *DTM) Write(addr, v uint32) error {
	return d.Retry(func() error {
		_, err := d.Exec([]Op{W(addr, v)})
		return err
	})
}
