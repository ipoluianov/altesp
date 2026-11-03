package elfdbg

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"
	"strings"
)

// A coredump as ESP-IDF saves it to the coredump partition or prints it to
// the UART (base64 between "CORE DUMP START" and "CORE DUMP END"):
//
//	core_dump_header_t   data_len, version, tasks_num, tcb_sz, mem_segs_num
//	ELF core file        (ELF format), or the tasks and memory (binary)
//	checksum             CRC-32 (4 bytes) or SHA-256 (32 bytes)
//
// data_len counts all of it, the header and the checksum too. version is
// chip<<16 | major<<8 | minor; the major is 1 for the ELF format and 0 for
// the legacy binary one (esp_core_dump_types.h; ESP-IDF v4.4 writes 0x100,
// ELF with CRC-32 on the ESP32). The checksum covers everything before it.
//
// The ELF core file has a PT_NOTE segment and a PT_LOAD segment for every
// saved memory region (the TCB and the stack of every task, some data).
// The notes: "CORE" NT_PRSTATUS per task (pr_pid is the TCB address, the
// registers at offset 72: xtensa_gregset_t or riscv_regs), "ESP_CORE_DUMP_INFO"
// (core_dump_elf_version_info_t: the version and the SHA-256 of the
// application .elf) and "EXTRA_INFO" (the crashed task's TCB address, then
// on Xtensa pairs of a special register number and its value: EXCCAUSE,
// EXCVADDR, EPCx, EPSx).

// ErrNoCoredump is an empty coredump partition
var ErrNoCoredump = errors.New("no coredump: the partition is empty")

// ErrChecksum is a coredump whose checksum doesn't match
var ErrChecksum = errors.New("the coredump checksum doesn't match: the data is damaged or incomplete")

// Task is a task of a coredump
type Task struct {
	Name  string
	TCB   uint32
	State string // running, ready, blocked, suspended, deleted; empty when unknown
	// Priority is the FreeRTOS priority, -1 when unknown
	Priority int
	Crashed  bool
	// Regs are the registers as ESP-IDF names them: PC, PS, A0-A15, SAR
	// on Xtensa; PC, RA, SP, GP, TP, T0-T6, S0/FP, S1-S11, A0-A7 on RISC-V
	Regs map[string]uint32
	// Stack is the unwound stack, the top first
	Stack []StackFrame
	// Frames are the frames of the stack with the inlined calls
	Frames []Frame
	// StackStart and StackEnd are the saved region that holds the stack
	StackStart, StackEnd uint32
}

// Coredump is a parsed coredump
type Coredump struct {
	Format    string // "ELF" or "binary"
	Version   string // major.minor of the format
	Chip      string // by the version word; empty for a bare ELF core file
	Arch      string
	Checksum  string // "CRC32", "SHA256", or empty for a bare ELF core file
	AppSHA256 string // the SHA-256 of the application .elf the dump names
	// ELFMismatch is set when the dump names an application .elf other
	// than the one given: the decoded frames are then wrong
	ELFMismatch bool
	Tasks       []Task
	// Exception is the cause of the crash: the exception and its address
	Exception string

	mem []memSeg
}

type memSeg struct {
	addr uint32
	data []byte
}

// Read32 reads the saved memory; the coredump is a Memory
func (c *Coredump) Read32(addr uint32) (uint32, bool) {
	for _, s := range c.mem {
		if addr >= s.addr && uint64(addr)+4 <= uint64(s.addr)+uint64(len(s.data)) {
			return binary.LittleEndian.Uint32(s.data[addr-s.addr:]), true
		}
	}
	return 0, false
}

func (c *Coredump) read(addr uint32, n int) []byte {
	for _, s := range c.mem {
		if addr >= s.addr && uint64(addr)+uint64(n) <= uint64(s.addr)+uint64(len(s.data)) {
			return s.data[addr-s.addr : addr-s.addr+uint32(n)]
		}
	}
	return nil
}

