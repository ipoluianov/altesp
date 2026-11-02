package chips

import (
	"errors"
	"fmt"
	"math/bits"
)

// Regs reads and writes the chip's registers: the READ_REG and WRITE_REG
// commands of the loader
type Regs interface {
	ReadReg(addr uint32) (uint32, error)
	WriteReg(addr, value, mask, delayUS uint32) error
}

// BaudRegs is a Regs that also tells the rate the chip's UART runs at.
// ReadInfo of the ESP32 needs it for the crystal: it is found from the
// divider of the UART, which is only meaningful while the ROM loader
// runs (the stub changes the clocks).
type BaudRegs interface {
	Regs
	Baud() int
}

// Info is what ReadInfo found out; zero values are unknown
type Info struct {
	Revision   Revision
	MAC        [6]byte
	HasMAC     bool
	FlashID    uint32 // JEDEC ID: manufacturer<<16 | type<<8 | capacity
	FlashSize  int    // bytes: by the JEDEC ID, else by the embedded flash
	CrystalMHz int
	PSRAMSize  int    // bytes of embedded PSRAM per eFuse
	Package    string // "ESP32-D0WD-V3" where the eFuse names it
	// Features are human readable: "Wi-Fi", "BLE", "Embedded flash 4MB"
	Features []string
}

// MACString is the MAC as "aa:bb:cc:dd:ee:ff"
func (in Info) MACString() string {
	m := in.MAC
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
}

// ReadInfo reads what it can: the MAC, the revision and the package from
// the eFuse, the crystal, and the JEDEC ID of the SPI flash by the
// user-command registers (the caller attaches the flash beforehand).
// What can't be read stays zero; the error is only for nothing read.
func (c *Chip) ReadInfo(r Regs) (Info, error) {
	var in Info
	cr := &c.regs
	e := &efuseReader{r: r, blocks: cr.efuse, words: map[uint32]uint32{}}
	var firstErr error
	got := false
	note := func(err error) bool {
		if err == nil {
			got = true
			return true
		}
		if firstErr == nil {
			firstErr = err
		}
		return false
	}

	if v, err := e.field(cr.mac); note(err) && v != 0 {
		for i := range in.MAC {
			in.MAC[i] = byte(v >> (8 * (5 - i)))
		}
		in.HasMAC = true
	}
	if len(cr.major) > 0 {
		if v, err := e.parts(cr.major); note(err) {
			in.Revision.Major = int(v)
		}
	}
	if v, err := e.parts(cr.minor); note(err) {
		in.Revision.Minor = int(v)
	}
	in.CrystalMHz = cr.xtalMHz
	in.Features = append(in.Features, cr.features...)
	embeddedFlash := 0
	if cr.flashCap.n > 0 {
		if v, err := e.field(cr.flashCap); note(err) && v != 0 {
			if mb, ok := cr.flashMB[uint32(v)]; ok {
				embeddedFlash = mb << 20
				in.Features = append(in.Features, fmt.Sprintf("Embedded flash %dMB", mb))
			} else {
				in.Features = append(in.Features, fmt.Sprintf("Embedded flash (eFuse code %d)", v))
			}
		}
	}
	if cr.psramCap.n > 0 {
		if v, err := e.field(cr.psramCap); note(err) && v != 0 {
			if mb, ok := cr.psramMB[uint32(v)]; ok {
				in.PSRAMSize = mb << 20
				in.Features = append(in.Features, fmt.Sprintf("Embedded PSRAM %dMB", mb))
			} else {
				in.Features = append(in.Features, fmt.Sprintf("Embedded PSRAM (eFuse code %d)", v))
			}
		}
	}
	if cr.extra != nil {
		note(cr.extra(e, &in))
	}

	if embeddedFlash != 0 {
		in.FlashSize = embeddedFlash
	}
	id, err := cr.spi.readID(r)
	if note(err) {
		in.FlashID = id
		if size := FlashSizeFromJEDEC(id); size != 0 {
			in.FlashSize = size
		}
	}
	if !got {
		if firstErr == nil {
			firstErr = errors.New("nothing could be read")
		}
		return in, firstErr
	}
	return in, nil
}

