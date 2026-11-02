package serial

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// iossiospeed is IOSSIOSPEED, _IOW('T', 2, speed_t): sets any baud rate
const iossiospeed = 0x80085402

func setTermios(fd int, c Config) error {
	if c.RS485 {
		return errors.New("RS-485 mode is not supported on macOS")
	}
	if c.Parity == ParityMark || c.Parity == ParitySpace {
		return errors.New("mark and space parity are not supported on macOS")
	}
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return err
	}
	rawFlags(t, c)
	// A standard speed for termios; the real one is set after it, as
	// TIOCSETA would put the speed back
	t.Ispeed = 9600
	t.Ospeed = 9600
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, t); err != nil {
		return fmt.Errorf("cannot apply %s: %w", c.Frame(), err)
	}
	speed := uint64(c.Baud)
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), iossiospeed, uintptr(unsafe.Pointer(&speed)))
	if e != 0 {
		return fmt.Errorf("cannot set %d baud: %w", c.Baud, e)
	}
	return nil
}

const (
	flushRead  = 1 // FREAD
	flushWrite = 2 // FWRITE
)

func flushInput(fd int) {
	unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, flushRead)
}

func flushOutput(fd int) {
	unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, flushWrite)
}

func wakePipe() (r, w int, err error) {
	var p [2]int
	if err = unix.Pipe(p[:]); err != nil {
		return 0, 0, err
	}
	for _, fd := range p {
		unix.SetNonblock(fd, true)
		unix.CloseOnExec(fd)
	}
	return p[0], p[1], nil
}
