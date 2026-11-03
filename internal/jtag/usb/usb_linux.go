//go:build linux

package usb

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux: devices are listed from sysfs and opened through usbfs
// (/dev/bus/usb/BBB/DDD) with the ioctls of linux/usbdevice_fs.h. Reading
// the usbfs node returns the device descriptor followed by the
// configuration descriptors (Documentation/driver-api/usb/usb.rst,
// "/dev/bus/usb/BBB/DDD").

var sysfsRoot = "/sys/bus/usb/devices"

func list() ([]DeviceInfo, error) { return listSysfs(sysfsRoot) }

// listSysfs reads the USB devices under a sysfs directory; the interface
// entries ("1-2:1.0") and the root hubs' ports are skipped by needing
// idVendor
func listSysfs(root string) ([]DeviceInfo, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, newErr("list", ErrUnsupported, err, "")
	}
	var out []DeviceInfo
	for _, e := range entries {
		name := e.Name()
		if strings.Contains(name, ":") {
			continue
		}
		dir := filepath.Join(root, name)
		vid, ok1 := hexAttr(dir, "idVendor")
		pid, ok2 := hexAttr(dir, "idProduct")
		bus, ok3 := decAttr(dir, "busnum")
		dev, ok4 := decAttr(dir, "devnum")
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		out = append(out, DeviceInfo{
			VID: uint16(vid), PID: uint16(pid),
			Serial:       strAttr(dir, "serial"),
			Manufacturer: strAttr(dir, "manufacturer"),
			Product:      strAttr(dir, "product"),
			Location:     name,
			path:         fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, dev),
		})
	}
	return out, nil
}

func strAttr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func hexAttr(dir, name string) (uint64, bool) {
	v, err := strconv.ParseUint(strAttr(dir, name), 16, 16)
	return v, err == nil
}

func decAttr(dir, name string) (uint64, bool) {
	v, err := strconv.ParseUint(strAttr(dir, name), 10, 16)
	return v, err == nil
}

// The ioctl numbers of linux/usbdevice_fs.h, made like _IOC of
// asm-generic/ioctl.h: dir<<30 | size<<16 | 'U'<<8 | nr. Architectures
// with another _IOC layout (mips, powerpc, sparc) are not supported.
const (
	iocWrite = 1
	iocRead  = 2
)

func ioc(dir, nr, size uintptr) uintptr { return dir<<30 | size<<16 | 'U'<<8 | nr }

// struct usbdevfs_ctrltransfer
type ctrlTransfer struct {
	reqType, req         uint8
	value, index, length uint16
	timeout              uint32
	data                 unsafe.Pointer
}

// struct usbdevfs_bulktransfer
type bulkTransfer struct {
	ep, length, timeout uint32
	data                unsafe.Pointer
}

// struct usbdevfs_getdriver
type getDriver struct {
	iface  uint32
	driver [256]byte
}

// struct usbdevfs_ioctl
type usbIoctl struct {
	ifno, code int32
	data       unsafe.Pointer
}

var (
	ioctlControl    = ioc(iocRead|iocWrite, 0, unsafe.Sizeof(ctrlTransfer{}))
	ioctlBulk       = ioc(iocRead|iocWrite, 2, unsafe.Sizeof(bulkTransfer{}))
	ioctlGetDriver  = ioc(iocWrite, 8, unsafe.Sizeof(getDriver{}))
	ioctlClaim      = ioc(iocRead, 15, 4)
	ioctlRelease    = ioc(iocRead, 16, 4)
	ioctlIoctl      = ioc(iocRead|iocWrite, 18, unsafe.Sizeof(usbIoctl{}))
	ioctlClearHalt  = ioc(iocRead, 21, 4)
	ioctlDisconnect = ioc(0, 22, 0)
)

type linuxHandle struct {
	fd    int
	iface uint32
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) (int, error) {
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return int(r), errno
	}
	return int(r), nil
}

