package elfdbg

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// The call frame information (DWARF 5 section 6.4, the same in DWARF 2-4):
// .debug_frame as the specification describes it and .eh_frame, its
// variant of the LSB (Linux Standard Base Core, "Exception Frames"):
// another CIE id, encoded pointers and the "z" augmentation. A table of the
// FDEs sorted by address; for an address, the instructions of its CIE and
// FDE are run up to it and give the row: how to find the CFA and where
// every register of the caller is.

// cie is a Common Information Entry
type cie struct {
	codeAlign uint64
	dataAlign int64
	raReg     uint64
	addrSize  int
	fdeEnc    byte // eh_frame: the encoding of the addresses of the FDEs
	ehFrame   bool
	augZ      bool
	initial   []byte
}

// fde is a Frame Description Entry
type fde struct {
	cie    *cie
	lo, hi uint64
	insns  []byte
	// base is the address of the FDE's instructions for DW_CFA_set_loc
	// with a pc-relative encoding
	base uint64
}

type cfiTable struct {
	fdes []fde
}

var errNoCFI = errors.New("no call frame information for the address")

// The encodings of pointers in .eh_frame (DW_EH_PE_*)
const (
	pePtr     = 0x00
	peULEB    = 0x01
	peUdata2  = 0x02
	peUdata4  = 0x03
	peUdata8  = 0x04
	peSLEB    = 0x09
	peSdata2  = 0x0a
	peSdata4  = 0x0b
	peSdata8  = 0x0c
	pePCRel   = 0x10
	peDataRel = 0x30
	peIndir   = 0x80
	peOmit    = 0xff
)

// cfiBuf reads the fields of the entries
type cfiBuf struct {
	b   []byte
	off int
	err error
}

func (r *cfiBuf) fail() {
	if r.err == nil {
		r.err = errors.New("call frame information truncated")
	}
	r.off = len(r.b)
}

func (r *cfiBuf) u8() byte {
	if r.off+1 > len(r.b) {
		r.fail()
		return 0
	}
	r.off++
	return r.b[r.off-1]
}

func (r *cfiBuf) u16() uint16 {
	if r.off+2 > len(r.b) {
		r.fail()
		return 0
	}
	r.off += 2
	return binary.LittleEndian.Uint16(r.b[r.off-2:])
}

func (r *cfiBuf) u32() uint32 {
	if r.off+4 > len(r.b) {
		r.fail()
		return 0
	}
	r.off += 4
	return binary.LittleEndian.Uint32(r.b[r.off-4:])
}

func (r *cfiBuf) u64() uint64 {
	if r.off+8 > len(r.b) {
		r.fail()
		return 0
	}
	r.off += 8
	return binary.LittleEndian.Uint64(r.b[r.off-8:])
}

func (r *cfiBuf) uleb() uint64 {
	var v uint64
	for shift := uint(0); ; shift += 7 {
		b := r.u8()
		if r.err != nil {
			return 0
		}
		if shift < 64 {
			v |= uint64(b&0x7f) << shift
		}
		if b&0x80 == 0 {
			return v
		}
	}
}

func (r *cfiBuf) sleb() int64 {
	var v int64
	var shift uint
	var b byte
	for {
		b = r.u8()
		if r.err != nil {
			return 0
		}
		if shift < 64 {
			v |= int64(b&0x7f) << shift
		}
		shift += 7
		if b&0x80 == 0 {
			break
		}
	}
	if shift < 64 && b&0x40 != 0 {
		v |= -1 << shift
	}
	return v
}

func (r *cfiBuf) addr(size int) uint64 {
	if size == 8 {
		return r.u64()
	}
	return uint64(r.u32())
}

func (r *cfiBuf) cstring() string {
	start := r.off
	for r.off < len(r.b) {
		if r.b[r.off] == 0 {
			s := string(r.b[start:r.off])
			r.off++
			return s
		}
		r.off++
	}
	r.fail()
	return ""
}

