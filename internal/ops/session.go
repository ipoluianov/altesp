// Package ops is what the window and the command line do with a board:
// connect and tell what it is, flash, read, erase. It puts together the
// loader, the chip table and the formats; the interface code calls only
// this package.
package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/ipoluianov/altesp/internal/chips"
	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/loader"
	"github.com/ipoluianov/altesp/internal/serial"
)

// Board is what is known about the connected board: the chip card
type Board struct {
	Port     serial.PortInfo
	Chip     *chips.Chip
	Info     chips.Info
	Security chips.Security
	// SecureDownload: the chip is in Secure Download Mode; only writing
	// works
	SecureDownload bool
	// Stub tells whether the stub loader runs; StubErr is why not
	Stub    bool
	StubErr error
	Reset   loader.Reset
	Baud    int
	// FlashSize is the size used for the checks: the detected one, or
	// DefaultFlashSize when it is unknown (FlashSizeKnown false)
	FlashSize      int
	FlashSizeKnown bool
	// Partitions is the partition table read from the board, at
	// PartitionTableAddr; nil when it couldn't be read
	Partitions         []formats.Partition
	PartitionTableAddr uint32
	// Apps are the applications found in the app partitions, by name
	Apps map[string]*formats.Image
	// CanRead tells whether flash can be read: the stub, or the ROM's
	// slow read
	CanRead bool
	// Notes are things the user should know about the board, in words
	Notes []string
}

// DefaultFlashSize is assumed when the flash size can't be detected
const DefaultFlashSize = 4 << 20

// Session is a board in download mode
type Session struct {
	port  serial.Port
	l     *loader.Loader
	board Board
}

// Options of Connect
type Options struct {
	Reset loader.Reset
	// Baud is the rate after the stub starts: 0 is DefaultBaud. Boards
	// with built-in USB ignore it.
	Baud   int
	NoStub bool
	// Manual is called when the user has to put the board into download
	// mode by hand (see loader.Options)
	Manual func()
	// Status tells what is being done, for the status bar
	Status func(string)
}

// DefaultBaud is the rate of flashing
const DefaultBaud = 921600

// openPort opens the port for the loader; tests replace it
var openPort = func(info serial.PortInfo) (serial.Port, error) {
	return serial.Open(info.Name, serial.DefaultConfig())
}

// Connect resets the board into download mode and finds out what it is
func Connect(ctx context.Context, info serial.PortInfo, opt Options) (*Session, error) {
	status := opt.Status
	if status == nil {
		status = func(string) {}
	}
	port, err := openPort(info)
	if err != nil {
		return nil, portError(info, err)
	}
	status("Entering download mode")
	l, err := loader.Connect(ctx, port, info, loader.Options{Reset: opt.Reset, Manual: opt.Manual})
	if err != nil {
		port.Close()
		return nil, err
	}
	s := &Session{port: port, l: l, board: Board{Port: info, Reset: l.Reset(), Baud: l.Baud()}}
	if err := s.identify(ctx, opt, status); err != nil {
		s.Close(ctx, false)
		return nil, err
	}
	return s, nil
}

// portError adds what to do to the errors of opening a port
func portError(info serial.PortInfo, err error) error {
	switch {
	case errors.Is(err, serial.ErrBusy):
		if h := serial.Holder(info.Name); h != "" {
			return fmt.Errorf("%s is used by %s", info.Short, h)
		}
		return fmt.Errorf("%s is used by another program", info.Short)
	case errors.Is(err, serial.ErrPermission):
		return fmt.Errorf("%s: %w%s", info.Short, err, permissionHint())
	}
	return fmt.Errorf("%s: %w", info.Short, err)
}

