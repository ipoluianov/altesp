package chips

import (
	"errors"
	"slices"
	"testing"

	"github.com/ipoluianov/altesp/internal/loader"
)

func TestTable(t *testing.T) {
	stubs := loader.Stubs()
	keys := map[string]bool{}
	ids := map[uint16]bool{}
	names := map[string]bool{}
	for _, c := range All() {
		if keys[c.Key] || ids[c.ChipID] || names[c.Name] {
			t.Fatalf("%s: duplicate key, ID or name", c.Name)
		}
		keys[c.Key], ids[c.ChipID], names[c.Name] = true, true, true
		if !slices.Contains(stubs, c.Key) {
			t.Errorf("%s: no stub %q", c.Name, c.Key)
		}
		if ByKey(c.Key) != c || ByChipID(uint32(c.ChipID)) != c {
			t.Errorf("%s: not found by its key or ID", c.Name)
		}
		if c.ROMEncryptWord == (c.Key == "esp32") {
			t.Errorf("%s: ROMEncryptWord %v", c.Name, c.ROMEncryptWord)
		}
		if c.Stage < 1 || c.Stage > 5 {
			t.Errorf("%s: stage %d", c.Name, c.Stage)
		}
		r := c.regs
		if r.efuse[0] == 0 || r.efuse[1] == 0 || r.efuse[2] == 0 || r.spi.base == 0 || r.mac.n != 48 || len(r.minor) == 0 {
			t.Errorf("%s: register data missing", c.Name)
		}
		if len(r.major) == 0 && r.extra == nil {
			t.Errorf("%s: no major revision", c.Name)
		}
	}
	// Every stub is used by a chip, the P4 rev1 one by the old P4
	for _, s := range stubs {
		used := false
		for _, c := range All() {
			if c.StubName(Revision{}) == s || c.StubName(Revision{3, 1}) == s {
				used = true
			}
		}
		if !used {
			t.Errorf("stub %s isn't used by any chip", s)
		}
	}

	boot := map[string]uint32{
		"esp32": 0x1000, "esp32s2": 0x1000, "esp32s3": 0, "esp32c3": 0,
		"esp32c2": 0, "esp32c6": 0, "esp32h2": 0, "esp32c61": 0, "esp32h21": 0,
		"esp32c5": 0x2000, "esp32p4": 0x2000, "esp32h4": 0x2000, "esp32s31": 0x2000,
	}
	stage := map[string]int{
		"esp32": 1, "esp32s3": 1, "esp32c3": 1, "esp32s2": 2, "esp32c6": 2,
		"esp32h2": 3, "esp32c2": 3,
	}
	for key, addr := range boot {
		c := ByKey(key)
		if c == nil {
			t.Fatalf("no chip %s", key)
		}
		if c.BootloaderAddr != addr {
			t.Errorf("%s: bootloader at 0x%X", key, c.BootloaderAddr)
		}
		want := 5
		if s, ok := stage[key]; ok {
			want = s
		}
		if c.Stage != want {
			t.Errorf("%s: stage %d", key, c.Stage)
		}
	}
	if len(All()) != len(boot) {
		t.Errorf("%d chips", len(All()))
	}

	if ByKey("esp8266") != nil || ByChipID(0xFFFF) != nil || ByMagic(0) != nil || ByMagic(0x12345678) != nil {
		t.Fatal("unknown chip found")
	}
	if ByMagic(0x00F01D83).Key != "esp32" || ByMagic(0x000007C6).Key != "esp32s2" {
		t.Fatal("magic")
	}
	if ByChipID(9).Name != "ESP32-S3" || ByChipID(0).Key != "esp32" {
		t.Fatal("chip ID")
	}

	p4 := ByKey("esp32p4")
	if p4.StubName(Revision{1, 0}) != "esp32p4-rev1" || p4.StubName(Revision{3, 0}) != "esp32p4" {
		t.Fatal("P4 stub")
	}
	if ByKey("esp32c3").StubName(Revision{0, 4}) != "esp32c3" {
		t.Fatal("C3 stub")
	}
	r := Revision{3, 1}
	if r.String() != "v3.1" || r.Full() != 301 || RevisionFromFull(301) != r {
		t.Fatal("revision")
	}
}

