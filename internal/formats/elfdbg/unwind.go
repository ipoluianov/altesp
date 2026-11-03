package elfdbg

import (
	"errors"
	"fmt"
)

// The stack unwinders: by the call frame information (RISC-V; it knows
// nothing of the architecture but the numbers of the registers) and by
// the register windows of Xtensa.

// Memory is what an unwinder reads the stack from: the stack dump of a
// panic or the memory of a coredump
type Memory interface {
	// Read32 reads a little-endian word; false when the address wasn't
	// saved
	Read32(addr uint32) (uint32, bool)
}

// MemMap is memory made of words, as the "Stack memory:" dump of a panic
type MemMap map[uint32]uint32

// Read32 implements Memory
func (m MemMap) Read32(addr uint32) (uint32, bool) {
	if addr&3 == 0 {
		v, ok := m[addr]
		return v, ok
	}
	lo, ok1 := m[addr&^3]
	hi, ok2 := m[addr&^3+4]
	if !ok1 || !ok2 {
		return 0, false
	}
	sh := (addr & 3) * 8
	return lo>>sh | hi<<(32-sh), true
}

// StackFrame is a frame of an unwound stack: the address of the code and
// the stack pointer, and what the address is
type StackFrame struct {
	PC, SP uint32
	Frames []Frame // the lookup of PC; nil without an .elf
}

// maxDepth limits the walk of a broken stack
const maxDepth = 64

// The RISC-V registers: DWARF numbers 0-31 are x0-x31 (psABI); the names
// are as ESP-IDF prints them, with MEPC for the pc
var riscvRegNames = [32]string{
	"MEPC", "RA", "SP", "GP", "TP", "T0", "T1", "T2",
	"S0/FP", "S1", "A0", "A1", "A2", "A3", "A4", "A5",
	"A6", "A7", "S2", "S3", "S4", "S5", "S6", "S7",
	"S8", "S9", "S10", "S11", "T3", "T4", "T5", "T6",
}

const (
	rvRA = 1
	rvSP = 2
)

// regState is the registers of a frame; x0 is always zero, so its slot
// holds the pc
type regState struct {
	r     [32]uint32
	valid [32]bool
}

func (s *regState) get(reg uint64) (uint32, bool) {
	if reg == 0 {
		return 0, true
	}
	if reg >= 32 || !s.valid[reg] {
		return 0, false
	}
	return s.r[reg], true
}

// riscvRegs takes the registers by the names ESP-IDF prints
func riscvRegs(regs map[string]uint32) (regState, uint32, bool) {
	var s regState
	pc, ok := regs["MEPC"]
	if !ok {
		pc, ok = regs["PC"]
	}
	for i := 1; i < 32; i++ {
		name := riscvRegNames[i]
		v, have := regs[name]
		if !have && name == "S0/FP" {
			v, have = regs["S0"]
			if !have {
				v, have = regs["FP"]
			}
		}
		s.r[i], s.valid[i] = v, have
	}
	return s, pc, ok && s.valid[rvSP]
}

// UnwindRISCV walks the stack of a RISC-V chip from the registers (named
// as ESP-IDF prints them: MEPC, RA, SP, ...) by the call frame information
// of the .elf
func (e *ELF) UnwindRISCV(regs map[string]uint32, mem Memory) []StackFrame {
	s, pc, ok := riscvRegs(regs)
	if !ok {
		return nil
	}
	return e.unwindCFI(s, pc, mem)
}