// efuseReader reads the fields of the eFuse; each word is read once
type efuseReader struct {
	r      Regs
	blocks [3]uint32
	words  map[uint32]uint32
}

func (e *efuseReader) word(addr uint32) (uint32, error) {
	if v, ok := e.words[addr]; ok {
		return v, nil
	}
	v, err := e.r.ReadReg(addr)
	if err != nil {
		return 0, err
	}
	e.words[addr] = v
	return v, nil
}

// field reads a field of up to 64 bits
func (e *efuseReader) field(f field) (uint64, error) {
	if f.n == 0 || f.n > 64 || f.blk >= len(e.blocks) {
		return 0, fmt.Errorf("no eFuse field %d/%d/%d", f.blk, f.bit, f.n)
	}
	var v uint64
	for i := 0; i < f.n; {
		bit := f.bit + i
		w, err := e.word(e.blocks[f.blk] + uint32(bit/32)*4)
		if err != nil {
			return 0, err
		}
		take := min(32-bit%32, f.n-i)
		part := uint64(w>>(bit%32)) & (1<<take - 1)
		v |= part << i
		i += take
	}
	return v, nil
}

// parts joins fields, the first the most significant
func (e *efuseReader) parts(fs []field) (uint64, error) {
	var v uint64
	for _, f := range fs {
		p, err := e.field(f)
		if err != nil {
			return 0, err
		}
		v = v<<f.n | p
	}
	return v, nil
}

// The JEDEC "Read Identification" command of SPI NOR flash: the answer is
// the manufacturer, the memory type and the capacity
const (
	flashCmdRDID = 0x9F
	flashIDBits  = 24
)

// idPolls is how many times CMD is read while the command runs: each read
// is a round trip over the port, much longer than the command
const idPolls = 10

// readID runs RDID by the user-command registers of the flash's SPI
// controller (the TRMs, chapter "SPI Controller", user-defined commands:
// USR_COMMAND with the command in USER2, USR_MISO with the length in
// MISO_DLEN, start by CMD.USR, which clears when done; the data comes in
// W0, the first byte in its lowest bits). USER, USER2 and CTRL are
// restored afterwards.
//
// unverified: that the CTRL line-mode bits also apply to user commands on
// every chip (they are cleared here only if the ROM left one set).
func (s spiRegs) readID(r Regs) (id uint32, err error) {
	if s.base == 0 {
		return 0, errors.New("no SPI flash registers")
	}
	read := func(off uint32) (uint32, error) { return r.ReadReg(s.base + off) }
	write := func(off, v uint32) error { return r.WriteReg(s.base+off, v, 0xFFFFFFFF, 0) }

	user, err := read(s.user)
	if err != nil {
		return 0, err
	}
	user2, err := read(s.user2)
	if err != nil {
		return 0, err
	}
	ctrl, err := read(spiCtrlReg)
	if err != nil {
		return 0, err
	}
	defer func() {
		// The registers are restored even when the command failed
		for _, w := range []struct{ off, v uint32 }{{s.user, user}, {s.user2, user2}} {
			if e := write(w.off, w.v); e != nil && err == nil {
				err = e
			}
		}
		if ctrl&s.lines != 0 {
			if e := write(spiCtrlReg, ctrl); e != nil && err == nil {
				err = e
			}
		}
	}()

	if ctrl&s.lines != 0 {
		if err := write(spiCtrlReg, ctrl&^s.lines); err != nil {
			return 0, err
		}
	}
	steps := []struct{ off, v uint32 }{
		{s.user, spiUserCommand | spiUserMISO},
		{s.user2, 7<<spiCmdBitlenS | flashCmdRDID},
		{s.misoDLen, flashIDBits - 1},
		{s.dataW, 0},
		{spiCmdReg, spiCmdUSR},
	}
	for _, st := range steps {
		if err := write(st.off, st.v); err != nil {
			return 0, err
		}
	}
	done := false
	for i := 0; i < idPolls; i++ {
		v, err := read(spiCmdReg)
		if err != nil {
			return 0, err
		}
		if v&spiCmdUSR == 0 {
			done = true
			break
		}
	}
	if !done {
		return 0, errors.New("the SPI flash command did not finish")
	}
	d, err := read(s.dataW)
	if err != nil {
		return 0, err
	}
	id = (d&0xFF)<<16 | (d>>8&0xFF)<<8 | d>>16&0xFF
	if id == 0 || id == 0xFFFFFF {
		return 0, errors.New("the SPI flash does not answer")
	}
	return id, nil
}