func (c *Coredump) segment(addr uint32) (memSeg, bool) {
	for _, s := range c.mem {
		if addr >= s.addr && uint64(addr) < uint64(s.addr)+uint64(len(s.data)) {
			return s, true
		}
	}
	return memSeg{}, false
}

// The chip ids of the version word (esp_chip_id_t)
var chipNames = map[uint32]string{
	0x0000: "ESP32", 0x0002: "ESP32-S2", 0x0005: "ESP32-C3", 0x0009: "ESP32-S3",
	0x000C: "ESP32-C2", 0x000D: "ESP32-C6", 0x0010: "ESP32-H2", 0x0012: "ESP32-P4",
	0x0014: "ESP32-C61", 0x0017: "ESP32-C5", 0x0019: "ESP32-H21", 0x001C: "ESP32-H4",
}

func chipArch(chip uint32) string {
	switch chip {
	case 0x0000, 0x0002, 0x0009:
		return ArchXtensa
	}
	return ArchRISCV
}

const (
	cdHeaderLen   = 20 // core_dump_header_t
	cdHeaderLenV1 = 16 // without mem_segs_num, the first binary version
)

// ParseCoredump reads a coredump: the data of the coredump partition, a
// file with it, the text printed to the UART, or a bare ELF core file. The
// .elf of the application (may be nil) names the tasks' functions and,
// on RISC-V, unwinds their stacks.
func ParseCoredump(data []byte, e *ELF) (*Coredump, error) {
	if b, ok := fromUARTText(data); ok {
		data = b
	}
	if bytes.HasPrefix(data, []byte(elf.ELFMAG)) {
		c := &Coredump{Format: "ELF"}
		if err := c.parseELF(data); err != nil {
			return nil, err
		}
		c.finish(e)
		return c, nil
	}
	if len(data) < cdHeaderLen+4 {
		return nil, errors.New("too short for a coredump")
	}
	if binary.LittleEndian.Uint32(data) == 0xffffffff {
		return nil, ErrNoCoredump
	}
	n := binary.LittleEndian.Uint32(data)
	if n < cdHeaderLen+4 || uint64(n) > uint64(len(data)) {
		return nil, fmt.Errorf("not a coredump: data length %d of %d bytes", n, len(data))
	}
	body := data[:n]
	ver := binary.LittleEndian.Uint32(body[4:])
	chip, major, minor := ver>>16, ver>>8&0xff, ver&0xff
	c := &Coredump{Version: fmt.Sprintf("%d.%d", major, minor), Chip: chipNames[chip]}
	if c.Chip == "" {
		c.Chip = fmt.Sprintf("chip 0x%04x", chip)
	}
	payload, kind, err := verifyChecksum(body)
	if err != nil {
		return nil, err
	}
	c.Checksum = kind
	switch {
	case major == 1:
		c.Format = "ELF"
		off := bytes.Index(payload[:min(len(payload), 64)], []byte(elf.ELFMAG))
		if off < cdHeaderLenV1 {
			return nil, errors.New("coredump: no ELF after the header")
		}
		if err := c.parseELF(payload[off:]); err != nil {
			return nil, err
		}
	case major == 0:
		c.Format = "binary"
		c.Arch = chipArch(chip)
		if e != nil {
			c.Arch = e.Arch()
		}
		if err := c.parseBinary(payload); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("coredump version 0x%x is not known", ver)
	}
	c.finish(e)
	return c, nil
}

// verifyChecksum finds the checksum at the end of the data, CRC-32 or
// SHA-256, and gives the data before it
func verifyChecksum(body []byte) ([]byte, string, error) {
	for _, skip := range []int{0, cdHeaderLen} {
		if len(body) >= 4+skip {
			p := body[:len(body)-4]
			if crc32.ChecksumIEEE(p[skip:]) == binary.LittleEndian.Uint32(body[len(body)-4:]) {
				return p, "CRC32", nil
			}
		}
		if len(body) >= 32+skip {
			p := body[:len(body)-32]
			sum := sha256.Sum256(p[skip:])
			if bytes.Equal(sum[:], body[len(body)-32:]) {
				return p, "SHA256", nil
			}
		}
	}
	return nil, "", ErrChecksum
}