func (e *ELF) unwindCFI(s regState, pc uint32, mem Memory) []StackFrame {
	e.cfiOnce.Do(e.loadCFI)
	var out []StackFrame
	for depth := 0; depth < maxDepth; depth++ {
		sp := s.r[rvSP]
		lookupPC := pc
		if depth > 0 {
			// A return address: the call is before it, and the call of a
			// noreturn function may be the last instruction of its
			// function
			lookupPC = pc - 1
		}
		fr := e.Lookup(lookupPC)
		for i := range fr {
			fr[i].PC = pc
		}
		out = append(out, StackFrame{PC: pc, SP: sp, Frames: fr})
		next, npc, err := e.stepCFI(&s, pc, lookupPC, mem)
		if err != nil {
			if depth == 0 && errors.Is(err, errNoCFI) && s.valid[rvRA] {
				// Code without CFI (the ROM): taken as a leaf function
				// that keeps the return address in RA
				next = s
				npc = s.r[rvRA]
			} else {
				break
			}
		}
		nsp := next.r[rvSP]
		if npc == 0 || !e.IsCode(npc) || !next.valid[rvSP] || nsp < sp || (npc == pc && nsp == sp) {
			break
		}
		s, pc = next, npc
	}
	return out
}

// stepCFI gives the registers of the caller
func (e *ELF) stepCFI(s *regState, pc, lookupPC uint32, mem Memory) (regState, uint32, error) {
	f := e.cfi.find(uint64(lookupPC))
	if f == nil {
		return regState{}, 0, errNoCFI
	}
	row, err := f.rowAt(uint64(lookupPC))
	if err != nil {
		return regState{}, 0, err
	}
	var cfa uint32
	if row.cfa.expr != nil {
		v, err := evalExpr(row.cfa.expr, s, mem, nil)
		if err != nil {
			return regState{}, 0, err
		}
		cfa = v
	} else {
		v, ok := s.get(row.cfa.reg)
		if !ok {
			return regState{}, 0, fmt.Errorf("CFA register %d unknown", row.cfa.reg)
		}
		cfa = v + uint32(row.cfa.off)
	}
	next := *s
	for reg, rule := range row.regs {
		if reg >= 32 {
			continue
		}
		var v uint32
		ok := true
		switch rule.kind {
		case ruleSame:
			continue
		case ruleUndefined:
			ok = false
		case ruleOffset:
			v, ok = mem.Read32(cfa + uint32(rule.off))
		case ruleValOffset:
			v = cfa + uint32(rule.off)
		case ruleRegister:
			v, ok = s.get(rule.reg)
		case ruleExpr:
			var addr uint32
			addr, err = evalExpr(rule.expr, s, mem, &cfa)
			if err == nil {
				v, ok = mem.Read32(addr)
			} else {
				ok = false
			}
		case ruleValExpr:
			v, err = evalExpr(rule.expr, s, mem, &cfa)
			ok = err == nil
		}
		next.r[reg], next.valid[reg] = v, ok
	}
	next.r[rvSP], next.valid[rvSP] = cfa, true
	ra := f.cie.raReg
	if ra >= 32 || !next.valid[ra] {
		return next, 0, errors.New("return address unknown")
	}
	return next, next.r[ra], nil
}

