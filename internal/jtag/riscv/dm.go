package riscv

import (
	"errors"
	"fmt"
	"time"
)

// DM register addresses (spec 3.12, table 3.8)
const (
	DMData0        = 0x04
	DMData1        = 0x05
	DMControl      = 0x10
	DMStatus       = 0x11
	DMHartInfo     = 0x12
	DMAbstractCS   = 0x16
	DMCommand      = 0x17
	DMAbstractAuto = 0x18
	DMProgBuf0     = 0x20
	DMSBCS         = 0x38
	DMSBAddress0   = 0x39
	DMSBData0      = 0x3C
)

// dmcontrol fields (spec 3.12.2)
const (
	CtlHaltReq      = 1 << 31
	CtlResumeReq    = 1 << 30
	CtlHartReset    = 1 << 29
	CtlAckHaveReset = 1 << 28
	CtlNDMReset     = 1 << 1
	CtlDMActive     = 1 << 0
)

// dmstatus fields (spec 3.12.1)
const (
	StImpEbreak     = 1 << 22
	StAllHaveReset  = 1 << 19
	StAnyHaveReset  = 1 << 18
	StAllResumeAck  = 1 << 17
	StAllNonexist   = 1 << 15
	StAnyNonexist   = 1 << 14
	StAllUnavail    = 1 << 13
	StAllRunning    = 1 << 11
	StAllHalted     = 1 << 9
	StAuthenticated = 1 << 7
	StVersionMask   = 0xF
)

// abstractcs fields (spec 3.12.6)
const (
	acsProgBufSizeS = 24
	acsBusy         = 1 << 12
	acsCmdErrS      = 8
	acsCmdErrMask   = 7
	acsDataCountM   = 0xF
)

// Abstract commands (spec 3.6.1)
const (
	cmdAccessReg   = 0 << 24
	cmdAccessMem   = 2 << 24
	aarSize32      = 2 << 20
	aarPostInc     = 1 << 19
	aarPostExec    = 1 << 18
	aarTransfer    = 1 << 17
	aarWrite       = 1 << 16
	aamSize32      = 2 << 20
	aamPostInc     = 1 << 19
	aamWrite       = 1 << 16
	RegGPR0        = 0x1000
	RegCSR0        = 0x0000
	CSRDcsr        = 0x7B0
	CSRDpc         = 0x7B1
	CSRMstatus     = 0x300
	CSRMisa        = 0x301
	DcsrEbreakM    = 1 << 15
	DcsrEbreakS    = 1 << 13
	DcsrEbreakU    = 1 << 12
	DcsrStep       = 1 << 2
	DcsrCauseShift = 6
	DcsrCauseMask  = 7
	MstatusMIE     = 1 << 3
)

// dcsr.cause values (spec 4.9.1)
const (
	CauseEbreak    = 1
	CauseTrigger   = 2
	CauseHaltReq   = 3
	CauseStep      = 4
	CauseResetHalt = 5
)

// sbcs fields (spec 3.12.18)
const (
	sbVersionS      = 29
	sbBusyError     = 1 << 22
	sbBusy          = 1 << 21
	sbReadOnAddr    = 1 << 20
	sbAccessS       = 17
	sbAutoIncrement = 1 << 16
	sbReadOnData    = 1 << 15
	sbErrorS        = 12
	sbErrorMask     = 7
	sbASizeS        = 5
	sbASizeMask     = 0x7F
	sbAccess32      = 1 << 2
)

// GPR numbers used here
const (
	RegRA = 1
	RegSP = 2
	RegS0 = 8
	RegS1 = 9
	RegA0 = 10
)

// Instructions written into the program buffer (RV32I base encoding, the
// RISC-V unprivileged specification, chapter 2)
const (
	insnLwS1S0   = 0x00042483 // lw   s1, 0(s0)
	insnSwS1S0   = 0x00942023 // sw   s1, 0(s0)
	insnAddiS0_4 = 0x00440413 // addi s0, s0, 4
	InsnEbreak   = 0x00100073 // ebreak
)

// MemPath is how the DM reaches memory
type MemPath int

const (
	MemNone MemPath = iota
	MemSysBus
	MemAbstract
	MemProgBuf
)

func (m MemPath) String() string {
	return [...]string{"none", "system bus", "abstract memory access", "program buffer"}[m]
}