// fromUARTText takes the base64 of a coredump printed to the UART
func fromUARTText(data []byte) ([]byte, bool) {
	const start, end = "CORE DUMP START", "CORE DUMP END"
	i := bytes.Index(data, []byte(start))
	if i < 0 {
		return nil, false
	}
	text := data[i+len(start):]
	if j := bytes.Index(text, []byte(end)); j >= 0 {
		text = text[:j]
	}
	var b64 strings.Builder
	for _, line := range strings.Split(string(text), "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "=")
		if line == "" {
			continue
		}
		// The lines may carry a log prefix; the base64 is the last field
		f := strings.Fields(line)
		b64.WriteString(f[len(f)-1])
	}
	s := strings.TrimRight(b64.String(), "=")
	out, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return out, true
}

// The notes of the ELF core file
const (
	ntPRStatus      = 1
	prstatusRegs    = 72 // the registers in the PRSTATUS note
	xtensaARegsOff  = prstatusRegs + 64*4
	noteCoreDumpInf = "ESP_CORE_DUMP_INFO"
	noteExtraInfo   = "EXTRA_INFO"
)

// The special registers of Xtensa in EXTRA_INFO (Xtensa ISA reference)
const (
	xtSREPC1     = 177
	xtSREXCCAUSE = 232
	xtSREXCVADDR = 238
)

type rawTask struct {
	tcb  uint32
	regs map[string]uint32
}

func (c *Coredump) parseELF(data []byte) error {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("coredump ELF: %w", err)
	}
	switch f.Machine {
	case elf.EM_XTENSA:
		c.Arch = ArchXtensa
	case elf.EM_RISCV:
		c.Arch = ArchRISCV
	default:
		return fmt.Errorf("coredump ELF for %v", f.Machine)
	}
	var tasks []rawTask
	var crashedTCB uint32
	extra := map[uint32]uint32{}
	for _, p := range f.Progs {
		if p.Filesz == 0 {
			continue
		}
		b := make([]byte, p.Filesz)
		if _, err := p.ReadAt(b, 0); err != nil {
			return fmt.Errorf("coredump ELF: segment at 0x%x: %w", p.Vaddr, err)
		}
		switch p.Type {
		case elf.PT_LOAD:
			c.mem = append(c.mem, memSeg{uint32(p.Vaddr), b})
		case elf.PT_NOTE:
			for _, n := range parseNotes(b) {
				switch {
				case n.name == "CORE" && n.typ == ntPRStatus:
					if t, ok := c.prstatus(n.desc); ok {
						tasks = append(tasks, t)
					}
				case n.name == noteCoreDumpInf && len(n.desc) >= 4:
					if c.Version == "" {
						v := binary.LittleEndian.Uint32(n.desc)
						c.Version = fmt.Sprintf("%d.%d", v>>8&0xff, v&0xff)
						if name, ok := chipNames[v>>16]; ok {
							c.Chip = name
						}
					}
					sha := n.desc[4:]
					if i := bytes.IndexByte(sha, 0); i >= 0 {
						sha = sha[:i]
					}
					c.AppSHA256 = string(sha)
				case n.name == noteExtraInfo && len(n.desc) >= 4:
					crashedTCB = binary.LittleEndian.Uint32(n.desc)
					if c.Arch == ArchXtensa {
						for i := 4; i+8 <= len(n.desc); i += 8 {
							extra[binary.LittleEndian.Uint32(n.desc[i:])] = binary.LittleEndian.Uint32(n.desc[i+4:])
						}
					}
				}
			}
		}
	}
	sort.SliceStable(c.mem, func(i, j int) bool { return c.mem[i].addr < c.mem[j].addr })
	if len(tasks) == 0 {
		return errors.New("coredump ELF: no tasks")
	}
	for _, rt := range tasks {
		t := Task{TCB: rt.tcb, Regs: rt.regs, Crashed: crashedTCB != 0 && rt.tcb == crashedTCB}
		c.Tasks = append(c.Tasks, t)
	}
	for i := range c.Tasks {
		t := &c.Tasks[i]
		if !t.Crashed {
			continue
		}
		pc := t.Regs["PC"]
		if c.Arch == ArchXtensa {
			if cause, ok := extra[xtSREXCCAUSE]; ok {
				c.Exception = xtensaException(cause, extra[xtSREXCVADDR], pc)
			}
		} else if mcause, mtval, ok := c.riscvTrap(t.Regs); ok {
			c.Exception = riscvException(mcause, mtval, pc)
		}
	}
	return nil
}

