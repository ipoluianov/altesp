package elfdbg

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// xtensaChain is the call chain of xtensa_app.elf: the faulting store in
// crash, the calls in level2, level1 and app_main (as their lines), and the
// return addresses with the window increment of call8 in the top bits
type xtensaChain struct {
	crashPC, storePC uint32
	lines            [4]int    // crash, level2, level1, app_main
	ret              [3]uint32 // raw a0: into level2, level1, app_main
}

func xtensaFixture(t *testing.T) (*ELF, xtensaChain) {
	e := openTest(t, "xtensa_app.elf")
	src := filepath.Join("testdata", "xtensa_app.c")
	var c xtensaChain
	c.lines = [4]int{
		markerLine(t, src, "LINE_CRASH"),
		markerLine(t, src, "LINE_CALL_CRASH"),
		markerLine(t, src, "LINE_CALL_LEVEL2"),
		markerLine(t, src, "LINE_CALL_LEVEL1"),
	}
	c.crashPC = lineAddr(t, e, "xtensa_app.c", c.lines[0])
	c.storePC = lineAddr(t, e, "xtensa_app.c", markerLine(t, src, "LINE_STORE"))
	// The return address of a call is the first address of the line after it
	for i := 0; i < 3; i++ {
		c.ret[i] = 0x80000000 | lineAddr(t, e, "xtensa_app.c", c.lines[i+1]+1)&0x3fffffff
	}
	return e, c
}

func feed(d *Decoder, text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		out = append(out, d.Line(l)...)
	}
	return append(out, d.Flush()...)
}

func checkLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Fatalf("line %d: %q, want %q in it\n%s", i, got[i], want[i], strings.Join(got, "\n"))
		}
	}
}

func TestDecoderXtensa(t *testing.T) {
	e, c := xtensaFixture(t)
	// As ESP-IDF v4.4 prints it ("Fatal Errors"): the addresses of the
	// backtrace already turned into the addresses of the calls
	callPC := func(i int) uint32 { return xtensaCallPC(c.ret[i]) }
	text := fmt.Sprintf(`I (312) cpu_start: Starting scheduler on PRO CPU.
Guru Meditation Error: Core  0 panic'ed (StoreProhibited). Exception was unhandled.

Core  0 register dump:
PC      : 0x%08x  PS      : 0x00060730  A0      : 0x%08x  A1      : 0x3ffb5070
A2      : 0x00000003  A3      : 0x00000000  A4      : 0x00000001  A5      : 0x00000000
EXCVADDR: 0x00000000  LBEG    : 0x00000000  LEND    : 0x00000000  LCOUNT  : 0x00000000

Backtrace:0x%08x:0x3ffb5070 0x%08x:0x3ffb5090 0x%08x:0x3ffb50b0 0x%08x:0x3ffb50d0 |<-CORRUPTED

ELF file SHA256: %s

Rebooting...`, c.crashPC, c.ret[0], c.crashPC, callPC(0), callPC(1), callPC(2), e.SHA256()[:16])
	d := NewDecoder(e)
	got := feed(d, text)
	checkLines(t, got,
		fmt.Sprintf("0x%08x: crash at testdata/xtensa_app.c:%d", c.crashPC, c.lines[0]),
		fmt.Sprintf("level2 at testdata/xtensa_app.c:%d", c.lines[1]),
		fmt.Sprintf("level1 at testdata/xtensa_app.c:%d", c.lines[2]),
		fmt.Sprintf("app_main at testdata/xtensa_app.c:%d", c.lines[3]))

	// The same backtrace again in the same panic gives nothing; raw return
	// addresses (top bits set) are turned into calls
	d.Line("Guru Meditation Error: Core  0 panic'ed (StoreProhibited). Exception was unhandled.")
	bt := fmt.Sprintf("Backtrace: 0x%08x:0x3ffb5070 0x%08x:0x3ffb5090", c.crashPC, c.ret[0])
	checkLines(t, d.Line(bt), "crash at", fmt.Sprintf("level2 at testdata/xtensa_app.c:%d", c.lines[1]))
	checkLines(t, d.Line(bt))

	// An address alone, inside inlined code
	got = d.Line(fmt.Sprintf("abort() was called at PC 0x%08x on core 0", c.storePC))
	checkLines(t, got, fmt.Sprintf("0x%08x: store at", c.storePC), "            inlined into crash at")

	// Not the code of the .elf, or not an address
	checkLines(t, d.Line("I (100) app: PC: 0x3ffb0000"))
	checkLines(t, d.Line("I (100) app: counter: 0x400d0040"))
	checkLines(t, d.Line(fmt.Sprintf("PC: 0x%08x", c.crashPC)), "crash at")

	// Another .elf
	got = d.Line("ELF file SHA256: 0123456789abcdef")
	checkLines(t, got, "doesn't match")
}