// encoded reads a pointer of .eh_frame; secAddr is the address of the
// section, for the pc-relative encoding
func (r *cfiBuf) encoded(enc byte, addrSize int, secAddr uint64) uint64 {
	if enc == peOmit {
		return 0
	}
	pos := secAddr + uint64(r.off)
	var v uint64
	switch enc & 0x0f {
	case pePtr:
		v = r.addr(addrSize)
	case peULEB:
		v = r.uleb()
	case peUdata2:
		v = uint64(r.u16())
	case peUdata4:
		v = uint64(r.u32())
	case peUdata8:
		v = r.u64()
	case peSLEB:
		v = uint64(r.sleb())
	case peSdata2:
		v = uint64(int64(int16(r.u16())))
	case peSdata4:
		v = uint64(int64(int32(r.u32())))
	case peSdata8:
		v = r.u64()
	default:
		if r.err == nil {
			r.err = fmt.Errorf("pointer encoding 0x%02x", enc)
		}
		return 0
	}
	switch enc & 0x70 {
	case 0:
	case pePCRel:
		v += pos
	default:
		// textrel, datarel, funcrel: not produced for the ESP chips
		if r.err == nil {
			r.err = fmt.Errorf("pointer encoding 0x%02x", enc)
		}
	}
	if addrSize == 4 {
		v &= 0xffffffff
	}
	return v
}

// parseCFI reads .debug_frame (ehFrame false) or .eh_frame; secAddr is
// the address of an .eh_frame
func parseCFI(data []byte, ehFrame bool, secAddr uint64, addrSize int) ([]fde, error) {
	cies := map[int]*cie{}
	var fdes []fde
	off := 0
	for off+4 <= len(data) {
		start := off
		r := &cfiBuf{b: data, off: off}
		length := uint64(r.u32())
		is64 := false
		if length == 0xffffffff {
			length = r.u64()
			is64 = true
		}
		if length == 0 {
			if ehFrame {
				// The terminator of .eh_frame
				break
			}
			off = r.off
			continue
		}
		hdr := r.off
		end := hdr + int(length)
		if length > uint64(len(data)) || end > len(data) || r.err != nil {
			return fdes, fmt.Errorf("entry at 0x%x: bad length", start)
		}
		r.b = data[:end]
		var id uint64
		idPos := r.off
		if is64 {
			id = r.u64()
		} else {
			id = uint64(r.u32())
		}
		isCIE := (ehFrame && id == 0) || (!ehFrame && (id == 0xffffffff || (is64 && id == 0xffffffffffffffff)))
		if isCIE {
			c, err := parseCIE(r, ehFrame, addrSize)
			if err != nil {
				return fdes, fmt.Errorf("CIE at 0x%x: %w", start, err)
			}
			cies[start] = c
			off = end
			continue
		}
		cieOff := int(id)
		if ehFrame {
			cieOff = idPos - int(id)
		}
		c := cies[cieOff]
		if c == nil {
			// A CIE after its FDE is allowed: read it from where it is
			var err error
			c, err = cieAt(data, cieOff, ehFrame, addrSize)
			if err != nil {
				return fdes, fmt.Errorf("FDE at 0x%x: %w", start, err)
			}
			cies[cieOff] = c
		}
		var f fde
		f.cie = c
		if ehFrame {
			f.lo = r.encoded(c.fdeEnc, c.addrSize, secAddr)
			f.hi = f.lo + r.encoded(c.fdeEnc&0x0f, c.addrSize, secAddr)
			if c.augZ {
				n := r.uleb()
				r.off += int(n)
			}
		} else {
			f.lo = r.addr(c.addrSize)
			f.hi = f.lo + r.addr(c.addrSize)
		}
		if r.err != nil || r.off > end {
			return fdes, fmt.Errorf("FDE at 0x%x: truncated", start)
		}
		f.base = secAddr + uint64(r.off)
		f.insns = data[r.off:end]
		if f.hi > f.lo {
			fdes = append(fdes, f)
		}
		off = end
	}
	return fdes, nil
}

