package serial

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// setTermios applies the settings through termios2, which takes any baud
// rate (BOTHER), not only the standard ones
func setTermios(fd int, c Config) error {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS2)
	if err != nil {
		return err
	}
	rawFlags(t, c)
	t.Cflag &^= unix.CBAUD | unix.CIBAUD | unix.CMSPAR
	t.Cflag |= unix.BOTHER | unix.BOTHER<<16 // IBSHIFT: the input speed too
	t.Ispeed = uint32(c.Baud)
	t.Ospeed = uint32(c.Baud)
	switch c.Parity {
	case ParityMark:
		t.Cflag |= unix.PARENB | unix.CMSPAR | unix.PARODD
	case ParitySpace:
		t.Cflag |= unix.PARENB | unix.CMSPAR
	}
	if err := unix.IoctlSetTermios(fd, unix.TCSETS2, t); err != nil {
		return fmt.Errorf("cannot apply %d %s: %w", c.Baud, c.Frame(), err)
	}
	return setRS485(fd, c.RS485)
}

// serialRS485 is struct serial_rs485 of linux/serial.h
type serialRS485 struct {
	flags              uint32
	delayRTSBeforeSend uint32
	delayRTSAfterSend  uint32
	padding            [5]uint32
}

const (
	serRS485Enabled      = 1 << 0
	serRS485RTSOnSend    = 1 << 1
	serRS485RTSAfterSend = 1 << 2
)

// setRS485 turns the RS-485 mode of the driver on or off. Most drivers
// don't have the mode: turning it off is fine for them, on is an error.
func setRS485(fd int, on bool) error {
	var r serialRS485
	got := ioctlPtr(fd, unix.TIOCGRS485, unsafe.Pointer(&r)) == nil
	if !on && (!got || r.flags&serRS485Enabled == 0) {
		return nil
	}
	if on {
		// RTS is high while sending and low after it; the delays are kept
		r.flags |= serRS485Enabled | serRS485RTSOnSend
		r.flags &^= serRS485RTSAfterSend
	} else {
		r.flags &^= serRS485Enabled
	}
	err := ioctlPtr(fd, unix.TIOCSRS485, unsafe.Pointer(&r))
	switch {
	case err == nil:
		return nil
	case !on && (errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EINVAL)):
		return nil
	case on:
		return fmt.Errorf("the driver doesn't support RS-485 mode: %w", err)
	}
	return fmt.Errorf("cannot turn RS-485 mode off: %w", err)
}

// serialICounter is struct serial_icounter_struct of linux/serial.h
type serialICounter struct {
	cts, dsr, rng, dcd, rx, tx int32
	frame, overrun, parity     int32
	brk, bufOverrun            int32
	reserved                   [9]int32
}

// Errors reads the counters of the driver; a pseudo terminal and some
// USB adapters have none
func (p *unixPort) Errors() (Errors, error) {
	var e Errors
	err := p.do(func(fd int) error {
		var ic serialICounter
		if err := ioctlPtr(fd, unix.TIOCGICOUNT, unsafe.Pointer(&ic)); err != nil {
			return fmt.Errorf("the driver doesn't count line errors: %w", err)
		}
		e.Framing = uint64(uint32(ic.frame))
		e.Parity = uint64(uint32(ic.parity))
		e.Overrun = uint64(uint32(ic.overrun)) + uint64(uint32(ic.bufOverrun))
		e.Break = uint64(uint32(ic.brk))
		return nil
	})
	return e, err
}

func ioctlPtr(fd int, req uint, arg unsafe.Pointer) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func flushInput(fd int) {
	unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}

func flushOutput(fd int) {
	unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCOFLUSH)
}

func wakePipe() (r, w int, err error) {
	var p [2]int
	err = unix.Pipe2(p[:], unix.O_NONBLOCK|unix.O_CLOEXEC)
	return p[0], p[1], err
}