// The ESP32-C3 fixture: crc32 (a leaf function without a frame) called by
// main, main called by _start. The addresses are from readelf of
// binutils: the FDE of main (CFA = sp+48 from 0x42000340, ra at CFA-4,
// s0 at CFA-8), the line table (0x420003c4-0x420003c7 is main.c:151) and
// the c.jal main of _start at 0x42000060.
const (
	c3CrcPC   = 0x42000210
	c3MainRA  = 0x420003c8
	c3StartRA = 0x42000062
	c3SP      = 0x3fc8f000
)

func c3Panic() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `Guru Meditation Error: Core  0 panic'ed (Load access fault). Exception was unhandled.

Core  0 register dump:
MEPC    : 0x%08x  RA      : 0x%08x  SP      : 0x%08x  GP      : 0x3fc8a600
TP      : 0x3fc8a2ac  T0      : 0x40057fa6  T1      : 0x0000000f  T2      : 0x00000000
S0/FP   : 0x00000001  S1      : 0x00000002  A0      : 0x00000001  A1      : 0x00000001
A2      : 0x00000064  A3      : 0x00000004  A4      : 0x00000001  A5      : 0x3c025f04
A6      : 0x00000000  A7      : 0x3fc8f2f0  S2      : 0x00000000  S3      : 0x00000000
S4      : 0x00000000  S5      : 0x00000000  S6      : 0x00000000  S7      : 0x00000000
S8      : 0x00000000  S9      : 0x00000000  S10     : 0x00000000  S11     : 0x00000000
T3      : 0x00000000  T4      : 0x00000000  T5      : 0x00000000  T6      : 0x00000000
MSTATUS : 0x00001881  MTVEC   : 0x40380001  MCAUSE  : 0x00000005  MTVAL   : 0x00000000
MHARTID : 0x00000000

Stack memory:
`, c3CrcPC, c3MainRA, c3SP)
	// main's frame: 48 bytes from SP; ra at +44, s0 at +40
	words := make([]uint32, 32)
	words[11] = c3StartRA
	words[10] = 0x3fc8f100
	for i := 0; i < len(words); i += 8 {
		fmt.Fprintf(&sb, "%08x:", c3SP+4*i)
		for _, w := range words[i : i+8] {
			fmt.Fprintf(&sb, " 0x%08x", w)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nELF file SHA256: ")
	return sb.String()
}

func TestDecoderRISCV(t *testing.T) {
	e := openTest(t, "esp32c3.elf")
	d := NewDecoder(e)
	text := c3Panic() + e.SHA256()[:16] + "\n\nRebooting...\n"
	var got []string
	var at int
	for i, l := range strings.Split(text, "\n") {
		x := d.Line(l)
		if len(x) > 0 && at == 0 {
			at = i
		}
		got = append(got, x...)
	}
	checkLines(t, got,
		"Backtrace by the stack dump",
		fmt.Sprintf("0x%08x: crc32 at src/main.c:", c3CrcPC),
		fmt.Sprintf("0x%08x: main at src/main.c:151", c3MainRA),
		fmt.Sprintf("0x%08x: _start", c3StartRA))
	// Right after the stack dump, at its blank line
	if lines := strings.Split(text, "\n"); lines[at] != "" || !strings.HasPrefix(lines[at-1], "3fc8f060:") {
		t.Fatalf("decoded at line %d %q", at, lines[at])
	}
	// The end of the output without a line after the dump: Flush
	d = NewDecoder(e)
	text = strings.TrimRight(strings.TrimSuffix(c3Panic(), "\nELF file SHA256: "), "\n")
	for _, l := range strings.Split(text, "\n") {
		if x := d.Line(l); len(x) > 0 {
			t.Fatalf("early output %q", x)
		}
	}
	checkLines(t, d.Flush(), "Backtrace", "crc32", "main", "_start")
	if len(d.Flush()) != 0 {
		t.Fatal("decoded twice")
	}
}

func TestDecoderRISCVBacktrace(t *testing.T) {
	// Newer ESP-IDF with the eh_frame option prints a backtrace on RISC-V:
	// no unwinding then, the return addresses are looked up before them
	e := openTest(t, "esp32c3.elf")
	d := NewDecoder(e)
	got := feed(d, fmt.Sprintf(`Guru Meditation Error: Core  0 panic'ed (Load access fault). Exception was unhandled.
Core  0 register dump:
MEPC    : 0x%08x  RA      : 0x%08x  SP      : 0x%08x  GP      : 0x3fc8a600

Backtrace: 0x%08x:0x%08x 0x%08x:0x%08x

Stack memory:
%08x: 0x00000000 0x00000000 0x00000000 0x00000000 0x00000000 0x00000000 0x00000000 0x00000000
`, c3CrcPC, c3MainRA, c3SP, c3CrcPC, c3SP, c3MainRA, c3SP, c3SP))
	checkLines(t, got, "crc32 at", fmt.Sprintf("0x%08x: main at src/main.c:151", c3MainRA))
}
