// Package efuse reads, decodes and burns the eFuses of the ESP chips
// through the register access of the ROM loader or the stub (READ_REG and
// WRITE_REG): the field tables of every chip, the decoding of a dump with
// the write and read protection of each field, the planning of a burn
// (OR-only, protection, coding schemes) and the burn itself by the eFuse
// controller's registers, verified by reading back.
//
// Sources, all Apache-2.0 parts of ESP-IDF at commit 185194117695 of
// master (2026-11-02), and the ESP32-C3 TRM v1.4 chapter 4 "eFuse
// Controller":
//   - the fields: components/efuse/<chip>/esp_efuse_table*.csv and the
//     key purposes of components/efuse/<chip>/include/esp_efuse_chip.h,
//     generated into tables.go by gen/main.go;
//   - the registers: components/soc/<chip>/register/soc/efuse_reg.h and
//     reg_base.h (layout.go);
//   - the programming sequence: components/hal/<chip>/efuse_hal.c and
//     include/hal/efuse_ll.h, components/efuse/src/esp_efuse_utility.c
//     (WR_DIS burned last) and components/efuse/<chip>/esp_efuse_utility.c
//     (blocks from the last to BLOCK0, retries, read-back check, the
//     coding of the ESP32's 3/4 and repeat schemes, the RS check registers);
//   - the coding schemes: esp_efuse_utility.c of the ESP32 (3/4, repeat)
//     and the ESP32-C3 TRM 4.3.1.3 for RS(44,32) and its primitive
//     polynomial.
//
// espefuse and esptool (GPL) were not used. Facts these sources don't
// settle are marked "unverified:" and listed in TZ.md under "Hardware
// checks". All of the writing is tested against Model, an in-memory eFuse
// controller; nothing here has burned a real chip.
package efuse

//go:generate go run ./gen -commit 185194117695d717f0c13000516fc007f5430bfb -date 2026-11-02 -out tables.go

import (
	"fmt"
	"strings"

	"github.com/ipoluianov/altesp/internal/chips"
)

// The categories of the fields
const (
	CatProtection  = "protection"
	CatMAC         = "MAC"
	CatKeys        = "keys"
	CatSecurity    = "security"
	CatVersion     = "version"
	CatFlash       = "flash/PSRAM"
	CatCalibration = "calibration"
	CatUser        = "user data"
	CatConfig      = "config"
)

// Part is a run of bits of a field: bits [Bit, Bit+Bits) of a block
type Part struct{ Block, Bit, Bits int }

// Field is a field of esp_efuse_table.csv. Most are one run of bits; the
// factory MAC is six parts of 8 bits, its first byte first. Block and Bit
// are those of the first part, Bits the length of all of them.
type Field struct {
	Name     string   // "KEY_PURPOSE_0", "WR_DIS.BLOCK_KEY0"
	Aliases  []string // other names of the table: "KEY0_PURPOSE"
	Desc     string
	Category string
	Block    int
	Bit      int
	Bits     int
	Parts    []Part
	// Values names the values where the table does: 1: "Enable"
	Values map[uint64]string
}

// Fields are the eFuse fields of a chip of that revision; nil for a chip
// without a table
func Fields(chip *chips.Chip, rev chips.Revision) []Field {
	t := tableFor(chip, rev)
	if t == nil {
		return nil
	}
	out := make([]Field, len(t.fields))
	for i := range t.fields {
		out[i] = t.fields[i].export()
	}
	return out
}

// The generated tables (tables.go)

type part struct{ blk, bit, n int }

type fieldDef struct {
	name, cat, desc string
	aliases         []string
	parts           []part
	enum            map[uint64]string
}

// protRule is a bit of WR_DIS or RD_DIS (its number in BLOCK0) and the
// bits [lo, hi) of block blk it protects
type protRule struct {
	bit, blk, lo, hi int
	target           string
}

type tableDef struct {
	key            string
	minRev, maxRev int
	fields         []fieldDef
	wr, rd         []protRule
}

func (f *fieldDef) bits() int {
	n := 0
	for _, p := range f.parts {
		n += p.n
	}
	return n
}

func (f *fieldDef) export() Field {
	out := Field{Name: f.name, Desc: f.desc, Category: f.cat, Block: f.parts[0].blk,
		Bit: f.parts[0].bit, Bits: f.bits(), Values: f.enum}
	out.Aliases = append(out.Aliases, f.aliases...)
	for _, p := range f.parts {
		out.Parts = append(out.Parts, Part{p.blk, p.bit, p.n})
	}
	return out
}

func (f *fieldDef) is(name string) bool {
	if strings.EqualFold(f.name, name) {
		return true
	}
	for _, a := range f.aliases {
		if strings.EqualFold(a, name) {
			return true
		}
	}
	return false
}

func tableFor(chip *chips.Chip, rev chips.Revision) *tableDef {
	if chip == nil {
		return nil
	}
	full := rev.Full()
	for _, t := range tableList {
		if t.key == chip.Key && full >= t.minRev && full <= t.maxRev {
			return t
		}
	}
	return nil
}

func (t *tableDef) find(name string) *fieldDef {
	for i := range t.fields {
		if t.fields[i].is(name) {
			return &t.fields[i]
		}
	}
	return nil
}

// overlaps: the field has a bit in [lo, hi) of block blk
func (f *fieldDef) overlaps(blk, lo, hi int) bool {
	for _, p := range f.parts {
		if p.blk == blk && p.bit < hi && p.bit+p.n > lo {
			return true
		}
	}
	return false
}

// protectedBy are the bits of WR_DIS (or RD_DIS) that protect the field
func (f *fieldDef) protectedBy(rs []protRule) []int {
	var bits []int
	for _, r := range rs {
		if f.overlaps(r.blk, r.lo, r.hi) {
			dup := false
			for _, b := range bits {
				dup = dup || b == r.bit
			}
			if !dup {
				bits = append(bits, r.bit)
			}
		}
	}
	return bits
}

// errNoChip is a chip without an eFuse table or controller layout
func errNoChip(chip *chips.Chip) error {
	if chip == nil {
		return fmt.Errorf("no chip")
	}
	return fmt.Errorf("%s: the eFuse layout is not known", chip.Name)
}
