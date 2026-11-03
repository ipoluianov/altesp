package elfdbg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The panic output of ESP-IDF ("Fatal Errors" of the Programming Guide),
// as the monitor shows it line by line:
//
//	Guru Meditation Error: Core  0 panic'ed (LoadProhibited). Exception was unhandled.
//	Core  0 register dump:
//	PC      : 0x400d0b4e  PS      : 0x00060730  A0      : 0x800d0b6c  A1      : 0x3ffb5070
//	...
//	Backtrace: 0x400d0b4b:0x3ffb5070 0x400d0b69:0x3ffb5090 0x40087f45:0x3ffb50b0
//
// Xtensa chips print the backtrace: pairs of a code address and a stack
// pointer, the first the place of the exception, the others the calls (the
// chip has already turned the return addresses into the addresses of the
// calls). RISC-V chips print the register dump (MEPC, RA, SP, ...) and
// "Stack memory:", lines of an address and eight words; the stack is
// unwound here from them. Newer ESP-IDF may print a backtrace on RISC-V
// too (eh_frame or frame pointer options): it is decoded as is.
//
// The decoded lines come once per panic, after the line that completes
// what they decode: the backtrace line, or the end of the stack dump.

var (
	reRegPair    = regexp.MustCompile(`([A-Z][A-Z0-9]*(?:/[A-Z0-9]+)?)\s*:\s*0x([0-9a-fA-F]{1,8})\b`)
	reStackLine  = regexp.MustCompile(`^\s*([0-9a-fA-F]{8}):((?:\s+0x[0-9a-fA-F]{8})+)\s*$`)
	reBTEntry    = regexp.MustCompile(`0x([0-9a-fA-F]{8}):0x([0-9a-fA-F]{8})`)
	reAbortPC    = regexp.MustCompile(`(?:abort\(\) was called at PC|Setting breakpoint at|PC(?: is at)?)\s*:?\s*0x([0-9a-fA-F]{8})`)
	reELFSHA     = regexp.MustCompile(`ELF file SHA256:\s*([0-9a-fA-F]{8,64})`)
	panicStarts  = []string{"Guru Meditation Error", "abort() was called", "assert failed", "***ERROR***", "Stack smashing protect failure", "CPU halted"}
	bootMarkers  = []string{"ets ", "ESP-ROM:", "rst:0x", "Rebooting..."}
	maxStackDump = 1 << 16 // words of a stack dump kept
)

// Decoder decodes the panics in the lines of the monitor. It is fed with
// every line, without the line ending and the ANSI escapes, and gives the
// lines to show under it. Not safe for concurrent use.
type Decoder struct {
	e *ELF

	regs    map[string]uint32
	inDump  bool // in a register dump
	inStack bool // in "Stack memory:"
	stack   MemMap
	pending bool // a stack dump to unwind
	// decoded are the backtraces and addresses of this panic already
	// decoded, so that a repeated line gives nothing
	decoded   map[string]bool
	backtrace bool // this panic had a backtrace
}

// NewDecoder decodes with the .elf of the application
func NewDecoder(e *ELF) *Decoder {
	d := &Decoder{e: e}
	d.reset()
	return d
}

func (d *Decoder) reset() {
	d.regs = map[string]uint32{}
	d.inDump, d.inStack, d.pending, d.backtrace = false, false, false, false
	d.stack = MemMap{}
	d.decoded = map[string]bool{}
}

