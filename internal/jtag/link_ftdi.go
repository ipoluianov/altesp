package jtag

import (
	"errors"
	"runtime"

	"github.com/ipoluianov/altesp/internal/jtag/ftdi"
	"github.com/ipoluianov/altesp/internal/jtag/usb"
)

// Adapter is the kind of JTAG adapter
type Adapter int

const (
	// AdapterUSBJTAG is the built-in USB-Serial-JTAG of the chip
	AdapterUSBJTAG Adapter = iota
	// AdapterFTDI is an FT2232H in MPSSE mode: ESP-Prog, or a board with
	// the same circuit, wired to the JTAG pins of the chip
	AdapterFTDI
)

func (a Adapter) String() string {
	if a == AdapterFTDI {
		return "ESP-Prog"
	}
	return "USB-JTAG"
}

// Hints for the FT2232H. The JTAG channel A is interface 0; channel B
// (interface 1) is the UART of ESP-Prog and keeps its driver.
const (
	FTDIUdevRule = `SUBSYSTEMS=="usb", ATTRS{idVendor}=="0403", ATTRS{idProduct}=="6010", MODE="0660", TAG+="uaccess"`
	FTDIUdevHint = "Add the udev rule:\n" +
		"  echo '" + FTDIUdevRule + "' | sudo tee /etc/udev/rules.d/60-altesp-ftdi.rules\n" +
		"  sudo udevadm control --reload-rules && sudo udevadm trigger\n" +
		"then unplug and plug ESP-Prog again"
	FTDIWinUSBHint = "The JTAG channel of ESP-Prog (\"Dual RS232-HS (Interface 0)\") needs the WinUSB driver instead of the FTDI driver. " +
		"Install it with Zadig (https://zadig.akeo.ie): Options > List All Devices, select \"Dual RS232-HS (Interface 0)\", " +
		"choose WinUSB and press Replace Driver. " +
		"Leave Interface 1 alone: it is the serial port"
	// unverified: whether the macOS FTDI driver holds interface 0
	FTDIMacHint = "The macOS FTDI serial driver may hold the JTAG channel of ESP-Prog; " +
		"close programs that use its serial port, and if that is not enough, unload the driver " +
		"(sudo kextunload -b com.apple.driver.AppleUSBFTDI) while flashing over JTAG"
)

var ftdiMatch = usb.Match{Class: ftdi.Class, SubClass: ftdi.SubClass, Protocol: ftdi.Protocol, ByNumber: true, Number: ftdi.JTAGInterface}

// ftdiPort is an FTDI interface over the usb layer
type ftdiPort struct{ dev *usb.Device }

func (p ftdiPort) Write(b []byte) error { return p.dev.BulkOut(b, usbTimeout) }

func (p ftdiPort) Read(b []byte) (int, error) { return p.dev.BulkIn(b, usbTimeout) }

func (p ftdiPort) Control(req uint8, value, index uint16) error {
	_, err := p.dev.Control(0x40, req, value, index, nil, usbTimeout)
	return err
}

func (p ftdiPort) Close() error { return p.dev.Close() }

// ftdiHint gives the usb errors the fix for the FT2232H
func ftdiHint(err error) error {
	var ue *usb.Error
	if !errors.As(err, &ue) {
		return err
	}
	e := *ue
	switch {
	case errors.Is(err, usb.ErrPermission):
		e.Hint = FTDIUdevHint
	case errors.Is(err, usb.ErrNoDriver):
		e.Hint = FTDIWinUSBHint
	case runtime.GOOS == "windows" && errors.Is(err, usb.ErrNotFound):
		// with the FTDI driver the interfaces are not USB interfaces of
		// a composite device, so none is found
		e.Kind, e.Hint = usb.ErrNoDriver, FTDIWinUSBHint
	case runtime.GOOS == "darwin" && errors.Is(err, usb.ErrBusy):
		e.Hint = FTDIMacHint
	default:
		return err
	}
	return &e
}

// openFTDI opens channel A of an FT2232H in MPSSE mode
func openFTDI(d Device) (*ftdi.Link, error) {
	infos, err := usb.List(d.VID, d.PID)
	if err != nil {
		return nil, err
	}
	for _, in := range infos {
		if d.Serial != "" && in.Serial != d.Serial {
			continue
		}
		if d.Location != "" && d.Serial == "" && in.Location != d.Location {
			continue
		}
		dev, err := usb.Open(in, ftdiMatch)
		if err != nil {
			return nil, ftdiHint(err)
		}
		l, err := ftdi.Open(ftdiPort{dev}, ftdi.JTAGInterface, dev.EP.InPacket)
		if err != nil {
			dev.Close()
			return nil, err
		}
		return l, nil
	}
	return nil, &usb.Error{Op: "open", Kind: usb.ErrNotFound}
}
