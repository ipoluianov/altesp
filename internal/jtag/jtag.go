// Package jtag flashes and reads the SPI flash of the ESP chips through
// JTAG, without the UART bootloader and without OpenOCD. Stage 6 of
// TZ.md: the built-in USB-Serial-JTAG of the RISC-V chips (ESP32-C3, C6,
// H2). Stage 7: ESP-Prog (FT2232H) as a second adapter for the same
// chips through their JTAG pins, and the Xtensa chips (ESP32-S3, ESP32),
// whose debug module is reached and probed but not yet driven (see
// package xtensa, Missing).
//
// The layers, each in its own package:
//
//	usb      USB access from user space: usbfs, WinUSB, IOKit (no cgo)
//	espjtag  the Espressif USB-JTAG command protocol and the TAP
//	ftdi     the FT2232H MPSSE and the TAP through it (ESP-Prog)
//	chain    one TAP of a chain of several
//	riscv    the RISC-V debug transport and debug module
//	xtensa   the Xtensa debug module (XDM) and instruction encodings
//	jtag     this package: the chip table, the flash routine, the session
//	jtagsim  the simulated controllers (USB-JTAG, FTDI), TAPs, debug
//	         modules and hart
//
// The flash routine is the ROM of the chip: its SPI flash functions are
// called on the halted hart (the arguments in a0.., ra pointing at an
// ebreak in RAM, dpc at the function), and the data goes through a
// buffer in RAM. On connect the chip is reset into the ROM's download
// mode (the force-download bit, then a system reset), halted, and its
// watchdogs are disabled; Reset(true) boots the application again.
package jtag

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ipoluianov/altesp/internal/chips"
	"github.com/ipoluianov/altesp/internal/jtag/chain"
	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
	"github.com/ipoluianov/altesp/internal/jtag/ftdi"
	"github.com/ipoluianov/altesp/internal/jtag/riscv"
	"github.com/ipoluianov/altesp/internal/jtag/usb"
	"github.com/ipoluianov/altesp/internal/jtag/xtensa"
)

// Device is a JTAG adapter found by List
type Device struct {
	VID, PID uint16
	Serial   string
	Location string
	Product  string
	Adapter  Adapter
}

// Kind names the adapter for the user: "USB-JTAG", "ESP-Prog"
func (d Device) Kind() string { return d.Adapter.String() }

// List finds the JTAG adapters: the Espressif USB-Serial-JTAG devices
// and the FT2232H (ESP-Prog)
func List() ([]Device, error) {
	var out []Device
	for _, a := range []struct {
		vid, pid uint16
		kind     Adapter
	}{
		{EspressifVID, USBSerialJTAG, AdapterUSBJTAG},
		{ftdi.VID, ftdi.PID2232H, AdapterFTDI},
	} {
		infos, err := usb.List(a.vid, a.pid)
		if err != nil {
			return nil, err
		}
		for _, in := range infos {
			out = append(out, Device{VID: in.VID, PID: in.PID, Serial: in.Serial, Location: in.Location, Product: in.Product, Adapter: a.kind})
		}
	}
	return out, nil
}

// Errors of the session
var (
	// ErrJTAGDisabled: the TAP or the debug module doesn't answer. The
	// usual cause is the eFuse (DIS_USB_JTAG, DIS_PAD_JTAG, or JTAG
	// soft-disabled by SOFT_DIS_JTAG).
	ErrJTAGDisabled = errors.New("JTAG is disabled by eFuse")
	// ErrUnsupported: the chip has no JTAG support here (yet)
	ErrUnsupported = errors.New("flashing over JTAG is not supported for this chip")
	// ErrXtensaUnavailable: the chip is an Xtensa one; its debug module
	// answers but can't be driven yet (XtensaError tells why)
	ErrXtensaUnavailable = xtensa.ErrUnavailable
	// ErrFlash: a ROM flash function returned an error
	ErrFlash = errors.New("the ROM flash function failed")
	// ErrVerify: the flash content differs from what was written
	ErrVerify = errors.New("verification failed: the flash differs from the data written")
)