func cieAt(data []byte, off int, ehFrame bool, addrSize int) (*cie, error) {
	if off < 0 || off+4 > len(data) {
		return nil, errors.New("no CIE")
	}
	r := &cfiBuf{b: data, off: off}
	length := uint64(r.u32())
	is64 := length == 0xffffffff
	if is64 {
		length = r.u64()
	}
	end := r.off + int(length)
	if length > uint64(len(data)) || end > len(data) {
		return nil, errors.New("bad CIE")
	}
	r.b = data[:end]
	if is64 {
		r.u64()
	} else {
		r.u32()
	}
	return parseCIE(r, ehFrame, addrSize)
}

func parseCIE(r *cfiBuf, ehFrame bool, addrSize int) (*cie, error) {
	c := &cie{addrSize: addrSize, ehFrame: ehFrame}
	version := r.u8()
	if version != 1 && version != 3 && version != 4 {
		return nil, fmt.Errorf("version %d", version)
	}
	aug := r.cstring()
	if aug == "eh" {
		// GCC 2: the address of the exception table follows
		r.addr(addrSize)
	}
	if version >= 4 {
		if as := int(r.u8()); as == 4 || as == 8 {
			c.addrSize = as
		}
		r.u8() // segment selector size
	}
	c.codeAlign = r.uleb()
	c.dataAlign = r.sleb()
	if version == 1 {
		c.raReg = uint64(r.u8())
	} else {
		c.raReg = r.uleb()
	}
	if len(aug) > 0 && aug[0] == 'z' {
		c.augZ = true
		n := r.uleb()
		augEnd := r.off + int(n)
		for _, ch := range aug[1:] {
			switch ch {
			case 'R':
				c.fdeEnc = r.u8()
			case 'L':
				r.u8()
			case 'P':
				enc := r.u8()
				r.encoded(enc&^peIndir, c.addrSize, 0)
			case 'S', 'B':
			default:
				// Unknown augmentation: its data is skipped below
			}
		}
		r.off = augEnd
	} else if aug != "" && aug != "eh" {
		return nil, fmt.Errorf("augmentation %q", aug)
	}
	if r.err != nil || r.off > len(r.b) {
		return nil, errors.New("truncated")
	}
	c.initial = r.b[r.off:]
	return c, nil
}

// loadCFI reads the call frame information of the file: .debug_frame and
// .eh_frame, both when there are both
func (e *ELF) loadCFI() {
	t := &cfiTable{}
	if s := e.f.Section(".debug_frame"); s != nil {
		if data, err := s.Data(); err == nil {
			fdes, _ := parseCFI(data, false, 0, 4)
			t.fdes = append(t.fdes, fdes...)
		}
	}
	if s := e.f.Section(".eh_frame"); s != nil && s.Type != elf.SHT_NOBITS {
		if data, err := s.Data(); err == nil {
			fdes, _ := parseCFI(data, true, s.Addr, 4)
			t.fdes = append(t.fdes, fdes...)
		}
	}
	sort.SliceStable(t.fdes, func(i, j int) bool { return t.fdes[i].lo < t.fdes[j].lo })
	e.cfi = t
}

// find is the FDE of an address
func (t *cfiTable) find(pc uint64) *fde {
	i := sort.Search(len(t.fdes), func(i int) bool { return t.fdes[i].lo > pc })
	// FDEs don't nest; a few steps back pass the stale ones that the
	// linker leaves for the functions it dropped
	for n := 0; i > 0 && n < 16; n++ {
		i--
		if pc < t.fdes[i].hi {
			return &t.fdes[i]
		}
	}
	return nil
}

// The rules of the registers
type ruleKind uint8

