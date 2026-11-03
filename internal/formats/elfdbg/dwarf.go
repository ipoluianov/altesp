package elfdbg

import (
	"debug/dwarf"
	"sort"
)

// The lookups by DWARF. The compilation units are indexed by their address
// ranges once; a unit is read the first time an address falls into it:
// its functions with their ranges and the inlined calls inside them
// (DW_TAG_subprogram, DW_TAG_inlined_subroutine), and the line table gives
// the file and line of the address.

type cuRange struct {
	lo, hi uint64
	off    dwarf.Offset
}

// cuInfo is a compilation unit read for lookups
type cuInfo struct {
	entry *dwarf.Entry
	funcs []*fnNode
	files []*dwarf.LineFile
}

// fnNode is a function or an inlined call, with the inlined calls inside
type fnNode struct {
	name     string
	ranges   [][2]uint64
	callFile int
	callLine int
	children []*fnNode
}

func (n *fnNode) contains(pc uint64) bool {
	for _, r := range n.ranges {
		if pc >= r[0] && pc < r[1] {
			return true
		}
	}
	return false
}

func (e *ELF) indexUnits() {
	if e.dw == nil {
		return
	}
	r := e.dw.Reader()
	for {
		ent, err := r.Next()
		if err != nil || ent == nil {
			break
		}
		if ent.Tag == dwarf.TagCompileUnit || ent.Tag == dwarf.TagPartialUnit {
			ranges, err := e.dw.Ranges(ent)
			if err == nil {
				for _, rg := range ranges {
					if rg[1] > rg[0] {
						e.cus = append(e.cus, cuRange{rg[0], rg[1], ent.Offset})
					}
				}
			}
		}
		r.SkipChildren()
	}
	sort.Slice(e.cus, func(i, j int) bool { return e.cus[i].lo < e.cus[j].lo })
}

// unitAt finds the compilation unit of an address
func (e *ELF) unitAt(pc uint64) (dwarf.Offset, bool) {
	e.cuOnce.Do(e.indexUnits)
	i := sort.Search(len(e.cus), func(i int) bool { return e.cus[i].lo > pc })
	// Ranges may nest or overlap: the nearest start that covers pc wins
	for i--; i >= 0; i-- {
		if pc < e.cus[i].hi {
			return e.cus[i].off, true
		}
	}
	return 0, false
}

// unit reads a compilation unit; e.mu is held
func (e *ELF) unit(off dwarf.Offset) *cuInfo {
	if ci, ok := e.cuCache[off]; ok {
		return ci
	}
	ci := &cuInfo{}
	e.cuCache[off] = ci
	r := e.dw.Reader()
	r.Seek(off)
	cu, err := r.Next()
	if err != nil || cu == nil {
		return ci
	}
	ci.entry = cu
	if lr, err := e.dw.LineReader(cu); err == nil && lr != nil {
		ci.files = lr.Files()
	}
	if !cu.Children {
		return ci
	}
	// stack holds the function in which the entries of each level are
	stack := []*fnNode{nil}
	for len(stack) > 0 {
		ent, err := r.Next()
		if err != nil || ent == nil {
			break
		}
		if ent.Tag == 0 {
			stack = stack[:len(stack)-1]
			continue
		}
		cur := stack[len(stack)-1]
		inner := cur
		if ent.Tag == dwarf.TagSubprogram || ent.Tag == dwarf.TagInlinedSubroutine {
			if ranges, err := e.dw.Ranges(ent); err == nil && len(ranges) > 0 {
				n := &fnNode{name: e.entryName(ent), ranges: ranges}
				if ent.Tag == dwarf.TagInlinedSubroutine {
					n.callFile = intAttr(ent, dwarf.AttrCallFile)
					n.callLine = intAttr(ent, dwarf.AttrCallLine)
				}
				if cur == nil {
					ci.funcs = append(ci.funcs, n)
				} else {
					cur.children = append(cur.children, n)
				}
				inner = n
			}
		}
		if ent.Children {
			stack = append(stack, inner)
		}
	}
	return ci
}

func intAttr(ent *dwarf.Entry, a dwarf.Attr) int {
	switch v := ent.Val(a).(type) {
	case int64:
		return int(v)
	case uint64:
		return int(v)
	}
	return 0
}

// entryName is the name of a function entry, through its abstract origin
// or specification; e.mu is held
func (e *ELF) entryName(ent *dwarf.Entry) string {
	for depth := 0; ent != nil && depth < 8; depth++ {
		if name, ok := ent.Val(dwarf.AttrName).(string); ok && name != "" {
			return name
		}
		var ref dwarf.Offset
		var ok bool
		if ref, ok = ent.Val(dwarf.AttrAbstractOrigin).(dwarf.Offset); !ok {
			if ref, ok = ent.Val(dwarf.AttrSpecification).(dwarf.Offset); !ok {
				return ""
			}
		}
		if name, ok := e.names[ref]; ok {
			return name
		}
		r := e.dw.Reader()
		r.Seek(ref)
		next, err := r.Next()
		if err != nil {
			return ""
		}
		name := e.entryName(next)
		e.names[ref] = name
		return name
	}
	return ""
}

func (ci *cuInfo) file(i int) string {
	if i >= 0 && i < len(ci.files) && ci.files[i] != nil {
		return ci.files[i].Name
	}
	return ""
}

// dwarfLookup gives the frames of an address by DWARF, or nothing
func (e *ELF) dwarfLookup(pc32 uint32) []Frame {
	if e.dw == nil {
		return nil
	}
	pc := uint64(pc32)
	off, ok := e.unitAt(pc)
	if !ok {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ci := e.unit(off)
	if ci.entry == nil {
		return nil
	}
	// The chain of the function and the inlined calls that hold pc
	var chain []*fnNode
	level := ci.funcs
	for {
		var found *fnNode
		for _, n := range level {
			if n.contains(pc) {
				found = n
				break
			}
		}
		if found == nil {
			break
		}
		chain = append(chain, found)
		level = found.children
	}
	file, line := "", 0
	if lr, err := e.dw.LineReader(ci.entry); err == nil && lr != nil {
		var le dwarf.LineEntry
		if lr.SeekPC(pc, &le) == nil && le.File != nil {
			file, line = le.File.Name, le.Line
		}
	}
	if len(chain) == 0 {
		return []Frame{{File: file, Line: line}}
	}
	frames := make([]Frame, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		frames = append(frames, Frame{Func: chain[i].name, File: file, Line: line, Inlined: i > 0})
		// The caller's place is the place of the inlined call
		file, line = ci.file(chain[i].callFile), chain[i].callLine
	}
	return frames
}
