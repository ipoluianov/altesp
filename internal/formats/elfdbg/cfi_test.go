package elfdbg

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
)

// ehFrame builds an .eh_frame by hand (LSB "Exception Frames"): a CIE with
// the "zR" augmentation and pc-relative sdata4 addresses, and one FDE
func ehFrame(secAddr, fnAddr, fnLen uint32, insns []byte) []byte {
	le := binary.LittleEndian
	var b []byte
	cie := []byte{
		0, 0, 0, 0, // CIE id
		1,           // version
		'z', 'R', 0, // augmentation
		1,          // code alignment
		0x7c,       // data alignment -4
		1,          // return address column: ra
		1,          // augmentation data length
		0x1b,       // pcrel | sdata4
		0x0c, 2, 0, // def_cfa sp+0
	}
	for len(cie)%4 != 0 {
		cie = append(cie, 0)
	}
	b = le.AppendUint32(b, uint32(len(cie)))
	b = append(b, cie...)
	fdeStart := len(b)
	fde := le.AppendUint32(nil, uint32(fdeStart+4)) // back to the CIE
	pcField := secAddr + uint32(fdeStart+8)
	fde = le.AppendUint32(fde, fnAddr-pcField)
	fde = le.AppendUint32(fde, fnLen)
	fde = append(fde, 0) // augmentation data length
	fde = append(fde, insns...)
	for len(fde)%4 != 0 {
		fde = append(fde, 0)
	}
	b = le.AppendUint32(b, uint32(len(fde)))
	b = append(b, fde...)
	return le.AppendUint32(b, 0)
}

