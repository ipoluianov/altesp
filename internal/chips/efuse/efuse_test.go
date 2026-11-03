package efuse

import (
	"errors"
	"strings"
	"testing"

	"github.com/ipoluianov/altesp/internal/chips"
)

// revs are revisions to try per chip: both tables of the ESP32-P4 and the
// ESP32-H2
var revs = []chips.Revision{{Major: 0, Minor: 0}, {Major: 1, Minor: 1}, {Major: 3, Minor: 0}}

func chip(t *testing.T, key string) *chips.Chip {
	t.Helper()
	c := chips.ByKey(key)
	if c == nil {
		t.Fatalf("no chip %s", key)
	}
	return c
}

func TestEveryChipHasATableAndALayout(t *testing.T) {
	for _, c := range chips.All() {
		l := layoutFor(c)
		if l == nil {
			t.Errorf("%s: no layout", c.Name)
			continue
		}
		if c.ProbeAddr() != l.blocks[0].rd {
			t.Errorf("%s: BLOCK0 at 0x%08X, the chip table probes 0x%08X", c.Name, l.blocks[0].rd, c.ProbeAddr())
		}
		for _, rev := range revs {
			tb := tableFor(c, rev)
			if tb == nil {
				t.Errorf("%s %v: no table", c.Name, rev)
				continue
			}
			names := map[string]bool{}
			for _, f := range tb.fields {
				if names[f.name] {
					t.Errorf("%s: %s twice", c.Name, f.name)
				}
				names[f.name] = true
				for _, p := range f.parts {
					if p.blk >= len(l.blocks) || p.bit+p.n > 32*max(l.blocks[p.blk].words, 8) && p.blk > 0 ||
						p.blk == 0 && p.bit+p.n > 32*l.blocks[0].words {
						t.Errorf("%s: %s bits %d+%d of BLOCK%d are outside the block", c.Name, f.name, p.bit, p.n, p.blk)
					}
				}
			}
			for _, r := range append(append([]protRule{}, tb.wr...), tb.rd...) {
				if r.bit >= 32*l.blocks[0].words || r.blk >= len(l.blocks) {
					t.Errorf("%s: rule %+v out of range", c.Name, r)
				}
			}
			if tb.find("WR_DIS") == nil {
				t.Errorf("%s: no WR_DIS", c.Name)
			}
			if len(Fields(c, rev)) != len(tb.fields) {
				t.Errorf("%s: Fields", c.Name)
			}
		}
	}
}

// The test vectors of ESP-IDF's test_efuse_coding_scheme.c (Apache-2.0)
func TestEncode34(t *testing.T) {
	vectors := []struct {
		in  [24]byte
		out [8]uint32
	}{
		{[24]byte{0x96, 0xa9, 0xab, 0xb2, 0xda, 0xdd, 0x21, 0xd2, 0x35, 0x22, 0xd3, 0x30, 0x3b, 0xf8, 0xcb, 0x77, 0x8d, 0x8d, 0xf4, 0x96, 0x25, 0xc4, 0xb9, 0x94},
			[8]uint32{0xb2aba996, 0x6821ddda, 0x2235d221, 0x430730d3, 0x77cbf83b, 0x627f8d8d, 0xc42596f4, 0x4dae94b9}},
		{[24]byte{0x0e, 0x6b, 0x1a, 0x1d, 0xa5, 0x9f, 0x24, 0xcf, 0x91, 0x5b, 0xe7, 0xe1, 0x7c, 0x0a, 0x6e, 0xdc, 0x5e, 0x8e, 0xb1, 0xec, 0xd1, 0xf3, 0x75, 0x48},
			[8]uint32{0x1d1a6b0e, 0x5e589fa5, 0x5b91cf24, 0x6127e1e7, 0xdc6e0a7c, 0x5d148e5e, 0xf3d1ecb1, 0x57424875}},
	}
	for i, v := range vectors {
		words := make([]uint32, 6)
		for w := range words {
			words[w] = uint32(v.in[4*w]) | uint32(v.in[4*w+1])<<8 | uint32(v.in[4*w+2])<<16 | uint32(v.in[4*w+3])<<24
		}
		if got := encode34(words); got != v.out {
			t.Errorf("vector %d: %08x, want %08x", i, got, v.out)
		}
		data, bad := decode34(v.out)
		if bad != [4]bool{} || [6]uint32(words) != data {
			t.Errorf("vector %d: decoded %08x %v", i, data, bad)
		}
		broken := v.out
		broken[3] ^= 0x100
		if _, bad := decode34(broken); !bad[1] {
			t.Errorf("vector %d: a broken group passes", i)
		}
	}
}

