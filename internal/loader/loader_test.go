package loader

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/internal/serial"
)

func TestSLIP(t *testing.T) {
	p := []byte{0x01, slipEnd, 0x02, slipEsc, slipEnd, slipEsc, 0x03}
	enc := slipEncode(p)
	if bytes.Count(enc, []byte{slipEnd}) != 2 {
		t.Fatalf("0xC0 inside the frame: % X", enc)
	}
	var d slipDecoder
	var got [][]byte
	stream := append([]byte("boot text\r\n"), enc...)
	stream = append(stream, slipEncode([]byte("OHAI"))...)
	// Fed byte by byte: a packet may be split anywhere
	for _, b := range stream {
		d.feed([]byte{b}, func(p []byte) { got = append(got, p) })
	}
	if len(got) != 2 || !bytes.Equal(got[0], p) || string(got[1]) != "OHAI" {
		t.Fatalf("decoded %q", got)
	}
	if text := string(d.takeText()); text != "boot text\r\n" {
		t.Fatalf("text %q", text)
	}
}

func TestParseResponse(t *testing.T) {
	// The READ_REG answer of the protocol documentation's trace
	p := []byte{0x01, 0x0a, 0x02, 0x00, 0x09, 0x00, 0x00, 0x00, 0x00, 0x00}
	r, ok := parseResponse(p, 4)
	if !ok || r.op != opReadReg || r.value != 9 || r.status != 0 || len(r.data) != 0 {
		t.Fatalf("parsed %+v %v", r, ok)
	}
	// A stub failure: code 0xC100 big-endian
	p = []byte{0x01, 0x03, 0x02, 0x00, 0, 0, 0, 0, 0xC1, 0x00}
	r, _ = parseResponse(p, 2)
	e := &Error{Op: r.op, Status: r.status, Code: r.code, Stub: true}
	if e.Reason() != "bad data checksum" {
		t.Fatalf("reason %q", e.Reason())
	}
}

func c3Security() []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint32(b[12:], 5) // ESP_CHIP_ID_ESP32C3
	binary.LittleEndian.PutUint32(b[16:], 4)
	return b
}