// evalExpr runs a DWARF expression (DWARF 5 section 2.5) on the
// registers of a frame. With cfa, the CFA is pushed first, as for
// DW_CFA_expression and DW_CFA_val_expression.
func evalExpr(expr []byte, s *regState, mem Memory, cfa *uint32) (uint32, error) {
	var st []uint32
	if cfa != nil {
		st = append(st, *cfa)
	}
	pop := func() (uint32, bool) {
		if len(st) == 0 {
			return 0, false
		}
		v := st[len(st)-1]
		st = st[:len(st)-1]
		return v, true
	}
	r := &cfiBuf{b: expr}
	errStack := errors.New("DWARF expression: stack underflow")
	for steps := 0; r.off < len(r.b); steps++ {
		if steps > 10000 || r.err != nil {
			return 0, errors.New("DWARF expression: broken")
		}
		op := r.u8()
		switch {
		case op >= 0x30 && op <= 0x4f: // DW_OP_lit0..31
			st = append(st, uint32(op-0x30))
			continue
		case op >= 0x70 && op <= 0x8f: // DW_OP_breg0..31
			v, ok := s.get(uint64(op - 0x70))
			if !ok {
				return 0, fmt.Errorf("DWARF expression: register %d unknown", op-0x70)
			}
			st = append(st, v+uint32(r.sleb()))
			continue
		case op >= 0x50 && op <= 0x6f: // DW_OP_reg0..31: a location, not a value
			return 0, errors.New("DWARF expression: register location")
		}
		switch op {
		case 0x03: // addr
			st = append(st, r.u32())
		case 0x06: // deref
			a, ok := pop()
			if !ok {
				return 0, errStack
			}
			v, ok := mem.Read32(a)
			if !ok {
				return 0, fmt.Errorf("DWARF expression: no memory at 0x%08x", a)
			}
			st = append(st, v)
		case 0x94: // deref_size
			n := r.u8()
			a, ok := pop()
			if !ok {
				return 0, errStack
			}
			v, ok := mem.Read32(a)
			if !ok {
				return 0, fmt.Errorf("DWARF expression: no memory at 0x%08x", a)
			}
			if n < 4 {
				v &= 1<<(8*uint(n)) - 1
			}
			st = append(st, v)
		case 0x08: // const1u
			st = append(st, uint32(r.u8()))
		case 0x09: // const1s
			st = append(st, uint32(int8(r.u8())))
		case 0x0a: // const2u
			st = append(st, uint32(r.u16()))
		case 0x0b: // const2s
			st = append(st, uint32(int16(r.u16())))
		case 0x0c, 0x0d: // const4u, const4s
			st = append(st, r.u32())
		case 0x0e, 0x0f: // const8u, const8s
			st = append(st, uint32(r.u64()))
		case 0x10: // constu
			st = append(st, uint32(r.uleb()))
		case 0x11: // consts
			st = append(st, uint32(r.sleb()))
		case 0x12: // dup
			if len(st) == 0 {
				return 0, errStack
			}
			st = append(st, st[len(st)-1])
		case 0x13: // drop
			if _, ok := pop(); !ok {
				return 0, errStack
			}
		case 0x14: // over
			if len(st) < 2 {
				return 0, errStack
			}
			st = append(st, st[len(st)-2])
		case 0x15: // pick
			i := int(r.u8())
			if i >= len(st) {
				return 0, errStack
			}
			st = append(st, st[len(st)-1-i])
		case 0x16: // swap
			if len(st) < 2 {
				return 0, errStack
			}
			n := len(st)
			st[n-1], st[n-2] = st[n-2], st[n-1]
		case 0x17: // rot
			if len(st) < 3 {
				return 0, errStack
			}
			n := len(st)
			st[n-1], st[n-2], st[n-3] = st[n-2], st[n-3], st[n-1]
		case 0x19: // abs
			v, ok := pop()
			if !ok {
				return 0, errStack
			}
			if int32(v) < 0 {
				v = -v
			}
			st = append(st, v)
		case 0x1f: // neg
			v, ok := pop()
			if !ok {
				return 0, errStack
			}
			st = append(st, -v)
		case 0x20: // not
			v, ok := pop()
			if !ok {
				return 0, errStack
			}
			st = append(st, ^v)
		case 0x23: // plus_uconst
			v, ok := pop()
			if !ok {
				return 0, errStack
			}
			st = append(st, v+uint32(r.uleb()))
		case 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x21, 0x22, 0x24, 0x25, 0x26, 0x27,
			0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e:
			b, ok1 := pop()
			a, ok2 := pop()
			if !ok1 || !ok2 {
				return 0, errStack
			}
			v, err := binaryOp(op, a, b)
			if err != nil {
				return 0, err
			}
			st = append(st, v)
		case 0x2f: // skip
			n := int16(r.u16())
			r.off += int(n)
			if r.off < 0 || r.off > len(r.b) {
				return 0, errors.New("DWARF expression: bad skip")
			}
		case 0x28: // bra
			n := int16(r.u16())
			v, ok := pop()
			if !ok {
				return 0, errStack
			}
			if v != 0 {
				r.off += int(n)
				if r.off < 0 || r.off > len(r.b) {
					return 0, errors.New("DWARF expression: bad branch")
				}
			}
		case 0x92: // bregx
			reg := r.uleb()
			v, ok := s.get(reg)
			if !ok {
				return 0, fmt.Errorf("DWARF expression: register %d unknown", reg)
			}
			st = append(st, v+uint32(r.sleb()))
		case 0x96: // nop
		case 0x9c: // call_frame_cfa
			if cfa == nil {
				return 0, errors.New("DWARF expression: no CFA")
			}
			st = append(st, *cfa)
		default:
			return 0, fmt.Errorf("DWARF expression: operation 0x%02x", op)
		}
	}
	v, ok := pop()
	if !ok {
		return 0, errStack
	}
	return v, nil
}