// prstatus takes the registers of a task from its NT_PRSTATUS note
func (c *Coredump) prstatus(d []byte) (rawTask, bool) {
	if len(d) < prstatusRegs+32*4 {
		return rawTask{}, false
	}
	t := rawTask{tcb: binary.LittleEndian.Uint32(d[24:]), regs: map[string]uint32{}}
	w := func(i int) uint32 { return binary.LittleEndian.Uint32(d[prstatusRegs+4*i:]) }
	if c.Arch == ArchRISCV {
		for i, name := range riscvRegNames {
			if i == 0 {
				name = "PC"
			}
			t.regs[name] = w(i)
		}
		return t, true
	}
	// xtensa_gregset_t: pc, ps, lbeg, lend, lcount, sar, windowstart,
	// windowbase, 56 reserved words, then the physical registers ar[64]
	if len(d) < xtensaARegsOff+16*4 {
		return rawTask{}, false
	}
	for i, name := range []string{"PC", "PS", "LBEG", "LEND", "LCOUNT", "SAR", "WINDOWSTART", "WINDOWBASE"} {
		t.regs[name] = w(i)
	}
	nregs := (len(d) - xtensaARegsOff) / 4
	if nregs > 64 {
		nregs = 64
	}
	nregs &^= 3
	wb := int(t.regs["WINDOWBASE"])
	for i := 0; i < 16; i++ {
		// The logical register ai is the physical a(4*windowbase+i)
		phys := (wb*4 + i) % nregs
		t.regs[fmt.Sprintf("A%d", i)] = binary.LittleEndian.Uint32(d[xtensaARegsOff+4*phys:])
	}
	return t, true
}

// riscvTrap finds the trap frame of the crashed task (RvExcFrame of
// rvruntime-frames.h: mepc, x1-x31, mstatus, mtvec, mcause, mtval,
// mhartid) in the saved memory by its first registers, for MCAUSE and
// MTVAL
func (c *Coredump) riscvTrap(regs map[string]uint32) (mcause, mtval uint32, ok bool) {
	pc, sp, ra := regs["PC"], regs["SP"], regs["RA"]
	for _, s := range c.mem {
		for off := 0; off+37*4 <= len(s.data); off += 4 {
			w := func(i int) uint32 { return binary.LittleEndian.Uint32(s.data[off+4*i:]) }
			if w(0) == pc && w(1) == ra && w(2) == sp {
				return w(34), w(35), true
			}
		}
	}
	return 0, 0, false
}

type note struct {
	name string
	typ  uint32
	desc []byte
}

func parseNotes(b []byte) []note {
	var out []note
	align := func(n int) int { return (n + 3) &^ 3 }
	for off := 0; off+12 <= len(b); {
		namesz := int(binary.LittleEndian.Uint32(b[off:]))
		descsz := int(binary.LittleEndian.Uint32(b[off+4:]))
		typ := binary.LittleEndian.Uint32(b[off+8:])
		p := off + 12
		if namesz < 0 || descsz < 0 || p+align(namesz) > len(b) || p+align(namesz)+descsz > len(b) {
			break
		}
		name := strings.TrimRight(string(b[p:p+namesz]), "\x00")
		p += align(namesz)
		out = append(out, note{name: name, typ: typ, desc: b[p : p+descsz]})
		off = p + align(descsz)
	}
	return out
}