// FlashSizeFromJEDEC is the size in bytes by the capacity byte of a JEDEC
// ID; 0 when the code isn't known. The capacity byte is log2 of the size
// in bytes (JEDEC JEP106 manufacturers' datasheets: 0x16 is 4 MB).
//
// unverified: 0x20, 0x21, 0x22 for 64, 128 and 256 MB, the codes that
// Winbond and Micron use for their 512 Mbit and larger chips.
func FlashSizeFromJEDEC(id uint32) int {
	c := id & 0xFF
	switch {
	case c >= 0x10 && c <= 0x1A:
		return 1 << c
	case c >= 0x20 && c <= 0x22:
		return 64 << 20 << (c - 0x20)
	}
	return 0
}

// The ESP32 particulars.
//
// Fields of BLK0 (components/efuse/esp32/esp_efuse_table.csv), the major
// revision (components/hal/esp32/efuse_hal.c), the packages
// (components/soc/esp32/include/soc/efuse_defs.h, the in-package memory
// from the ESP32 datasheet v5.3 table 1-1 and its part numbering),
// APB_CTRL_DATE_REG (soc/esp32/register/soc/apb_ctrl_reg.h),
// UART_CLKDIV_REG of UART0 (soc/esp32/register/soc/uart_reg.h).
var (
	esp32DisableAppCPU = field{0, 96, 1}
	esp32Package3Bit   = field{0, 105, 3}
	esp32DisableBT     = field{0, 97, 1}
	esp32Package4Bit   = field{0, 98, 1}
	esp32CPUFreqLow    = field{0, 108, 1}
	esp32CPUFreqRated  = field{0, 109, 1}
	esp32ChipVerRev1   = field{0, 111, 1}
	esp32ChipVerRev2   = field{0, 180, 1}
)

const (
	esp32APBCtrlDate uint32 = 0x3FF6607C
	esp32UART0ClkDiv uint32 = 0x3FF40014
)

type esp32Package struct {
	name           string
	flashMB, psram int
}

var esp32Packages = map[uint64]esp32Package{
	0: {"ESP32-D0WDQ6", 0, 0},
	1: {"ESP32-D0WD", 0, 0},
	2: {"ESP32-D2WD", 2, 0}, // "2: 2 MB flash" of the part numbering
	4: {"ESP32-U4WDH", 4, 0},
	// unverified: the in-package memory of the PICO packages (the
	// ESP32-PICO datasheets, not the ESP32 one)
	5: {"ESP32-PICO-D4", 4, 0},
	6: {"ESP32-PICO-V3-02", 8, 2},
	7: {"ESP32-D0WDR2-V3", 0, 2},
}

