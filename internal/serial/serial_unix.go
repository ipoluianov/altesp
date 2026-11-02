//go:build linux || darwin

package serial

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// unixPort is a port on Linux and macOS: a non-blocking descriptor waited
// on with poll, together with a pipe that wakes the waiting up on Close
type unixPort struct {
	fd     int
	wakeR  int // read end of the pipe: readable once the port is closing
	wakeW  int
	mu     sync.Mutex // closed, and the descriptor against its closing
	closed bool
	busy   sync.WaitGroup // calls in progress; Close waits for them
	wmu    sync.Mutex     // one Write at a time
}

func openPort(name string, c Config) (Port, error) {
	fd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openError(err)
	}
	ok := false
	defer func() {
		if !ok {
			// The exclusive mode would outlive the descriptor while the
			// other side (a pty master) keeps the terminal open
			ioctlNoArg(fd, unix.TIOCNXCL)
			unix.Close(fd)
		}
	}()
	// Other programs that respect locks (pyserial, screen, minicom) and
	// this one see the port as taken
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
	}
	if err := ioctlNoArg(fd, unix.TIOCEXCL); err != nil {
		return nil, err
	}
	if err := setTermios(fd, c); err != nil {
		return nil, err
	}
	// What came before the port was opened is from another session
	flushInput(fd)

	r, w, err := wakePipe()
	if err != nil {
		return nil, err
	}
	ok = true
	return &unixPort{fd: fd, wakeR: r, wakeW: w}, nil
}

func openError(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENXIO), errors.Is(err, unix.ENODEV):
		return ErrNotFound
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return ErrPermission
	case errors.Is(err, unix.EBUSY):
		return ErrBusy
	}
	return err
}

func ioctlNoArg(fd int, req uint) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), 0)
	if e != 0 {
		return e
	}
	return nil
}

// enter registers a call; false when the port is already closed
func (p *unixPort) enter() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.busy.Add(1)
	return true
}

// wait blocks until the descriptor is ready for events (POLLIN or
// POLLOUT); it returns ErrClosed when the port is closing and ErrGone
// when the device went away
func (p *unixPort) wait(events int16) error {
	fds := []unix.PollFd{
		{Fd: int32(p.fd), Events: events},
		{Fd: int32(p.wakeR), Events: unix.POLLIN},
	}
	for {
		_, err := unix.Poll(fds, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if fds[1].Revents != 0 {
			return ErrClosed
		}
		r := fds[0].Revents
		if r&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 && r&events == 0 {
			return ErrGone
		}
		if r != 0 {
			return nil
		}
	}
}

func (p *unixPort) Read(b []byte) (int, error) {
	if !p.enter() {
		return 0, ErrClosed
	}
	defer p.busy.Done()
	for {
		if err := p.wait(unix.POLLIN); err != nil {
			return 0, err
		}
		n, err := unix.Read(p.fd, b)
		switch {
		case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EIO), errors.Is(err, unix.ENXIO), errors.Is(err, unix.ENODEV):
			return 0, ErrGone
		case err != nil:
			return 0, err
		case n == 0:
			// Readable with nothing to read: the other side hung up
			return 0, ErrGone
		}
		return n, nil
	}
}

func (p *unixPort) Write(b []byte) (int, error) {
	if !p.enter() {
		return 0, ErrClosed
	}
	defer p.busy.Done()
	p.wmu.Lock()
	defer p.wmu.Unlock()
	written := 0
	for written < len(b) {
		n, err := unix.Write(p.fd, b[written:])
		if n > 0 {
			written += n
		}
		switch {
		case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
			if err := p.wait(unix.POLLOUT); err != nil {
				return written, err
			}
		case errors.Is(err, unix.EIO), errors.Is(err, unix.ENXIO), errors.Is(err, unix.ENODEV):
			return written, ErrGone
		case err != nil:
			return written, err
		}
	}
	return written, nil
}

func (p *unixPort) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	unix.Write(p.wakeW, []byte{0})
	p.busy.Wait()
	// Unsent bytes would keep a close waiting for a stuck line
	flushOutput(p.fd)
	ioctlNoArg(p.fd, unix.TIOCNXCL)
	err := unix.Close(p.fd)
	unix.Close(p.wakeR)
	unix.Close(p.wakeW)
	return err
}

// do runs f with the descriptor while the port is open
func (p *unixPort) do(f func(fd int) error) error {
	if !p.enter() {
		return ErrClosed
	}
	defer p.busy.Done()
	return f(p.fd)
}

func (p *unixPort) SetConfig(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return p.do(func(fd int) error { return setTermios(fd, c) })
}

func (p *unixPort) setModemBit(bit int, on bool) error {
	return p.do(func(fd int) error {
		req := uint(unix.TIOCMBIC)
		if on {
			req = unix.TIOCMBIS
		}
		return unix.IoctlSetPointerInt(fd, req, bit)
	})
}

func (p *unixPort) SetDTR(on bool) error { return p.setModemBit(unix.TIOCM_DTR, on) }
func (p *unixPort) SetRTS(on bool) error { return p.setModemBit(unix.TIOCM_RTS, on) }

func (p *unixPort) Signals() (Signals, error) {
	var s Signals
	err := p.do(func(fd int) error {
		bits, err := unix.IoctlGetInt(fd, unix.TIOCMGET)
		if err != nil {
			return err
		}
		s.CTS = bits&unix.TIOCM_CTS != 0
		s.DSR = bits&unix.TIOCM_DSR != 0
		s.DCD = bits&unix.TIOCM_CAR != 0
		s.RI = bits&unix.TIOCM_RNG != 0
		return nil
	})
	return s, err
}

func (p *unixPort) Break(d time.Duration) error {
	if err := p.do(func(fd int) error { return ioctlNoArg(fd, unix.TIOCSBRK) }); err != nil {
		return err
	}
	time.Sleep(d)
	return p.do(func(fd int) error { return ioctlNoArg(fd, unix.TIOCCBRK) })
}

// rawFlags clears what a terminal does to the bytes: echo, line editing,
// signals, conversions of the line ends, and sets the frame
func rawFlags(t *unix.Termios, c Config) {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR |
		unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY | unix.INPCK
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS
	t.Cflag |= unix.CREAD | unix.CLOCAL
	switch c.DataBits {
	case 5:
		t.Cflag |= unix.CS5
	case 6:
		t.Cflag |= unix.CS6
	case 7:
		t.Cflag |= unix.CS7
	default:
		t.Cflag |= unix.CS8
	}
	switch c.Parity {
	case ParityEven:
		t.Cflag |= unix.PARENB
	case ParityOdd:
		t.Cflag |= unix.PARENB | unix.PARODD
	}
	// 1.5 stop bits are CSTOPB with 5 data bits; with more, 2 is the nearest
	if c.StopBits != StopBits1 {
		t.Cflag |= unix.CSTOPB
	}
	switch c.Flow {
	case FlowRTSCTS:
		t.Cflag |= unix.CRTSCTS
	case FlowXonXoff:
		t.Iflag |= unix.IXON | unix.IXOFF
	}
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
}
