package serial

import (
	"errors"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The flags of DCB.Flags
const (
	dcbBinary            = 0x0001
	dcbParity            = 0x0002
	dcbOutxCtsFlow       = 0x0004
	dcbOutxDsrFlow       = 0x0008
	dcbDtrControlMask    = 0x0030
	dcbDtrControlEnable  = 0x0010
	dcbDsrSensitivity    = 0x0040
	dcbTXContinueOnXoff  = 0x0080
	dcbOutX              = 0x0100
	dcbInX               = 0x0200
	dcbErrorChar         = 0x0400
	dcbNull              = 0x0800
	dcbRtsControlMask    = 0x3000
	dcbRtsControlEnable  = 0x1000
	dcbRtsControlHandshk = 0x2000
	dcbAbortOnError      = 0x4000
)

// EscapeCommFunction and GetCommModemStatus
const (
	setRTS   = 3
	clrRTS   = 4
	setDTR   = 5
	clrDTR   = 6
	setBreak = 8
	clrBreak = 9

	msCTSOn  = 0x10
	msDSROn  = 0x20
	msRingOn = 0x40
	msRLSDOn = 0x80
)

// The errors of ClearCommError
const (
	ceRxOver   = 0x01
	ceOverrun  = 0x02
	ceRxParity = 0x04
	ceFrame    = 0x08
	ceBreak    = 0x10
)

// windowsPort is a COM port opened for overlapped I/O, so a Read waiting
// for data doesn't hold a Write back; closing sets an event the waits see
type windowsPort struct {
	h       windows.Handle
	closing windows.Handle // a manual-reset event, set by Close

	mu     sync.Mutex // closed, dtr, rts, errs
	closed bool
	dtr    bool
	rts    bool
	cfg    Config
	errs   Errors // what ClearCommError has told so far
	busy   sync.WaitGroup
	wmu    sync.Mutex
}

func openPort(name string, c Config) (Port, error) {
	path := name
	if !strings.HasPrefix(path, `\\.\`) {
		path = `\\.\` + name // COM10 and higher are reached only so
	}
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p16, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_ACCESS_DENIED), errors.Is(err, windows.ERROR_SHARING_VIOLATION):
			// A COM port opened by another program is denied
			return nil, ErrBusy
		case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
			return nil, ErrNotFound
		}
		return nil, err
	}
	p := &windowsPort{h: h, dtr: true, rts: true}
	ok := false
	defer func() {
		if !ok {
			windows.CloseHandle(h)
		}
	}()
	windows.SetupComm(h, 64*1024, 64*1024)
	if err := p.apply(c); err != nil {
		return nil, err
	}
	// A read returns what has come at once, or waits for the first byte
	timeouts := windows.CommTimeouts{
		ReadIntervalTimeout:        0xFFFFFFFF,
		ReadTotalTimeoutMultiplier: 0xFFFFFFFF,
		ReadTotalTimeoutConstant:   500,
	}
	if err := windows.SetCommTimeouts(h, &timeouts); err != nil {
		return nil, err
	}
	windows.PurgeComm(h, windows.PURGE_RXCLEAR|windows.PURGE_TXCLEAR)
	p.closing, err = windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	ok = true
	return p, nil
}

// apply writes the settings and the current DTR and RTS into the DCB
func (p *windowsPort) apply(c Config) error {
	if c.RS485 {
		return errors.New("RS-485 mode is not supported on Windows")
	}
	var dcb windows.DCB
	dcb.DCBlength = uint32(unsafe.Sizeof(dcb))
	if err := windows.GetCommState(p.h, &dcb); err != nil {
		return err
	}
	dcb.BaudRate = uint32(c.Baud)
	dcb.ByteSize = uint8(c.DataBits)
	switch c.Parity {
	case ParityOdd:
		dcb.Parity = 1
	case ParityEven:
		dcb.Parity = 2
	case ParityMark:
		dcb.Parity = 3
	case ParitySpace:
		dcb.Parity = 4
	default:
		dcb.Parity = 0
	}
	switch c.StopBits {
	case StopBits1Half:
		dcb.StopBits = 1
	case StopBits2:
		dcb.StopBits = 2
	default:
		dcb.StopBits = 0
	}
	f := dcb.Flags &^ (dcbParity | dcbOutxCtsFlow | dcbOutxDsrFlow | dcbDtrControlMask |
		dcbDsrSensitivity | dcbOutX | dcbInX | dcbErrorChar | dcbNull | dcbRtsControlMask | dcbAbortOnError)
	f |= dcbBinary | dcbTXContinueOnXoff
	if c.Parity != ParityNone {
		f |= dcbParity
	}
	p.mu.Lock()
	dtr, rts := p.dtr, p.rts
	p.mu.Unlock()
	if dtr {
		f |= dcbDtrControlEnable
	}
	switch {
	case c.Flow == FlowRTSCTS:
		f |= dcbOutxCtsFlow | dcbRtsControlHandshk
	case rts:
		f |= dcbRtsControlEnable
	}
	if c.Flow == FlowXonXoff {
		f |= dcbOutX | dcbInX
		dcb.XonChar = 0x11
		dcb.XoffChar = 0x13
		dcb.XonLim = 2048
		dcb.XoffLim = 512
	}
	dcb.Flags = f
	if err := windows.SetCommState(p.h, &dcb); err != nil {
		return err
	}
	p.cfg = c
	return nil
}

func (p *windowsPort) enter() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.busy.Add(1)
	return true
}

// overlapped runs an overlapped call and waits for it or for Close
func (p *windowsPort) overlapped(call func(ov *windows.Overlapped) error) (uint32, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(ev)
	ov := windows.Overlapped{HEvent: ev}
	err = call(&ov)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return 0, err
	}
	which, err := windows.WaitForMultipleObjects([]windows.Handle{ev, p.closing}, false, windows.INFINITE)
	if err != nil {
		return 0, err
	}
	if which == windows.WAIT_OBJECT_0+1 {
		windows.CancelIoEx(p.h, &ov)
		var n uint32
		windows.GetOverlappedResult(p.h, &ov, &n, true)
		return 0, ErrClosed
	}
	var n uint32
	err = windows.GetOverlappedResult(p.h, &ov, &n, true)
	return n, err
}

// present tells whether the device is still there: a COM port of an
// unplugged USB device fails ClearCommError
func (p *windowsPort) present() bool {
	return p.clearErrors() == nil
}

// clearErrors reads and clears the error flags of the port and adds them
// to the counters
func (p *windowsPort) clearErrors() error {
	var errs uint32
	var stat windows.ComStat
	if err := windows.ClearCommError(p.h, &errs, &stat); err != nil {
		return err
	}
	if errs == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if errs&ceFrame != 0 {
		p.errs.Framing++
	}
	if errs&ceRxParity != 0 {
		p.errs.Parity++
	}
	if errs&ceOverrun != 0 {
		p.errs.Overrun++
	}
	if errs&ceRxOver != 0 {
		p.errs.Overrun++
	}
	if errs&ceBreak != 0 {
		p.errs.Break++
	}
	return nil
}

// Errors counts the error flags of the port seen since it was opened. The
// system tells only that an error happened since the last look, so a burst
// of errors counts as one.
func (p *windowsPort) Errors() (Errors, error) {
	if err := p.do(p.clearErrors); err != nil {
		return Errors{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.errs, nil
}

func (p *windowsPort) Read(b []byte) (int, error) {
	if !p.enter() {
		return 0, ErrClosed
	}
	defer p.busy.Done()
	for {
		n, err := p.overlapped(func(ov *windows.Overlapped) error {
			return windows.ReadFile(p.h, b, nil, ov)
		})
		if errors.Is(err, ErrClosed) {
			return 0, err
		}
		if err != nil {
			return 0, ErrGone
		}
		if n > 0 {
			return int(n), nil
		}
		// The timeout passed without data
		if !p.present() {
			return 0, ErrGone
		}
	}
}

func (p *windowsPort) Write(b []byte) (int, error) {
	if !p.enter() {
		return 0, ErrClosed
	}
	defer p.busy.Done()
	p.wmu.Lock()
	defer p.wmu.Unlock()
	n, err := p.overlapped(func(ov *windows.Overlapped) error {
		return windows.WriteFile(p.h, b, nil, ov)
	})
	if err != nil && !errors.Is(err, ErrClosed) {
		if !p.present() {
			return int(n), ErrGone
		}
	}
	return int(n), err
}

func (p *windowsPort) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	windows.SetEvent(p.closing)
	p.busy.Wait()
	windows.PurgeComm(p.h, windows.PURGE_TXABORT|windows.PURGE_RXABORT|windows.PURGE_TXCLEAR|windows.PURGE_RXCLEAR)
	err := windows.CloseHandle(p.h)
	windows.CloseHandle(p.closing)
	return err
}

func (p *windowsPort) do(f func() error) error {
	if !p.enter() {
		return ErrClosed
	}
	defer p.busy.Done()
	return f()
}

func (p *windowsPort) SetConfig(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return p.do(func() error { return p.apply(c) })
}

func (p *windowsPort) SetDTR(on bool) error {
	return p.do(func() error {
		p.mu.Lock()
		p.dtr = on
		p.mu.Unlock()
		fn := uint32(clrDTR)
		if on {
			fn = setDTR
		}
		return windows.EscapeCommFunction(p.h, fn)
	})
}

func (p *windowsPort) SetRTS(on bool) error {
	return p.do(func() error {
		p.mu.Lock()
		p.rts = on
		p.mu.Unlock()
		fn := uint32(clrRTS)
		if on {
			fn = setRTS
		}
		return windows.EscapeCommFunction(p.h, fn)
	})
}

func (p *windowsPort) Signals() (Signals, error) {
	var s Signals
	err := p.do(func() error {
		var bits uint32
		if err := windows.GetCommModemStatus(p.h, &bits); err != nil {
			return err
		}
		s.CTS = bits&msCTSOn != 0
		s.DSR = bits&msDSROn != 0
		s.DCD = bits&msRLSDOn != 0
		s.RI = bits&msRingOn != 0
		return nil
	})
	return s, err
}

func (p *windowsPort) Break(d time.Duration) error {
	if err := p.do(func() error { return windows.EscapeCommFunction(p.h, setBreak) }); err != nil {
		return err
	}
	time.Sleep(d)
	return p.do(func() error { return windows.EscapeCommFunction(p.h, clrBreak) })
}