func open(d DeviceInfo, m Match) (*Device, error) {
	if d.path == "" {
		return nil, newErr("open", ErrNotFound, nil, "")
	}
	fd, err := unix.Open(d.path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		switch {
		case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
			return nil, newErr("open "+d.path, ErrPermission, err, UdevHint)
		case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENODEV):
			return nil, newErr("open "+d.path, ErrDisconnected, err, "")
		}
		return nil, newErr("open "+d.path, ErrNotFound, err, "")
	}
	ok := false
	defer func() {
		if !ok {
			unix.Close(fd)
		}
	}()
	desc := make([]byte, 4096)
	n, err := unix.Pread(fd, desc, 0)
	if err != nil {
		return nil, newErr("read descriptors", ErrNotFound, err, "")
	}
	desc = desc[:n]
	if len(desc) < 18 || desc[1] != descDevice {
		return nil, newErr("read descriptors", ErrNotFound, errors.New("no device descriptor"), "")
	}
	ep, err := FindEndpoints(desc[desc[0]:], m)
	if err != nil {
		return nil, err
	}
	h := &linuxHandle{fd: fd, iface: uint32(ep.Interface)}
	if err := h.claim(); err != nil {
		return nil, err
	}
	ok = true
	return &Device{Info: d, EP: ep, h: h}, nil
}

// claim claims the interface; a kernel driver bound to it is detached
// first, another usbfs user means the interface is busy
func (h *linuxHandle) claim() error {
	iface := h.iface
	_, err := ioctl(h.fd, ioctlClaim, unsafe.Pointer(&iface))
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EBUSY) {
		return newErr("claim interface", ErrBusy, err, "")
	}
	gd := getDriver{iface: iface}
	if _, e := ioctl(h.fd, ioctlGetDriver, unsafe.Pointer(&gd)); e == nil {
		drv := string(gd.driver[:max(0, bytes.IndexByte(gd.driver[:], 0))])
		if drv == "usbfs" {
			return newErr("claim interface", ErrBusy, err,
				"Another program (OpenOCD, another AltEsp window) has the JTAG interface open; close it")
		}
		req := usbIoctl{ifno: int32(iface), code: int32(ioctlDisconnect)}
		if _, e := ioctl(h.fd, ioctlIoctl, unsafe.Pointer(&req)); e == nil {
			if _, err = ioctl(h.fd, ioctlClaim, unsafe.Pointer(&iface)); err == nil {
				return nil
			}
		}
		return newErr("claim interface", ErrBusy, err, "The kernel driver "+drv+" holds the JTAG interface")
	}
	return newErr("claim interface", ErrBusy, err, "")
}

func ms(t time.Duration) uint32 {
	if t <= 0 {
		return 0
	}
	if v := t / time.Millisecond; v > 0 {
		return uint32(v)
	}
	return 1
}

func (h *linuxHandle) mapErr(op string, ep uint8, err error) error {
	switch {
	case errors.Is(err, unix.ETIMEDOUT):
		return newErr(op, ErrTimeout, err, "")
	case errors.Is(err, unix.EPIPE):
		e := uint32(ep)
		ioctl(h.fd, ioctlClearHalt, unsafe.Pointer(&e))
		return newErr(op, ErrStall, err, "")
	case errors.Is(err, unix.ENODEV), errors.Is(err, unix.ESHUTDOWN), errors.Is(err, unix.ENOENT):
		return newErr(op, ErrDisconnected, err, "")
	}
	return newErr(op, ErrDisconnected, err, "")
}

func (h *linuxHandle) bulk(op string, ep uint8, buf []byte, timeout time.Duration) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	bt := bulkTransfer{ep: uint32(ep), length: uint32(len(buf)), timeout: ms(timeout), data: unsafe.Pointer(&buf[0])}
	n, err := ioctl(h.fd, ioctlBulk, unsafe.Pointer(&bt))
	if err != nil {
		return 0, h.mapErr(op, ep, err)
	}
	return n, nil
}

func (h *linuxHandle) bulkOut(ep uint8, data []byte, timeout time.Duration) (int, error) {
	return h.bulk("bulk out", ep, data, timeout)
}

func (h *linuxHandle) bulkIn(ep uint8, buf []byte, timeout time.Duration) (int, error) {
	return h.bulk("bulk in", ep, buf, timeout)
}

func (h *linuxHandle) control(reqType, req uint8, value, index uint16, data []byte, timeout time.Duration) (int, error) {
	ct := ctrlTransfer{reqType: reqType, req: req, value: value, index: index, length: uint16(len(data)), timeout: ms(timeout)}
	if len(data) > 0 {
		ct.data = unsafe.Pointer(&data[0])
	}
	n, err := ioctl(h.fd, ioctlControl, unsafe.Pointer(&ct))
	if err != nil {
		return 0, h.mapErr("control", 0, err)
	}
	return n, nil
}

func (h *linuxHandle) close() error {
	iface := h.iface
	ioctl(h.fd, ioctlRelease, unsafe.Pointer(&iface))
	return unix.Close(h.fd)
}
