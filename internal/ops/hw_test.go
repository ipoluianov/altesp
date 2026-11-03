package ops

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/internal/formats/nvs"
)

// The tests on a real board run only when ALTESP_HW_PORT names its port.
// They write: run them only on a board whose flash may be changed. Each
// puts back what it changed.

func hwSession(t *testing.T) *Session {
	t.Helper()
	name := os.Getenv("ALTESP_HW_PORT")
	if name == "" {
		t.Skip("no board: set ALTESP_HW_PORT")
	}
	info, err := PickPort(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	s, err := Connect(ctx, info, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// bootLog resets the board into its application and returns what it
// prints in a few seconds
func bootLog(t *testing.T, s *Session) string {
	t.Helper()
	info := s.Board().Port
	if err := s.Close(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	m, err := OpenMonitor(info, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := m.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	go func() {
		<-ctx.Done()
		m.Close()
	}()
	buf := make([]byte, 4096)
	for {
		n, err := m.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			return out.String()
		}
	}
}

// TestHardwareNVSEdit adds a key in a namespace of its own to the board's
// NVS, keeping the format version, checks that the firmware still boots
// and the key reads back, then writes the original partition back
func TestHardwareNVSEdit(t *testing.T) {
	s := hwSession(t)
	ctx := context.Background()
	part := os.Getenv("ALTESP_HW_NVS")
	if part == "" {
		part = "nvs"
	}
	orig, err := s.ReadPartition(ctx, part, nil)
	if err != nil {
		t.Fatal(err)
	}
	im, err := nvs.Parse(orig)
	if err != nil {
		t.Fatal(err)
	}
	version, count := im.Version, len(im.Entries)
	if err := im.Set(nvs.Entry{Namespace: "altesp_test", Key: "probe", Type: nvs.U8, Value: uint64(42)}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteNVS(ctx, part, im); err != nil {
		t.Fatal(err)
	}
	log := bootLog(t, s)
	t.Logf("boot log after the edit:\n%s", lastLines(log, 12))

	s = hwSession(t)
	back, err := s.ReadNVS(ctx, part, "")
	if err != nil {
		t.Fatal(err)
	}
	e, ok := back.Get("altesp_test", "probe")
	if !ok || e.Value != uint64(42) || back.Version != version || len(back.Problems) != 0 {
		t.Errorf("after the boot: probe %v %v, version %d (was %d), problems %v", e.Value, ok, back.Version, version, back.Problems)
	}
	if len(back.Entries) < count+1 {
		t.Errorf("%d entries after the boot, %d before the edit", len(back.Entries), count)
	}
	// Put the original back
	if err := s.WritePartition(ctx, part, orig, true, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadPartition(ctx, part, nil); !bytes.Equal(got, orig) {
		t.Error("the original NVS was not restored")
	}
	s.Close(ctx, true)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// TestHardwareEFuseRead reads the eFuses (read-only) and checks the MAC
// and the revision against what the chip table read directly
func TestHardwareEFuseRead(t *testing.T) {
	s := hwSession(t)
	defer s.Close(context.Background(), true)
	d, err := s.ReadEFuse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b := s.Board()
	t.Logf("coding: %s; %d fields; errors: %v", d.CodingScheme, len(d.Values), d.Errors)
	for _, v := range d.Values {
		switch {
		case strings.Contains(v.Field.Name, "MAC") && !strings.Contains(v.Field.Name, "CRC"),
			strings.Contains(v.Field.Name, "VER"), strings.Contains(v.Field.Name, "CHIP_PACKAGE"),
			strings.Contains(v.Field.Name, "FLASH_CRYPT_CNT"), strings.Contains(v.Field.Name, "ABS_DONE"):
			t.Logf("%-28s %s", v.Field.Name, v.Text)
		}
	}
	if b.Info.HasMAC {
		want := b.Summary().MAC
		found := false
		for _, v := range d.Values {
			if strings.EqualFold(v.Text, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no eFuse field reads the MAC %s", want)
		}
	}
	if len(d.Errors) > 0 {
		t.Errorf("eFuse errors: %v", d.Errors)
	}
}

// TestHardwareFlashAddr maps addresses of the running app to the flash,
// as its bootloader prints them ("segment 0: paddr=... vaddr=..."): set
// ALTESP_HW_MAP to "vaddr=paddr,..."
func TestHardwareFlashAddr(t *testing.T) {
	pairs := os.Getenv("ALTESP_HW_MAP")
	if pairs == "" {
		t.Skip("set ALTESP_HW_MAP")
	}
	s := hwSession(t)
	defer s.Close(context.Background(), true)
	for _, p := range strings.Split(pairs, ",") {
		v, f, _ := strings.Cut(p, "=")
		vaddr, _ := ParseNumber(v)
		paddr, _ := ParseNumber(f)
		got, err := s.FlashAddrOf(context.Background(), vaddr)
		if err != nil || got != paddr {
			t.Errorf("0x%X: 0x%X, %v; the bootloader says 0x%X", vaddr, got, err, paddr)
		}
	}
}
