// Package elfdbg decodes what an ESP chip leaves behind when it crashes,
// with the .elf of its application: the panic printed to the monitor and
// the coredump saved to flash or printed to the UART. It maps addresses to
// functions, files and lines (with the inlined calls), and unwinds stacks:
// the register windows of Xtensa and the DWARF call frame information of
// RISC-V.
//
// Sources: the DWARF 4 and 5 specifications (line tables, debugging
// information entries, call frame information), the Xtensa ISA reference
// (windowed register ABI), the RISC-V psABI (register numbering), the
// ESP-IDF Programming Guide "Fatal Errors" and "Core Dump", and the
// Apache-2.0 sources and headers of ESP-IDF: components/espcoredump
// (esp_core_dump_types.h, core_dump_elf.c, the port headers), the FreeRTOS
// port frames (xtensa_context.h, rvruntime-frames.h) and esp_cpu.h. The
// coredump structures were checked against the debug information of the
// libespcoredump of ESP-IDF v4.4.7 (Arduino core 2.0.17). No code of
// idf.py monitor, esp-coredump, gdb or binutils is used.
package elfdbg

import (
	"bytes"
	"crypto/sha256"
	"debug/dwarf"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// Frame is a place in the code: a function, a file and a line. An address
// inside inlined code gives several frames: the inlined function first,
// then the functions it was inlined into.
type Frame struct {
	PC   uint32
	Func string // empty when nothing is known about the address
	File string
	Line int
	// Inlined is set when this function is inlined into the next frame,
	// whose File and Line are then the place of the inlined call
	Inlined bool
}

// String gives the frame as "func at file:line"
func (f Frame) String() string {
	name := f.Func
	if name == "" {
		name = "??"
	}
	if f.File == "" {
		return name
	}
	return fmt.Sprintf("%s at %s:%d", name, f.File, f.Line)
}

// Architectures of Arch
const (
	ArchXtensa = "xtensa"
	ArchRISCV  = "riscv"
)

// ErrNotESP is an ELF file that is not for a 32-bit Xtensa or RISC-V chip
var ErrNotESP = errors.New("not an ELF of an ESP application (Xtensa or RISC-V, 32-bit)")

// ELF is the .elf of an application, open for lookups. It is safe for
// concurrent use.
type ELF struct {
	f    *elf.File
	arch string
	sha  [32]byte

	syms   []symbol
	byName map[string]symbol
	exec   []span
	alloc  []span

	dw    *dwarf.Data
	dwErr error

	mu      sync.Mutex
	cuOnce  sync.Once
	cus     []cuRange
	cuCache map[dwarf.Offset]*cuInfo
	names   map[dwarf.Offset]string

	cfiOnce sync.Once
	cfi     *cfiTable

	tcbOnce sync.Once
	tcb     tcbLayout
}

type span struct{ lo, hi uint32 }

func inSpans(s []span, a uint32) bool {
	for _, x := range s {
		if a >= x.lo && a < x.hi {
			return true
		}
	}
	return false
}

// Open reads the .elf of an application
func Open(path string) (*ELF, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	e, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return e, nil
}

// Parse reads the .elf of an application from memory. The data is kept.
func Parse(data []byte) (*ELF, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if f.Class != elf.ELFCLASS32 || f.Data != elf.ELFDATA2LSB {
		return nil, ErrNotESP
	}
	e := &ELF{f: f, sha: sha256.Sum256(data)}
	switch f.Machine {
	case elf.EM_XTENSA:
		e.arch = ArchXtensa
	case elf.EM_RISCV:
		e.arch = ArchRISCV
	default:
		return nil, ErrNotESP
	}
	for _, s := range f.Sections {
		if s.Flags&elf.SHF_ALLOC == 0 || s.Type == elf.SHT_NOBITS || s.Size == 0 {
			continue
		}
		sp := span{uint32(s.Addr), uint32(s.Addr + s.Size)}
		e.alloc = append(e.alloc, sp)
		if s.Flags&elf.SHF_EXECINSTR != 0 {
			e.exec = append(e.exec, sp)
		}
	}
	e.loadSymbols()
	e.dw, e.dwErr = f.DWARF()
	e.cuCache = map[dwarf.Offset]*cuInfo{}
	e.names = map[dwarf.Offset]string{}
	return e, nil
}

// Arch is ArchXtensa or ArchRISCV
func (e *ELF) Arch() string { return e.arch }

// SHA256 is the SHA-256 of the file in hex: what ESP-IDF keeps in the
// app description (esp_app_desc_t.app_elf_sha256) and prints at a panic
// ("ELF file SHA256:", the first 16 hex digits by default)
func (e *ELF) SHA256() string { return hex.EncodeToString(e.sha[:]) }

// MatchSHA256 tells whether s, a prefix of the SHA-256 in hex as the chip
// prints it, is of this file. Fewer than 8 digits match nothing.
func (e *ELF) MatchSHA256(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	return strings.HasPrefix(e.SHA256(), s)
}

// HasDebugInfo tells whether the file has DWARF: without it lookups give
// only the function names of the symbol table
func (e *ELF) HasDebugInfo() bool { return e.dw != nil }

// IsCode tells whether the address is in the code of the file
func (e *ELF) IsCode(pc uint32) bool { return inSpans(e.exec, pc) }

// read32 reads a word of the loaded sections of the file
func (e *ELF) read32(addr uint32) (uint32, bool) {
	for _, s := range e.f.Sections {
		if s.Flags&elf.SHF_ALLOC == 0 || s.Type == elf.SHT_NOBITS {
			continue
		}
		if uint64(addr) >= s.Addr && uint64(addr)+4 <= s.Addr+s.Size {
			var b [4]byte
			if _, err := s.ReadAt(b[:], int64(uint64(addr)-s.Addr)); err != nil {
				return 0, false
			}
			return binary.LittleEndian.Uint32(b[:]), true
		}
	}
	return 0, false
}

// symbol is a function of the symbol table
type symbol struct {
	addr, size uint32
	name       string
	fn         bool // STT_FUNC
}

func (e *ELF) loadSymbols() {
	syms, _ := e.f.Symbols()
	e.byName = map[string]symbol{}
	for _, s := range syms {
		if _, dup := e.byName[s.Name]; !dup && s.Section != elf.SHN_UNDEF && s.Name != "" {
			e.byName[s.Name] = symbol{addr: uint32(s.Value), size: uint32(s.Size), name: s.Name}
		}
		t := elf.ST_TYPE(s.Info)
		if s.Value == 0 || s.Name == "" || s.Section == elf.SHN_UNDEF || s.Section >= elf.SHN_LORESERVE {
			continue
		}
		// Mapping symbols of RISC-V ($x, $d) and local labels
		if strings.HasPrefix(s.Name, "$") || strings.HasPrefix(s.Name, ".L") {
			continue
		}
		addr := uint32(s.Value)
		switch {
		case t == elf.STT_FUNC:
		case t == elf.STT_NOTYPE && inSpans(e.exec, addr):
		default:
			continue
		}
		e.syms = append(e.syms, symbol{addr: addr, size: uint32(s.Size), name: s.Name, fn: t == elf.STT_FUNC})
	}
	sort.SliceStable(e.syms, func(i, j int) bool {
		a, b := e.syms[i], e.syms[j]
		if a.addr != b.addr {
			return a.addr < b.addr
		}
		// Functions before labels, global names before the local ones
		return a.fn && !b.fn
	})
}

// symbolAt is the function of the symbol table at the address
func (e *ELF) symbolAt(pc uint32) (symbol, bool) {
	i := sort.Search(len(e.syms), func(i int) bool { return e.syms[i].addr > pc }) - 1
	for ; i >= 0; i-- {
		s := e.syms[i]
		if s.size > 0 && pc >= s.addr+s.size {
			// A function before ends here; a label with no size may cover
			// the address, but not past the end of its section
			continue
		}
		if s.size == 0 && !e.sameSection(s.addr, pc) {
			return symbol{}, false
		}
		// The first of the symbols at this address
		for i > 0 && e.syms[i-1].addr == s.addr && (e.syms[i-1].size == 0 || pc < s.addr+e.syms[i-1].size) {
			i--
			s = e.syms[i]
		}
		return s, true
	}
	return symbol{}, false
}

func (e *ELF) sameSection(a, b uint32) bool {
	for _, s := range e.exec {
		if a >= s.lo && a < s.hi {
			return b >= s.lo && b < s.hi
		}
	}
	return false
}

// Symbol gives the address and size of a symbol of the file
func (e *ELF) Symbol(name string) (addr, size uint32, ok bool) {
	sym, ok := e.byName[name]
	return sym.addr, sym.size, ok
}

// Lookup maps a code address to the frames there: the innermost inlined
// function first and the function that holds the code last. It always
// gives at least one frame; an unknown address has an empty Func.
func (e *ELF) Lookup(pc uint32) []Frame {
	frames := e.dwarfLookup(pc)
	if len(frames) == 0 {
		frames = []Frame{{PC: pc}}
	}
	if frames[len(frames)-1].Func == "" {
		if s, ok := e.symbolAt(pc); ok {
			frames[len(frames)-1].Func = s.name
		}
	}
	for i := range frames {
		frames[i].PC = pc
	}
	return frames
}