const (
	ruleSame      ruleKind = iota // the same value as in the callee
	ruleUndefined                 // not recoverable
	ruleOffset                    // saved at CFA+off
	ruleValOffset                 // the value is CFA+off
	ruleRegister                  // in another register
	ruleExpr                      // saved at the address the expression gives
	ruleValExpr                   // the value is what the expression gives
)

type regRule struct {
	kind ruleKind
	reg  uint64
	off  int64
	expr []byte
}

// cfaRule is either a register and an offset or an expression
type cfaRule struct {
	reg  uint64
	off  int64
	expr []byte
}

type cfiRow struct {
	cfa  cfaRule
	regs map[uint64]regRule
}

func (r cfiRow) clone() cfiRow {
	c := cfiRow{cfa: r.cfa, regs: make(map[uint64]regRule, len(r.regs))}
	for k, v := range r.regs {
		c.regs[k] = v
	}
	return c
}

// The instructions (DW_CFA_*)
const (
	cfaAdvanceLoc       = 0x40
	cfaOffset           = 0x80
	cfaRestore          = 0xc0
	cfaNop              = 0x00
	cfaSetLoc           = 0x01
	cfaAdvanceLoc1      = 0x02
	cfaAdvanceLoc2      = 0x03
	cfaAdvanceLoc4      = 0x04
	cfaOffsetExtended   = 0x05
	cfaRestoreExtended  = 0x06
	cfaUndefined        = 0x07
	cfaSameValue        = 0x08
	cfaRegister         = 0x09
	cfaRememberState    = 0x0a
	cfaRestoreState     = 0x0b
	cfaDefCFA           = 0x0c
	cfaDefCFARegister   = 0x0d
	cfaDefCFAOffset     = 0x0e
	cfaDefCFAExpression = 0x0f
	cfaExpression       = 0x10
	cfaOffsetExtendedSF = 0x11
	cfaDefCFASF         = 0x12
	cfaDefCFAOffsetSF   = 0x13
	cfaValOffset        = 0x14
	cfaValOffsetSF      = 0x15
	cfaValExpression    = 0x16
	cfaGNUWindowSave    = 0x2d
	cfaGNUArgsSize      = 0x2e
	cfaGNUNegOffsetExt  = 0x2f
)

// maxRememberedStates limits DW_CFA_remember_state of broken data
const maxRememberedStates = 64

// rowAt runs the instructions of the CIE and the FDE up to pc
func (f *fde) rowAt(pc uint64) (cfiRow, error) {
	row := cfiRow{regs: map[uint64]regRule{}}
	// The initial instructions give the row that DW_CFA_restore returns to
	if _, err := f.run(f.cie.initial, &row, nil, f.lo, ^uint64(0)); err != nil {
		return row, err
	}
	initial := row.clone()
	_, err := f.run(f.insns, &row, &initial, f.lo, pc)
	return row, err
}