func connect(t *testing.T, b SimBoard, info serial.PortInfo, opt Options) (*Loader, *Sim) {
	t.Helper()
	sim := NewSim(b)
	t.Cleanup(func() { sim.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	l, err := Connect(ctx, sim, info, opt)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return l, sim
}

var bridgeInfo = serial.PortInfo{Name: "/dev/ttyUSB0", USB: true, VID: 0x10c4, PID: 0xea60}
var usbJTAGInfo = serial.PortInfo{Name: "/dev/ttyACM0", USB: true, VID: 0x303a, PID: 0x1001}

func TestConnectBridge(t *testing.T) {
	l, sim := connect(t, SimBoard{Circuit: ResetBridge, Magic: 0x00f01d83, Flash: make([]byte, 1<<20)}, bridgeInfo, Options{})
	if sim.Mode() != "rom" || sim.DownloadBoots != 1 || l.Reset() != ResetBridge {
		t.Fatalf("mode %s, %d download boots, reset %v", sim.Mode(), sim.DownloadBoots, l.Reset())
	}
	if l.statusLen != 4 {
		t.Fatalf("status length %d", l.statusLen)
	}
	v, err := l.ReadReg(context.Background(), 0x40001000)
	if err != nil || v != 0x00f01d83 {
		t.Fatalf("ReadReg = %08x, %v", v, err)
	}
	// The ESP32 doesn't know GET_SECURITY_INFO
	_, err = l.SecurityInfo(context.Background())
	var e *Error
	if !errors.As(err, &e) || !e.Unsupported() {
		t.Fatalf("SecurityInfo on ESP32: %v", err)
	}
}

func TestConnectUSBJTAG(t *testing.T) {
	l, sim := connect(t, SimBoard{Circuit: ResetUSBJTAG, SecurityInfo: c3Security(), EncryptWord: true, Flash: make([]byte, 1<<20)}, usbJTAGInfo, Options{})
	if sim.Mode() != "rom" || l.Reset() != ResetUSBJTAG {
		t.Fatalf("mode %s, reset %v", sim.Mode(), l.Reset())
	}
	si, err := l.SecurityInfo(context.Background())
	if err != nil || !si.HasChipID || si.ChipID != 5 || si.ECOVersion != 4 {
		t.Fatalf("SecurityInfo = %+v, %v", si, err)
	}
	if err := l.RunApp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sim.Mode() != "app" {
		t.Fatalf("after RunApp the board is in %s", sim.Mode())
	}
}

func TestConnectAutoTriesOtherCircuit(t *testing.T) {
	// An Espressif ID, but the board resets through a bridge circuit
	info := usbJTAGInfo
	_, sim := connect(t, SimBoard{Circuit: ResetBridge, Magic: 1, Flash: make([]byte, 4096)}, info, Options{})
	if sim.Mode() != "rom" {
		t.Fatalf("mode %s", sim.Mode())
	}
}

func TestConnectFailsWithBootText(t *testing.T) {
	sim := NewSim(SimBoard{Circuit: ResetUSBJTAG, AppLog: "I (31) boot: ESP-IDF v5.4\r\n", Flash: make([]byte, 4096)})
	defer sim.Close()
	_, err := Connect(context.Background(), sim, bridgeInfo, Options{Reset: ResetBridge, Attempts: 1})
	var e *ErrNoSync
	if !errors.As(err, &e) {
		t.Fatalf("err = %v", err)
	}
	// The bridge sequence only resets this board into its application,
	// whose boot messages tell the user what happened
	if !strings.Contains(e.Text, "SPI_FAST_FLASH_BOOT") || !strings.Contains(e.Text, "ESP-IDF v5.4") {
		t.Fatalf("boot text %q", e.Text)
	}
	if !strings.Contains(e.Error(), "did not enter download mode") {
		t.Fatalf("message %q", e.Error())
	}
}

func TestManualWait(t *testing.T) {
	sim := NewSim(SimBoard{Circuit: ResetUSBJTAG, Flash: make([]byte, 4096)})
	defer sim.Close()
	asked := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l, err := Connect(ctx, sim, bridgeInfo, Options{Reset: ResetBridge, Attempts: 1, Manual: func() {
		asked = true
		// The user holds BOOT and presses RESET
		go func() {
			time.Sleep(200 * time.Millisecond)
			sim.mu.Lock()
			sim.boot(true)
			sim.mu.Unlock()
		}()
	}})
	if err != nil || !asked || l.Reset() != ResetManual {
		t.Fatalf("Connect = %v, asked %v", err, asked)
	}
}

func testData(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b[:n/2])
	// The second half compresses well
	for i := n / 2; i < n; i++ {
		b[i] = byte(i / 100)
	}
	return b
}

// exercise writes, verifies, reads and erases through the loader
func exercise(t *testing.T, l *Loader, sim *Sim, canErase bool) {
	t.Helper()
	ctx := context.Background()
	if err := l.SPIAttach(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.SetFlashSize(ctx, 1<<20); err != nil {
		t.Fatal(err)
	}
	data := testData(70000)
	for _, compress := range []bool{false, true} {
		addr := uint32(0x10000)
		if compress {
			addr = 0x30000
		}
		var last int
		if err := l.WriteFlash(ctx, addr, data, compress, func(done, total int) { last = done }); err != nil {
			t.Fatalf("WriteFlash compress=%v: %v", compress, err)
		}
		if last != len(data) {
			t.Fatalf("progress ended at %d", last)
		}
		sum, err := l.FlashMD5(ctx, addr, len(data))
		if err != nil || sum != md5.Sum(data) {
			t.Fatalf("FlashMD5 compress=%v: %v", compress, err)
		}
		if !bytes.Equal(sim.FlashData()[addr:int(addr)+len(data)], data) {
			t.Fatalf("flash differs, compress=%v", compress)
		}
	}
	got, err := l.ReadFlash(ctx, 0x10000, 9000, nil)
	if err != nil || !bytes.Equal(got, data[:9000]) {
		t.Fatalf("ReadFlash: %v", err)
	}
	if err := l.EraseRegion(ctx, 0x10000, 0x2000); err != nil {
		t.Fatalf("EraseRegion: %v", err)
	}
	if f := sim.FlashData(); f[0x10000] != 0xFF || f[0x11FFF] != 0xFF || f[0x12000] != data[0x2000] {
		t.Fatal("EraseRegion erased the wrong bytes")
	}
	err = l.EraseFlash(ctx)
	if canErase {
		if err != nil {
			t.Fatalf("EraseFlash: %v", err)
		}
		if bytes.Count(sim.FlashData(), []byte{0xFF}) != 1<<20 {
			t.Fatal("EraseFlash left data")
		}
	} else if !errors.Is(err, ErrNeedsStub) {
		t.Fatalf("EraseFlash in ROM: %v", err)
	}
}

func TestROMOperations(t *testing.T) {
	l, sim := connect(t, SimBoard{Circuit: ResetBridge, Magic: 1, ReadSlow: true, Flash: make([]byte, 1<<20)}, bridgeInfo, Options{})
	exercise(t, l, sim, false)
}

func TestROMWithoutSlowRead(t *testing.T) {
	l, _ := connect(t, SimBoard{Circuit: ResetUSBJTAG, SecurityInfo: c3Security(), EncryptWord: true, Flash: make([]byte, 1<<20)}, usbJTAGInfo, Options{})
	l.SetEncryptWord(true)
	_, err := l.ReadFlash(context.Background(), 0, 64, nil)
	if !errors.Is(err, ErrNeedsStub) {
		t.Fatalf("ReadFlash = %v", err)
	}
}

func TestStubOperations(t *testing.T) {
	l, sim := connect(t, SimBoard{Circuit: ResetUSBJTAG, SecurityInfo: c3Security(), EncryptWord: true, Flash: make([]byte, 1<<20)}, usbJTAGInfo, Options{})
	l.SetEncryptWord(true)
	stub, err := StubFor("esp32c3")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.LoadStub(ctx, stub); err != nil {
		t.Fatalf("LoadStub: %v", err)
	}
	if !l.Stub() || sim.Mode() != "stub" {
		t.Fatalf("stub %v, mode %s", l.Stub(), sim.Mode())
	}
	if err := l.ChangeBaud(ctx, 921600); err != nil || sim.Baud() != 921600 {
		t.Fatalf("ChangeBaud: %v, sim at %d", err, sim.Baud())
	}
	exercise(t, l, sim, true)
}

func TestStubThatDoesNotStart(t *testing.T) {
	l, _ := connect(t, SimBoard{Circuit: ResetBridge, Magic: 1, NoStub: true, Flash: make([]byte, 4096)}, bridgeInfo, Options{})
	stub, _ := StubFor("esp32")
	if err := l.LoadStub(context.Background(), stub); err == nil || l.Stub() {
		t.Fatal("LoadStub succeeded without OHAI")
	}
}

func TestSecureDownloadMode(t *testing.T) {
	l, _ := connect(t, SimBoard{Circuit: ResetUSBJTAG, SecurityInfo: c3Security(), SecureDownload: true, Flash: make([]byte, 4096)}, usbJTAGInfo, Options{})
	if _, err := l.ReadReg(context.Background(), 0x40001000); err == nil {
		t.Fatal("READ_REG worked in Secure Download Mode")
	}
	if _, err := l.SecurityInfo(context.Background()); err != nil {
		t.Fatalf("GET_SECURITY_INFO: %v", err)
	}
}

func TestStubsEmbedded(t *testing.T) {
	names := Stubs()
	for _, want := range []string{"esp32", "esp32s3", "esp32c3", "esp32c6", "esp32p4-rev1"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Fatalf("no stub %s among %v", want, names)
		}
	}
	for _, n := range names {
		s, err := StubFor(n)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Text) == 0 || s.Entry < s.TextStart || s.Entry >= s.TextStart+uint32(len(s.Text)) {
			t.Errorf("%s: entry 0x%X outside text 0x%X+%d", n, s.Entry, s.TextStart, len(s.Text))
		}
	}
}

func TestReadResumesAfterStall(t *testing.T) {
	l, sim := connect(t, SimBoard{Circuit: ResetBridge, Magic: 1, ReadStallAfter: 3, Flash: testData(1 << 20)}, bridgeInfo, Options{})
	stub, _ := StubFor("esp32")
	ctx := context.Background()
	if err := l.LoadStub(ctx, stub); err != nil {
		t.Fatal(err)
	}
	if err := l.ChangeBaud(ctx, 921600); err != nil {
		t.Fatal(err)
	}
	got, err := l.ReadFlash(ctx, 0x1000, 20*readPacket+100, nil)
	if err != nil {
		t.Fatalf("ReadFlash: %v", err)
	}
	if !bytes.Equal(got, sim.FlashData()[0x1000:0x1000+20*readPacket+100]) {
		t.Fatal("the data differs")
	}
	// Resumed: the whole was checked with an MD5 of the flash
	if sim.Commands[opSPIFlashMD5] == 0 {
		t.Fatal("no SPI_FLASH_MD5 after a resumed read")
	}
}