func binaryOp(op byte, a, b uint32) (uint32, error) {
	bool32 := func(c bool) uint32 {
		if c {
			return 1
		}
		return 0
	}
	switch op {
	case 0x1a:
		return a & b, nil
	case 0x1b:
		if b == 0 {
			return 0, errors.New("DWARF expression: division by zero")
		}
		return uint32(int32(a) / int32(b)), nil
	case 0x1c:
		return a - b, nil
	case 0x1d:
		if b == 0 {
			return 0, errors.New("DWARF expression: division by zero")
		}
		return a % b, nil
	case 0x1e:
		return a * b, nil
	case 0x21:
		return a | b, nil
	case 0x22:
		return a + b, nil
	case 0x24:
		return a << (b & 63), nil
	case 0x25:
		return a >> (b & 63), nil
	case 0x26:
		return uint32(int32(a) >> (b & 63)), nil
	case 0x27:
		return a ^ b, nil
	case 0x29:
		return bool32(a == b), nil
	case 0x2a:
		return bool32(int32(a) >= int32(b)), nil
	case 0x2b:
		return bool32(int32(a) > int32(b)), nil
	case 0x2c:
		return bool32(int32(a) <= int32(b)), nil
	case 0x2d:
		return bool32(int32(a) < int32(b)), nil
	case 0x2e:
		return bool32(a != b), nil
	}
	return 0, fmt.Errorf("DWARF expression: operation 0x%02x", op)
}

// xtensaCallPC turns a return address of the windowed ABI into the address
// of its call: the top two bits are the window increment of the call, not
// a part of the address (Xtensa ISA, windowed register option), and the
// CALL4/8/12 and CALLX instructions are 3 bytes long. All the code of the
// ESP chips is in 0x40000000-0x7FFFFFFF. As esp_cpu_process_stack_pc of
// ESP-IDF.
func xtensaCallPC(ra uint32) uint32 {
	if ra&0x80000000 != 0 {
		ra = ra&0x3fffffff | 0x40000000
	}
	return ra - 3
}

// stackSane is a plausible stack pointer of an Xtensa chip: aligned to
// 16 bytes (windowed ABI) and in the data address space. A loose check:
// the walk stops at memory that wasn't saved too.
func stackSane(sp uint32) bool {
	return sp&0xf == 0 && sp >= 0x3c000000 && sp < 0x50000000
}

// UnwindXtensa walks the stack of an Xtensa chip by the windowed ABI from
// the pc, a0 and a1 (sp) of the top frame. The register windows must be
// spilled to the stack (ESP-IDF spills them before a panic dump, at a
// context switch and before a coredump): the 16 bytes under the sp of a
// frame are the base save area that holds a0 and a1 of its caller, that
// is the return address into the caller's caller and the caller's sp.
func (e *ELF) UnwindXtensa(pc, a0, sp uint32, mem Memory) []StackFrame {
	out := []StackFrame{{PC: pc, SP: sp}}
	next := a0
	for len(out) < maxDepth && next != 0 {
		base := sp
		callPC := xtensaCallPC(next)
		nextRA, ok1 := mem.Read32(base - 16)
		callerSP, ok2 := mem.Read32(base - 12)
		if !ok1 || !ok2 || !stackSane(callerSP) || callerSP <= sp {
			// The caller is known, its frame is not
			out = append(out, StackFrame{PC: callPC})
			break
		}
		out = append(out, StackFrame{PC: callPC, SP: callerSP})
		next, sp = nextRA, callerSP
	}
	if e != nil {
		for i := range out {
			out[i].Frames = e.Lookup(out[i].PC)
		}
	}
	return out
}
