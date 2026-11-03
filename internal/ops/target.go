package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/ipoluianov/altesp/internal/chips"
	"github.com/ipoluianov/altesp/internal/jtag"
	"github.com/ipoluianov/altesp/internal/loader"
)

// target is what the operations do with the flash and the registers of a
// chip, the same through the serial loader and through JTAG
type target interface {
	ReadFlash(ctx context.Context, addr uint32, size int, progress loader.Progress) ([]byte, error)
	// WriteFlash erases what it covers; compress is a hint for the loader
	WriteFlash(ctx context.Context, addr uint32, data []byte, compress bool, progress loader.Progress) error
	FlashMD5(ctx context.Context, addr uint32, size int) ([16]byte, error)
	EraseFlash(ctx context.Context) error
	EraseRegion(ctx context.Context, addr uint32, size int) error
	ReadReg(ctx context.Context, addr uint32) (uint32, error)
	WriteReg(ctx context.Context, addr, value, mask, delayUS uint32) error
	RunApp(ctx context.Context) error
}

// The serial loader is a target as it is
var _ target = (*loader.Loader)(nil)

// jtagTarget is a JTAG session as a target
type jtagTarget struct{ s *jtag.Session }

func (j jtagTarget) ReadFlash(ctx context.Context, addr uint32, size int, progress loader.Progress) ([]byte, error) {
	return j.s.ReadFlash(ctx, addr, size, progress)
}

func (j jtagTarget) WriteFlash(ctx context.Context, addr uint32, data []byte, _ bool, progress loader.Progress) error {
	return j.s.WriteFlash(ctx, addr, data, progress)
}

func (j jtagTarget) FlashMD5(ctx context.Context, addr uint32, size int) ([16]byte, error) {
	return j.s.FlashMD5(ctx, addr, size)
}

func (j jtagTarget) EraseFlash(ctx context.Context) error { return j.s.EraseFlash(ctx) }

func (j jtagTarget) EraseRegion(ctx context.Context, addr uint32, size int) error {
	return j.s.EraseRegion(ctx, addr, size)
}

func (j jtagTarget) ReadReg(_ context.Context, addr uint32) (uint32, error) { return j.s.ReadReg(addr) }

func (j jtagTarget) WriteReg(_ context.Context, addr, value, mask, delayUS uint32) error {
	return j.s.WriteReg(addr, value, mask)
}

func (j jtagTarget) RunApp(context.Context) error { return j.s.Reset(true) }

// JTAGDevice is a chip's built-in USB-JTAG on the USB bus
type JTAGDevice = jtag.Device

// JTAGDevices lists the JTAG adapters: the built-in USB-JTAG devices
// (ESP32-C3, C6, H2...) and ESP-Prog (FT2232H)
func JTAGDevices() ([]JTAGDevice, error) { return listJTAG() }

// listJTAG and openJTAG are jtag.List and jtag.OpenWith; tests replace them
var (
	listJTAG = jtag.List
	openJTAG = func(d JTAGDevice, chipKey string, opt jtag.Options) (*jtag.Session, error) {
		return jtag.OpenWith(d, chipKey, opt)
	}
)

// JTAGSupported tells whether a chip can be flashed over its built-in
// USB-JTAG by this program
func JTAGSupported(chipKey string) bool { return jtag.Supported(chipKey) }

// ConnectJTAG connects to a chip over its built-in USB-JTAG, without the
// UART loader: the chip is halted in its ROM, which writes the flash.
// chipKey may be "" to tell the chip by its JTAG IDCODE.
func ConnectJTAG(ctx context.Context, d JTAGDevice, chipKey string, status func(string)) (*Session, error) {
	if status == nil {
		status = func(string) {}
	}
	js, err := openJTAG(d, chipKey, jtag.Options{Status: status})
	if err != nil {
		return nil, err
	}
	return sessionOverJTAG(ctx, js, d)
}

// sessionOverJTAG fills the card of a chip reached over JTAG
func sessionOverJTAG(ctx context.Context, js *jtag.Session, d JTAGDevice) (*Session, error) {
	info, err := js.Info()
	if err != nil {
		js.Close()
		return nil, err
	}
	c := chips.ByKey(info.Key)
	if c == nil {
		js.Close()
		return nil, fmt.Errorf("unknown chip %s over JTAG", info.Key)
	}
	s := &Session{t: jtagTarget{js}, js: js, board: Board{
		Port:    PortInfo{Name: "jtag:" + d.Serial, Short: d.Kind() + " " + d.Serial, USB: true, VID: d.VID, PID: d.PID, SerialNumber: d.Serial, Location: d.Location},
		Chip:    c,
		Info:    info.Info,
		CanRead: true,
		JTAG:    true,
	}}
	b := &s.board
	b.FlashSize, b.FlashSizeKnown = js.FlashSize(), js.FlashSize() > 0
	if !b.FlashSizeKnown {
		b.FlashSize = DefaultFlashSize
		b.Notes = append(b.Notes, "The flash size could not be detected; 4 MB is assumed")
	}
	b.Notes = append(b.Notes, info.Warnings...)
	s.readPartitions(ctx)
	s.checkFirmware(ctx)
	return s, nil
}

// PickJTAG finds a USB-JTAG device: by its serial number when given, else
// the only one
func PickJTAG(serial string) (JTAGDevice, error) {
	devs, err := listJTAG()
	if err != nil {
		return JTAGDevice{}, err
	}
	for _, d := range devs {
		if serial == "" && len(devs) == 1 || serial != "" && d.Serial == serial {
			return d, nil
		}
	}
	switch {
	case len(devs) == 0:
		return JTAGDevice{}, errors.New("no USB-JTAG device found (ESP32-C3, C6, H2 on their own USB port)")
	case serial != "":
		return JTAGDevice{}, fmt.Errorf("no USB-JTAG device with the serial number %s", serial)
	}
	return JTAGDevice{}, errors.New("several USB-JTAG devices: give one's serial number")
}
