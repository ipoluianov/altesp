package ops

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/internal/loader"
	"github.com/ipoluianov/altesp/internal/serial"
)

// partitionTable builds a table as ESP-IDF writes it: 32-byte entries
// (magic AA 50, type, subtype, offset, size, name, flags), the MD5 entry
// (EB EB, 14 x FF, the MD5 of the entries) and 0xFF after
func partitionTable() []byte {
	entry := func(name string, typ, sub byte, off, size uint32) []byte {
		e := make([]byte, 32)
		e[0], e[1], e[2], e[3] = 0xAA, 0x50, typ, sub
		binary.LittleEndian.PutUint32(e[4:], off)
		binary.LittleEndian.PutUint32(e[8:], size)
		copy(e[12:28], name)
		return e
	}
	var t []byte
	t = append(t, entry("nvs", 1, 0x02, 0x9000, 0x6000)...)
	t = append(t, entry("phy_init", 1, 0x01, 0xF000, 0x1000)...)
	t = append(t, entry("factory", 0, 0x00, 0x10000, 0x100000)...)
	sum := md5.Sum(t)
	m := make([]byte, 32)
	m[0], m[1] = 0xEB, 0xEB
	for i := 2; i < 16; i++ {
		m[i] = 0xFF
	}
	copy(m[16:], sum[:])
	t = append(t, m...)
	return append(t, bytes.Repeat([]byte{0xFF}, 0xC00-len(t))...)
}

// appHead builds the start of an application image (esp_image_header_t,
// esp_image_segment_header_t, esp_app_desc_t of ESP-IDF esp_app_format.h
// and esp_app_desc.h) for the chip ID
func appHead(chipID uint16, project, version string) []byte {
	h := make([]byte, 0x120)
	h[0], h[1] = 0xE9, 1                          // magic, one segment
	binary.LittleEndian.PutUint16(h[12:], chipID) // extended header: chip_id
	binary.LittleEndian.PutUint16(h[17:], 0xFFFF) // max_chip_rev_full
	binary.LittleEndian.PutUint32(h[24:], 0x3C000020)
	binary.LittleEndian.PutUint32(h[28:], 0x100)
	d := h[32:]
	binary.LittleEndian.PutUint32(d[0:], 0xABCD5432)
	copy(d[16:48], version)
	copy(d[48:80], project)
	copy(d[80:96], "12:00:00")
	copy(d[96:112], "Nov  2 2026")
	copy(d[112:144], "v5.4")
	return h
}

// appImage is a whole application image: appHead, the padding and the
// checksum byte (0xEF XOR the segment data) at the end of a 16-byte block
func appImage(chipID uint16) []byte {
	img := appHead(chipID, "app", "1.0")
	sum := byte(0xEF)
	for _, b := range img[32:] {
		sum ^= b
	}
	for (len(img)+1)%16 != 0 {
		img = append(img, 0)
	}
	return append(img, sum)
}

func c3Board() loader.SimBoard {
	si := make([]byte, 20)
	binary.LittleEndian.PutUint32(si[12:], 5) // ESP32-C3
	flash := bytes.Repeat([]byte{0xFF}, 4<<20)
	copy(flash[0x8000:], partitionTable())
	copy(flash[0x10000:], appHead(5, "sensor-node", "1.4.2"))
	return loader.SimBoard{Circuit: loader.ResetUSBJTAG, SecurityInfo: si, EncryptWord: true, Flash: flash}
}

var c3Port = serial.PortInfo{Name: "/dev/ttyACM0", Short: "ttyACM0", USB: true, VID: 0x303a, PID: 0x1001}

