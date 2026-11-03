package elfdbg

import (
	"debug/dwarf"
	"strings"
)

// The FreeRTOS task control block (TCB) of a coredump: its layout is read
// from the DWARF of tasks.c in the .elf (struct tskTaskControlBlock).
// Without it, the layouts of ESP-IDF v4.4 (the DWARF of its libfreertos)
// are tried, the one that gives readable task names wins: pxTopOfStack,
// [xMPUSettings on Xtensa], xStateListItem, xEventListItem, uxPriority,
// pxStack, pcTaskName[16].

type tcbLayout struct {
	name      int // pcTaskName
	nameLen   int
	stateItem int // xStateListItem
	eventItem int // xEventListItem
	container int // pvContainer inside a ListItem_t
	priority  int // uxPriority
	fromDWARF bool
}

var (
	tcbXtensa = tcbLayout{name: 56, nameLen: 16, stateItem: 8, eventItem: 28, container: 16, priority: 48}
	tcbRISCV  = tcbLayout{name: 52, nameLen: 16, stateItem: 4, eventItem: 24, container: 16, priority: 44}
)

// tcbLayouts are the layouts to try for a coredump
func (e *ELF) tcbLayouts(arch string) []tcbLayout {
	if e != nil {
		e.tcbOnce.Do(e.loadTCB)
		if e.tcb.fromDWARF {
			return []tcbLayout{e.tcb}
		}
	}
	if arch == ArchXtensa {
		return []tcbLayout{tcbXtensa, tcbRISCV}
	}
	return []tcbLayout{tcbRISCV, tcbXtensa}
}

func (e *ELF) loadTCB() {
	if e.dw == nil {
		return
	}
	r := e.dw.Reader()
	for {
		cu, err := r.Next()
		if err != nil || cu == nil {
			return
		}
		name, _ := cu.Val(dwarf.AttrName).(string)
		if cu.Tag != dwarf.TagCompileUnit || !strings.HasSuffix(name, "tasks.c") || !cu.Children {
			r.SkipChildren()
			continue
		}
		for {
			ent, err := r.Next()
			if err != nil || ent == nil || ent.Tag == 0 {
				break
			}
			n, _ := ent.Val(dwarf.AttrName).(string)
			if ent.Tag == dwarf.TagStructType && n == "tskTaskControlBlock" {
				if l, ok := e.tcbFromType(ent.Offset); ok {
					e.tcb = l
					return
				}
			}
			r.SkipChildren()
		}
	}
}

func (e *ELF) tcbFromType(off dwarf.Offset) (tcbLayout, bool) {
	t, err := e.dw.Type(off)
	if err != nil {
		return tcbLayout{}, false
	}
	st, ok := t.(*dwarf.StructType)
	if !ok {
		return tcbLayout{}, false
	}
	l := tcbLayout{name: -1, stateItem: -1, eventItem: -1, priority: -1, container: -1, fromDWARF: true}
	for _, f := range st.Field {
		switch f.Name {
		case "pcTaskName":
			l.name = int(f.ByteOffset)
			l.nameLen = int(f.Type.Size())
		case "xStateListItem", "xGenericListItem":
			l.stateItem = int(f.ByteOffset)
			if li, ok := underlying(f.Type).(*dwarf.StructType); ok {
				for _, g := range li.Field {
					if g.Name == "pvContainer" {
						l.container = int(g.ByteOffset)
					}
				}
			}
		case "xEventListItem":
			l.eventItem = int(f.ByteOffset)
		case "uxPriority":
			l.priority = int(f.ByteOffset)
		}
	}
	if l.name < 0 || l.nameLen <= 0 || l.nameLen > 64 {
		return tcbLayout{}, false
	}
	return l, true
}

func underlying(t dwarf.Type) dwarf.Type {
	for i := 0; i < 8; i++ {
		switch x := t.(type) {
		case *dwarf.TypedefType:
			t = x.Type
		case *dwarf.QualType:
			t = x.Type
		default:
			return t
		}
	}
	return t
}

// taskState tells the state of a task by the list its xStateListItem is
// in, as eTaskGetState of FreeRTOS: the lists are static variables of
// tasks.c, found by the symbol table; a suspended task that waits for an
// event is blocked. Without an .elf or the symbols it is empty.
func (e *ELF) taskState(container uint32, waits bool) string {
	if e == nil || container == 0 {
		return ""
	}
	lists := []struct{ sym, state string }{
		{"pxReadyTasksLists", "ready"},
		{"xPendingReadyList", "ready"},
		{"xDelayedTaskList1", "blocked"},
		{"xDelayedTaskList2", "blocked"},
		{"xSuspendedTaskList", "suspended"},
		{"xTasksWaitingTermination", "deleted"},
	}
	for _, l := range lists {
		addr, size, ok := e.Symbol(l.sym)
		if !ok {
			continue
		}
		if size == 0 {
			size = 20 // a List_t
		}
		if container >= addr && container < addr+size {
			if l.state == "suspended" && waits {
				return "blocked"
			}
			return l.state
		}
	}
	return ""
}
