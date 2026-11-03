// Package usb opens a USB device from user space without cgo and moves
// bulk and control transfers through one of its interfaces: usbfs on
// Linux, WinUSB on Windows, the IOKit user client on macOS. It is the
// lowest layer of the JTAG transport: the Espressif USB-JTAG interface is
// a vendor-class interface with one bulk OUT and one bulk IN endpoint.
//
// Sources: USB 2.0 specification, chapter 9 (standard descriptors and
// requests); linux/usbdevice_fs.h and Documentation/driver-api/usb (usbfs
// ABI); Microsoft WinUSB and SetupAPI documentation; Apple IOKit
// IOUSBLib.h (the IOUSBInterfaceInterface plug-in).
package usb

import (
	"errors"
	"fmt"
	"time"
)

// DeviceInfo is a USB device found by List
type DeviceInfo struct {
	VID, PID     uint16
	Serial       string
	Location     string // Linux: the sysfs name "1-2.3"; Windows: the device instance; macOS: locationID
	Manufacturer string
	Product      string

	// path is what Open needs on this OS
	path string
}

// Match selects an interface of a device by its class triple and, with
// ByNumber, by its bInterfaceNumber too (the FT2232H has two interfaces
// of the same class)
type Match struct {
	Class, SubClass, Protocol uint8
	ByNumber                  bool
	Number                    uint8
}

// matches tells whether an interface descriptor's number and class
// triple are those of m
func (m Match) matches(number, class, sub, proto uint8) bool {
	return class == m.Class && sub == m.SubClass && proto == m.Protocol && (!m.ByNumber || number == m.Number)
}

// Errors of the transport. Error wraps them with a hint for the user.
var (
	ErrNotFound     = errors.New("USB device not found")
	ErrPermission   = errors.New("no permission to open the USB device")
	ErrNoDriver     = errors.New("the USB interface has no WinUSB driver")
	ErrBusy         = errors.New("the USB interface is used by another program")
	ErrDisconnected = errors.New("the USB device was disconnected")
	ErrTimeout      = errors.New("USB transfer timed out")
	ErrStall        = errors.New("USB endpoint stalled")
	ErrUnsupported  = errors.New("USB access is not implemented on this OS")
)

// Error is a failed USB operation: Kind is one of the Err* values, Hint
// tells the user how to fix it when there is a fix
type Error struct {
	Op   string
	Kind error
	Err  error
	Hint string
}

func (e *Error) Error() string {
	s := e.Op + ": " + e.Kind.Error()
	if e.Err != nil && e.Err != e.Kind {
		s += " (" + e.Err.Error() + ")"
	}
	if e.Hint != "" {
		s += ". " + e.Hint
	}
	return s
}

func (e *Error) Unwrap() []error { return []error{e.Kind, e.Err} }

func newErr(op string, kind, err error, hint string) error {
	return &Error{Op: op, Kind: kind, Err: err, Hint: hint}
}

// UdevRule is the rule that gives the logged-in user access to the
// Espressif USB-JTAG on Linux (VID 0x303A, PID 0x1001)
const UdevRule = `SUBSYSTEMS=="usb", ATTRS{idVendor}=="303a", ATTRS{idProduct}=="1001", MODE="0660", TAG+="uaccess"`

// UdevHint is the fix for ErrPermission on Linux
const UdevHint = "Add the udev rule:\n" +
	"  echo '" + UdevRule + "' | sudo tee /etc/udev/rules.d/60-altesp-usbjtag.rules\n" +
	"  sudo udevadm control --reload-rules && sudo udevadm trigger\n" +
	"then unplug and plug the board again"

// WinUSBHint is the fix for ErrNoDriver on Windows
const WinUSBHint = "The JTAG interface of the board (\"USB JTAG/serial debug unit\", interface 2) needs the WinUSB driver. " +
	"Install it with Zadig (https://zadig.akeo.ie): Options > List All Devices, select " +
	"\"USB JTAG/serial debug unit (Interface 2)\", choose WinUSB and press Replace Driver; " +
	"or run the Espressif driver installer (idf-env driver install --espressif). " +
	"The serial port (interface 0) keeps its driver"