// parseBinary reads the legacy binary format: after the header, for every
// task its header (TCB address, stack start, stack end), the TCB and the
// stack; then the memory segments (start, size, data). The crashed task is
// the first one. The registers of a task are in the frame at the start of
// its saved stack (pxTopOfStack).
func (c *Coredump) parseBinary(p []byte) error {
	var lastErr error
	for _, hl := range []int{cdHeaderLen, cdHeaderLenV1} {
		mem, tasks, err := binaryLayout(p, hl)
		if err != nil {
			lastErr = err
			continue
		}
		c.mem = mem
		sort.SliceStable(c.mem, func(i, j int) bool { return c.mem[i].addr < c.mem[j].addr })
		for i, bt := range tasks {
			t := Task{TCB: bt.tcb, Crashed: i == 0}
			frame := c.read(bt.top, 37*4)
			if frame == nil {
				frame = c.read(bt.top, 8*4)
			}
			if frame == nil {
				continue
			}
			var cause, vaddr uint32
			var hasCause bool
			t.Regs, cause, vaddr, hasCause = c.frameRegs(frame, bt.top)
			if t.Crashed && hasCause {
				if c.Arch == ArchXtensa {
					c.Exception = xtensaException(cause, vaddr, t.Regs["PC"])
				} else {
					c.Exception = riscvException(cause, vaddr, t.Regs["PC"])
				}
			}
			c.Tasks = append(c.Tasks, t)
		}
		return nil
	}
	return lastErr
}

type binTask struct{ tcb, top uint32 }

func binaryLayout(p []byte, hl int) ([]memSeg, []binTask, error) {
	if len(p) < hl {
		return nil, nil, errors.New("binary coredump: too short")
	}
	u := func(off int) uint32 { return binary.LittleEndian.Uint32(p[off:]) }
	ntasks, tcbSize := int(u(8)), int(u(12))
	nsegs := 0
	if hl == cdHeaderLen {
		nsegs = int(u(16))
	}
	if ntasks > 1024 || tcbSize > 4096 || nsegs > 1024 {
		return nil, nil, errors.New("binary coredump: bad header")
	}
	a4 := func(n int) int { return (n + 3) &^ 3 }
	var mem []memSeg
	var tasks []binTask
	off := hl
	for i := 0; i < ntasks; i++ {
		if off+12 > len(p) {
			return nil, nil, errors.New("binary coredump: truncated task")
		}
		tcb, start, end := u(off), u(off+4), u(off+8)
		off += 12
		lo, hi := start, end
		if hi < lo {
			lo, hi = hi, lo
		}
		slen := int(hi - lo)
		if hi-lo > 1<<20 || off+a4(tcbSize)+a4(slen) > len(p) {
			return nil, nil, errors.New("binary coredump: truncated task")
		}
		mem = append(mem, memSeg{tcb, p[off : off+tcbSize]})
		off += a4(tcbSize)
		mem = append(mem, memSeg{lo, p[off : off+slen]})
		off += a4(slen)
		tasks = append(tasks, binTask{tcb: tcb, top: lo})
	}
	for i := 0; i < nsegs; i++ {
		if off+8 > len(p) {
			return nil, nil, errors.New("binary coredump: truncated segment")
		}
		start, size := u(off), int(u(off+4))
		off += 8
		if size < 0 || off+a4(size) > len(p) {
			return nil, nil, errors.New("binary coredump: truncated segment")
		}
		mem = append(mem, memSeg{start, p[off : off+size]})
		off += a4(size)
	}
	// What follows the data is padding at most
	if len(p)-off >= 32 {
		return nil, nil, fmt.Errorf("binary coredump: %d bytes after the data", len(p)-off)
	}
	return mem, tasks, nil
}

