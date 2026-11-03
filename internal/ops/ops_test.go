package ops

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/internal/loader"
	"github.com/ipoluianov/altesp/internal/serial"
)

var c3Port = simPort

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

func TestBaudFallback(t *testing.T) {
	b := c3Board()
	b.Circuit = loader.ResetBridge
	b.MaxBaud = 460800
	info := serial.PortInfo{Name: "/dev/ttyUSB0", Short: "ttyUSB0", USB: true, VID: 0x1a86, PID: 0x7523}
	// Every open of the port is the same board powered on again, as a
	// real port opened anew
	var sim *loader.Sim
	old := openPort
	openPort = func(serial.PortInfo) (serial.Port, error) {
		sim = loader.NewSim(b)
		return sim, nil
	}
	defer func() { openPort = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := Connect(ctx, info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx, false)
	if s.Board().Baud != 460800 || sim.Baud() != 460800 {
		t.Fatalf("working at %d (sim %d), want 460800", s.Board().Baud, sim.Baud())
	}
	found := false
	for _, n := range s.Board().Notes {
		found = found || strings.Contains(n, "921600")
	}
	if !found {
		t.Fatalf("no note about the fallback: %v", s.Board().Notes)
	}
}

func TestEncryptedFlash(t *testing.T) {
	s, sim := connectSim(t, c3Board(), c3Port, Options{})
	ctx := context.Background()
	data := []byte("plain firmware that the chip encrypts")
	if err := s.Flash(ctx, []Item{{Name: "app.bin", Addr: 0x200000, Data: data}}, FlashOptions{Encrypt: true, Verify: true}, nil); err != nil {
		t.Fatal(err)
	}
	f := sim.FlashData()
	for i, c := range data {
		if f[0x200000+i] != c^0x5A {
			t.Fatalf("byte %d not written encrypted", i)
		}
	}
	// Padded to 32 bytes with 0xFF (encrypted too)
	if f[0x200000+len(data)] != 0xFF^0x5A || f[0x200000+64] != 0xFF {
		t.Fatal("the padding is wrong")
	}
	if err := s.Flash(ctx, []Item{{Name: "x", Addr: 0x200010, Data: data}}, FlashOptions{Encrypt: true}, nil); err == nil {
		t.Fatal("an unaligned encrypted write was taken")
	}
	// Check: development mode warns, release mode refuses
	b := s.Board()
	b.Security.FlashEncryption = true
	ps := Check(b, []Item{{Name: "a", Addr: 0x10000, Data: []byte{1}}})
	if len(ps) == 0 || ps[len(ps)-1].Severity != Warning {
		t.Fatalf("development mode: %v", ps)
	}
	b.EncryptionRelease = true
	ps = Check(b, []Item{{Name: "a", Addr: 0x10000, Data: []byte{1}}})
	if len(ps) == 0 || ps[len(ps)-1].Severity != Fatal {
		t.Fatalf("release mode: %v", ps)
	}
}
