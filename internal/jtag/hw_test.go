package jtag

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests on a real board run only when ALTESP_HW_JTAG is set: "1" for
// the first JTAG adapter found, or its USB serial number.
// ALTESP_HW_JTAG_ADAPTER limits the choice to "usb-jtag" or "esp-prog".
// ALTESP_HW_JTAG_CHIP names the chip ("esp32c3"; empty: by the IDCODE).
// They read only, unless ALTESP_HW_JTAG_SCRATCH gives the address of a
// 4 KB-aligned flash region of 64 KB that may be overwritten: it is read,
// written with a pattern, checked, and written back.
func hwDevice(t *testing.T) Device {
	t.Helper()
	sel := os.Getenv("ALTESP_HW_JTAG")
	if sel == "" {
		t.Skip("no board: set ALTESP_HW_JTAG=1 (or the USB serial number)")
	}
	devs, err := List()
	if err != nil {
		t.Fatal(err)
	}
	kind := strings.ToLower(os.Getenv("ALTESP_HW_JTAG_ADAPTER"))
	for _, d := range devs {
		if kind != "" && strings.ToLower(d.Kind()) != kind {
			continue
		}
		if sel == "1" || d.Serial == sel {
			return d
		}
	}
	t.Fatalf("no JTAG adapter %q %q among %+v", sel, kind, devs)
	return Device{}
}

func hwSession(t *testing.T) *Session {
	t.Helper()
	dev := hwDevice(t)
	s, err := OpenWith(dev, os.Getenv("ALTESP_HW_JTAG_CHIP"), Options{Status: func(m string) { t.Log(m) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Reset(true); err != nil {
			t.Errorf("reset: %v", err)
		}
		s.Close()
	})
	return s
}

func TestHWInfoAndRead(t *testing.T) {
	s := hwSession(t)
	in, _ := s.Info()
	t.Logf("%s IDCODE 0x%08x MAC %s rev %s flash %06x %d bytes; %s; memory by %s; warnings %v",
		in.Chip, in.IDCode, in.MACString(), in.Revision, in.FlashID, in.FlashSize, in.Debug, in.MemPath, in.Warnings)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	b, err := s.ReadFlash(ctx, 0, 64<<10, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("64 KB read in %v; first bytes % x", time.Since(start), b[:16])
	sum, err := s.FlashMD5(ctx, 0, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if sum != md5.Sum(b) {
		t.Fatalf("the ROM MD5 %x differs from the MD5 of the data read %x", sum, md5.Sum(b))
	}
}

func TestHWWriteScratch(t *testing.T) {
	v := os.Getenv("ALTESP_HW_JTAG_SCRATCH")
	if v == "" {
		t.Skip("set ALTESP_HW_JTAG_SCRATCH to a 64 KB region that may be overwritten")
	}
	addr, err := strconv.ParseUint(v, 0, 32)
	if err != nil || addr%sectorSize != 0 {
		t.Fatalf("bad ALTESP_HW_JTAG_SCRATCH %q", v)
	}
	s := hwSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a := uint32(addr)
	old, err := s.ReadFlash(ctx, a, 64<<10, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := pattern(64<<10-100, time.Now().UnixNano())
	start := time.Now()
	if err := s.WriteFlash(ctx, a+50, data, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("64 KB written and verified in %v", time.Since(start))
	got, err := s.ReadFlash(ctx, a+50, len(data), nil)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read back: %v", err)
	}
	if err := s.WriteFlash(ctx, a, old, nil); err != nil {
		t.Fatalf("restoring: %v", err)
	}
}

// An Xtensa chip (ESP32-S3 over its USB-JTAG, ESP32 or ESP32-S3 over
// ESP-Prog): the chain and the debug module are probed and the session
// stops with ErrXtensaUnavailable. Run with ALTESP_HW_JTAG_CHIP=esp32s3
// or esp32. It changes only the XDM power control (debug domain kept on).
func TestHWXtensaProbe(t *testing.T) {
	dev := hwDevice(t)
	chip := os.Getenv("ALTESP_HW_JTAG_CHIP")
	if chip != "esp32" && chip != "esp32s3" {
		t.Skip("set ALTESP_HW_JTAG_CHIP=esp32s3 or esp32")
	}
	_, err := OpenWith(dev, chip, Options{NoReset: true, Status: func(m string) { t.Log(m) }})
	var xe *XtensaError
	if !errors.As(err, &xe) {
		t.Fatalf("expected the Xtensa probe to stop with XtensaError, got %v", err)
	}
	t.Logf("TAP %d of %d: %v", xe.TAP, xe.TAPs, xe.Probe)
}