// frameRegs takes the registers from the frame FreeRTOS saved at the top
// of a task's stack. Xtensa (xtensa_context.h): an interrupt or exception
// frame XtExcFrame (exit, pc, ps, a0-a15, sar, exccause, excvaddr) when
// its exit word is set, a solicited frame XtSolFrame (exit = 0, pc, ps,
// next, a0-a3) after a yield. RISC-V: RvExcFrame.
func (c *Coredump) frameRegs(f []byte, top uint32) (regs map[string]uint32, cause, vaddr uint32, hasCause bool) {
	w := func(i int) uint32 {
		if 4*i+4 > len(f) {
			return 0
		}
		return binary.LittleEndian.Uint32(f[4*i:])
	}
	regs = map[string]uint32{}
	if c.Arch == ArchRISCV {
		for i, name := range riscvRegNames {
			if i == 0 {
				name = "PC"
			}
			regs[name] = w(i)
		}
		if len(f) >= 36*4 {
			return regs, w(34), w(35), true
		}
		return regs, 0, 0, false
	}
	if w(0) != 0 && len(f) >= 22*4 {
		regs["PC"], regs["PS"] = w(1), w(2)
		for i := 0; i < 16; i++ {
			regs[fmt.Sprintf("A%d", i)] = w(3 + i)
		}
		regs["SAR"] = w(19)
		return regs, w(20), w(21), true
	}
	// The pc of a solicited frame is a return address with the window
	// increment in its top bits
	pc := w(1)
	if pc&0x80000000 != 0 {
		pc = xtensaCallPC(pc)
	}
	regs["PC"], regs["PS"] = pc, w(2)
	regs["A0"], regs["A1"], regs["A2"], regs["A3"] = w(4), w(5), w(6), w(7)
	if !stackSane(regs["A1"]) {
		regs["A1"] = top + 32 // sizeof(XtSolFrame)
	}
	return regs, 0, 0, false
}

// finish names the tasks, tells their states and unwinds their stacks
func (c *Coredump) finish(e *ELF) {
	if e != nil && (e.Arch() != c.Arch || c.AppSHA256 != "" && !e.MatchSHA256(c.AppSHA256)) {
		c.ELFMismatch = true
	}
	l := c.pickTCB(e.tcbLayouts(c.Arch))
	for i := range c.Tasks {
		t := &c.Tasks[i]
		t.Name, _ = c.taskName(t.TCB, l)
		t.Priority = -1
		if l.priority >= 0 {
			if v, ok := c.Read32(t.TCB + uint32(l.priority)); ok && v < 256 {
				t.Priority = int(v)
			}
		}
		if t.Crashed {
			t.State = "running"
		} else if l.stateItem >= 0 && l.container >= 0 {
			if v, ok := c.Read32(t.TCB + uint32(l.stateItem+l.container)); ok {
				ev, _ := c.Read32(t.TCB + uint32(l.eventItem+l.container))
				t.State = e.taskState(v, l.eventItem >= 0 && ev != 0)
			}
		}
		sp := t.Regs["SP"]
		if c.Arch == ArchXtensa {
			sp = t.Regs["A1"]
		}
		if s, ok := c.segment(sp); ok {
			t.StackStart, t.StackEnd = s.addr, s.addr+uint32(len(s.data))
		}
		t.Stack = c.unwind(t, e)
		for _, sf := range t.Stack {
			if sf.Frames == nil {
				t.Frames = append(t.Frames, Frame{PC: sf.PC})
				continue
			}
			t.Frames = append(t.Frames, sf.Frames...)
		}
	}
	// The crashed task first
	sort.SliceStable(c.Tasks, func(i, j int) bool { return c.Tasks[i].Crashed && !c.Tasks[j].Crashed })
}

// taskName reads the name from a TCB; false when it doesn't look like one
func (c *Coredump) taskName(tcb uint32, l tcbLayout) (string, bool) {
	b := c.read(tcb+uint32(l.name), l.nameLen)
	if b == nil {
		return "", false
	}
	j := bytes.IndexByte(b, 0)
	if j < 0 {
		return printable(b), false
	}
	b = b[:j]
	for _, ch := range b {
		if ch < 0x20 || ch >= 0x7f {
			return printable(b), false
		}
	}
	return string(b), j > 0
}