// DisabledError tells why JTAG looks disabled
type DisabledError struct {
	IDCode uint32
	Detail string
}

func (e *DisabledError) Error() string {
	return fmt.Sprintf("%v: %s (IDCODE 0x%08x). Check the eFuses DIS_USB_JTAG, DIS_PAD_JTAG and SOFT_DIS_JTAG; "+
		"flash over the serial port instead", ErrJTAGDisabled, e.Detail, e.IDCode)
}

func (e *DisabledError) Unwrap() error { return ErrJTAGDisabled }

// Options of a connection
type Options struct {
	// NoReset halts the chip where it runs instead of resetting it into
	// the ROM's download mode first
	NoReset bool
	// BootWait is how long the ROM gets to reach download mode after the
	// reset; 100 ms if zero
	BootWait time.Duration
	// Reopen gives a new link after the USB device re-enumerated (a
	// system reset may reset the USB-Serial-JTAG controller); OpenWith
	// sets it, OpenLink callers may
	Reopen func() (Link, error)
	// Status receives progress messages
	Status func(string)
	// Adapter is the kind of the link given to OpenLink; OpenWith sets
	// it from the device
	Adapter Adapter
	// ClockHz is the TCK frequency of an FTDI adapter; 5 MHz when 0
	ClockHz int
}

// tapDriver is what the session needs of a TAP driver: espjtag.TAP for
// the built-in USB-JTAG, ftdi.TAP for ESP-Prog
type tapDriver interface {
	chain.Resetter
	ReadIDCode() (uint32, error)
	SetLink(Link)
}

// Info is what is known about the connected chip
type Info struct {
	Chip   string // "ESP32-C3"
	Key    string // "esp32c3"
	IDCode uint32
	chips.Info
	Debug    string // the debug module and what it supports
	MemPath  string // how memory is reached
	Warnings []string
}

// Session is a connection to a chip over JTAG. Its methods are safe for
// concurrent use but run one at a time.
type Session struct {
	mu    sync.Mutex
	opt   Options
	link  Link
	tap   tapDriver
	t     *target
	chip  *chips.Chip
	cpu   cpu
	info  Info
	ready bool // halted, watchdogs off, flash attached
	// inApp: the chip was reset into the application; the next operation
	// resets it into download mode again
	inApp  bool
	closed bool
}

// Open connects to the chip behind a JTAG adapter. chipKey is a key of
// the chip table ("esp32c3"); empty detects the chip by its IDCODE.
func Open(d Device, chipKey string) (*Session, error) {
	return OpenWith(d, chipKey, Options{})
}

// OpenWith is Open with options
func OpenWith(d Device, chipKey string, opt Options) (*Session, error) {
	open := func() (Link, error) { return openUSB(d) }
	if d.Adapter == AdapterFTDI {
		open = func() (Link, error) { return openFTDI(d) }
	}
	opt.Adapter = d.Adapter
	l, err := open()
	if err != nil {
		return nil, err
	}
	if opt.Reopen == nil {
		opt.Reopen = open
	}
	s, err := OpenLink(l, chipKey, opt)
	if err != nil {
		if c, ok := l.(io.Closer); ok {
			c.Close()
		}
		return nil, err
	}
	return s, nil
}