// Endpoints are the bulk endpoints of the claimed interface
type Endpoints struct {
	Interface  uint8
	In, Out    uint8 // endpoint addresses: In has bit 7 set
	InPacket   int   // wMaxPacketSize
	OutPacket  int
	AltSetting uint8
}

// Device is an open device with one claimed interface
type Device struct {
	Info DeviceInfo
	EP   Endpoints
	h    handle
}

// handle is the OS part of a Device
type handle interface {
	bulkOut(ep uint8, data []byte, timeout time.Duration) (int, error)
	bulkIn(ep uint8, buf []byte, timeout time.Duration) (int, error)
	control(reqType, req uint8, value, index uint16, data []byte, timeout time.Duration) (int, error)
	close() error
}

// List finds the devices with this VID and PID; 0 matches any
func List(vid, pid uint16) ([]DeviceInfo, error) {
	all, err := list()
	if err != nil {
		return nil, err
	}
	var out []DeviceInfo
	for _, d := range all {
		if (vid == 0 || d.VID == vid) && (pid == 0 || d.PID == pid) {
			out = append(out, d)
		}
	}
	return out, nil
}

// Open opens the device and claims the first interface of the class
// triple with a bulk IN and a bulk OUT endpoint
func Open(d DeviceInfo, m Match) (*Device, error) {
	return open(d, m)
}

// BulkOut writes all of data to the OUT endpoint
func (d *Device) BulkOut(data []byte, timeout time.Duration) error {
	for len(data) > 0 {
		n, err := d.h.bulkOut(d.EP.Out, data, timeout)
		if err != nil {
			return err
		}
		if n <= 0 {
			return newErr("bulk out", ErrTimeout, nil, "")
		}
		data = data[n:]
	}
	return nil
}

// BulkIn reads what the IN endpoint has, up to len(buf); it waits up to
// timeout for the first packet
func (d *Device) BulkIn(buf []byte, timeout time.Duration) (int, error) {
	return d.h.bulkIn(d.EP.In, buf, timeout)
}

// Control is a control transfer on endpoint 0; the direction is bit 7
// of reqType
func (d *Device) Control(reqType, req uint8, value, index uint16, data []byte, timeout time.Duration) (int, error) {
	return d.h.control(reqType, req, value, index, data, timeout)
}

// Close releases the interface and closes the device
func (d *Device) Close() error {
	if d.h == nil {
		return nil
	}
	err := d.h.close()
	d.h = nil
	return err
}

// Descriptor types and the bulk transfer type (USB 2.0, tables 9-5 and
// 9-13)
const (
	descDevice    = 1
	descConfig    = 2
	descInterface = 4
	descEndpoint  = 5
	xferBulk      = 2
)

// FindEndpoints finds the interface of the class triple in a
// configuration descriptor (with its interface and endpoint descriptors
// following it, as GET_DESCRIPTOR(CONFIGURATION) returns it) and its
// bulk endpoints
func FindEndpoints(config []byte, m Match) (Endpoints, error) {
	var cur *Endpoints
	var found []Endpoints
	for i := 0; i+2 <= len(config); {
		l := int(config[i])
		if l < 2 || i+l > len(config) {
			return Endpoints{}, fmt.Errorf("bad USB descriptor at offset %d", i)
		}
		d := config[i : i+l]
		switch d[1] {
		case descInterface:
			if cur != nil {
				found = append(found, *cur)
				cur = nil
			}
			if l >= 9 && m.matches(d[2], d[5], d[6], d[7]) {
				cur = &Endpoints{Interface: d[2], AltSetting: d[3]}
			}
		case descEndpoint:
			if cur != nil && l >= 7 && d[3]&3 == xferBulk {
				size := int(d[4]) | int(d[5]&0x07)<<8
				if d[2]&0x80 != 0 {
					if cur.In == 0 {
						cur.In, cur.InPacket = d[2], size
					}
				} else if cur.Out == 0 {
					cur.Out, cur.OutPacket = d[2], size
				}
			}
		}
		i += l
	}
	if cur != nil {
		found = append(found, *cur)
	}
	for _, e := range found {
		if e.In != 0 && e.Out != 0 {
			return e, nil
		}
	}
	return Endpoints{}, newErr("open", ErrNotFound, fmt.Errorf("no interface of class %02x/%02x/%02x with bulk endpoints", m.Class, m.SubClass, m.Protocol), "")
}