// run executes instructions from loc until the location passes pc
func (f *fde) run(insns []byte, row *cfiRow, initial *cfiRow, loc, pc uint64) (uint64, error) {
	c := f.cie
	r := &cfiBuf{b: insns}
	var stack []cfiRow
	advance := func(delta uint64) bool {
		next := loc + delta*c.codeAlign
		if next > pc {
			return false
		}
		loc = next
		return true
	}
	restore := func(reg uint64) {
		if initial != nil {
			if rule, ok := initial.regs[reg]; ok {
				row.regs[reg] = rule
				return
			}
		}
		delete(row.regs, reg)
	}
	for r.off < len(r.b) && r.err == nil {
		op := r.u8()
		switch op & 0xc0 {
		case cfaAdvanceLoc:
			if !advance(uint64(op & 0x3f)) {
				return loc, nil
			}
			continue
		case cfaOffset:
			row.regs[uint64(op&0x3f)] = regRule{kind: ruleOffset, off: int64(r.uleb()) * c.dataAlign}
			continue
		case cfaRestore:
			restore(uint64(op & 0x3f))
			continue
		}
		switch op {
		case cfaNop:
		case cfaSetLoc:
			var next uint64
			if c.ehFrame {
				next = r.encoded(c.fdeEnc, c.addrSize, f.base)
			} else {
				next = r.addr(c.addrSize)
			}
			if next > pc {
				return loc, nil
			}
			loc = next
		case cfaAdvanceLoc1:
			if !advance(uint64(r.u8())) {
				return loc, nil
			}
		case cfaAdvanceLoc2:
			if !advance(uint64(r.u16())) {
				return loc, nil
			}
		case cfaAdvanceLoc4:
			if !advance(uint64(r.u32())) {
				return loc, nil
			}
		case cfaOffsetExtended:
			reg := r.uleb()
			row.regs[reg] = regRule{kind: ruleOffset, off: int64(r.uleb()) * c.dataAlign}
		case cfaRestoreExtended:
			restore(r.uleb())
		case cfaUndefined:
			row.regs[r.uleb()] = regRule{kind: ruleUndefined}
		case cfaSameValue:
			row.regs[r.uleb()] = regRule{kind: ruleSame}
		case cfaRegister:
			reg := r.uleb()
			row.regs[reg] = regRule{kind: ruleRegister, reg: r.uleb()}
		case cfaRememberState:
			if len(stack) >= maxRememberedStates {
				return loc, errors.New("too many DW_CFA_remember_state")
			}
			stack = append(stack, row.clone())
		case cfaRestoreState:
			if len(stack) == 0 {
				return loc, errors.New("DW_CFA_restore_state without remember_state")
			}
			*row = stack[len(stack)-1]
			stack = stack[:len(stack)-1]
		case cfaDefCFA:
			row.cfa = cfaRule{reg: r.uleb(), off: int64(r.uleb())}
		case cfaDefCFASF:
			row.cfa = cfaRule{reg: r.uleb(), off: r.sleb() * c.dataAlign}
		case cfaDefCFARegister:
			row.cfa.reg = r.uleb()
			row.cfa.expr = nil
		case cfaDefCFAOffset:
			row.cfa.off = int64(r.uleb())
		case cfaDefCFAOffsetSF:
			row.cfa.off = r.sleb() * c.dataAlign
		case cfaDefCFAExpression:
			row.cfa = cfaRule{expr: r.block()}
		case cfaExpression:
			reg := r.uleb()
			row.regs[reg] = regRule{kind: ruleExpr, expr: r.block()}
		case cfaValExpression:
			reg := r.uleb()
			row.regs[reg] = regRule{kind: ruleValExpr, expr: r.block()}
		case cfaOffsetExtendedSF:
			reg := r.uleb()
			row.regs[reg] = regRule{kind: ruleOffset, off: r.sleb() * c.dataAlign}
		case cfaValOffset:
			reg := r.uleb()
			row.regs[reg] = regRule{kind: ruleValOffset, off: int64(r.uleb()) * c.dataAlign}
		case cfaValOffsetSF:
			reg := r.uleb()
			row.regs[reg] = regRule{kind: ruleValOffset, off: r.sleb() * c.dataAlign}
		case cfaGNUArgsSize:
			r.uleb()
		case cfaGNUNegOffsetExt:
			reg := r.uleb()
			row.regs[reg] = regRule{kind: ruleOffset, off: -int64(r.uleb()) * c.dataAlign}
		case cfaGNUWindowSave:
			// SPARC and AArch64 only: nothing to do for the ESP chips
		default:
			return loc, fmt.Errorf("unknown CFA instruction 0x%02x", op)
		}
	}
	return loc, r.err
}

func (r *cfiBuf) block() []byte {
	n := r.uleb()
	if n > uint64(len(r.b)-r.off) {
		r.fail()
		return nil
	}
	b := r.b[r.off : r.off+int(n)]
	r.off += int(n)
	return b
}
