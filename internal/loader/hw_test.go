package loader

import (
	"context"
	"crypto/md5"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/internal/serial"
)

// The tests on a real board run only when ALTESP_HW_PORT names its port
// and ALTESP_HW_STUB its stub ("esp32"). They only read: nothing on the
// board is written or erased.
func hwLoader(t *testing.T) *Loader {
	t.Helper()
	name, stub := os.Getenv("ALTESP_HW_PORT"), os.Getenv("ALTESP_HW_STUB")
	if name == "" || stub == "" {
		t.Skip("no board: set ALTESP_HW_PORT and ALTESP_HW_STUB")
	}
	port, err := serial.Open(name, serial.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { port.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	l, err := Connect(ctx, port, serial.PortInfo{Name: name}, Options{Reset: ResetBridge})
	if err != nil {
		t.Fatal(err)
	}
	img, err := StubFor(stub)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.LoadStub(ctx, img); err != nil {
		t.Fatal(err)
	}
	if err := l.SPIAttach(ctx); err != nil {
		t.Fatal(err)
	}
	if baud, _ := strconv.Atoi(os.Getenv("ALTESP_HW_BAUD")); baud > 0 && (baud != syncBaud || os.Getenv("ALTESP_HW_FORCE") != "") {
		if err := l.ChangeBaud(ctx, baud); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.SetFlashSize(ctx, 4<<20); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestHardwareRead(t *testing.T) {
	l := hwLoader(t)
	ctx := context.Background()
	for _, size := range []int{0x1000, 0x6000, 0x10000} {
		start := time.Now()
		data, err := l.ReadFlash(ctx, 0, size, nil)
		if err != nil {
			t.Fatalf("read 0x%X: %v", size, err)
		}
		sum, err := l.FlashMD5(ctx, 0, size)
		if err != nil {
			t.Fatal(err)
		}
		if got := md5sum(data); got != sum {
			t.Fatalf("read 0x%X: MD5 differs", size)
		}
		t.Logf("read 0x%X in %v", size, time.Since(start))
	}
}

func md5sum(b []byte) [16]byte { return md5.Sum(b) }

// TestHardwareDump saves a region read at the sync rate, for looking at
func TestHardwareDump(t *testing.T) {
	out := os.Getenv("ALTESP_HW_DUMP")
	if out == "" {
		t.Skip("set ALTESP_HW_DUMP to a file")
	}
	l := hwLoader(t)
	data, err := l.ReadFlash(context.Background(), 0, 0x10000, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(out, data, 0o644)
}