func TestCFIRules(t *testing.T) {
	const sec, fn = 0x3c010000, 0x42001000
	insns := []byte{
		0x42,     // advance_loc 2
		0x0e, 32, // def_cfa_offset 32
		0x81, 1, // offset ra at cfa-4
		0x88, 2, // offset s0 at cfa-8
		0x44,    // advance_loc 4 -> fn+6
		0x0d, 8, // def_cfa_register s0
		0x0a,       // remember_state
		0x42,       // advance_loc 2 -> fn+8
		0x0c, 2, 0, // def_cfa sp+0 (epilogue)
		0xc1,    // restore ra
		0x08, 8, // same_value s0
		0x42,                   // advance_loc 2 -> fn+10
		0x0b,                   // restore_state
		0x10, 9, 2, 0x78, 0x0c, // expression s1: breg8(s0) +12
		0x14, 18, 4, // val_offset s2 = cfa-16
		0x02, 6, // advance_loc1 6 -> fn+16
		0x0f, 4, 0x72, 0x10, 0x23, 0x10, // def_cfa_expression: breg2(sp)+16, plus_uconst 16
	}
	fdes, err := parseCFI(ehFrame(sec, fn, 0x40, insns), true, sec, 4)
	if err != nil || len(fdes) != 1 || fdes[0].lo != fn || fdes[0].hi != fn+0x40 {
		t.Fatalf("%v %+v", err, fdes)
	}
	f := &fdes[0]
	row := func(pc uint32) cfiRow {
		r, err := f.rowAt(uint64(pc))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := row(fn); r.cfa.reg != 2 || r.cfa.off != 0 || len(r.regs) != 0 {
		t.Fatalf("entry: %+v", r)
	}
	if r := row(fn + 2); r.cfa.off != 32 || r.regs[1].kind != ruleOffset || r.regs[1].off != -4 || r.regs[8].off != -8 {
		t.Fatalf("prologue: %+v", r)
	}
	if r := row(fn + 6); r.cfa.reg != 8 || r.cfa.off != 32 {
		t.Fatalf("frame pointer: %+v", r)
	}
	if r := row(fn + 8); r.cfa.reg != 2 || r.cfa.off != 0 || len(r.regs) != 1 || r.regs[8].kind != ruleSame {
		t.Fatalf("epilogue: %+v", r)
	}
	r := row(fn + 10)
	if r.cfa.reg != 8 || r.cfa.off != 32 || r.regs[1].off != -4 || r.regs[9].kind != ruleExpr || r.regs[18].kind != ruleValOffset || r.regs[18].off != -16 {
		t.Fatalf("restored: %+v", r)
	}
	if r := row(fn + 16); r.cfa.expr == nil {
		t.Fatalf("expression: %+v", r)
	}

	// A step through the frame of fn+10: CFA = s0+32
	tbl := &cfiTable{fdes: fdes}
	e := &ELF{cfi: tbl}
	e.cfiOnce.Do(func() {})
	var s regState
	s.r[2], s.valid[2] = 0x3fc80000, true
	s.r[8], s.valid[8] = 0x3fc80010, true
	mem := MemMap{0x3fc8002c: 0x42002000, 0x3fc80028: 0x3fc80100, 0x3fc8001c: 0x77}
	next, pc, err := e.stepCFI(&s, fn+10, fn+10, mem)
	if err != nil || pc != 0x42002000 || next.r[2] != 0x3fc80030 || next.r[8] != 0x3fc80100 ||
		next.r[9] != 0x77 || next.r[18] != 0x3fc80020 {
		t.Fatalf("%v %x %+v", err, pc, next)
	}
	// The CFA by the expression at fn+16: sp+32
	_, _, err = e.stepCFI(&s, fn+16, fn+16, MemMap{0x3fc8001c: 0x42002000})
	if err != nil {
		t.Fatal(err)
	}
	if tbl.find(fn-1) != nil || tbl.find(fn+0x40) != nil || tbl.find(fn+0x3f) == nil {
		t.Fatal("find")
	}
}

func TestDebugFrameVersions(t *testing.T) {
	le := binary.LittleEndian
	// .debug_frame version 4 (address and segment sizes in the CIE), the
	// FDE before its CIE
	cie := []byte{0xff, 0xff, 0xff, 0xff, 4, 0, 4, 0, 1, 0x7c, 1, 0x0c, 2, 0}
	for len(cie)%4 != 0 {
		cie = append(cie, 0)
	}
	fde := le.AppendUint32(nil, 0) // the CIE offset, set below
	fde = le.AppendUint32(fde, 0x42000100)
	fde = le.AppendUint32(fde, 0x20)
	fde = append(fde, 0x44, 0x0e, 16, 0x81, 1) // advance 4, cfa+16, ra at cfa-4
	for len(fde)%4 != 0 {
		fde = append(fde, 0)
	}
	var b []byte
	b = le.AppendUint32(b, uint32(len(fde)))
	cieOff := 4 + len(fde)
	le.PutUint32(fde, uint32(cieOff))
	b = append(b[:0], le.AppendUint32(nil, uint32(len(fde)))...)
	b = append(b, fde...)
	b = le.AppendUint32(b, uint32(len(cie)))
	b = append(b, cie...)
	fdes, err := parseCFI(b, false, 0, 4)
	if err != nil || len(fdes) != 1 {
		t.Fatalf("%v %d", err, len(fdes))
	}
	r, err := fdes[0].rowAt(0x42000104)
	if err != nil || r.cfa.off != 16 || r.regs[1].off != -4 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestEvalExpr(t *testing.T) {
	var s regState
	s.r[2], s.valid[2] = 100, true
	mem := MemMap{104: 7}
	cases := []struct {
		expr []byte
		want uint32
	}{
		{[]byte{0x72, 4, 0x06}, 7},                  // breg2 +4, deref
		{[]byte{0x35, 0x33, 0x1c}, 2},               // lit5 lit3 minus
		{[]byte{0x08, 0xff, 0x09, 0xff, 0x22}, 254}, // const1u 255 + const1s -1
		{[]byte{0x31, 0x28, 1, 0, 0x30, 0x3a}, 10},  // lit1, bra +1 over lit0, lit10
		{[]byte{0x92, 2, 0x7e, 0x34, 0x1e}, 392},    // bregx sp -2, lit4, mul
	}
	for i, c := range cases {
		v, err := evalExpr(c.expr, &s, mem, nil)
		if err != nil || v != c.want {
			t.Errorf("case %d: %d %v, want %d", i, v, err, c.want)
		}
	}
	if _, err := evalExpr([]byte{0x22}, &s, mem, nil); err == nil {
		t.Error("underflow")
	}
	if _, err := evalExpr([]byte{0x75, 0}, &s, mem, nil); err == nil {
		t.Error("unknown register")
	}
}

// TestRealELFs checks the parsers on large real files: set
// ALTESP_ELFDBG_ELF to a list of .elf files (separated by the path list
// separator), e.g. the firmware.elf of an Arduino or ESP-IDF build. Every
// FDE of .debug_frame must parse, lookups of all functions must work.
func TestRealELFs(t *testing.T) {
	list := os.Getenv("ALTESP_ELFDBG_ELF")
	if list == "" {
		t.Skip("ALTESP_ELFDBG_ELF not set")
	}
	for _, path := range strings.Split(list, string(os.PathListSeparator)) {
		start := time.Now()
		e, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		opened := time.Since(start)
		if s := e.f.Section(".debug_frame"); s != nil {
			data, _ := s.Data()
			fdes, err := parseCFI(data, false, 0, 4)
			if err != nil {
				t.Fatalf("%s: .debug_frame: %v after %d FDEs", path, err, len(fdes))
			}
			bad := 0
			for i := range fdes {
				if _, err := fdes[i].rowAt(fdes[i].hi - 1); err != nil {
					bad++
				}
			}
			t.Logf("%s: %d FDEs, %d with errors", path, len(fdes), bad)
			if bad > 0 {
				t.Errorf("%d FDEs failed", bad)
			}
		}
		start = time.Now()
		n, named, lined := 0, 0, 0
		for _, s := range e.syms {
			if !s.fn {
				continue
			}
			n++
			fr := e.Lookup(s.addr)
			if fr[len(fr)-1].Func != "" {
				named++
			}
			if fr[0].Line > 0 {
				lined++
			}
		}
		e.tcbOnce.Do(e.loadTCB)
		t.Logf("%s (%s): open %v, %d functions looked up in %v: %d named, %d with lines; TCB %+v",
			path, e.Arch(), opened, n, time.Since(start), named, lined, e.tcb)
	}
}
