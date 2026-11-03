package elfdbg

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"
)

// The coredumps here are built by the tests as ESP-IDF v4.4 lays them out
// (the structures checked against the DWARF of its libespcoredump): an ELF
// core file with the notes and the memory segments, in the flash wrapper
// of a header and a checksum. The memory is made up: stacks whose saved
// registers form the call chains of the fixtures.

type coreNote struct {
	name string
	typ  uint32
	desc []byte
}

type coreSeg struct {
	addr uint32
	data []byte
}

// buildELFCore makes an ELF core file: a PT_NOTE and a PT_LOAD per segment
func buildELFCore(machine uint16, notes []coreNote, segs []coreSeg) []byte {
	le := binary.LittleEndian
	var nb bytes.Buffer
	pad := func(b *bytes.Buffer) {
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
	}
	for _, n := range notes {
		var h [12]byte
		le.PutUint32(h[0:], uint32(len(n.name)+1))
		le.PutUint32(h[4:], uint32(len(n.desc)))
		le.PutUint32(h[8:], n.typ)
		nb.Write(h[:])
		nb.WriteString(n.name)
		nb.WriteByte(0)
		pad(&nb)
		nb.Write(n.desc)
		pad(&nb)
	}
	nph := 1 + len(segs)
	off := 52 + 32*nph
	var out bytes.Buffer
	eh := make([]byte, 52)
	copy(eh, "\x7fELF\x01\x01\x01")
	le.PutUint16(eh[16:], 4) // ET_CORE
	le.PutUint16(eh[18:], machine)
	le.PutUint32(eh[20:], 1)
	le.PutUint32(eh[28:], 52) // phoff
	le.PutUint16(eh[40:], 52) // ehsize
	le.PutUint16(eh[42:], 32) // phentsize
	le.PutUint16(eh[44:], uint16(nph))
	le.PutUint16(eh[46:], 40) // shentsize
	out.Write(eh)
	ph := func(typ, offset, vaddr, size, flags uint32) {
		var p [32]byte
		le.PutUint32(p[0:], typ)
		le.PutUint32(p[4:], offset)
		le.PutUint32(p[8:], vaddr)
		le.PutUint32(p[12:], vaddr)
		le.PutUint32(p[16:], size)
		le.PutUint32(p[20:], size)
		le.PutUint32(p[24:], flags)
		le.PutUint32(p[28:], 4)
		out.Write(p[:])
	}
	ph(4, uint32(off), 0, uint32(nb.Len()), 4) // PT_NOTE
	off += nb.Len()
	for _, s := range segs {
		ph(1, uint32(off), s.addr, uint32(len(s.data)), 6) // PT_LOAD RW
		off += len(s.data)
	}
	out.Write(nb.Bytes())
	for _, s := range segs {
		out.Write(s.data)
	}
	return out.Bytes()
}

// wrapFlash puts the core file into the flash layout: the header, the
// data, padding to 32 bytes, the checksum
func wrapFlash(version uint32, payload []byte, sha bool) []byte {
	le := binary.LittleEndian
	csLen := 4
	if sha {
		csLen = 32
	}
	body := make([]byte, 20, 20+len(payload)+64)
	body = append(body, payload...)
	for len(body)%32 != 0 {
		body = append(body, 0)
	}
	le.PutUint32(body[0:], uint32(len(body)+csLen))
	le.PutUint32(body[4:], version)
	if sha {
		s := sha256.Sum256(body)
		body = append(body, s[:]...)
	} else {
		body = le.AppendUint32(body, crc32.ChecksumIEEE(body))
	}
	// The rest of the partition is erased flash
	return append(body, bytes.Repeat([]byte{0xff}, 100)...)
}

func words(ws ...uint32) []byte {
	b := make([]byte, 0, 4*len(ws))
	for _, w := range ws {
		b = binary.LittleEndian.AppendUint32(b, w)
	}
	return b
}

func put32(b []byte, base, addr, v uint32) { binary.LittleEndian.PutUint32(b[addr-base:], v) }

// prstatus is the NT_PRSTATUS note of a task
func prstatus(tcb uint32, regs []byte) coreNote {
	d := make([]byte, 72, 72+len(regs)+4)
	binary.LittleEndian.PutUint32(d[24:], tcb)
	d = append(d, regs...)
	return coreNote{"CORE", 1, append(d, 0, 0, 0, 0)}
}