// Errors of the DM
var (
	ErrNoDM       = errors.New("the RISC-V debug module does not respond")
	ErrNotAuth    = errors.New("the debug module needs authentication")
	ErrNoHart     = errors.New("no hart behind the debug module")
	ErrTimeout    = errors.New("the hart did not respond in time")
	ErrNotHalted  = errors.New("the hart is not halted")
	ErrNoMemPath  = errors.New("the debug module has no way to reach memory")
	ErrReset      = errors.New("the chip was reset (a watchdog?)")
	ErrCmdFailed  = errors.New("abstract command failed")
	ErrBusFailed  = errors.New("system bus access failed")
	cmdErrorNames = [...]string{"none", "busy", "not supported", "exception", "halt/resume", "bus", "6", "other"}
)

// CmdError is a failed abstract command
type CmdError struct {
	Code    int
	Command uint32
}

func (e *CmdError) Error() string {
	return fmt.Sprintf("%v: cmderr %d (%s), command 0x%08x", ErrCmdFailed, e.Code, cmdErrorNames[e.Code&7], e.Command)
}

func (e *CmdError) Unwrap() error { return ErrCmdFailed }

// DM is the Debug Module, hart 0 selected
type DM struct {
	dtm *DTM

	Version     int // dmstatus.version: 2 is 0.13, 3 is 1.0
	ProgBufSize int
	DataCount   int
	ImpEbreak   bool
	SBA         bool // 32-bit system bus access
	Mem         MemPath
	// Timeout for halt and resume
	Timeout time.Duration
	// Chunk is how many words a memory batch moves in one round trip
	Chunk int

	progbufFor int // which program the buffer holds: 1 read, 2 write
}

// NewDM activates the DM and finds out what it can do
func NewDM(dtm *DTM) (*DM, error) {
	m := &DM{dtm: dtm, Timeout: time.Second, Chunk: 64}
	if err := dtm.Write(DMControl, CtlDMActive); err != nil {
		return nil, err
	}
	var st uint32
	var err error
	for i := 0; i < 10; i++ {
		var ctl uint32
		if ctl, err = dtm.Read(DMControl); err != nil {
			return nil, err
		}
		if ctl&CtlDMActive != 0 {
			break
		}
		if i == 9 {
			return nil, fmt.Errorf("%w (dmactive stays 0)", ErrNoDM)
		}
	}
	if st, err = dtm.Read(DMStatus); err != nil {
		return nil, err
	}
	m.Version = int(st & StVersionMask)
	if st == 0 || st == 0xFFFFFFFF || (m.Version != 2 && m.Version != 3) {
		return nil, fmt.Errorf("%w (dmstatus 0x%08x)", ErrNoDM, st)
	}
	if st&StAuthenticated == 0 {
		return nil, ErrNotAuth
	}
	if st&StAllNonexist != 0 || st&StAllUnavail != 0 {
		return nil, fmt.Errorf("%w (dmstatus 0x%08x)", ErrNoHart, st)
	}
	m.ImpEbreak = st&StImpEbreak != 0
	acs, err := dtm.Read(DMAbstractCS)
	if err != nil {
		return nil, err
	}
	m.ProgBufSize = int(acs >> acsProgBufSizeS & 0x1F)
	m.DataCount = int(acs & acsDataCountM)
	sbcs, err := dtm.Read(DMSBCS)
	if err != nil {
		return nil, err
	}
	m.SBA = sbcs>>sbVersionS == 1 && sbcs&sbAccess32 != 0 && sbcs>>sbASizeS&sbASizeMask >= 32
	return m, nil
}

// DTM is the transport under the DM
func (m *DM) DTM() *DTM { return m.dtm }

// Status reads dmstatus
func (m *DM) Status() (uint32, error) { return m.dtm.Read(DMStatus) }

func (m *DM) wait(what string, cond func(st uint32) bool) error {
	deadline := time.Now().Add(m.Timeout)
	for {
		st, err := m.Status()
		if err != nil {
			return err
		}
		if cond(st) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s (dmstatus 0x%08x)", ErrTimeout, what, st)
		}
	}
}

// Halted tells whether the hart is halted
func (m *DM) Halted() (bool, error) {
	st, err := m.Status()
	return st&StAllHalted != 0, err
}

