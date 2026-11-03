package efuse

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"sort"
	"strings"

	"github.com/ipoluianov/altesp/internal/chips"
)

// Change is a value to burn into a field: Value holds its bits as
// Value.Raw does, the first bit in bit 0 of Value[0]. Bits that are 1 now
// must stay 1: a change can only add bits.
type Change struct {
	Field string
	Value []byte
}

// Uint is a change of a field of up to 64 bits
func Uint(field string, v uint64) Change {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return Change{Field: field, Value: b}
}

// BlockWrite is what a burn writes to a block
type BlockWrite struct {
	Block  int
	Coding Coding
	// Old and New are the data words of the block before and after;
	// Set are the bits to program (New &^ Old)
	Old, New, Set []uint32
}

// Plan is a checked burn: what changes, why it is dangerous, and the words
// to program. It is made by PlanBurn and carried out by Burn.
type Plan struct {
	// Changes are "FIELD: old → new"
	Changes []string
	// Warnings are the consequences that can't be undone
	Warnings []string
	// Blocks are the blocks to be burned, in the order of the burn
	Blocks []int
	Writes []BlockWrite

	chip *chips.Chip
	rev  chips.Revision
	t    *tableDef
	l    *layout
	old  [][]uint32
	new  [][]uint32
	// wrDis are the bits of WR_DIS (word 0 of BLOCK0) that are burned
	// separately, after everything else (esp_efuse_utility_burn_chip of
	// components/efuse/src/esp_efuse_utility.c)
	wrDis uint32
}

// Empty: nothing to burn
func (p *Plan) Empty() bool { return len(p.Writes) == 0 }

// RefusedError lists why a burn is refused
type RefusedError struct{ Reasons []string }

func (e *RefusedError) Error() string {
	return "the eFuse burn is refused: " + strings.Join(e.Reasons, "; ")
}