// xtensaGregs is xtensa_gregset_t with a0-a15 in ar[0..15]
func xtensaGregs(pc, a0, a1 uint32) []byte {
	g := make([]byte, 512)
	le := binary.LittleEndian
	le.PutUint32(g[0:], pc)
	le.PutUint32(g[4:], 0x00060030)
	le.PutUint32(g[24:], 1) // windowstart
	le.PutUint32(g[256:], a0)
	le.PutUint32(g[260:], a1)
	return g
}

// tcb is a TCB of ESP-IDF v4.4 on Xtensa (pcTaskName at 56, uxPriority
// at 48); the RISC-V test takes it too, to check that the layout is found
// by the names
func tcb(name string) []byte {
	b := make([]byte, 0x160)
	copy(b[56:], name)
	b[48] = 5
	return b
}

func xtensaCore(t *testing.T, e *ELF, c xtensaChain) []byte {
	const stackBase, s0 = 0x3ffb5000, 0x3ffb5070
	const tcb1, tcb2, stack2 = 0x3ffb6000, 0x3ffb6200, 0x3ffb7000
	// The base save areas: under the sp of each frame the caller's a0
	// (the return address into its caller) and a1 (its sp)
	stack := make([]byte, 0x100)
	sp := []uint32{s0, s0 + 0x20, s0 + 0x40, s0 + 0x60}
	put32(stack, stackBase, sp[0]-16, c.ret[1])
	put32(stack, stackBase, sp[0]-12, sp[1])
	put32(stack, stackBase, sp[1]-16, c.ret[2])
	put32(stack, stackBase, sp[1]-12, sp[2])
	put32(stack, stackBase, sp[2]-16, 0) // app_main is the first frame of the task
	put32(stack, stackBase, sp[2]-12, sp[3])
	info := words(0x0100)
	info = append(info, []byte(e.SHA256())...)
	info = append(info, 0, 0)
	extra := words(tcb1, 232, 29, 238, 0, 177, c.crashPC)
	level1, _, _ := e.Symbol("level1")
	// The idle task waits in a delayed list: xStateListItem.pvContainer
	idle := tcb("IDLE0")
	delayed, _, _ := e.Symbol("xDelayedTaskList1")
	binary.LittleEndian.PutUint32(idle[8+16:], delayed)
	core := buildELFCore(94, []coreNote{
		prstatus(tcb2, xtensaGregs(level1, 0, stack2+0x80)),
		prstatus(tcb1, xtensaGregs(c.crashPC, c.ret[0], s0)),
		{"ESP_CORE_DUMP_INFO", 8266, info},
		{"EXTRA_INFO", 677, extra},
	}, []coreSeg{
		{tcb1, tcb("main")}, {stackBase, stack},
		{tcb2, idle}, {stack2, make([]byte, 0x100)},
	})
	return wrapFlash(0x0100, core, false)
}