// OpenLink connects over a link that is already open: the USB link, or
// the simulator in the tests
func OpenLink(l Link, chipKey string, opt Options) (*Session, error) {
	if opt.BootWait == 0 {
		opt.BootWait = 100 * time.Millisecond
	}
	s := &Session{opt: opt, link: l}
	if opt.Adapter == AdapterFTDI {
		ft := ftdi.NewTAP(l)
		ft.ClockHz = opt.ClockHz
		s.tap = ft
	} else {
		s.tap = espjtag.New(l)
	}
	id, err := s.tap.ReadIDCode()
	if err != nil {
		return nil, err
	}
	s.info.IDCode = id
	if id == 0 || id == 0xFFFFFFFF || id&1 == 0 {
		return nil, &DisabledError{IDCode: id, Detail: "the TAP gives no IDCODE"}
	}
	t := targetByKey(chipKey)
	byID := targetByIDCode(id, t, opt.Adapter)
	switch {
	case chipKey == "" && byID == nil:
		return nil, fmt.Errorf("%w: unknown IDCODE 0x%08x", ErrUnsupported, id)
	case chipKey == "":
		t = byID
		if a := ambiguous(id); a != "" {
			s.info.Warnings = append(s.info.Warnings, a)
		}
	case t == nil:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, chipKey)
	case byID != nil && byID.arch != t.arch:
		return nil, fmt.Errorf("%w: the IDCODE 0x%08x is that of %s, not %s", ErrUnsupported, id, byID.key, t.key)
	case byID != nil && byID != t:
		s.info.Warnings = append(s.info.Warnings,
			fmt.Sprintf("the IDCODE 0x%08x is that of %s, not %s", id, byID.key, t.key))
	case byID == nil:
		s.info.Warnings = append(s.info.Warnings, fmt.Sprintf("unexpected IDCODE 0x%08x", id))
	}
	s.t = t
	s.chip = t.chip()
	s.info.Key = t.key
	if s.chip != nil {
		s.info.Chip = s.chip.Name
	}
	if err := s.attach(); err != nil {
		return nil, err
	}
	if !opt.NoReset {
		if err := s.enterDownload(); err != nil {
			return nil, err
		}
	}
	if err := s.prepare(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) status(msg string) {
	if s.opt.Status != nil {
		s.opt.Status(msg)
	}
}

// attach finds the debug module and halts the hart
func (s *Session) attach() error {
	if s.t.arch == archXtensa {
		c, err := attachXtensa(s.tap, s.t)
		if err != nil {
			return err
		}
		s.cpu = c
		s.info.Debug = c.describe()
		return s.cpu.halt()
	}
	c, err := attachRISCV(s.tap)
	if err != nil {
		if errors.Is(err, riscv.ErrNoDTM) || errors.Is(err, riscv.ErrNoDM) {
			return &DisabledError{IDCode: s.info.IDCode, Detail: "the debug module does not answer: " + err.Error()}
		}
		if errors.Is(err, riscv.ErrNotAuth) {
			return &DisabledError{IDCode: s.info.IDCode, Detail: err.Error()}
		}
		return err
	}
	s.cpu = c
	s.info.Debug = c.describe()
	return s.cpu.halt()
}