// connectSim connects to a simulated board
func connectSim(t *testing.T, b loader.SimBoard, info serial.PortInfo, opt Options) (*Session, *loader.Sim) {
	t.Helper()
	sim := loader.NewSim(b)
	old := openPort
	openPort = func(serial.PortInfo) (serial.Port, error) { return sim, nil }
	t.Cleanup(func() { openPort = old; sim.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	s, err := Connect(ctx, info, opt)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return s, sim
}

func TestConnectReadsCard(t *testing.T) {
	s, sim := connectSim(t, c3Board(), c3Port, Options{})
	b := s.Board()
	if b.Chip == nil || b.Chip.Name != "ESP32-C3" {
		t.Fatalf("chip %v", b.Chip)
	}
	if !b.Stub || sim.Mode() != "stub" {
		t.Fatalf("stub %v (%v), sim %s", b.Stub, b.StubErr, sim.Mode())
	}
	if b.PartitionTableAddr != 0x8000 || len(b.Partitions) != 3 {
		t.Fatalf("partitions at 0x%X: %v", b.PartitionTableAddr, b.Partitions)
	}
	app := b.Apps["factory"]
	if app == nil || app.App == nil || app.App.ProjectName != "sensor-node" || app.App.Version != "1.4.2" {
		t.Fatalf("app %+v", app)
	}
	sum := b.Summary()
	if sum.Chip != "ESP32-C3" || sum.Loader != "stub" || len(sum.Apps) != 1 {
		t.Fatalf("summary %+v", sum)
	}
	if AppAddress(b) != 0x10000 {
		t.Fatalf("app address 0x%X", AppAddress(b))
	}
	// Built-in USB: no baud change
	if b.Baud != 115200 {
		t.Fatalf("baud %d", b.Baud)
	}
}

func TestConnectESP32OverBridge(t *testing.T) {
	flash := bytes.Repeat([]byte{0xFF}, 4<<20)
	copy(flash[0x8000:], partitionTable())
	b := loader.SimBoard{Circuit: loader.ResetBridge, Magic: 0x00f01d83, ReadSlow: true, Flash: flash}
	info := serial.PortInfo{Name: "/dev/ttyUSB0", Short: "ttyUSB0", USB: true, VID: 0x10c4, PID: 0xea60}
	s, sim := connectSim(t, b, info, Options{})
	bd := s.Board()
	if bd.Chip == nil || bd.Chip.Name != "ESP32" {
		t.Fatalf("chip %v", bd.Chip)
	}
	if !bd.Stub || sim.Baud() != DefaultBaud {
		t.Fatalf("stub %v, baud %d", bd.Stub, sim.Baud())
	}
}

func TestROMOnly(t *testing.T) {
	s, _ := connectSim(t, c3Board(), c3Port, Options{NoStub: true})
	b := s.Board()
	if b.Stub || b.CanRead {
		t.Fatalf("stub %v, can read %v", b.Stub, b.CanRead)
	}
	if _, err := s.Read(context.Background(), 0, 16, nil); !errors.Is(err, ErrCantRead) {
		t.Fatalf("Read: %v", err)
	}
	// Erasing goes through FLASH_BEGIN
	if err := s.Erase(context.Background(), 0x9000, 0x1000); err != nil {
		t.Fatalf("Erase: %v", err)
	}
}

func TestFlashAndVerify(t *testing.T) {
	s, sim := connectSim(t, c3Board(), c3Port, Options{})
	ctx := context.Background()
	app := append(appHead(5, "new", "2.0"), bytes.Repeat([]byte{0x5A}, 50000)...)
	items := []Item{{Name: "app.bin", Addr: 0x10000, Data: app}, {Name: "nvs.bin", Addr: 0x9000, Data: bytes.Repeat([]byte{1}, 0x6000)}}
	var seen []int
	err := s.Flash(ctx, items, FlashOptions{Verify: true}, func(i, done, total int) {
		if done == total {
			seen = append(seen, i)
		}
	})
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	f := sim.FlashData()
	if !bytes.Equal(f[0x10000:0x10000+len(app)], app) || f[0x9000] != 1 {
		t.Fatal("the flash doesn't hold the files")
	}
	if len(seen) != 2 {
		t.Fatalf("progress for %v", seen)
	}
	// Nothing changed: nothing is written again
	before := sim.Commands[0x11]
	if err := s.Flash(ctx, items, FlashOptions{Verify: true, OnlyChanged: true}, nil); err != nil {
		t.Fatal(err)
	}
	if sim.Commands[0x11] != before {
		t.Fatal("unchanged files were written again")
	}
	got, err := s.Read(ctx, 0x10000, len(app), nil)
	if err != nil || !bytes.Equal(got, app) {
		t.Fatalf("Read: %v", err)
	}
	if err := s.EraseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sim.FlashData()[:0x20000], bytes.Repeat([]byte{0xFF}, 0x20000)) {
		t.Fatal("EraseAll left data")
	}
	if err := s.Close(ctx, true); err != nil {
		t.Fatal(err)
	}
}

func TestCheck(t *testing.T) {
	s, _ := connectSim(t, c3Board(), c3Port, Options{})
	b := s.Board()
	items := []Item{
		{Name: "s3-app.bin", Addr: 0x10000, Data: appImage(9)}, // built for the S3
		{Name: "a.bin", Addr: 0x20000, Data: make([]byte, 0x2000)},
		{Name: "b.bin", Addr: 0x21000, Data: make([]byte, 0x100)}, // overlaps a.bin
		{Name: "big.bin", Addr: 0x3F0000, Data: make([]byte, 0x20000)},
	}
	problems := map[string]Severity{}
	text := ""
	for _, p := range Check(b, items) {
		problems[p.Item] = max(problems[p.Item], p.Severity)
		text += p.String() + "\n"
	}
	for _, want := range []string{"s3-app.bin: is built for ESP32-S3", "b.bin: overlaps a.bin", "big.bin: ends at"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	if problems["s3-app.bin"] != Fatal || problems["b.bin"] != Fatal {
		t.Errorf("not fatal: %v", problems)
	}
	// The simulated board doesn't tell its flash size: past the assumed
	// size is only a warning
	if b.FlashSizeKnown || problems["big.bin"] != Warning {
		t.Errorf("big.bin: %v, size known %v", problems["big.bin"], b.FlashSizeKnown)
	}
	// The right image passes
	if ps := Check(b, []Item{{Name: "ok.bin", Addr: 0x10000, Data: appImage(5)}}); len(ps) != 0 {
		t.Errorf("problems with a good image: %v", ps)
	}
}

func TestSecureDownloadBoard(t *testing.T) {
	b := c3Board()
	b.SecureDownload = true
	s, _ := connectSim(t, b, c3Port, Options{})
	bd := s.Board()
	if !bd.SecureDownload || bd.Stub || bd.Chip == nil {
		t.Fatalf("board %+v", bd)
	}
	if err := s.EraseAll(context.Background()); err == nil {
		t.Fatal("erased in Secure Download Mode")
	}
	// Plain writing works, without verification
	if err := s.Flash(context.Background(), []Item{{Name: "x", Addr: 0x10000, Data: []byte{1, 2, 3}}}, FlashOptions{Verify: true}, nil); err != nil {
		t.Fatalf("Flash in Secure Download Mode: %v", err)
	}
}

func TestParseNumber(t *testing.T) {
	for in, want := range map[string]uint32{"0x10000": 0x10000, "65536": 65536, "4MB": 4 << 20, "64K": 64 << 10, "0x1B": 0x1B} {
		got, err := ParseNumber(in)
		if err != nil || got != want {
			t.Errorf("%s = %d, %v", in, got, err)
		}
	}
	if _, err := ParseNumber("ten"); err == nil {
		t.Error("ten parsed")
	}
}

func TestPickPort(t *testing.T) {
	old := listPorts
	defer func() { listPorts = old }()
	listPorts = func() ([]serial.PortInfo, error) {
		return []serial.PortInfo{{Name: "/dev/ttyS0", Short: "ttyS0"}, c3Port}, nil
	}
	p, err := PickPort("")
	if err != nil || p.Name != c3Port.Name {
		t.Fatalf("PickPort = %v, %v", p.Name, err)
	}
	listPorts = func() ([]serial.PortInfo, error) { return []serial.PortInfo{{Name: "/dev/ttyS0"}}, nil }
	if _, err := PickPort(""); !errors.Is(err, ErrNoBoard) {
		t.Fatalf("no board: %v", err)
	}
}
