package jtag

import (
	"time"

	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
	"github.com/ipoluianov/altesp/internal/jtag/usb"
)

// Link is the pair of bulk endpoints of the JTAG interface
type Link = espjtag.Link

// The Espressif USB-Serial-JTAG device: VID 0x303A, PID 0x1001 (ESP-IDF
// docs "JTAG Debugging" > "Configure Other JTAG Interfaces"). Its JTAG
// interface is the vendor-specific interface 2: class 0xFF, subclass
// 0xFF, protocol 0x01 (TRM "USB Serial/JTAG Controller": the device has a
// CDC-ACM function and a vendor JTAG interface). unverified: the
// subclass and protocol values.
const (
	EspressifVID   = 0x303A
	USBSerialJTAG  = 0x1001
	jtagClass      = 0xFF
	jtagSubClass   = 0xFF
	jtagProtocol   = 0x01
	usbTimeout     = 2 * time.Second
	reopenAttempts = 50
)

var jtagMatch = usb.Match{Class: jtagClass, SubClass: jtagSubClass, Protocol: jtagProtocol}

// usbLink moves the commands over the bulk endpoints. Reads ask for
// whole packets (a smaller request would overflow on a full packet) and
// keep what the caller didn't take.
type usbLink struct {
	dev  *usb.Device
	rest []byte
}

func (l *usbLink) Write(p []byte) error { return l.dev.BulkOut(p, usbTimeout) }

func (l *usbLink) Read(p []byte) (int, error) {
	if len(l.rest) == 0 {
		size := l.dev.EP.InPacket
		if size <= 0 {
			size = 64
		}
		n := (len(p) + size - 1) / size * size
		buf := make([]byte, n)
		got, err := l.dev.BulkIn(buf, usbTimeout)
		if err != nil {
			return 0, err
		}
		l.rest = buf[:got]
		if got == 0 {
			return 0, usb.ErrTimeout
		}
	}
	n := copy(p, l.rest)
	l.rest = l.rest[n:]
	return n, nil
}

func (l *usbLink) Close() error { return l.dev.Close() }

func openUSB(d Device) (*usbLink, error) {
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
		dev, err := usb.Open(in, jtagMatch)
		if err != nil {
			return nil, err
		}
		return &usbLink{dev: dev}, nil
	}
	return nil, &usb.Error{Op: "open", Kind: usb.ErrNotFound}
}