func TestCoredumpXtensaELF(t *testing.T) {
	e, c := xtensaFixture(t)
	data := xtensaCore(t, e, c)
	cd, err := ParseCoredump(data, e)
	if err != nil {
		t.Fatal(err)
	}
	if cd.Format != "ELF" || cd.Version != "1.0" || cd.Chip != "ESP32" || cd.Checksum != "CRC32" || cd.Arch != ArchXtensa || cd.ELFMismatch {
		t.Fatalf("%+v", cd)
	}
	if !strings.HasPrefix(cd.Exception, "StoreProhibited (EXCCAUSE 29)") {
		t.Fatal(cd.Exception)
	}
	if len(cd.Tasks) != 2 {
		t.Fatalf("%d tasks", len(cd.Tasks))
	}
	m := cd.Tasks[0]
	if m.Name != "main" || !m.Crashed || m.State != "running" || m.Priority != 5 || m.Regs["PC"] != c.crashPC || m.StackStart != 0x3ffb5000 {
		t.Fatalf("%+v", m)
	}
	want := []string{"crash", "level2", "level1", "app_main"}
	if len(m.Stack) != len(want) {
		t.Fatalf("%+v", m.Stack)
	}
	for i, w := range want {
		fr := m.Stack[i].Frames
		if f := fr[len(fr)-1]; f.Func != w || f.Line != c.lines[i] {
			t.Fatalf("frame %d: %+v, want %s:%d", i, f, w, c.lines[i])
		}
	}
	if m.Stack[1].SP != 0x3ffb5090 || len(m.Frames) != 4 {
		t.Fatalf("%+v", m.Stack)
	}
	idle := cd.Tasks[1]
	if idle.Name != "IDLE0" || idle.State != "blocked" || idle.Crashed || len(idle.Stack) != 1 || idle.Frames[0].Func != "level1" {
		t.Fatalf("%+v", idle)
	}

	// Printed to the UART instead
	var sb strings.Builder
	sb.WriteString("I (100) esp_core_dump_uart: Press Enter to print core dump to UART...\n")
	sb.WriteString("================= CORE DUMP START =================\n")
	raw := data[:binary.LittleEndian.Uint32(data)]
	b64 := base64.StdEncoding.EncodeToString(raw)
	for len(b64) > 0 {
		n := min(len(b64), 76)
		sb.WriteString(b64[:n] + "\r\n")
		b64 = b64[n:]
	}
	sb.WriteString("================= CORE DUMP END ===================\n")
	cd2, err := ParseCoredump([]byte(sb.String()), e)
	if err != nil || len(cd2.Tasks) != 2 || cd2.Exception != cd.Exception {
		t.Fatalf("UART: %v %+v", err, cd2)
	}

	// Damaged, empty, another .elf
	bad := append([]byte(nil), data...)
	bad[200] ^= 1
	if _, err := ParseCoredump(bad, e); !errors.Is(err, ErrChecksum) {
		t.Fatalf("damaged: %v", err)
	}
	if _, err := ParseCoredump(bytes.Repeat([]byte{0xff}, 4096), e); !errors.Is(err, ErrNoCoredump) {
		t.Fatalf("empty: %v", err)
	}
	other := openTest(t, "esp32c3.elf")
	if cd, err := ParseCoredump(data, other); err != nil || !cd.ELFMismatch {
		t.Fatalf("mismatch: %v", err)
	}
	// Without an .elf: the addresses only
	cd, err = ParseCoredump(data, nil)
	if err != nil || len(cd.Tasks[0].Stack) != 4 || cd.Tasks[0].Frames[0].Func != "" {
		t.Fatalf("no elf: %v %+v", err, cd)
	}
}

func TestCoredumpRISCVELF(t *testing.T) {
	e := openTest(t, "esp32c3.elf")
	const tcb1, stackBase = 0x3fc8e000, 0x3fc8ef00
	stack := make([]byte, 0x200)
	// main's frame above c3SP; the trap frame (RvExcFrame) under it
	put32(stack, stackBase, c3SP+44, c3StartRA)
	frame := uint32(c3SP - 37*4 - 12)
	put32(stack, stackBase, frame, c3CrcPC)
	put32(stack, stackBase, frame+4, c3MainRA)
	put32(stack, stackBase, frame+8, c3SP)
	put32(stack, stackBase, frame+34*4, 5)    // mcause: load access fault
	put32(stack, stackBase, frame+35*4, 0x10) // mtval
	regs := make([]byte, 128)
	binary.LittleEndian.PutUint32(regs[0:], c3CrcPC)
	binary.LittleEndian.PutUint32(regs[4:], c3MainRA)
	binary.LittleEndian.PutUint32(regs[8:], c3SP)
	info := append(words(0x00050101), []byte(e.SHA256())...)
	core := buildELFCore(243, []coreNote{
		prstatus(tcb1, regs),
		{"ESP_CORE_DUMP_INFO", 8266, append(info, 0, 0)},
		{"EXTRA_INFO", 677, words(tcb1)},
	}, []coreSeg{{tcb1, tcb("main")}, {stackBase, stack}})
	cd, err := ParseCoredump(wrapFlash(0x00050101, core, true), e)
	if err != nil {
		t.Fatal(err)
	}
	if cd.Chip != "ESP32-C3" || cd.Checksum != "SHA256" || cd.Version != "1.1" || cd.Arch != ArchRISCV {
		t.Fatalf("%+v", cd)
	}
	if cd.Exception != fmt.Sprintf("Load access fault (MCAUSE 0x5) at PC 0x%08x, MTVAL 0x00000010", c3CrcPC) {
		t.Fatal(cd.Exception)
	}
	task := cd.Tasks[0]
	var names []string
	for _, f := range task.Frames {
		names = append(names, f.Func)
	}
	if task.Name != "main" || strings.Join(names, " ") != "crc32 main _start" {
		t.Fatalf("%s: %v", task.Name, names)
	}
	// A bare ELF core file, as esp-coredump saves it
	cd, err = ParseCoredump(core, e)
	if err != nil || cd.Checksum != "" || len(cd.Tasks[0].Frames) != 3 {
		t.Fatalf("bare: %v %+v", err, cd)
	}
}

