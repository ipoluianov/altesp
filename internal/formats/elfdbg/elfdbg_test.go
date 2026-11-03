package elfdbg

import (
	"bufio"
	"debug/dwarf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata:
//
//   - xtensa_app.elf: built from xtensa_app.c by build_xtensa.sh with
//     xtensa-esp32-elf-gcc 8.4.0 (PlatformIO toolchain-xtensa-esp32), -O2
//     -g: DWARF 4, an inlined function, a chain of call8.
//   - esp32c3.elf: the ESP32-C3 example of AltEmu (same author, MIT),
//     built by riscv-none-elf-gcc with newlib-nano (-O2 -g): DWARF 5 and
//     .debug_frame. It holds parts of newlib and libgcc (BSD-style
//     licenses, GCC Runtime Library Exception).

func openTest(t *testing.T, name string) *ELF {
	t.Helper()
	e, err := Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// markerLine is the line of the source that has the comment marker
func markerLine(t *testing.T, src, marker string) int {
	t.Helper()
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if strings.Contains(sc.Text(), marker) {
			return n
		}
	}
	t.Fatalf("no %s in %s", marker, src)
	return 0
}

// lineAddr is the lowest address of a line of a file by the line table
func lineAddr(t *testing.T, e *ELF, file string, line int) uint32 {
	t.Helper()
	r := e.dw.Reader()
	best := uint64(0)
	for {
		ent, err := r.Next()
		if err != nil || ent == nil {
			break
		}
		r.SkipChildren()
		lr, err := e.dw.LineReader(ent)
		if err != nil || lr == nil {
			continue
		}
		var le dwarf.LineEntry
		for lr.Next(&le) == nil {
			if le.File != nil && strings.HasSuffix(le.File.Name, file) && le.Line == line && !le.EndSequence {
				if best == 0 || le.Address < best {
					best = le.Address
				}
			}
		}
	}
	if best == 0 {
		t.Fatalf("no code for %s:%d", file, line)
	}
	return uint32(best)
}

func TestLookupXtensa(t *testing.T) {
	e := openTest(t, "xtensa_app.elf")
	if e.Arch() != ArchXtensa || !e.HasDebugInfo() {
		t.Fatalf("arch %s, debug info %v", e.Arch(), e.HasDebugInfo())
	}
	src := filepath.Join("testdata", "xtensa_app.c")
	lStore := markerLine(t, src, "LINE_STORE")
	lCrash := markerLine(t, src, "LINE_CRASH")
	// The store inlined into crash
	pc := lineAddr(t, e, "xtensa_app.c", lStore)
	fr := e.Lookup(pc)
	if len(fr) != 2 || fr[0].Func != "store" || fr[0].Line != lStore || !fr[0].Inlined ||
		fr[1].Func != "crash" || fr[1].Inlined || !strings.HasSuffix(fr[1].File, "xtensa_app.c") {
		t.Fatalf("inlined: %+v", fr)
	}
	pc = lineAddr(t, e, "xtensa_app.c", lCrash)
	fr = e.Lookup(pc)
	if len(fr) != 1 || fr[0].Func != "crash" || fr[0].Line != lCrash || fr[0].PC != pc {
		t.Fatalf("crash: %+v", fr)
	}
	// Outside the code
	if fr := e.Lookup(0x40000400); len(fr) != 1 || fr[0].Func != "" {
		t.Fatalf("ROM: %+v", fr)
	}
}

func TestLookupRISCV(t *testing.T) {
	e := openTest(t, "esp32c3.elf")
	if e.Arch() != ArchRISCV {
		t.Fatal(e.Arch())
	}
	addr, _, ok := e.Symbol("main")
	if !ok {
		t.Fatal("no main")
	}
	fr := e.Lookup(addr + 0x20)
	last := fr[len(fr)-1]
	if last.Func != "main" || !strings.HasSuffix(last.File, "main.c") || last.Line == 0 {
		t.Fatalf("main: %+v", fr)
	}
	// newlib has no debug information here: the symbol table names it
	addr, _, _ = e.Symbol("puts")
	fr = e.Lookup(addr + 2)
	if fr[len(fr)-1].Func != "puts" {
		t.Fatalf("puts: %+v", fr)
	}
	// crc32 is a local function renamed by the compiler (crc32.constprop.0)
	addr, _, _ = e.Symbol("crc32.constprop.0")
	fr = e.Lookup(addr + 4)
	if fr[len(fr)-1].Func != "crc32" {
		t.Fatalf("crc32: %+v", fr)
	}
}

func TestSHA256(t *testing.T) {
	e := openTest(t, "xtensa_app.elf")
	s := e.SHA256()
	if len(s) != 64 || !e.MatchSHA256(s[:16]) || !e.MatchSHA256(strings.ToUpper(s[:16])) || e.MatchSHA256(s[:4]) || e.MatchSHA256("0123456789abcdef") {
		t.Fatal(s)
	}
}

// TestBuildXtensa rebuilds the Xtensa fixture when the toolchain is here
// and checks that the lookups agree with the one in testdata
func TestBuildXtensa(t *testing.T) {
	gcc := os.Getenv("GCC")
	if gcc == "" {
		gcc, _ = exec.LookPath("xtensa-esp32-elf-gcc")
	}
	if gcc == "" {
		home, _ := os.UserHomeDir()
		p := filepath.Join(home, ".platformio/packages/toolchain-xtensa-esp32/bin/xtensa-esp32-elf-gcc")
		if _, err := os.Stat(p); err == nil {
			gcc = p
		}
	}
	if gcc == "" {
		t.Skip("no xtensa-esp32-elf-gcc")
	}
	dir := t.TempDir()
	src, _ := filepath.Abs(filepath.Join("testdata", "xtensa_app.c"))
	out := filepath.Join(dir, "app.elf")
	cmd := exec.Command(gcc, "-O2", "-g", "-mlongcalls", "-nostdlib", "-nostartfiles",
		"-Wl,-e,app_main", "-Wl,-Ttext=0x400d0020", "-Wl,-Tdata=0x3ffb0000", src, "-o", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	e, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	l := markerLine(t, src, "LINE_CALL_LEVEL2")
	fr := e.Lookup(lineAddr(t, e, "xtensa_app.c", l))
	if fr[len(fr)-1].Func != "level1" || fr[len(fr)-1].Line != l {
		t.Fatalf("%+v", fr)
	}
}