func TestDecodeSecurity(t *testing.T) {
	s := DecodeSecurity(0, 0)
	if s.SecureBoot || s.FlashEncryption || s.SecureDownload || s.JTAGDisabled || s.USBDisabled {
		t.Fatalf("plain chip: %+v", s)
	}
	s = DecodeSecurity(1<<0|1<<2|1<<7|1<<8, 0b001)
	if !s.SecureBoot || !s.SecureDownload || !s.JTAGDisabled || s.JTAGSoftDisabled || !s.USBDisabled || !s.FlashEncryption {
		t.Fatalf("protected chip: %+v", s)
	}
	s = DecodeSecurity(1<<6|1<<4|1<<9, 0b011)
	if !s.JTAGDisabled || !s.JTAGSoftDisabled || s.FlashEncryption || !s.KeyRevoked[1] || s.KeyRevoked[0] || !s.DownloadDCacheDisabled {
		t.Fatalf("soft JTAG: %+v", s)
	}
	// Flash encryption is an odd count of set bits
	for cnt, on := range map[byte]bool{0: false, 1: true, 3: false, 7: true, 0x7F: true, 0x3F: false} {
		if DecodeSecurity(0, cnt).FlashEncryption != on {
			t.Errorf("flash_crypt_cnt %#x", cnt)
		}
	}
	if DecodeSecurity(0xABCD, 5).Raw != 0xABCD {
		t.Fatal("raw")
	}
}

func TestFlashSizeFromJEDEC(t *testing.T) {
	for id, size := range map[uint32]int{
		0xC84016: 4 << 20,   // GigaDevice GD25Q32
		0xEF4018: 16 << 20,  // Winbond W25Q128
		0x204017: 8 << 20,   // XMC
		0xEF4019: 32 << 20,  // Winbond W25Q256
		0xEF4020: 64 << 20,  // Winbond W25Q512
		0x20BB21: 128 << 20, // Micron 1 Gbit
		0xC84014: 1 << 20,
		0xC84000: 0,
		0xFFFFFF: 0,
		0:        0,
	} {
		if got := FlashSizeFromJEDEC(id); got != size {
			t.Errorf("%06X: %d, want %d", id, got, size)
		}
	}
}

// fakeChip is the registers of a chip: a word memory, and an SPI
// controller that answers RDID
type fakeChip struct {
	t     *testing.T
	mem   map[uint32]uint32
	spi   spiRegs
	jedec uint32 // the answer to RDID; 0 for no flash
	busy  int    // how many reads of CMD the command runs
	baud  int
	fail  bool
	// ranRDID is set when the command ran
	ranRDID bool
	// linesDuring is CTRL's line bits when the command ran
	linesDuring uint32
}

func (f *fakeChip) Baud() int { return f.baud }

func (f *fakeChip) ReadReg(addr uint32) (uint32, error) {
	if f.fail {
		return 0, errors.New("no answer")
	}
	v := f.mem[addr]
	if addr == f.spi.base+spiCmdReg && v&spiCmdUSR != 0 {
		if f.busy > 0 {
			f.busy--
		} else {
			f.mem[addr] = v &^ spiCmdUSR
		}
	}
	return v, nil
}

func (f *fakeChip) WriteReg(addr, value, mask, delayUS uint32) error {
	if f.fail {
		return errors.New("no answer")
	}
	f.mem[addr] = f.mem[addr]&^mask | value&mask
	b := f.spi.base
	if addr == b+spiCmdReg && value&spiCmdUSR != 0 {
		user, user2, dlen := f.mem[b+f.spi.user], f.mem[b+f.spi.user2], f.mem[b+f.spi.misoDLen]
		if user != spiUserCommand|spiUserMISO || user2 != 7<<28|0x9F || dlen != 23 {
			f.t.Errorf("RDID set up wrong: USER %08X USER2 %08X MISO_DLEN %d", user, user2, dlen)
		}
		f.ranRDID = true
		f.linesDuring = f.mem[b+spiCtrlReg] & f.spi.lines
		// The flash answers on MISO: the first byte lands in W0's lowest
		// bits; no flash leaves the line high
		j := f.jedec
		w := uint32(0xFFFFFFFF)
		if j != 0 {
			w = j>>16&0xFF | (j>>8&0xFF)<<8 | (j&0xFF)<<16
		}
		f.mem[b+f.spi.dataW] = w
	}
	return nil
}

func newFake(t *testing.T, c *Chip, jedec uint32) *fakeChip {
	return &fakeChip{t: t, mem: map[uint32]uint32{}, spi: c.regs.spi, jedec: jedec, busy: 2}
}