func TestRS(t *testing.T) {
	// the generator's roots are a^0..a^11
	for i := 0; i < rsParity; i++ {
		var s byte
		for _, c := range rsGen {
			s = gfMul(s, gfExp[i]) ^ c
		}
		if s != 0 {
			t.Fatalf("a^%d is not a root of the generator", i)
		}
	}
	words := []uint32{0x01234567, 0x89ABCDEF, 0xDEADBEEF, 0, 0x55AA55AA, 1, 0xFFFFFFFF, 0x80000000}
	p := rsEncode(words)
	if !rsCheck(words, p) {
		t.Fatal("a codeword fails its check")
	}
	if rsEncode(make([]uint32, 8)) != [3]uint32{} || !rsCheck(nil, [3]uint32{}) {
		t.Fatal("the empty block has check bytes")
	}
	for w := range words {
		bad := append([]uint32(nil), words...)
		bad[w] ^= 1 << uint(w)
		if rsCheck(bad, p) {
			t.Errorf("a changed word %d passes the check", w)
		}
	}
	// six words of BLOCK1 are coded with the missing two as zeros
	if rsEncode(words[:6]) != rsEncode(append(append([]uint32(nil), words[:6]...), 0, 0)) {
		t.Error("a short block is not padded with zeros")
	}
}