// Halt requests a halt and waits for it
func (m *DM) Halt() error {
	if h, err := m.Halted(); err != nil || h {
		return err
	}
	if err := m.dtm.Write(DMControl, CtlDMActive|CtlHaltReq); err != nil {
		return err
	}
	err := m.wait("halt", func(st uint32) bool { return st&StAllHalted != 0 })
	if e := m.dtm.Write(DMControl, CtlDMActive); err == nil {
		err = e
	}
	return err
}

// Resume lets the hart run from dpc
func (m *DM) Resume() error {
	if err := m.dtm.Write(DMControl, CtlDMActive|CtlResumeReq); err != nil {
		return err
	}
	err := m.wait("resume", func(st uint32) bool { return st&StAllResumeAck != 0 })
	if e := m.dtm.Write(DMControl, CtlDMActive); err == nil {
		err = e
	}
	return err
}

// WaitHalted waits up to t for the hart to halt by itself (an ebreak);
// a reset of the chip meanwhile is ErrReset
func (m *DM) WaitHalted(t time.Duration, poll func() error) error {
	deadline := time.Now().Add(t)
	for {
		st, err := m.Status()
		if err != nil {
			return err
		}
		if st&StAllHalted != 0 {
			return nil
		}
		if st&StAnyHaveReset != 0 {
			return ErrReset
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w (dmstatus 0x%08x)", ErrTimeout, st)
		}
		if poll != nil {
			if err := poll(); err != nil {
				return err
			}
		}
	}
}

// ResetSystem pulses ndmreset (spec 3.12.2: resets everything but the DM
// and the DTM); with halt the hart is asked to halt as it comes out of
// reset. havereset is acknowledged.
func (m *DM) ResetSystem(halt bool) error {
	req := uint32(CtlDMActive | CtlNDMReset)
	if halt {
		req |= CtlHaltReq
	}
	if err := m.dtm.Write(DMControl, req); err != nil {
		return err
	}
	if err := m.dtm.Write(DMControl, req&^CtlNDMReset); err != nil {
		return err
	}
	return nil
}

// AckReset clears havereset and haltreq after a reset
func (m *DM) AckReset() error {
	m.progbufFor = 0
	return m.dtm.Write(DMControl, CtlDMActive|CtlAckHaveReset)
}

// cmdOps are the ops that run an abstract command and check it
func cmdOps(cmd uint32) []Op { return []Op{W(DMCommand, cmd), R(DMAbstractCS)} }

// checkCS looks at abstractcs read after a command: still busy, it is
// read again until done; a cmderr is cleared and returned
func (m *DM) checkCS(acs, cmd uint32) error {
	deadline := time.Now().Add(m.Timeout)
	for acs&acsBusy != 0 {
		var err error
		if acs, err = m.dtm.Read(DMAbstractCS); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: abstract command 0x%08x stays busy", ErrTimeout, cmd)
		}
	}
	if code := int(acs >> acsCmdErrS & acsCmdErrMask); code != 0 {
		m.dtm.Write(DMAbstractCS, acsCmdErrMask<<acsCmdErrS)
		if code == 1 {
			// busy: a command came while one ran; slow down and repeat
			m.dtm.busy()
			return ErrBusy
		}
		return &CmdError{Code: code, Command: cmd}
	}
	return nil
}

// ReadReg reads a register by its abstract number: RegGPR0+n, a CSR
func (m *DM) ReadReg(regno uint32) (uint32, error) {
	var v uint32
	cmd := cmdAccessReg | aarSize32 | aarTransfer | regno
	err := m.dtm.Retry(func() error {
		r, err := m.dtm.Exec(append(cmdOps(cmd), R(DMData0)))
		if err != nil {
			return err
		}
		if err := m.checkCS(r[1], cmd); err != nil {
			return err
		}
		if r[1]&acsBusy != 0 {
			if v, err = m.dtm.Read(DMData0); err != nil {
				return err
			}
			return nil
		}
		v = r[2]
		return nil
	})
	return v, err
}

// WriteReg writes a register by its abstract number
func (m *DM) WriteReg(regno, v uint32) error {
	cmd := cmdAccessReg | aarSize32 | aarTransfer | aarWrite | regno
	return m.dtm.Retry(func() error {
		r, err := m.dtm.Exec(append([]Op{W(DMData0, v)}, cmdOps(cmd)...))
		if err != nil {
			return err
		}
		return m.checkCS(r[2], cmd)
	})
}

// GPR and CSR are the abstract numbers of x<n> and of a CSR
func GPR(n int) uint32    { return RegGPR0 + uint32(n) }
func CSR(n uint32) uint32 { return RegCSR0 + n }