// set puts bits into a block of the fake eFuse, as the CSV counts them
func (f *fakeChip) set(c *Chip, fl field, v uint64) {
	for i := 0; i < fl.n; i++ {
		bit := fl.bit + i
		addr := c.regs.efuse[fl.blk] + uint32(bit/32)*4
		if v>>i&1 != 0 {
			f.mem[addr] |= 1 << (bit % 32)
		} else {
			f.mem[addr] &^= 1 << (bit % 32)
		}
	}
}

func TestReadInfoESP32(t *testing.T) {
	c := ByKey("esp32")
	f := newFake(t, c, 0xC84016)
	f.baud = 115200
	// MAC 24:0a:c4:12:34:56 in BLK0 bits 32..79 (RDATA1 and RDATA2)
	f.mem[0x3FF5A004] = 0xC4123456
	f.mem[0x3FF5A008] = 0x00AB240A // MAC_CRC above the MAC
	// v3.1: CHIP_VER_REV1, CHIP_VER_REV2, APB_CTRL_DATE bit 31
	f.set(c, esp32ChipVerRev1, 1)
	f.set(c, esp32ChipVerRev2, 1)
	f.mem[esp32APBCtrlDate] = 0x80000000 | 0x16042000
	f.set(c, field{0, 184, 2}, 1)
	f.set(c, esp32Package3Bit, 1) // ESP32-D0WD
	// The ROM's divider at 115200 from 40 MHz: 347 + 3/16
	f.mem[esp32UART0ClkDiv] = 3<<20 | 347
	// Bits around the SPI registers that must survive
	f.mem[c.regs.spi.base+c.regs.spi.user] = 0x80000041
	f.mem[c.regs.spi.base+c.regs.spi.user2] = 0x70000005

	in, err := c.ReadInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if !in.HasMAC || in.MACString() != "24:0a:c4:12:34:56" {
		t.Errorf("MAC %s", in.MACString())
	}
	if in.Revision != (Revision{3, 1}) {
		t.Errorf("revision %s", in.Revision)
	}
	if in.Package != "ESP32-D0WD-V3" {
		t.Errorf("package %q", in.Package)
	}
	for _, feat := range []string{"Wi-Fi", "Bluetooth Classic", "BLE", "Dual core", "240MHz"} {
		if !slices.Contains(in.Features, feat) {
			t.Errorf("no feature %q in %q", feat, in.Features)
		}
	}
	if in.CrystalMHz != 40 {
		t.Errorf("crystal %d", in.CrystalMHz)
	}
	if !f.ranRDID || in.FlashID != 0xC84016 || in.FlashSize != 4<<20 {
		t.Errorf("flash %06X %d", in.FlashID, in.FlashSize)
	}
	if f.mem[c.regs.spi.base+c.regs.spi.user] != 0x80000041 || f.mem[c.regs.spi.base+c.regs.spi.user2] != 0x70000005 {
		t.Error("SPI registers not restored")
	}

	// 26 MHz crystal, single core, revision v1.0, ESP32-U4WDH
	f = newFake(t, c, 0)
	f.baud = 115200
	f.set(c, esp32ChipVerRev1, 1)
	f.set(c, esp32DisableAppCPU, 1)
	f.set(c, esp32DisableBT, 1)
	f.set(c, esp32Package3Bit, 4)
	f.mem[esp32UART0ClkDiv] = 12<<20 | 225 // 26e6/115200 = 225.69
	in, err = c.ReadInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if in.HasMAC || in.Revision != (Revision{1, 0}) || in.CrystalMHz != 26 || in.Package != "ESP32-U4WDH" {
		t.Errorf("second ESP32: %+v", in)
	}
	if slices.Contains(in.Features, "BLE") || !slices.Contains(in.Features, "Single core") {
		t.Errorf("features %q", in.Features)
	}
	// No answer to RDID: the size is the embedded flash
	if in.FlashID != 0 || in.FlashSize != 4<<20 {
		t.Errorf("flash %06X %d", in.FlashID, in.FlashSize)
	}
}