func readModel(t *testing.T, c *chips.Chip, rev chips.Revision, m *Model) *Dump {
	t.Helper()
	d, err := Read(c, rev, m)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func value(t *testing.T, d *Dump, name string) Value {
	t.Helper()
	v, ok := d.Value(name)
	if !ok {
		t.Fatalf("no field %s", name)
	}
	return v
}

func opts(c *chips.Chip) Options {
	return Options{APBMHz: 40, CrystalMHz: 40}
}

// userField is a field of a whole user block on every chip
func userField(c *chips.Chip) string {
	if c.Key == "esp32" {
		return "MAC_CUSTOM"
	}
	return "USER_DATA"
}

// TestBurnEveryChip burns a user block and its write protection on a
// model of every chip, through the chip's own register sequence
func TestBurnEveryChip(t *testing.T) {
	for _, c := range chips.All() {
		for _, rev := range []chips.Revision{{Major: 0}, {Major: 3}} {
			m := NewModel(c, rev)
			m.Busy = 2
			d := readModel(t, c, rev, m)
			uf := userField(c)
			f := d.t.find(uf)
			val := make([]byte, (f.bits()+7)/8)
			for i := range val {
				val[i] = byte(0x11 * (i + 1))
			}
			if c.Key == "esp32" {
				val = []byte{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}
			}
			wr := f.protectedBy(d.t.wr)
			if len(wr) == 0 {
				t.Fatalf("%s: %s has no WR_DIS bit", c.Name, uf)
			}
			wrName := ""
			for _, r := range d.t.wr {
				if r.bit == wr[0] {
					wrName = "WR_DIS." + r.target
					break
				}
			}
			p, err := PlanBurn(d, []Change{{Field: uf, Value: val}, Uint(wrName, 1)})
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			blk := f.parts[0].blk
			if len(p.Blocks) != 2 || p.Blocks[0] != blk || p.Blocks[1] != 0 {
				t.Fatalf("%s: blocks %v", c.Name, p.Blocks)
			}
			res, err := Burn(c, m, p, opts(c))
			if err != nil {
				t.Fatalf("%s: %v %+v", c.Name, err, res)
			}
			// the user block first, WR_DIS separately at the end
			if len(res.Stages) != 2 || res.Stages[0].Block != blk || !res.Stages[1].WRDis {
				t.Fatalf("%s: stages %+v", c.Name, res.Stages)
			}
			if m.Reprogrammed != 0 || m.BadCommands != 0 {
				t.Errorf("%s: %d bits programmed twice, %d bad commands", c.Name, m.Reprogrammed, m.BadCommands)
			}
			after := readModel(t, c, rev, m)
			v := value(t, after, uf)
			if string(v.Raw) != string(val) || !v.WriteProtected {
				t.Errorf("%s: %s = %x, protected %v", c.Name, uf, v.Raw, v.WriteProtected)
			}
			if !after.WriteProtected[blk] {
				t.Errorf("%s: BLOCK%d not write-protected", c.Name, blk)
			}
			if len(after.Errors) != 0 {
				t.Errorf("%s: errors %v", c.Name, after.Errors)
			}
			// a second burn of the block is refused now
			var re *RefusedError
			if _, err := PlanBurn(after, []Change{{Field: uf, Value: []byte{0xFF}}}); !errors.As(err, &re) {
				t.Errorf("%s: a write-protected field is not refused: %v", c.Name, err)
			}
		}
	}
}

func TestRefusals(t *testing.T) {
	c := chip(t, "esp32c3")
	m := NewModel(c, chips.Revision{})
	m.Set(0, []uint32{0, 0, 3 << (80 - 64)}) // WDT_DELAY_SEL = 3
	m.Set(3, []uint32{1})                    // USER_DATA has data
	d := readModel(t, c, chips.Revision{}, m)
	cases := []struct {
		name string
		ch   []Change
		want string
	}{
		{"unknown", []Change{Uint("NO_SUCH_FIELD", 1)}, "no such field"},
		{"too wide", []Change{Uint("WDT_DELAY_SEL", 4)}, "more than 2 bits"},
		{"clear a bit", []Change{Uint("WDT_DELAY_SEL", 1)}, "can't be set back to 0"},
		{"RS block written", []Change{Uint("USER_DATA", 3)}, "written only once"},
		{"conflict", []Change{Uint("WR_DIS", 0), Uint("WR_DIS.RD_DIS", 1)}, "also set to 0"},
	}
	for _, tc := range cases {
		_, err := PlanBurn(d, tc.ch)
		var re *RefusedError
		if !errors.As(err, &re) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// the same value again: nothing to burn
	p, err := PlanBurn(d, []Change{Uint("WDT_DELAY_SEL", 3)})
	if err != nil || !p.Empty() {
		t.Errorf("same value: %v %+v", err, p)
	}
	// a read-protected key block
	m.Set(0, []uint32{0, 1 << (32 - 32)}) // RD_DIS bit 32: BLOCK_KEY0
	d = readModel(t, c, chips.Revision{}, m)
	if _, err := PlanBurn(d, []Change{Uint("KEY0", 1)}); err == nil || !strings.Contains(err.Error(), "read-protected") {
		t.Errorf("read-protected block: %v", err)
	}
	if v := value(t, d, "KEY0"); !v.ReadProtected || v.Text != "(read-protected)" {
		t.Errorf("KEY0: %+v", v)
	}
}

// A key, its purpose and its read protection in one burn: the key is
// burned and verified before BLOCK0 makes it unreadable
func TestBurnKeyAndReadProtection(t *testing.T) {
	c := chip(t, "esp32c3")
	rev := chips.Revision{Major: 0, Minor: 4}
	m := NewModel(c, rev)
	d := readModel(t, c, rev, m)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	p, err := PlanBurn(d, []Change{{Field: "KEY0", Value: key}, Uint("KEY_PURPOSE_0", 4)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "stays readable") {
		t.Errorf("no warning about a readable key: %v", p.Warnings)
	}
	p, err = PlanBurn(d, []Change{{Field: "KEY0", Value: key}, Uint("KEY_PURPOSE_0", 4),
		Uint("RD_DIS.BLOCK_KEY0", 1), Uint("WR_DIS.BLOCK_KEY0", 1), Uint("WR_DIS.KEY_PURPOSE_0", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Changes, "\n"), "KEY_PURPOSE_0: USER (0) → XTS_AES_128_KEY (4)") {
		t.Errorf("changes: %v", p.Changes)
	}
	res, err := Burn(c, m, p, opts(c))
	if err != nil {
		t.Fatal(err, res)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "BLOCK4 is read-protected now") {
		t.Errorf("notes: %v", res.Notes)
	}
	if got := m.Fuses(4); got[0] != 0x04030201 || got[7] != 0x201F1E1D {
		t.Errorf("key in the fuses: %08x", got)
	}
	after := readModel(t, c, rev, m)
	k := value(t, after, "KEY0")
	if !k.ReadProtected || !k.WriteProtected || k.Uint() != 0 {
		t.Errorf("KEY0 after: %+v", k)
	}
	if v := value(t, after, "KEY_PURPOSE_0"); v.Text != "XTS_AES_128_KEY (4)" || !v.WriteProtected {
		t.Errorf("KEY_PURPOSE_0: %+v", v)
	}
}

func TestBurnFailureStopsBeforeBLOCK0(t *testing.T) {
	c := chip(t, "esp32s3")
	m := NewModel(c, chips.Revision{})
	m.Stuck[[2]int{3, 0}] = 1 // bit 0 of USER_DATA doesn't burn
	d := readModel(t, c, chips.Revision{}, m)
	p, err := PlanBurn(d, []Change{Uint("USER_DATA", 0xFF), Uint("WR_DIS.BLOCK_USR_DATA", 1)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Burn(c, m, p, opts(c))
	if err == nil {
		t.Fatal("a failed burn succeeds")
	}
	if res.Stages[0].OK || res.Stages[0].Attempts != maxAttempts || !res.Stages[1].Skipped {
		t.Errorf("stages %+v", res.Stages)
	}
	if m.Fuses(0)[0] != 0 {
		t.Error("BLOCK0 burned after a failed block")
	}
}

func TestRSErrorReported(t *testing.T) {
	for _, key := range []string{"esp32c3", "esp32c6", "esp32c2", "esp32s31"} {
		c := chip(t, key)
		m := NewModel(c, chips.Revision{})
		m.Set(2, []uint32{0x12345678})
		m.CorruptParity(2)
		d := readModel(t, c, chips.Revision{}, m)
		if len(d.Errors) != 1 || !strings.Contains(d.Errors[0], "BLOCK2: RS decoding failed") {
			t.Errorf("%s: errors %v", key, d.Errors)
		}
	}
}

func TestESP32(t *testing.T) {
	c := chip(t, "esp32")
	rev := chips.Revision{Major: 3}
	m := NewModel(c, rev)
	// factory MAC aa:bb:cc:dd:ee:ff in BLOCK0 bits 32-79 and the 3/4
	// coding scheme
	m.Set(0, []uint32{0, 0xccddeeff, 0xaabb, 0, 0, 0, 1})
	d := readModel(t, c, rev, m)
	if v := value(t, d, "MAC"); v.Text != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC %q", v.Text)
	}
	if d.Coding[3] != Coding34 || !strings.Contains(d.CodingScheme, "3/4") {
		t.Errorf("coding %v %q", d.Coding, d.CodingScheme)
	}
	if _, err := PlanBurn(d, []Change{{Field: "BLOCK1", Value: make([]byte, 32)}}); err == nil || !strings.Contains(err.Error(), "192 bits") {
		t.Errorf("beyond 3/4 data: %v", err)
	}
	p, err := PlanBurn(d, []Change{{Field: "MAC_CUSTOM", Value: []byte{2, 1, 2, 3, 4, 5}}, Uint("CUSTOM_MAC_CRC", 0x5A)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Burn(c, m, p, Options{}); err == nil {
		t.Error("no APB clock is accepted")
	}
	res, err := Burn(c, m, p, Options{APBMHz: 40})
	if err != nil {
		t.Fatal(err, res)
	}
	// efuse_hal_set_timing at 40 MHz: CLK_SEL0 160, CLK_SEL1 255,
	// DAC_CLK_DIV 80
	if clk, dac := m.Reg(0x3FF5A0F8), m.Reg(0x3FF5A118); clk != 255<<8|160 || dac&0xFF != 80 {
		t.Errorf("timing CLK 0x%X DAC_CONF 0x%X", clk, dac)
	}
	// stored coded
	if f, w := m.Fuses(3), encode34([]uint32{0x0201025A, 0x00050403}); [8]uint32(f) != w {
		t.Errorf("BLOCK3 not 3/4 coded: %08x want %08x", f, w)
	}
	after := readModel(t, c, rev, m)
	if v := value(t, after, "MAC_CUSTOM"); v.Text != "02:01:02:03:04:05" {
		t.Errorf("MAC_CUSTOM %q", v.Text)
	}
	if len(after.Errors) != 0 || m.Reprogrammed != 0 {
		t.Errorf("errors %v, %d bits programmed twice", after.Errors, m.Reprogrammed)
	}
	// the first 3/4 group (bytes 0-5) has data now: ADC1_TP_LOW (byte 12)
	// is in the third group and still free, SECURE_VERSION's group too...
	if _, err := PlanBurn(after, []Change{Uint("ADC1_TP_LOW", 1)}); err != nil {
		t.Errorf("a free group: %v", err)
	}
	// ...but the first one is taken
	m2 := NewModel(c, rev)
	m2.Set(0, []uint32{0, 0, 0, 0, 0, 0, 1})
	m2.Set(3, []uint32{0x100})
	d2 := readModel(t, c, rev, m2)
	if _, err := PlanBurn(d2, []Change{Uint("CUSTOM_MAC_CRC", 1)}); err == nil || !strings.Contains(err.Error(), "each 6 bytes are written once") {
		t.Errorf("a used group: %v", err)
	}
}

func TestBurnRefusesChangedChip(t *testing.T) {
	c := chip(t, "esp32c6")
	m := NewModel(c, chips.Revision{})
	d := readModel(t, c, chips.Revision{}, m)
	p, err := PlanBurn(d, []Change{Uint("USER_DATA", 1)})
	if err != nil {
		t.Fatal(err)
	}
	m.Set(3, []uint32{2})
	if _, err := Burn(c, m, p, opts(c)); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Errorf("changed chip: %v", err)
	}
	if _, err := Burn(chip(t, "esp32c3"), m, p, opts(c)); err == nil {
		t.Error("a plan for another chip is accepted")
	}
}

func TestDryRun(t *testing.T) {
	c := chip(t, "esp32c2")
	m := NewModel(c, chips.Revision{})
	d := readModel(t, c, chips.Revision{}, m)
	p, err := PlanBurn(d, []Change{Uint("SPI_BOOT_CRYPT_CNT", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "flash encryption becomes enabled") {
		t.Errorf("warnings %v", p.Warnings)
	}
	res, err := DryRun(p, Options{CrystalMHz: 26})
	if err != nil {
		t.Fatal(err)
	}
	log := strings.Join(res.Log, "\n")
	// CMD (0x60008894) with PGM and BLOCK0; TPGM_INACTIVE 130 for 26 MHz
	if !strings.Contains(log, "write 0x60008894 = 0x00000002") || !strings.Contains(log, "write 0x60008910 = 0x00008200") {
		t.Errorf("log:\n%s", log)
	}
	if m.Programs != 0 || m.Fuses(0)[1] != 0 {
		t.Error("the dry run touched the chip")
	}
}

func TestDecode(t *testing.T) {
	c := chip(t, "esp32c3")
	m := NewModel(c, chips.Revision{})
	// MAC aa:bb:cc:dd:ee:ff (BLOCK1 bits 0-47, the first byte highest),
	// FLASH_CAP 1 ("4M", bits 123-125)
	m.Set(1, []uint32{0xccddeeff, 0xaabb, 0, 1 << (123 - 96)})
	m.Set(0, []uint32{0, 0, 7 << (88 - 64)}) // KEY_PURPOSE_0 = 7
	d := readModel(t, c, chips.Revision{}, m)
	for name, want := range map[string]string{
		"MAC":           "aa:bb:cc:dd:ee:ff",
		"MAC_FACTORY":   "aa:bb:cc:dd:ee:ff",
		"FLASH_CAP":     "4M (1)",
		"KEY_PURPOSE_0": "HMAC_DOWN_DIGITAL_SIGNATURE (7)",
		"KEY0":          "0x" + strings.Repeat("00", 32),
	} {
		if v := value(t, d, name); v.Text != want {
			t.Errorf("%s = %q, want %q", name, v.Text, want)
		}
	}
	if v := value(t, d, "MAC"); v.Field.Category != CatMAC || len(v.Field.Parts) != 6 || v.Field.Bits != 48 {
		t.Errorf("MAC field %+v", v.Field)
	}
	if !strings.Contains(d.CodingScheme, "RS(44,32)") || len(d.Blocks) != 11 {
		t.Errorf("%q, %d blocks", d.CodingScheme, len(d.Blocks))
	}
}

// The model takes no bits into a write-protected block, whatever is
// programmed
func TestModelWriteProtection(t *testing.T) {
	c := chip(t, "esp32c3")
	m := NewModel(c, chips.Revision{})
	m.Set(0, []uint32{1 << 22}) // WR_DIS.BLOCK_USR_DATA
	l := m.l
	if err := m.WriteReg(l.pgmData, 0xFF, 0xFFFFFFFF, 0); err != nil {
		t.Fatal(err)
	}
	m.WriteReg(l.conf, opWrite, 0xFFFF, 0)
	m.WriteReg(l.cmd, 3<<cmdBlkNumS|cmdPgm, 0xFFFFFFFF, 0)
	if m.Programs != 1 || m.Fuses(3)[0] != 0 {
		t.Errorf("programs %d, BLOCK3 %08x", m.Programs, m.Fuses(3))
	}
	// a PGM with the read op code is not a program
	m.WriteReg(l.conf, opRead, 0xFFFF, 0)
	m.WriteReg(l.cmd, 2<<cmdBlkNumS|cmdPgm, 0xFFFFFFFF, 0)
	if m.BadCommands != 1 || m.Fuses(2)[0] != 0 {
		t.Errorf("bad commands %d", m.BadCommands)
	}
}

func TestPlanWarnings(t *testing.T) {
	c := chip(t, "esp32c3")
	m := NewModel(c, chips.Revision{})
	d := readModel(t, c, chips.Revision{}, m)
	p, err := PlanBurn(d, []Change{Uint("DIS_DOWNLOAD_MODE", 1), Uint("WR_DIS.SECURE_VERSION", 1)})
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(p.Warnings, "\n")
	for _, want := range []string{"can't be undone", "DIS_DOWNLOAD_MODE: a download mode is disabled",
		"WR_DIS bit 18 write-protects for good:", "SECURE_VERSION"} {
		if !strings.Contains(w, want) {
			t.Errorf("no %q in\n%s", want, w)
		}
	}
	if len(p.Changes) != 2 || p.Changes[0] != "DIS_DOWNLOAD_MODE: 0 → 1" {
		t.Errorf("changes %q", p.Changes)
	}
}