// identify detects the chip, starts the stub and reads the card
func (s *Session) identify(ctx context.Context, opt Options, status func(string)) error {
	b := &s.board
	status("Detecting the chip")
	si, siErr := s.l.SecurityInfo(ctx)
	if siErr == nil {
		b.Security = chips.DecodeSecurity(si.Flags, si.FlashCryptCnt)
	}
	if siErr == nil && si.HasChipID {
		b.Chip = chips.ByChipID(si.ChipID)
		if b.Chip == nil {
			return fmt.Errorf("unknown chip (ID 0x%X)", si.ChipID)
		}
	}
	// The ESP32 and the ESP32-S2 are told by a word of their ROM; the
	// other chips are probed at a register of their own. Secure Download
	// Mode refuses the read, which tells that mode whatever the security
	// flags say.
	addr := chips.MagicRegAddr
	if b.Chip != nil {
		addr = b.Chip.ProbeAddr()
	}
	v, err := s.l.ReadReg(ctx, addr)
	var le *loader.Error
	switch {
	case errors.As(err, &le) && siErr == nil:
		b.SecureDownload = true
	case err != nil:
		return err
	case b.Chip == nil:
		if b.Chip = chips.ByMagic(v); b.Chip == nil {
			return fmt.Errorf("unknown chip (magic 0x%08X)", v)
		}
	}
	if b.SecureDownload {
		s.l.SetSecureDownload(true)
		b.Notes = append(b.Notes, "Secure Download Mode: the board can be flashed, but its flash can't be read or erased, and the stub loader can't run")
		if b.Chip == nil {
			return errors.New("the chip is in Secure Download Mode and doesn't tell what it is")
		}
	}
	c := b.Chip
	s.l.SetEncryptWord(c.ROMEncryptWord)
	if b.Security.SecureDownload && !b.SecureDownload {
		b.SecureDownload = true
		s.l.SetSecureDownload(true)
	}
	if b.SecureDownload {
		b.FlashSize = DefaultFlashSize
		return nil
	}

	status("Reading the chip")
	if err := s.l.SPIAttach(ctx); err != nil {
		return err
	}
	r := regs{ctx: ctx, l: s.l}
	info, err := c.ReadInfo(r)
	if err != nil {
		b.Notes = append(b.Notes, "Chip details could not be read: "+err.Error())
	}
	b.Info = info
	if siErr != nil {
		// The ESP32's ROM has no GET_SECURITY_INFO: the eFuses tell
		if sec, err := c.SecurityFromEFuse(r); err == nil {
			b.Security = sec
		}
	}

	if !opt.NoStub {
		status("Starting the stub loader")
		b.StubErr = s.startStub(ctx, c, info.Revision)
		b.Stub = b.StubErr == nil
		if b.StubErr != nil {
			b.Notes = append(b.Notes, "The stub loader did not start ("+b.StubErr.Error()+"): flashing is slower, erasing is done by writing, reading may be unavailable")
		}
	}
	if b.Stub && !builtinUSB(b.Port) {
		baud := opt.Baud
		if baud <= 0 {
			baud = DefaultBaud
		}
		if baud != s.l.Baud() {
			status(fmt.Sprintf("Switching to %d baud", baud))
			if err := s.l.ChangeBaud(ctx, baud); err != nil {
				return err
			}
		}
	}
	b.Baud = s.l.Baud()

	b.FlashSize, b.FlashSizeKnown = info.FlashSize, info.FlashSize > 0
	if !b.FlashSizeKnown {
		b.FlashSize = DefaultFlashSize
		b.Notes = append(b.Notes, "The flash size could not be detected; 4 MB is assumed")
	}
	if err := s.l.SetFlashSize(ctx, uint32(b.FlashSize)); err != nil {
		return err
	}
	if b.Security.FlashEncryption {
		b.Notes = append(b.Notes, "Flash encryption is on: the flash reads as ciphertext, and a backup can only be restored onto this chip")
	}

	b.CanRead = b.Stub || s.canReadSlow(ctx)
	if b.CanRead && !b.Security.FlashEncryption {
		status("Reading the partition table")
		s.readPartitions(ctx)
	}
	return nil
}

// startStub loads the stub of the chip and runs it
func (s *Session) startStub(ctx context.Context, c *chips.Chip, rev chips.Revision) error {
	img, err := loader.StubFor(c.StubName(rev))
	if err != nil {
		return err
	}
	if err := s.l.LoadStub(ctx, img); err != nil {
		return err
	}
	// The stub attaches the flash on its own on most chips; once more is
	// harmless and needed on some
	return s.l.SPIAttach(ctx)
}

// canReadSlow tries the ROM's slow read once
func (s *Session) canReadSlow(ctx context.Context) bool {
	_, err := s.l.ReadFlash(ctx, 0, 64, nil)
	return err == nil
}

// builtinUSB tells a port of the chip's own USB: its baud rate means nothing
func builtinUSB(p serial.PortInfo) bool {
	return p.USB && p.VID == 0x303a
}

// partitionScan is how much of the flash start is read to find the
// partition table: its offset is configurable, and the bootloader before
// it may be large
const partitionScan = 0x10000

// readPartitions finds and reads the partition table and the headers of
// the applications. Failures leave the card without them.
func (s *Session) readPartitions(ctx context.Context) {
	b := &s.board
	size := partitionScan
	if !b.Stub {
		// The ROM reads 64 bytes per command: only the usual place
		data, err := s.l.ReadFlash(ctx, 0x8000, 0xC00, nil)
		if err != nil {
			return
		}
		if parts, err := formats.ParsePartitionTable(data); err == nil {
			b.Partitions, b.PartitionTableAddr = parts, 0x8000
		}
	} else {
		data, err := s.l.ReadFlash(ctx, 0, size, nil)
		if err != nil {
			return
		}
		off, ok := formats.FindPartitionTable(data)
		if !ok {
			return
		}
		end := min(int(off)+0xC00, len(data))
		parts, err := formats.ParsePartitionTable(data[off:end])
		if err != nil {
			return
		}
		b.Partitions, b.PartitionTableAddr = parts, off
	}
	b.Apps = map[string]*formats.Image{}
	for _, p := range b.Partitions {
		if !p.IsApp() {
			continue
		}
		head, err := s.l.ReadFlash(ctx, p.Offset, formats.AppHeadSize, nil)
		if err != nil {
			continue
		}
		if img, ok := formats.ParseAppHead(head); ok {
			b.Apps[p.Name] = img
		}
	}
}

// Board returns the card of the board
func (s *Session) Board() Board { return s.board }

// Close ends the session; run resets the board into its application
// (with built-in USB its port then disappears and comes back)
func (s *Session) Close(ctx context.Context, run bool) error {
	var err error
	if run {
		err = s.l.RunApp(ctx)
	}
	if cerr := s.port.Close(); err == nil {
		err = cerr
	}
	return err
}

// regs lets the chip table read registers through the loader
type regs struct {
	ctx context.Context
	l   *loader.Loader
}

func (r regs) ReadReg(addr uint32) (uint32, error) { return r.l.ReadReg(r.ctx, addr) }

// Baud lets the chip table work out the ESP32's crystal from the divider
// of its UART (chips.BaudRegs); it is read before the stub starts
func (r regs) Baud() int { return r.l.Baud() }
func (r regs) WriteReg(addr, value, mask, delayUS uint32) error {
	return r.l.WriteReg(r.ctx, addr, value, mask, delayUS)
}