func TestReadInfoS3(t *testing.T) {
	c := ByKey("esp32s3")
	f := newFake(t, c, 0xEF4018)
	base := c.regs.efuse[1]
	// MAC f4:12:fa:01:02:03: BLK1 word 0 is the low 32 bits
	f.mem[base] = 0xFA010203
	f.mem[base+4] = 0x0000F412
	// v0.2: WAFER_VERSION_MINOR_LO = 2, the HI bit and the major 0
	f.set(c, field{1, 114, 3}, 2)
	f.set(c, field{1, 123, 3}, 2) // FLASH_CAP 4M
	f.set(c, field{1, 131, 2}, 1) // PSRAM_CAP 8M
	// The ROM left quad reads on: cleared for RDID and restored
	ctrl := c.regs.spi.base + spiCtrlReg
	f.mem[ctrl] = spiFreadQIO | 1<<13

	in, err := c.ReadInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if in.MACString() != "f4:12:fa:01:02:03" {
		t.Errorf("MAC %s", in.MACString())
	}
	if in.Revision != (Revision{0, 2}) {
		t.Errorf("revision %s", in.Revision)
	}
	if in.PSRAMSize != 8<<20 || !slices.Contains(in.Features, "Embedded flash 4MB") || !slices.Contains(in.Features, "Embedded PSRAM 8MB") {
		t.Errorf("memory %d %q", in.PSRAMSize, in.Features)
	}
	if in.CrystalMHz != 40 || in.FlashID != 0xEF4018 || in.FlashSize != 16<<20 {
		t.Errorf("info %+v", in)
	}
	if f.linesDuring != 0 || f.mem[ctrl] != spiFreadQIO|1<<13 {
		t.Errorf("CTRL: %08X during, %08X after", f.linesDuring, f.mem[ctrl])
	}

	// v0.9 is the HI bit of the minor revision
	f = newFake(t, c, 0xEF4018)
	f.set(c, field{1, 183, 1}, 1)
	f.set(c, field{1, 114, 3}, 1)
	f.set(c, field{1, 184, 2}, 1)
	in, _ = c.ReadInfo(f)
	if in.Revision != (Revision{1, 9}) {
		t.Errorf("revision %s", in.Revision)
	}
}

func TestReadInfoC3(t *testing.T) {
	c := ByKey("esp32c3")
	f := newFake(t, c, 0x204016)
	f.busy = idPolls + 1 // the command never finishes
	base := c.regs.efuse[1]
	f.mem[base] = 0x12345678
	f.mem[base+4] = 0x00005801
	f.set(c, field{1, 184, 2}, 1) // v1.1
	f.set(c, field{1, 114, 3}, 1)
	f.set(c, field{1, 123, 3}, 1) // FLASH_CAP 4M: an ESP32-C3FH4

	in, err := c.ReadInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if in.MACString() != "58:01:12:34:56:78" || in.Revision.String() != "v1.1" {
		t.Errorf("%s %s", in.MACString(), in.Revision)
	}
	if in.FlashID != 0 || in.FlashSize != 4<<20 || in.CrystalMHz != 40 {
		t.Errorf("info %+v", in)
	}

	f = newFake(t, c, 0x204016)
	in, _ = c.ReadInfo(f)
	if in.FlashID != 0x204016 || in.FlashSize != 4<<20 {
		t.Errorf("flash %06X", in.FlashID)
	}

	f.fail = true
	if _, err := c.ReadInfo(f); err == nil {
		t.Fatal("nothing read, no error")
	}
}

func TestRevisionParts(t *testing.T) {
	// The ESP32-P4's major is WAFER_VERSION_MAJOR_HI:WAFER_VERSION_MAJOR_LO
	c := ByKey("esp32p4")
	f := newFake(t, c, 0)
	f.set(c, field{1, 87, 1}, 1)
	f.set(c, field{1, 68, 2}, 1)
	f.set(c, field{1, 64, 4}, 3)
	in, err := c.ReadInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if in.Revision != (Revision{5, 3}) {
		t.Fatalf("revision %s", in.Revision)
	}
}

func TestSecurityFromEFuse(t *testing.T) {
	c := ByKey("esp32")
	f := newFake(t, c, 0)
	f.set(c, esp32FlashCryptCnt, 0b0000111)
	f.set(c, esp32AbsDone1, 1)
	f.set(c, esp32JTAGDisable, 1)
	s, err := c.SecurityFromEFuse(f)
	if err != nil {
		t.Fatal(err)
	}
	if !s.FlashEncryption || s.FlashCryptCnt != 7 || !s.SecureBoot || !s.JTAGDisabled || s.SecureDownload {
		t.Fatalf("%+v", s)
	}
	if _, err := ByKey("esp32c3").SecurityFromEFuse(f); err == nil {
		t.Fatal("C3 from eFuse")
	}
}