func TestCoredumpXtensaBinary(t *testing.T) {
	e, c := xtensaFixture(t)
	le := binary.LittleEndian
	const s0, tcb1, tcb2 = 0x3ffb5070, 0x3ffb6000, 0x3ffb6200
	// The crashed task: an exception frame (XtExcFrame) at the top of the
	// stack, then the stack with the base save areas
	top := uint32(s0 - 0x80)
	stack := make([]byte, 0x100)
	put32(stack, top, top, 0x40081234) // exit
	put32(stack, top, top+4, c.crashPC)
	put32(stack, top, top+8, 0x00060030)
	put32(stack, top, top+12, c.ret[0]) // a0
	put32(stack, top, top+16, s0)       // a1
	put32(stack, top, top+80, 29)       // exccause
	put32(stack, top, top+84, 0)        // excvaddr
	put32(stack, top, s0-16, 0)
	put32(stack, top, s0-12, s0+0x20)
	// The other task: a solicited frame
	sol := make([]byte, 0x40)
	level1, _, _ := e.Symbol("level1")
	le.PutUint32(sol[4:], 0x80000000|(level1+8)&0x3fffffff)
	le.PutUint32(sol[20:], 0x3ffb7040)
	var p []byte
	p = append(p, words(0, 0x0002, 2, 0x160, 0)...)
	p = append(p, words(tcb1, top, top+uint32(len(stack)))...)
	p = append(p, tcb("main")...)
	p = append(p, stack...)
	p = append(p, words(tcb2, 0x3ffb7000, 0x3ffb7040)...)
	p = append(p, tcb("IDLE1")...)
	p = append(p, sol...)
	le.PutUint32(p[0:], uint32(len(p)+4))
	p = le.AppendUint32(p, crc32.ChecksumIEEE(p))
	cd, err := ParseCoredump(p, e)
	if err != nil {
		t.Fatal(err)
	}
	if cd.Format != "binary" || cd.Version != "0.2" || len(cd.Tasks) != 2 || !strings.HasPrefix(cd.Exception, "StoreProhibited") {
		t.Fatalf("%+v", cd)
	}
	m := cd.Tasks[0]
	if m.Name != "main" || len(m.Stack) != 2 || m.Frames[0].Func != "crash" || m.Frames[1].Func != "level2" {
		t.Fatalf("%+v", m)
	}
	idle := cd.Tasks[1]
	if idle.Name != "IDLE1" || idle.Regs["A1"] != 0x3ffb7040 || idle.Frames[0].Func != "level1" {
		t.Fatalf("%+v", idle)
	}
}

func TestCoredumpRISCVBinary(t *testing.T) {
	e := openTest(t, "esp32c3.elf")
	le := binary.LittleEndian
	// The trap frame (RvExcFrame) at the top of the saved stack, then main's
	// frame from c3SP
	top := uint32(c3SP - 0xa0)
	stack := make([]byte, 0x100)
	put32(stack, top, top, c3CrcPC)
	put32(stack, top, top+4, c3MainRA)
	put32(stack, top, top+8, c3SP)
	put32(stack, top, top+34*4, 2) // mcause: illegal instruction
	put32(stack, top, c3SP+44, c3StartRA)
	var p []byte
	p = append(p, words(0, 0x00050002, 1, 0x160, 0)...)
	p = append(p, words(0x3fc8e000, top, top+uint32(len(stack)))...)
	p = append(p, tcb("main")...)
	p = append(p, stack...)
	le.PutUint32(p[0:], uint32(len(p)+4))
	p = le.AppendUint32(p, crc32.ChecksumIEEE(p))
	cd, err := ParseCoredump(p, e)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range cd.Tasks[0].Frames {
		names = append(names, f.Func)
	}
	if cd.Arch != ArchRISCV || cd.Chip != "ESP32-C3" || !strings.HasPrefix(cd.Exception, "Illegal instruction") ||
		strings.Join(names, " ") != "crc32 main _start" {
		t.Fatalf("%+v %v", cd, names)
	}
}