// ProbeMemory picks the way to memory: the system bus when the DM has
// 32-bit access, else abstract memory access when a read of addr works,
// else the program buffer when it holds two instructions and an ebreak.
// The hart must be halted for the last two.
func (m *DM) ProbeMemory(addr uint32) MemPath {
	m.Mem = MemNone
	if m.SBA {
		m.Mem = MemSysBus
		if _, err := m.ReadMem(addr, 1); err == nil {
			return m.Mem
		}
	}
	m.Mem = MemAbstract
	if _, err := m.ReadMem(addr, 1); err == nil {
		return m.Mem
	}
	need := 3
	if m.ImpEbreak {
		need = 2
	}
	if m.ProgBufSize >= need {
		m.Mem = MemProgBuf
		if _, err := m.ReadMem(addr, 1); err == nil {
			return m.Mem
		}
	}
	m.Mem = MemNone
	return m.Mem
}

// ReadMem reads n 32-bit words from an aligned address
func (m *DM) ReadMem(addr uint32, n int) ([]uint32, error) {
	out := make([]uint32, 0, n)
	for len(out) < n {
		k := min(n-len(out), m.Chunk)
		a := addr + uint32(4*len(out))
		var words []uint32
		err := m.dtm.Retry(func() error {
			var err error
			switch m.Mem {
			case MemSysBus:
				words, err = m.sbRead(a, k)
			case MemAbstract:
				words, err = m.amRead(a, k)
			case MemProgBuf:
				words, err = m.pbRead(a, k)
			default:
				err = ErrNoMemPath
			}
			return err
		})
		if err != nil {
			return out, err
		}
		out = append(out, words...)
	}
	return out, nil
}

// WriteMem writes 32-bit words to an aligned address
func (m *DM) WriteMem(addr uint32, words []uint32) error {
	for done := 0; done < len(words); {
		k := min(len(words)-done, m.Chunk)
		a := addr + uint32(4*done)
		part := words[done : done+k]
		err := m.dtm.Retry(func() error {
			switch m.Mem {
			case MemSysBus:
				return m.sbWrite(a, part)
			case MemAbstract:
				return m.amWrite(a, part)
			case MemProgBuf:
				return m.pbWrite(a, part)
			}
			return ErrNoMemPath
		})
		if err != nil {
			return err
		}
		done += k
	}
	return nil
}

// sbCheck reads sbcs after a batch: an error is cleared and returned
func (m *DM) sbCheck(sbcs uint32) error {
	if sbcs&sbBusyError != 0 {
		m.dtm.Write(DMSBCS, sbBusyError|sbErrorMask<<sbErrorS)
		m.dtm.busy()
		return ErrBusy
	}
	if e := sbcs >> sbErrorS & sbErrorMask; e != 0 {
		m.dtm.Write(DMSBCS, sbErrorMask<<sbErrorS)
		return fmt.Errorf("%w: sberror %d", ErrBusFailed, e)
	}
	return nil
}

// sbRead: sbreadonaddr starts the first read, each read of sbdata0
// starts the next one (spec 3.10); a single word doesn't read ahead, so
// that registers with read side effects are read once
func (m *DM) sbRead(addr uint32, n int) ([]uint32, error) {
	cs := uint32(2<<sbAccessS | sbReadOnAddr)
	if n > 1 {
		cs |= sbAutoIncrement | sbReadOnData
	}
	ops := []Op{W(DMSBCS, cs), W(DMSBAddress0, addr)}
	for i := 0; i < n; i++ {
		if i == n-1 && n > 1 {
			// the last read must not start another
			ops = append(ops, W(DMSBCS, 2<<sbAccessS))
		}
		ops = append(ops, R(DMSBData0))
	}
	ops = append(ops, R(DMSBCS))
	r, err := m.dtm.Exec(ops)
	if err != nil {
		return nil, err
	}
	if err := m.sbCheck(r[len(r)-1]); err != nil {
		return nil, err
	}
	out := make([]uint32, 0, n)
	for i, o := range ops[:len(ops)-1] {
		if !o.Write && o.Addr == DMSBData0 {
			out = append(out, r[i])
		}
	}
	return out, nil
}