func esp32Extra(e *efuseReader, in *Info) error {
	rev1, err := e.field(esp32ChipVerRev1)
	if err != nil {
		return err
	}
	rev2, err := e.field(esp32ChipVerRev2)
	if err != nil {
		return err
	}
	date, err := e.r.ReadReg(esp32APBCtrlDate)
	if err != nil {
		return err
	}
	switch date>>31<<2 | uint32(rev2)<<1 | uint32(rev1) {
	case 1:
		in.Revision.Major = 1
	case 3:
		in.Revision.Major = 2
	case 7:
		in.Revision.Major = 3
	default:
		in.Revision.Major = 0
	}

	if v, err := e.field(esp32DisableAppCPU); err == nil {
		in.Features = append(in.Features, featWiFi)
		if bt, err := e.field(esp32DisableBT); err == nil && bt == 0 {
			in.Features = append(in.Features, featBT, featBLE)
		}
		if v == 0 {
			in.Features = append(in.Features, featDual)
		} else {
			in.Features = append(in.Features, featSingle)
		}
	}
	low, err1 := e.field(esp32CPUFreqLow)
	rated, err2 := e.field(esp32CPUFreqRated)
	if err1 == nil && err2 == nil {
		// efuse_hal_get_rated_freq_mhz
		if low == 1 && rated == 1 {
			in.Features = append(in.Features, "160MHz")
		} else {
			in.Features = append(in.Features, "240MHz")
		}
	}
	pkg, err1 := e.field(esp32Package3Bit)
	hi, err2 := e.field(esp32Package4Bit)
	if err1 == nil && err2 == nil {
		if p, ok := esp32Packages[hi<<3|pkg]; ok {
			in.Package = p.name
			if in.Revision.Major == 3 && (pkg == 0 || pkg == 1) {
				in.Package += "-V3"
			}
			if p.flashMB != 0 {
				in.Features = append(in.Features, fmt.Sprintf("Embedded flash %dMB", p.flashMB))
				in.FlashSize = p.flashMB << 20
			}
			if p.psram != 0 {
				in.PSRAMSize = p.psram << 20
				in.Features = append(in.Features, fmt.Sprintf("Embedded PSRAM %dMB", p.psram))
			}
		}
	}

	if br, ok := e.r.(BaudRegs); ok && br.Baud() > 0 {
		if v, err := e.r.ReadReg(esp32UART0ClkDiv); err == nil {
			in.CrystalMHz = crystalFromDivider(v&0xFFFFF, v>>20&0xF, br.Baud(), 26, 40)
		}
	}
	return nil
}

// crystalFromDivider is the crystal by the divider of a UART clocked by it
// (div + frag/16 = clock / baud), rounded to one of the crystals the chip
// supports; 0 when none is within 10%.
//
// unverified: that the UART of the ROM loader runs from the crystal.
func crystalFromDivider(div, frag uint32, baud int, choices ...int) int {
	hz := (float64(div) + float64(frag)/16) * float64(baud)
	for _, mhz := range choices {
		f := float64(mhz) * 1e6
		if hz > f*0.9 && hz < f*1.1 {
			return mhz
		}
	}
	return 0
}

// esp32s2Extra names the package (components/esp_hw_support/port/esp32s2/
// chip_info.c: 0 is the ESP32-S2, 1 and 2 have embedded flash)
var s2Package = field{1, 128, 4}

func esp32s2Extra(e *efuseReader, in *Info) error {
	pkg, err := e.field(s2Package)
	if err != nil {
		return err
	}
	switch pkg {
	case 0:
		in.Package = "ESP32-S2"
	case 1:
		in.Package = "ESP32-S2FH16"
	case 2:
		in.Package = "ESP32-S2FH32"
	}
	return nil
}

// The crystal of the ESP32-C5: 40 or 48 MHz. With XTAL_48M_SEL_MODE set
// the eFuse XTAL_48M_SEL decides, 48 MHz with an odd number of its bits
// set; otherwise a strapping pin does (esp_efuse_table.csv).
var (
	c5Xtal48MSel     = field{0, 169, 3}
	c5Xtal48MSelMode = field{0, 172, 1}
)

func esp32c5Extra(e *efuseReader, in *Info) error {
	mode, err := e.field(c5Xtal48MSelMode)
	if err != nil {
		return err
	}
	if mode == 0 {
		return nil
	}
	sel, err := e.field(c5Xtal48MSel)
	if err != nil {
		return err
	}
	if bits.OnesCount64(sel)%2 == 1 {
		in.CrystalMHz = 48
	} else {
		in.CrystalMHz = 40
	}
	return nil
}