// PlanBurn checks the changes against the dump and computes the blocks to
// burn. All changes of a block go into one burn of it. It refuses: unknown
// fields, values that don't fit, bits set back to 0, write-protected
// fields, read-protected blocks (their contents are unknown and can't be
// verified), and what the coding scheme can't take: on the ESP32 bits
// beyond the 3/4 (192) or repeat (128) data and a 3/4 group of 6 bytes
// that already holds data; on the later chips an RS-coded block (BLOCK1
// and up) that already holds data, since its check bytes cover the whole
// block (the TRMs: "each block can only be written once").
func PlanBurn(d *Dump, changes []Change) (*Plan, error) {
	if d == nil || d.t == nil || d.l == nil {
		return nil, fmt.Errorf("no eFuse dump")
	}
	t, l := d.t, d.l
	p := &Plan{chip: d.Chip, rev: d.Revision, t: t, l: l, old: copyBlocks(d.Blocks), new: copyBlocks(d.Blocks)}
	want0 := copyBlocks(d.Blocks)
	for _, b := range want0 {
		clear(b)
	}
	var refused []string
	refuse := func(f string, a ...any) { refused = append(refused, fmt.Sprintf(f, a...)) }

	for _, c := range changes {
		f := t.find(c.Field)
		if f == nil {
			refuse("%s: no such field on the %s", c.Field, d.Chip.Name)
			continue
		}
		n := f.bits()
		if extraBits(c.Value, n) {
			refuse("%s: the value has more than %d bits", f.name, n)
			continue
		}
		if bs := f.protectedBy(t.rd); anyBitSet(d.Blocks, bs) {
			refuse("%s: BLOCK%d is read-protected: its contents can't be read or verified", f.name, f.parts[0].blk)
			continue
		}
		if bs := f.protectedBy(t.wr); anyBitSet(d.Blocks, bs) {
			refuse("%s: write-protected (WR_DIS bit %d)", f.name, firstSet(d.Blocks, bs))
			continue
		}
		if !d.inCodedData(f) {
			refuse("%s: BLOCK%d holds only %d bits with the %s coding scheme", f.name, f.parts[0].blk,
				32*d.Coding[f.parts[0].blk].dataWords(len(d.Blocks[f.parts[0].blk])), d.Coding[f.parts[0].blk])
			continue
		}
		ok := true
		changed := false
		i := 0
		for _, pt := range f.parts {
			for k := 0; k < pt.n; k++ {
				want := c.Value != nil && i/8 < len(c.Value) && c.Value[i/8]>>(uint(i)%8)&1 != 0
				pos := pt.bit + k
				switch {
				case want && bitOf(want0, pt.blk, pos):
					refuse("%s: bit %d of BLOCK%d is also set to 0 by another change", f.name, pos, pt.blk)
					ok = false
				case want:
					if !bitOf(p.new, pt.blk, pos) {
						changed = true
					}
					setBit(p.new, pt.blk, pos)
				case bitOf(p.old, pt.blk, pos):
					refuse("%s: bit %d is already burned and can't be set back to 0 (the field is %s)",
						f.name, i, formatValue(d.Chip.Key, f, fieldRaw(p.old, f)))
					ok = false
				case bitOf(p.new, pt.blk, pos):
					refuse("%s: bit %d of BLOCK%d is also set to 1 by another change", f.name, pos, pt.blk)
					ok = false
				default:
					setBit(want0, pt.blk, pos)
				}
				i++
				if !ok {
					break
				}
			}
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		oldText := formatValue(d.Chip.Key, f, fieldRaw(p.old, f))
		newRaw := make([]byte, (n+7)/8)
		copy(newRaw, c.Value)
		newText := formatValue(d.Chip.Key, f, newRaw)
		if !changed {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s already is %s: nothing to burn for it", f.name, oldText))
			continue
		}
		p.Changes = append(p.Changes, fmt.Sprintf("%s: %s → %s", f.name, oldText, newText))
	}

	// the blocks, the last first and BLOCK0 last, as ESP-IDF burns them:
	// BLOCK0 holds the protection of the others
	for b := len(p.new) - 1; b >= 0; b-- {
		set := make([]uint32, len(p.new[b]))
		any := false
		for w := range set {
			set[w] = p.new[b][w] &^ p.old[b][w]
			any = any || set[w] != 0
		}
		if !any {
			continue
		}
		switch d.Coding[b] {
		case CodingRS:
			if !zero(p.old[b]) {
				refuse("BLOCK%d already holds data: an RS-coded block can be written only once", b)
				continue
			}
		case Coding34:
			oldB, setB := wordBytes(p.old[b]), wordBytes(set)
			for g := 0; g < 4; g++ {
				if !zeroBytes(setB[6*g:6*g+6]) && !zeroBytes(oldB[6*g:6*g+6]) {
					refuse("BLOCK%d: bytes %d-%d already hold data: with the 3/4 coding each 6 bytes are written once", b, 6*g, 6*g+5)
				}
			}
		}
		if b > 0 && d.Chip.Key == "esp32" && (p.new[0][6]^p.old[0][6])&3 != 0 {
			refuse("BLOCK%d: CODING_SCHEME changes in the same burn; burn it first, then read again", b)
		}
		p.Writes = append(p.Writes, BlockWrite{Block: b, Coding: d.Coding[b],
			Old: append([]uint32(nil), p.old[b]...), New: append([]uint32(nil), p.new[b]...), Set: set})
		p.Blocks = append(p.Blocks, b)
	}
	if len(refused) > 0 {
		return nil, &RefusedError{Reasons: refused}
	}
	if len(p.Writes) > 0 {
		if wd := t.find("WR_DIS"); wd != nil {
			for _, pt := range wd.parts {
				if pt.blk == 0 && pt.bit < 32 {
					p.wrDis |= mask32(pt.bit, min(pt.n, 32-pt.bit))
				}
			}
		}
		p.Warnings = append([]string{"Burning eFuses can't be undone: a bit set to 1 stays 1 for the life of the chip."}, p.Warnings...)
		p.Warnings = append(p.Warnings, p.consequences()...)
	}
	return p, nil
}

func mask32(bit, n int) uint32 {
	if n >= 32 {
		return 0xFFFFFFFF
	}
	return (1<<uint(n) - 1) << uint(bit)
}

func extraBits(v []byte, n int) bool {
	for i := n; i < 8*len(v); i++ {
		if v[i/8]>>(uint(i)%8)&1 != 0 {
			return true
		}
	}
	return false
}

func firstSet(blocks [][]uint32, bs []int) int {
	for _, b := range bs {
		if bitOf(blocks, 0, b) {
			return b
		}
	}
	return -1
}

func copyBlocks(b [][]uint32) [][]uint32 {
	out := make([][]uint32, len(b))
	for i := range b {
		out[i] = append([]uint32(nil), b[i]...)
	}
	return out
}

func zero(w []uint32) bool {
	for _, v := range w {
		if v != 0 {
			return false
		}
	}
	return true
}

func zeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func wordBytes(w []uint32) []byte {
	b := make([]byte, 4*len(w))
	for i, v := range w {
		binary.LittleEndian.PutUint32(b[4*i:], v)
	}
	return b
}

// newlySet: a bit of BLOCK0 the plan sets
func (p *Plan) newlySet(bit int) bool {
	return bitOf(p.new, 0, bit) && !bitOf(p.old, 0, bit)
}

// fieldChanged: the plan sets a bit of the field
func (p *Plan) fieldChanged(f *fieldDef) bool {
	for _, pt := range f.parts {
		for k := 0; k < pt.n; k++ {
			if bitOf(p.new, pt.blk, pt.bit+k) && !bitOf(p.old, pt.blk, pt.bit+k) {
				return true
			}
		}
	}
	return false
}

// secretPurposes are the key purposes whose keys must not stay readable
var secretPurposes = []string{"XTS_AES", "HMAC", "ECDSA", "KM_INIT", "FLASH_ENCRYPTION"}

// consequences are the warnings about what the burn turns on or off
func (p *Plan) consequences() []string {
	var w []string
	t := p.t
	key := p.chip.Key
	// protection bits
	seen := map[int]bool{}
	for _, r := range t.wr {
		if seen[r.bit] || !p.newlySet(r.bit) {
			continue
		}
		seen[r.bit] = true
		var names []string
		for i := range t.fields {
			f := &t.fields[i]
			if strings.HasPrefix(f.name, "WR_DIS.") || strings.HasPrefix(f.name, "RD_DIS.") || strings.Contains(f.name, ".") {
				continue
			}
			for _, b := range f.protectedBy(t.wr) {
				if b == r.bit {
					names = append(names, f.name)
					break
				}
			}
		}
		sort.Strings(names)
		w = append(w, fmt.Sprintf("WR_DIS bit %d write-protects for good: %s", r.bit, strings.Join(names, ", ")))
	}
	seenRd := map[int]bool{}
	for _, r := range t.rd {
		if seenRd[r.bit] || !p.newlySet(r.bit) {
			continue
		}
		seenRd[r.bit] = true
		w = append(w, fmt.Sprintf("RD_DIS bit %d: BLOCK%d (%s) can no longer be read by software, only by the hardware that uses the key; it can't be verified afterwards", r.bit, r.blk, r.target))
	}
	for i := range t.fields {
		f := &t.fields[i]
		if strings.Contains(f.name, ".") || !p.fieldChanged(f) {
			continue
		}
		v := rawUint(fieldRaw(p.new, f))
		name := f.name
		switch {
		case name == "FLASH_CRYPT_CNT" || name == "SPI_BOOT_CRYPT_CNT":
			if bits.OnesCount64(v)%2 == 1 {
				w = append(w, name+": flash encryption becomes enabled; plaintext images will no longer boot")
			} else {
				w = append(w, name+": flash encryption becomes disabled")
			}
		case name == "ABS_DONE_0" || name == "ABS_DONE_1" || name == "SECURE_BOOT_EN":
			w = append(w, name+": Secure Boot becomes enabled; only signed bootloaders and apps will boot")
		case strings.Contains(name, "DOWNLOAD_MODE") || name == "UART_DOWNLOAD_DIS":
			w = append(w, name+": a download mode is disabled; this program may no longer be able to connect to the chip")
		case name == "ENABLE_SECURITY_DOWNLOAD":
			w = append(w, name+": Secure Download Mode: flash can't be read and the stub can't be loaded any more")
		case strings.Contains(name, "JTAG"):
			w = append(w, name+": JTAG debugging is disabled or restricted")
		case strings.HasPrefix(name, "SECURE_BOOT_KEY_REVOKE"):
			w = append(w, name+": the Secure Boot key is revoked; images signed with it won't boot")
		case name == "CODING_SCHEME":
			w = append(w, name+": changes how BLOCK1-3 store data; existing data in them may become unreadable")
		case name == "SECURE_VERSION":
			w = append(w, name+": apps with a lower secure version will no longer boot")
		case f.cat == CatKeys && f.bits() >= 128:
			w = append(w, name+": a key is burned; it can't be changed later")
		case isPurpose(name):
			pn := keyPurposeNames[key][v]
			secret := false
			for _, s := range secretPurposes {
				secret = secret || strings.Contains(pn, s)
			}
			if !secret {
				break
			}
			blk := 4 + int(name[len(name)-1]-'0')
			prot := false
			for _, r := range t.rd {
				if r.blk == blk && bitOf(p.new, 0, r.bit) {
					prot = true
				}
			}
			if !prot {
				w = append(w, fmt.Sprintf("%s is %s but BLOCK%d stays readable by software: set its RD_DIS bit too", name, pn, blk))
			}
		}
	}
	return w
}