func (m *DM) sbWrite(addr uint32, words []uint32) error {
	ops := []Op{W(DMSBCS, 2<<sbAccessS|sbAutoIncrement), W(DMSBAddress0, addr)}
	for _, w := range words {
		ops = append(ops, W(DMSBData0, w))
	}
	ops = append(ops, R(DMSBCS))
	r, err := m.dtm.Exec(ops)
	if err != nil {
		return err
	}
	return m.sbCheck(r[len(r)-1])
}

// amRead uses abstract memory access with post-increment of data1
func (m *DM) amRead(addr uint32, n int) ([]uint32, error) {
	cmd := uint32(cmdAccessMem | aamSize32 | aamPostInc)
	ops := []Op{W(DMData1, addr)}
	for i := 0; i < n; i++ {
		ops = append(ops, W(DMCommand, cmd), R(DMData0))
	}
	ops = append(ops, R(DMAbstractCS))
	r, err := m.dtm.Exec(ops)
	if err != nil {
		return nil, err
	}
	if err := m.checkCS(r[len(r)-1], cmd); err != nil {
		return nil, err
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = r[2+2*i]
	}
	return out, nil
}

func (m *DM) amWrite(addr uint32, words []uint32) error {
	cmd := uint32(cmdAccessMem | aamSize32 | aamPostInc | aamWrite)
	ops := []Op{W(DMData1, addr)}
	for _, w := range words {
		ops = append(ops, W(DMData0, w), W(DMCommand, cmd))
	}
	ops = append(ops, R(DMAbstractCS))
	r, err := m.dtm.Exec(ops)
	if err != nil {
		return err
	}
	return m.checkCS(r[len(r)-1], cmd)
}

// program loads the program buffer: the access instruction, addi s0,
// s0, 4 and an ebreak unless the DM has an implicit one. s0 and s1 are
// clobbered.
func (m *DM) program(kind int, insn uint32) []Op {
	if m.progbufFor == kind {
		return nil
	}
	m.progbufFor = kind
	ops := []Op{W(DMProgBuf0, insn), W(DMProgBuf0+1, insnAddiS0_4)}
	if !m.ImpEbreak || m.ProgBufSize > 2 {
		ops = append(ops, W(DMProgBuf0+2, InsnEbreak))
	}
	return ops
}

// pbRead: s0 = addr with postexec runs lw s1,(s0); s0 += 4. Then each
// "read s1 into data0, postexec" returns a word and loads the next.
func (m *DM) pbRead(addr uint32, n int) ([]uint32, error) {
	wS0 := uint32(cmdAccessReg | aarSize32 | aarTransfer | aarWrite | aarPostExec | RegGPR0 + RegS0)
	rS1 := uint32(cmdAccessReg | aarSize32 | aarTransfer | aarPostExec | RegGPR0 + RegS1)
	ops := m.program(1, insnLwS1S0)
	ops = append(ops, W(DMData0, addr), W(DMCommand, wS0))
	first := len(ops)
	for i := 0; i < n; i++ {
		ops = append(ops, W(DMCommand, rS1), R(DMData0))
	}
	ops = append(ops, R(DMAbstractCS))
	r, err := m.dtm.Exec(ops)
	if err != nil {
		m.progbufFor = 0
		return nil, err
	}
	if err := m.checkCS(r[len(r)-1], rS1); err != nil {
		m.progbufFor = 0
		return nil, err
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = r[first+2*i+1]
	}
	return out, nil
}

// pbWrite: s0 = addr, then each "write data0 to s1, postexec" stores a
// word with sw s1,(s0); s0 += 4
func (m *DM) pbWrite(addr uint32, words []uint32) error {
	wS0 := uint32(cmdAccessReg | aarSize32 | aarTransfer | aarWrite | RegGPR0 + RegS0)
	wS1 := uint32(cmdAccessReg | aarSize32 | aarTransfer | aarWrite | aarPostExec | RegGPR0 + RegS1)
	ops := m.program(2, insnSwS1S0)
	ops = append(ops, W(DMData0, addr), W(DMCommand, wS0))
	for _, w := range words {
		ops = append(ops, W(DMData0, w), W(DMCommand, wS1))
	}
	ops = append(ops, R(DMAbstractCS))
	r, err := m.dtm.Exec(ops)
	if err != nil {
		m.progbufFor = 0
		return err
	}
	if err := m.checkCS(r[len(r)-1], wS1); err != nil {
		m.progbufFor = 0
		return err
	}
	return nil
}