// reconnect waits for the device to come back after a reset dropped it
func (s *Session) reconnect() error {
	if s.opt.Reopen == nil {
		return errors.New("the USB-JTAG device went away and can't be reopened")
	}
	s.status("Waiting for the USB-JTAG device")
	if c, ok := s.link.(io.Closer); ok {
		c.Close()
	}
	var err error
	for i := 0; i < reopenAttempts; i++ {
		var l Link
		if l, err = s.opt.Reopen(); err == nil {
			s.link = l
			s.tap.SetLink(l)
			if _, err = s.tap.ReadIDCode(); err == nil {
				return s.attach()
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the USB-JTAG device did not come back after the reset: %w", err)
}

// afterReset waits for the ROM, halts the hart and acknowledges the
// reset, reconnecting when the reset took the USB device with it
//
// resetErr is the error of the reset request itself: it stands when the
// device didn't go away (the reset was not sent)
func (s *Session) afterReset(halt bool, resetErr error) error {
	time.Sleep(s.opt.BootWait)
	err := s.cpu.ackReset()
	if err != nil {
		if err = s.reconnect(); err != nil {
			return err
		}
		err = s.cpu.ackReset()
	} else if resetErr != nil {
		return resetErr
	}
	if err == nil && halt {
		err = s.cpu.halt()
	}
	return err
}

func (s *Session) setBit(b regBit, on bool) error {
	v, err := s.readReg(b.addr)
	if err != nil {
		return err
	}
	if on {
		v |= b.bit
	} else {
		v &^= b.bit
	}
	return s.writeReg(b.addr, v)
}

// enterDownload resets the chip into the ROM's download mode: the ROM
// sets up its flash driver state and stays in its command loop, a known
// state for calling its functions
func (s *Session) enterDownload() error {
	s.status("Resetting the chip into download mode")
	if s.t.forceDownload.addr == 0 {
		// TODO(stage 7, ESP32): reset with the core halted at its reset
		// vector instead (xtensa.Missing)
		return fmt.Errorf("%w: %s can't be reset into download mode over JTAG; hold GPIO0 low while resetting it", ErrUnsupported, s.t.key)
	}
	if err := s.cpu.halt(); err != nil {
		return err
	}
	if err := s.probeMem(); err != nil {
		return err
	}
	if err := s.setBit(s.t.forceDownload, true); err != nil {
		return err
	}
	// a reset that takes the USB device with it may fail the write;
	// afterReset reconnects then
	s.ready = false
	if err := s.afterReset(true, s.cpu.resetSystem(false)); err != nil {
		return err
	}
	if err := s.probeMem(); err != nil {
		return err
	}
	// the next reset boots the application
	return s.setBit(s.t.forceDownload, false)
}

func (s *Session) probeMem() error {
	c, ok := s.cpu.(*riscvCPU)
	if !ok {
		return nil
	}
	if c.dm.ProbeMemory(s.t.work) == riscv.MemNone {
		return fmt.Errorf("%w: %v", ErrUnsupported, riscv.ErrNoMemPath)
	}
	s.info.MemPath = c.memPath()
	return nil
}

// disableWatchdogs stops the watchdogs that would reset the chip while it
// is halted or inside a long flash operation
func (s *Session) disableWatchdogs() error {
	for _, w := range s.t.wdts {
		for _, op := range [][2]uint32{{w.protect, w.key}, {w.config, 0}, {w.protect, 0}} {
			if err := s.writeReg(op[0], op[1]); err != nil {
				return fmt.Errorf("disabling the %s: %w", w.name, err)
			}
		}
	}
	if sw := s.t.swd; sw.config != 0 {
		if err := s.writeReg(sw.protect, sw.key); err != nil {
			return err
		}
		if err := s.setBit(regBit{sw.config, sw.autoFeed}, true); err != nil {
			return err
		}
		if err := s.writeReg(sw.protect, 0); err != nil {
			return err
		}
	}
	return nil
}

// prepare makes the halted chip ready for the flash routine: the
// watchdogs off, the ebreak in RAM, the flash attached, its size and
// protection set; the chip info is read on the way
func (s *Session) prepare(ctx context.Context) error {
	if s.ready {
		return nil
	}
	if s.inApp && !s.opt.NoReset {
		if err := s.enterDownload(); err != nil {
			return err
		}
	}
	s.inApp = false
	if err := s.cpu.halt(); err != nil {
		return err
	}
	if s.info.MemPath == "" {
		if err := s.probeMem(); err != nil {
			return err
		}
	}
	if err := s.disableWatchdogs(); err != nil {
		return err
	}
	brk := s.t.work + workEbreak
	if s.t.brk != 0 {
		brk = s.t.brk
	}
	if err := s.cpu.writeMem(brk, []uint32{s.cpu.breakpoint()}); err != nil {
		return err
	}
	if _, err := s.call(ctx, s.t.rom.attach, callShort, 0, 0); err != nil {
		return fmt.Errorf("attaching the SPI flash: %w", err)
	}
	if s.chip != nil {
		in, err := s.chip.ReadInfo(regs{s})
		if err != nil {
			s.info.Warnings = append(s.info.Warnings, "chip info: "+err.Error())
		}
		s.info.Info = in
	}
	size := uint32(s.info.FlashSize)
	if size == 0 {
		size = 16 << 20
		s.info.Warnings = append(s.info.Warnings, "flash size unknown, 16 MB assumed")
	}
	r, err := s.call(ctx, s.t.rom.configParam, callShort, 0, size, 0x10000, 0x1000, 0x100, 0xFFFF)
	if err != nil {
		return err
	}
	if r != 0 {
		return fmt.Errorf("%w: config_param returned %d", ErrFlash, r)
	}
	if s.t.rom.unlock == 0 {
		// not in this ROM (ESP32)
	} else if r, err := s.call(ctx, s.t.rom.unlock, callErase); err != nil {
		return err
	} else if r != 0 {
		s.info.Warnings = append(s.info.Warnings, fmt.Sprintf("the flash protection could not be cleared (unlock returned %d)", r))
	}
	s.ready = true
	return nil
}

// Call timeouts
const (
	callShort     = 5 * time.Second
	callErase     = 10 * time.Second
	callEraseChip = 5 * time.Minute
)

// call runs a ROM function: returns to the ebreak of the work area, with
// the stack at its top
func (s *Session) call(ctx context.Context, fn uint32, timeout time.Duration, args ...uint32) (uint32, error) {
	return s.cpu.call(ctx, fn, s.t.retAddr(), s.t.work+workStack, timeout, args...)
}

func (s *Session) readReg(addr uint32) (uint32, error) {
	w, err := s.cpu.readMem(addr, 1)
	if err != nil {
		return 0, err
	}
	return w[0], nil
}

func (s *Session) writeReg(addr, v uint32) error { return s.cpu.writeMem(addr, []uint32{v}) }

// regs is chips.Regs over the session (for chips.ReadInfo)
type regs struct{ s *Session }

func (r regs) ReadReg(addr uint32) (uint32, error) { return r.s.readReg(addr) }

func (r regs) WriteReg(addr, value, mask, delayUS uint32) error {
	if mask != 0xFFFFFFFF {
		old, err := r.s.readReg(addr)
		if err != nil {
			return err
		}
		value = old&^mask | value&mask
	}
	if err := r.s.writeReg(addr, value); err != nil {
		return err
	}
	if delayUS > 0 {
		time.Sleep(time.Duration(delayUS) * time.Microsecond)
	}
	return nil
}

func (s *Session) lock(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("the JTAG session is closed")
	}
	if err := s.prepare(ctx); err != nil {
		s.mu.Unlock()
		return err
	}
	return nil
}

// Info is the connected chip
func (s *Session) Info() (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.info
	in.Warnings = append([]string(nil), s.info.Warnings...)
	return in, nil
}

// ReadReg reads a 32-bit register or word of memory
func (s *Session) ReadReg(addr uint32) (uint32, error) {
	if err := s.lock(context.Background()); err != nil {
		return 0, err
	}
	defer s.mu.Unlock()
	return s.readReg(addr)
}

// WriteReg writes the bits of mask of a 32-bit register
func (s *Session) WriteReg(addr, value, mask uint32) error {
	if err := s.lock(context.Background()); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return regs{s}.WriteReg(addr, value, mask, 0)
}

// FlashSize is the size of the flash in bytes, 0 if unknown
func (s *Session) FlashSize() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info.FlashSize
}

// Reset resets the chip: with run it boots the application, otherwise it
// comes back to download mode, halted and ready for more
func (s *Session) Reset(run bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("the JTAG session is closed")
	}
	if !run {
		s.ready = false
		if err := s.enterDownload(); err != nil {
			return err
		}
		return s.prepare(context.Background())
	}
	if err := s.setBit(s.t.forceDownload, false); err != nil {
		return err
	}
	s.ready = false
	s.inApp = true
	return s.afterReset(false, s.cpu.resetSystem(false))
}

// Close closes the link; the chip stays as it is (halted unless Reset
// was called)
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if c, ok := s.link.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// md5Of is the MD5 of data, as the flash check compares it
func md5Of(b []byte) [16]byte { return md5.Sum(b) }