// Line takes a line of the monitor and gives the decoded lines to show
// after it, or nothing
func (d *Decoder) Line(line string) (extra []string) {
	line = strings.TrimRight(line, "\r\n \t")
	trimmed := strings.TrimSpace(line)

	if d.inStack {
		if m := reStackLine.FindStringSubmatch(line); m != nil {
			d.addStackLine(m[1], m[2])
			return nil
		}
		d.inStack = false
		extra = append(extra, d.Flush()...)
	}

	for _, s := range panicStarts {
		if strings.Contains(line, s) {
			extra = append(extra, d.Flush()...)
			d.reset()
			break
		}
	}
	for _, s := range bootMarkers {
		if strings.HasPrefix(trimmed, s) {
			extra = append(extra, d.Flush()...)
			d.reset()
			break
		}
	}

	switch {
	case strings.Contains(line, "register dump:"):
		d.inDump = true
		d.regs = map[string]uint32{}
		return extra
	case strings.HasPrefix(trimmed, "Stack memory:"):
		d.inDump = false
		d.inStack = true
		d.stack = MemMap{}
		return extra
	case trimmed == "":
		d.inDump = false
		return extra
	}

	if i := strings.Index(line, "Backtrace:"); i >= 0 {
		d.inDump = false
		return append(extra, d.decodeBacktrace(line[i+len("Backtrace:"):])...)
	}
	if m := reELFSHA.FindStringSubmatch(line); m != nil {
		if !d.e.MatchSHA256(m[1]) {
			key := "sha:" + m[1]
			if !d.decoded[key] {
				d.decoded[key] = true
				extra = append(extra, fmt.Sprintf("The .elf doesn't match the running application (its SHA256 is %s): the decoded lines may be wrong", d.e.SHA256()[:16]))
			}
		}
		return extra
	}

	pairs := reRegPair.FindAllStringSubmatch(line, -1)
	if d.inDump {
		for _, p := range pairs {
			v, _ := strconv.ParseUint(p[2], 16, 32)
			d.regs[p[1]] = uint32(v)
		}
		return extra
	}
	if len(pairs) > 1 {
		// Registers outside a dump: nothing to decode alone
		return extra
	}
	// An address alone: "abort() was called at PC 0x...", "PC: 0x...";
	// only an address of the code of the .elf, not to decode any number
	// of a log line
	if m := reAbortPC.FindStringSubmatch(line); m != nil {
		v, _ := strconv.ParseUint(m[1], 16, 32)
		key := "pc:" + m[1]
		if fr := d.e.Lookup(uint32(v)); !d.decoded[key] && fr[len(fr)-1].Func != "" && d.e.IsCode(uint32(v)) {
			d.decoded[key] = true
			extra = append(extra, FormatFrames(fr)...)
		}
	}
	return extra
}

// Flush gives what is waiting for the end of a stack dump: for when the
// monitor saw no line after it for a while
func (d *Decoder) Flush() []string {
	d.inStack = false
	if !d.pending {
		return nil
	}
	d.pending = false
	if d.backtrace || d.decoded["unwound"] || d.e.Arch() != ArchRISCV {
		return nil
	}
	frames := d.e.UnwindRISCV(d.regs, d.stack)
	if len(frames) == 0 {
		return nil
	}
	d.decoded["unwound"] = true
	out := []string{"Backtrace by the stack dump:"}
	for _, f := range frames {
		out = append(out, FormatFrames(f.Frames)...)
	}
	return out
}

func (d *Decoder) addStackLine(addr, words string) {
	a, err := strconv.ParseUint(addr, 16, 32)
	if err != nil || len(d.stack) > maxStackDump {
		return
	}
	for i, w := range strings.Fields(words) {
		v, err := strconv.ParseUint(strings.TrimPrefix(w, "0x"), 16, 32)
		if err != nil {
			return
		}
		d.stack[uint32(a)+uint32(4*i)] = uint32(v)
	}
	if _, ok := d.regs["SP"]; ok {
		d.pending = true
	}
}

func (d *Decoder) decodeBacktrace(s string) []string {
	key := "bt:" + strings.TrimSpace(s)
	if d.decoded[key] {
		return nil
	}
	d.decoded[key] = true
	d.backtrace = true
	d.pending = false
	var out []string
	for i, m := range reBTEntry.FindAllStringSubmatch(s, -1) {
		v, _ := strconv.ParseUint(m[1], 16, 32)
		pc := uint32(v)
		if pc == 0 {
			continue
		}
		var fr []Frame
		switch {
		case d.e.Arch() == ArchXtensa && pc&0x80000000 != 0:
			// A raw return address, not yet the address of its call
			pc = xtensaCallPC(pc)
			fr = d.e.Lookup(pc)
		case d.e.Arch() == ArchRISCV && i > 0:
			// A return address: the call is before it
			fr = d.e.Lookup(pc - 1)
			for j := range fr {
				fr[j].PC = pc
			}
		default:
			fr = d.e.Lookup(pc)
		}
		out = append(out, FormatFrames(fr)...)
	}
	return out
}

// FormatFrames gives the lines of a lookup:
//
//	0x400d0b4b: store at main/app.c:10
//	            inlined into crash at main/app.c:15
func FormatFrames(frames []Frame) []string {
	var out []string
	for i, f := range frames {
		if i == 0 {
			out = append(out, fmt.Sprintf("0x%08x: %s", f.PC, f))
			continue
		}
		out = append(out, fmt.Sprintf("            inlined into %s", f))
	}
	return out
}
