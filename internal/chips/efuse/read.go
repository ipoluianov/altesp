package efuse

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ipoluianov/altesp/internal/chips"
)

// Value is a field as read from a chip
type Value struct {
	Field Field
	// Raw are the bits of the field, the first bit in bit 0 of Raw[0]
	// (ESP-IDF's esp_efuse_read_field_blob); for the factory MAC Raw[0] is
	// the first byte of the address
	Raw []byte
	// Text is the value for people: a number, a name of the table, a MAC,
	// hex for keys
	Text string
	// WriteProtected: a bit of WR_DIS that covers the field is set
	WriteProtected bool
	// ReadProtected: a bit of RD_DIS that covers the field is set; the
	// field then reads as zeros
	ReadProtected bool
}

// Uint is the value of a field of up to 64 bits
func (v Value) Uint() uint64 { return rawUint(v.Raw) }

// Dump is everything read from the eFuse controller
type Dump struct {
	Chip     *chips.Chip
	Revision chips.Revision
	Values   []Value
	// Blocks are the words of the read registers, BLOCK0 first
	Blocks [][]uint32
	// Coding is the coding scheme of each block
	Coding []Coding
	// CodingScheme describes the coding of the chip: "3/4 (BLOCK1-3)",
	// "BLOCK0: 4 copies; BLOCK1-10: RS(44,32)"
	CodingScheme string
	// WriteProtected and ReadProtected are per block: the whole block is
	// protected (BLOCK0 is never protected as a whole)
	WriteProtected, ReadProtected []bool
	// Errors are what the controller's error registers report
	Errors []string

	t *tableDef
	l *layout
}

// Value finds a value by the name of its field or an alias
func (d *Dump) Value(name string) (Value, bool) {
	for _, v := range d.Values {
		if (&fieldDef{name: v.Field.Name, aliases: v.Field.Aliases}).is(name) {
			return v, true
		}
	}
	return Value{}, false
}

// Read reads all blocks and the error registers and decodes every field
func Read(chip *chips.Chip, rev chips.Revision, r chips.Regs) (*Dump, error) {
	t, l := tableFor(chip, rev), layoutFor(chip)
	if t == nil || l == nil {
		return nil, errNoChip(chip)
	}
	d := &Dump{Chip: chip, Revision: rev, t: t, l: l}
	for i, b := range l.blocks {
		words := make([]uint32, b.words)
		for w := range words {
			v, err := r.ReadReg(b.rd + uint32(4*w))
			if err != nil {
				return nil, fmt.Errorf("reading eFuse BLOCK%d: %w", i, err)
			}
			words[w] = v
		}
		d.Blocks = append(d.Blocks, words)
	}
	d.Coding = l.codings(d.Blocks)
	d.CodingScheme = l.describeCoding(d.Coding)
	if err := d.readErrors(r); err != nil {
		return nil, err
	}

	d.WriteProtected = make([]bool, len(l.blocks))
	d.ReadProtected = make([]bool, len(l.blocks))
	for b := 1; b < len(l.blocks); b++ {
		d.WriteProtected[b] = blockProtected(t.wr, d.Blocks, b)
		d.ReadProtected[b] = blockProtected(t.rd, d.Blocks, b)
	}
	for i := range t.fields {
		f := &t.fields[i]
		v := Value{Field: f.export(), Raw: fieldRaw(d.Blocks, f)}
		v.WriteProtected = anyBitSet(d.Blocks, f.protectedBy(t.wr))
		v.ReadProtected = anyBitSet(d.Blocks, f.protectedBy(t.rd))
		v.Text = d.text(f, v.Raw)
		if v.ReadProtected {
			v.Text = "(read-protected)"
		} else if !d.inCodedData(f) {
			v.Text += " (outside the data of the " + d.Coding[f.parts[0].blk].String() + " coding)"
		}
		d.Values = append(d.Values, v)
	}
	return d, nil
}

// codings: BLOCK0 is never coded; BLOCK1-3 of the ESP32 by CODING_SCHEME
// (BLOCK0 bits 192-193: 1 is 3/4, 2 repeat, 0 and 3 none; esp_efuse_api_
// key.c of three_key_blocks), the other chips RS from BLOCK1
// (one_key_block and with_key_purposes: EFUSE_CODING_SCHEME_RS)
func (l *layout) codings(blocks [][]uint32) []Coding {
	c := make([]Coding, len(l.blocks))
	for b := 1; b < len(c); b++ {
		if l.kind == kindRS {
			c[b] = CodingRS
			continue
		}
		switch blocks[0][6] & 3 {
		case 1:
			c[b] = Coding34
		case 2:
			c[b] = CodingRepeat
		}
	}
	return c
}

func (l *layout) describeCoding(c []Coding) string {
	if l.kind == kindESP32 {
		return fmt.Sprintf("BLOCK1-3: %s", c[1])
	}
	return fmt.Sprintf("BLOCK0: stored 4 times; BLOCK1-%d: %s", len(c)-1, CodingRS)
}

