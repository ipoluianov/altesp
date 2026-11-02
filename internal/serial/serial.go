// Package serial opens serial ports on Linux, macOS and Windows and lists
// the ports of the computer. It talks to the systems directly, without cgo.
package serial

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Parity is the parity bit of a frame
type Parity byte

const (
	ParityNone  Parity = 'N'
	ParityEven  Parity = 'E'
	ParityOdd   Parity = 'O'
	ParityMark  Parity = 'M'
	ParitySpace Parity = 'S'
)

// StopBits is the number of stop bits of a frame
type StopBits byte

const (
	StopBits1 StopBits = iota
	StopBits1Half
	StopBits2
)

// Flow is the flow control of a port
type Flow byte

const (
	FlowNone Flow = iota
	FlowRTSCTS
	FlowXonXoff
)

// Config is the settings of a port
type Config struct {
	Baud     int
	DataBits int // 5..8
	Parity   Parity
	StopBits StopBits
	Flow     Flow
	// RS485 lets the driver drive RTS for the direction of an RS-485
	// transceiver: on while sending, off after it (Linux only)
	RS485 bool
}

// DefaultConfig is right for most devices: 115200 8N1 without flow control
func DefaultConfig() Config {
	return Config{Baud: 115200, DataBits: 8, Parity: ParityNone, StopBits: StopBits1}
}

// Frame returns the frame in the usual short form: "8N1", "7E2", "8N1.5"
func (c Config) Frame() string {
	stop := "1"
	switch c.StopBits {
	case StopBits1Half:
		stop = "1.5"
	case StopBits2:
		stop = "2"
	}
	return fmt.Sprintf("%d%c%s", c.DataBits, c.Parity, stop)
}

// SetFrame sets the data bits, the parity and the stop bits from the
// short form ("8N1", "7e1", "8N1.5")
func (c *Config) SetFrame(frame string) error {
	f := strings.ToUpper(strings.TrimSpace(frame))
	if len(f) < 3 || f[0] < '5' || f[0] > '8' {
		return fmt.Errorf("invalid frame %q", frame)
	}
	var stop StopBits
	switch f[2:] {
	case "1":
		stop = StopBits1
	case "1.5":
		stop = StopBits1Half
	case "2":
		stop = StopBits2
	default:
		return fmt.Errorf("invalid frame %q", frame)
	}
	p := Parity(f[1])
	switch p {
	case ParityNone, ParityEven, ParityOdd, ParityMark, ParitySpace:
	default:
		return fmt.Errorf("invalid frame %q", frame)
	}
	c.DataBits = int(f[0] - '0')
	c.Parity = p
	c.StopBits = stop
	return nil
}

// Validate tells whether the settings can be applied to a port
func (c Config) Validate() error {
	if c.Baud <= 0 || c.Baud > 100_000_000 {
		return fmt.Errorf("invalid baud rate %d", c.Baud)
	}
	if c.DataBits < 5 || c.DataBits > 8 {
		return fmt.Errorf("invalid data bits %d", c.DataBits)
	}
	return nil
}

// ByteTime is the time one byte takes on the line with these settings
func (c Config) ByteTime() time.Duration {
	bits := 1 + c.DataBits + 1 // start, data, stop
	if c.Parity != ParityNone {
		bits++
	}
	if c.StopBits != StopBits1 {
		bits++
	}
	if c.Baud <= 0 {
		return 0
	}
	return time.Duration(bits) * time.Second / time.Duration(c.Baud)
}

// Signals is the state of the input lines of a port
type Signals struct {
	CTS, DSR, DCD, RI bool
}

// Port is an open serial port. Read may run in one goroutine while Write
// and the other methods are called from others.
type Port interface {
	// Read blocks until some bytes come. After Close it returns ErrClosed;
	// when the device is unplugged it returns ErrGone.
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error

	SetConfig(c Config) error
	SetDTR(on bool) error
	SetRTS(on bool) error
	Signals() (Signals, error)
	// Break holds the line in the break state for d
	Break(d time.Duration) error
}

var (
	ErrClosed     = errors.New("port is closed")
	ErrGone       = errors.New("device is disconnected")
	ErrBusy       = errors.New("port is busy")
	ErrPermission = errors.New("access denied")
	ErrNotFound   = errors.New("port not found")
)

// Open opens the port with the settings. The port is taken exclusively:
// another program, or another copy of this one, gets ErrBusy.
func Open(name string, c Config) (Port, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return openPort(name, c)
}

// Holder tells which programs hold the port open: "minicom (pid 1234)",
// several joined with ", ". It is "" when nobody is found or the system
// doesn't tell (Windows).
func Holder(name string) string {
	return holder(name)
}

// Errors counts the errors of the received bytes since the port was opened
// (on Linux, since the driver was loaded)
type Errors struct {
	Framing, Parity, Overrun, Break uint64
}

// ErrorCounter is implemented by the ports whose system counts the line
// errors: Linux (not for every driver) and Windows
type ErrorCounter interface {
	Errors() (Errors, error)
}

// StandardBauds are the rates offered in the lists; any other can be typed
var StandardBauds = []int{
	300, 1200, 2400, 4800, 9600, 14400, 19200, 28800, 38400, 57600,
	74880, 115200, 230400, 250000, 460800, 500000, 921600,
	1000000, 1500000, 2000000, 3000000,
}

// Frames are the frames offered in the lists, the common ones first
var Frames = []string{"8N1", "8E1", "8O1", "8N2", "7E1", "7O1", "7N1", "7E2", "8M1", "8S1"}
