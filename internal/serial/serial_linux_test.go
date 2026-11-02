package serial

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY returns the master of a new pseudo terminal and the path of its
// slave, which stands for a serial port in the tests
func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no /dev/ptmx:", err)
	}
	fd := int(m.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	return m, fmt.Sprintf("/dev/pts/%d", n)
}

func TestPortReadWrite(t *testing.T) {
	m, name := openPTY(t)
	defer m.Close()
	p, err := Open(name, Config{Baud: 250000, DataBits: 8, Parity: ParityNone})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if _, err := m.Write([]byte("hello\r\n\x00\xff")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	got := []byte{}
	for len(got) < 9 {
		n, err := p.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, buf[:n]...)
	}
	// Raw mode: no conversion of CR, LF and the other bytes
	if string(got) != "hello\r\n\x00\xff" {
		t.Fatalf("read %q", got)
	}

	if _, err := p.Write([]byte("AT\r")); err != nil {
		t.Fatal(err)
	}
	n, err := m.Read(buf)
	if err != nil || string(buf[:n]) != "AT\r" {
		t.Fatalf("master read %q, %v", buf[:n], err)
	}
}

func TestPortCloseUnblocksRead(t *testing.T) {
	m, name := openPTY(t)
	defer m.Close()
	p, err := Open(name, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.Read(make([]byte, 16))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	p.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("read after close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read still blocked after Close")
	}
	if _, err := p.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func TestPortGoneOnHangup(t *testing.T) {
	m, name := openPTY(t)
	p, err := Open(name, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	done := make(chan error, 1)
	go func() {
		_, err := p.Read(make([]byte, 16))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	m.Close() // as a USB adapter unplugged
	select {
	case err := <-done:
		if !errors.Is(err, ErrGone) {
			t.Fatalf("read after hangup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read still blocked after hangup")
	}
}

func TestPortExclusive(t *testing.T) {
	m, name := openPTY(t)
	defer m.Close()
	p, err := Open(name, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := Open(name, DefaultConfig()); !errors.Is(err, ErrBusy) {
		t.Fatalf("second open: %v", err)
	}
}

func TestOpenMissing(t *testing.T) {
	if _, err := Open("/dev/ttyNOSUCHPORT9", DefaultConfig()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestFrame(t *testing.T) {
	c := DefaultConfig()
	for _, f := range []string{"8N1", "7E2", "5O1.5", "8M1", "8S2"} {
		if err := c.SetFrame(f); err != nil {
			t.Fatal(f, err)
		}
		if c.Frame() != f {
			t.Fatalf("%s became %s", f, c.Frame())
		}
	}
	for _, f := range []string{"", "9N1", "8X1", "8N3", "8N"} {
		if err := c.SetFrame(f); err == nil {
			t.Fatalf("%q accepted", f)
		}
	}
	if c.SetFrame("7e1"); c.Frame() != "7E1" {
		t.Fatal("lower case")
	}
}

func TestNaturalSort(t *testing.T) {
	ports := []PortInfo{{Short: "COM10"}, {Short: "COM2"}, {Short: "ttyUSB1", USB: true}, {Short: "ttyACM0", USB: true}}
	sortPorts(ports)
	var got []string
	for _, p := range ports {
		got = append(got, p.Short)
	}
	if fmt.Sprint(got) != "[ttyACM0 ttyUSB1 COM2 COM10]" {
		t.Fatal(got)
	}
}

func TestListLinux(t *testing.T) {
	ports, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ports {
		t.Logf("%s | %s | %v", p.Title(), p.Key(), p.Details())
	}
}

func TestHolder(t *testing.T) {
	m, name := openPTY(t)
	defer m.Close()
	if h := Holder(name); h != "" {
		t.Fatalf("free port held by %q", h)
	}
	f, err := os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := Holder(name)
	if !strings.Contains(h, fmt.Sprintf("(pid %d)", os.Getpid())) {
		t.Fatalf("holder %q", h)
	}
	t.Log(h)
}

func TestRS485(t *testing.T) {
	m, name := openPTY(t)
	defer m.Close()
	c := DefaultConfig()
	c.RS485 = true
	// A pseudo terminal has no RS-485 mode
	if _, err := Open(name, c); err == nil || !strings.Contains(err.Error(), "RS-485") {
		t.Fatalf("got %v", err)
	}
	p, err := Open(name, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SetConfig(c); err == nil {
		t.Fatal("RS-485 set on a pseudo terminal")
	}
}

func TestErrorCounter(t *testing.T) {
	m, name := openPTY(t)
	defer m.Close()
	p, err := Open(name, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ec, ok := p.(ErrorCounter)
	if !ok {
		t.Fatal("no ErrorCounter on Linux")
	}
	// A pseudo terminal has no counters
	if _, err := ec.Errors(); err == nil {
		t.Fatal("counters of a pseudo terminal")
	}
	p.Close()
	if _, err := ec.Errors(); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}