// pickTCB takes the layout that gives readable names to the most tasks
func (c *Coredump) pickTCB(layouts []tcbLayout) tcbLayout {
	best, bestN := layouts[0], -1
	for _, l := range layouts {
		n := 0
		for _, t := range c.Tasks {
			if _, ok := c.taskName(t.TCB, l); ok {
				n++
			}
		}
		if n > bestN {
			best, bestN = l, n
		}
	}
	return best
}

func (c *Coredump) unwind(t *Task, e *ELF) []StackFrame {
	if c.Arch == ArchXtensa {
		return e.UnwindXtensa(t.Regs["PC"], t.Regs["A0"], t.Regs["A1"], c)
	}
	if e == nil || e.Arch() != ArchRISCV {
		return []StackFrame{{PC: t.Regs["PC"], SP: t.Regs["SP"]}}
	}
	return e.UnwindRISCV(t.Regs, c)
}

func printable(b []byte) string {
	var sb strings.Builder
	for _, ch := range b {
		if ch >= 0x20 && ch < 0x7f {
			sb.WriteByte(ch)
		} else {
			sb.WriteByte('?')
		}
	}
	return sb.String()
}

// The exception causes of Xtensa (EXCCAUSE, Xtensa ISA reference), with
// the names ESP-IDF prints
var xtensaCauses = map[uint32]string{
	0: "IllegalInstruction", 1: "Syscall", 2: "InstructionFetchError", 3: "LoadStoreError",
	4: "Level1Interrupt", 5: "Alloca", 6: "IntegerDivideByZero", 7: "PCValue",
	8: "Privileged", 9: "LoadStoreAlignment", 12: "InstrPIFDataError", 13: "LoadStorePIFDataError",
	14: "InstrPIFAddrError", 15: "LoadStorePIFAddrError", 16: "InstTLBMiss", 17: "InstTLBMultiHit",
	18: "InstFetchPrivilege", 20: "InstrFetchProhibited", 24: "LoadStoreTLBMiss",
	25: "LoadStoreTLBMultihit", 26: "LoadStorePrivilege", 28: "LoadProhibited", 29: "StoreProhibited",
	32: "Cp0Disabled", 33: "Cp1Disabled", 34: "Cp2Disabled", 35: "Cp3Disabled",
	36: "Cp4Disabled", 37: "Cp5Disabled", 38: "Cp6Disabled", 39: "Cp7Disabled",
}

func xtensaException(cause, vaddr, pc uint32) string {
	name := xtensaCauses[cause]
	if name == "" {
		name = "exception"
	}
	return fmt.Sprintf("%s (EXCCAUSE %d) at PC 0x%08x, EXCVADDR 0x%08x", name, cause, pc, vaddr)
}

// The trap causes of RISC-V (mcause, the privileged specification)
var riscvCauses = map[uint32]string{
	0: "Instruction address misaligned", 1: "Instruction access fault", 2: "Illegal instruction",
	3: "Breakpoint", 4: "Load address misaligned", 5: "Load access fault",
	6: "Store address misaligned", 7: "Store access fault", 8: "Environment call from U-mode",
	9: "Environment call from S-mode", 11: "Environment call from M-mode",
	12: "Instruction page fault", 13: "Load page fault", 15: "Store page fault",
}

func riscvException(mcause, mtval, pc uint32) string {
	name := riscvCauses[mcause]
	if mcause&0x80000000 != 0 {
		name = fmt.Sprintf("Interrupt %d", mcause&0x7fffffff)
	}
	if name == "" {
		name = "exception"
	}
	return fmt.Sprintf("%s (MCAUSE 0x%x) at PC 0x%08x, MTVAL 0x%08x", name, mcause, pc, mtval)
}