func (d *Dump) readErrors(r chips.Regs) error {
	l := d.l
	if l.kind == kindESP32 {
		v, err := r.ReadReg(l.decStatus)
		if err != nil {
			return fmt.Errorf("reading DEC_STATUS: %w", err)
		}
		for b := 1; b <= 3; b++ {
			if n := v >> (4 * uint(b-1)) & 0xF; n != 0 && d.Coding[b] == Coding34 {
				d.Errors = append(d.Errors, fmt.Sprintf("BLOCK%d: the 3/4 coding check failed (DEC_STATUS 0x%03X)", b, v&0xFFF))
			}
		}
		return nil
	}
	for i, a := range l.repeatErr {
		v, err := r.ReadReg(a)
		if err != nil {
			return fmt.Errorf("reading the BLOCK0 error registers: %w", err)
		}
		if v != 0 {
			d.Errors = append(d.Errors, fmt.Sprintf("BLOCK0: the copies of bits 0x%08X of word %d disagree (RD_REPEAT_ERR%d)", v, i+1, i))
		}
	}
	errs := make([]uint32, len(l.rsErr))
	for i, a := range l.rsErr {
		v, err := r.ReadReg(a)
		if err != nil {
			return fmt.Errorf("reading the RS error registers: %w", err)
		}
		errs[i] = v
	}
	for b := 1; b < len(l.blocks); b++ {
		num, fail, _ := l.rsBlockError(errs, b)
		switch {
		case fail:
			d.Errors = append(d.Errors, fmt.Sprintf("BLOCK%d: RS decoding failed, the data may be wrong", b))
		case num > 0:
			d.Errors = append(d.Errors, fmt.Sprintf("BLOCK%d: RS decoding corrected %d byte(s)", b, num))
		}
	}
	return nil
}

// inCodedData: the field lies in the bits a coded ESP32 block holds
func (d *Dump) inCodedData(f *fieldDef) bool {
	for _, p := range f.parts {
		if p.blk < len(d.Coding) && p.bit+p.n > 32*d.Coding[p.blk].dataWords(len(d.Blocks[p.blk])) {
			return false
		}
	}
	return true
}

func bitOf(blocks [][]uint32, blk, bit int) bool {
	if blk >= len(blocks) || bit/32 >= len(blocks[blk]) {
		return false
	}
	return blocks[blk][bit/32]>>(uint(bit)%32)&1 != 0
}

func setBit(blocks [][]uint32, blk, bit int) {
	blocks[blk][bit/32] |= 1 << (uint(bit) % 32)
}

func anyBitSet(blocks [][]uint32, bits []int) bool {
	for _, b := range bits {
		if bitOf(blocks, 0, b) {
			return true
		}
	}
	return false
}

// blockProtected: a rule over the whole of block b has its bit set
func blockProtected(rs []protRule, blocks [][]uint32, b int) bool {
	for _, r := range rs {
		if r.blk == b && bitOf(blocks, 0, r.bit) {
			return true
		}
	}
	return false
}

// fieldRaw collects the bits of a field, the first in bit 0 of byte 0
func fieldRaw(blocks [][]uint32, f *fieldDef) []byte {
	raw := make([]byte, (f.bits()+7)/8)
	i := 0
	for _, p := range f.parts {
		for k := 0; k < p.n; k++ {
			if bitOf(blocks, p.blk, p.bit+k) {
				raw[i/8] |= 1 << (uint(i) % 8)
			}
			i++
		}
	}
	return raw
}

func rawUint(raw []byte) uint64 {
	var v uint64
	for i := 0; i < len(raw) && i < 8; i++ {
		v |= uint64(raw[i]) << (8 * i)
	}
	return v
}

// text formats a value of a field
func (d *Dump) text(f *fieldDef, raw []byte) string {
	return formatValue(d.Chip.Key, f, raw)
}

func formatValue(chipKey string, f *fieldDef, raw []byte) string {
	n := f.bits()
	switch {
	case n == 48 && f.cat == CatMAC:
		parts := make([]string, 6)
		for i := range parts {
			parts[i] = fmt.Sprintf("%02x", raw[i])
		}
		return strings.Join(parts, ":")
	case n > 64:
		return "0x" + fmt.Sprintf("%x", raw)
	}
	v := rawUint(raw)
	if isPurpose(f.name) {
		if s, ok := keyPurposeNames[chipKey][v]; ok {
			return fmt.Sprintf("%s (%d)", s, v)
		}
	}
	if s, ok := f.enum[v]; ok {
		return fmt.Sprintf("%s (%d)", s, v)
	}
	if n == 1 {
		return strconv.FormatUint(v, 10)
	}
	if v < 10 {
		return strconv.FormatUint(v, 10)
	}
	return fmt.Sprintf("%d (0x%X)", v, v)
}

func isPurpose(name string) bool {
	return strings.HasPrefix(name, "KEY_PURPOSE_")
}
