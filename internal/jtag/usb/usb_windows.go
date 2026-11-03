//go:build windows

package usb

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows: devices are listed with SetupAPI (enumerator "USB"), the
// interface of a composite device is its child "USB\VID_xxxx&PID_xxxx&MI_nn"
// device, recognized by its compatible ID "USB\Class_ff&SubClass_ff&Prot_01".
// It must have the WinUSB service; its device interface path comes from
// the DeviceInterfaceGUIDs value of its device key (set by the WinUSB INF
// or by the MS OS descriptors) through CM_Get_Device_Interface_List. The
// transfers go through winusb.dll (Microsoft docs "WinUSB Functions").

var (
	winusb                  = windows.NewLazySystemDLL("winusb.dll")
	procInitialize          = winusb.NewProc("WinUsb_Initialize")
	procFree                = winusb.NewProc("WinUsb_Free")
	procQueryInterface      = winusb.NewProc("WinUsb_QueryInterfaceSettings")
	procQueryPipe           = winusb.NewProc("WinUsb_QueryPipe")
	procSetPipePolicy       = winusb.NewProc("WinUsb_SetPipePolicy")
	procReadPipe            = winusb.NewProc("WinUsb_ReadPipe")
	procWritePipe           = winusb.NewProc("WinUsb_WritePipe")
	procControlTransfer     = winusb.NewProc("WinUsb_ControlTransfer")
	procResetPipe           = winusb.NewProc("WinUsb_ResetPipe")
	devpkeyDeviceParent     = windows.DEVPROPKEY{FmtID: windows.DEVPROPGUID{Data1: 0x4340a6c5, Data2: 0x93fa, Data3: 0x4706, Data4: [8]byte{0x97, 0x2c, 0x7b, 0x64, 0x80, 0x08, 0xa5, 0xa7}}, PID: 8}
	pipeTransferTimeout     = uintptr(0x03) // PIPE_TRANSFER_TIMEOUT, winusbio.h
	usbdPipeTypeBulk        = int32(2)      // UsbdPipeTypeBulk, usb.h
	cmGetInterfacePresent   = uint32(0)     // CM_GET_DEVICE_INTERFACE_LIST_PRESENT
	errorSemTimeout         = windows.Errno(121)
	errorDeviceNotConnected = windows.Errno(1167)
)

type winDev struct {
	instance, parent, service string
	compatible                []string
	location, name            string
	set                       windows.DevInfo
	data                      *windows.DevInfoData
}

// usbDevices lists the present devices of the USB enumerator; the caller
// destroys the set
func usbDevices() (windows.DevInfo, []winDev, error) {
	set, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_ALLCLASSES|windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return 0, nil, newErr("list", ErrUnsupported, err, "")
	}
	var out []winDev
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if err != nil {
			break
		}
		d := winDev{set: set, data: data}
		d.instance, _ = set.DeviceInstanceID(data)
		if v, err := windows.SetupDiGetDeviceProperty(set, data, &devpkeyDeviceParent); err == nil {
			d.parent, _ = v.(string)
		}
		d.service = regString(set, data, windows.SPDRP_SERVICE)
		d.location = regString(set, data, windows.SPDRP_LOCATION_INFORMATION)
		if d.name = regString(set, data, windows.SPDRP_FRIENDLYNAME); d.name == "" {
			d.name = regString(set, data, windows.SPDRP_DEVICEDESC)
		}
		if v, err := set.DeviceRegistryProperty(data, windows.SPDRP_COMPATIBLEIDS); err == nil {
			d.compatible, _ = v.([]string)
		}
		out = append(out, d)
	}
	return set, out, nil
}

func regString(set windows.DevInfo, data *windows.DevInfoData, p windows.SPDRP) string {
	v, err := set.DeviceRegistryProperty(data, p)
	if err != nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// parseInstance reads "USB\VID_303A&PID_1001\SERIAL" or
// "USB\VID_303A&PID_1001&MI_02\..."; mi is -1 without MI_
func parseInstance(id string) (vid, pid uint16, mi int, serial string, ok bool) {
	parts := strings.Split(id, `\`)
	if len(parts) != 3 || !strings.EqualFold(parts[0], "USB") {
		return 0, 0, 0, "", false
	}
	mi = -1
	var v, p uint32
	got := 0
	for _, f := range strings.Split(strings.ToUpper(parts[1]), "&") {
		switch {
		case strings.HasPrefix(f, "VID_"):
			if _, err := fmt.Sscanf(f[4:], "%x", &v); err == nil {
				got++
			}
		case strings.HasPrefix(f, "PID_"):
			if _, err := fmt.Sscanf(f[4:], "%x", &p); err == nil {
				got++
			}
		case strings.HasPrefix(f, "MI_"):
			fmt.Sscanf(f[3:], "%x", &mi)
		}
	}
	return uint16(v), uint16(p), mi, parts[2], got == 2
}

func list() ([]DeviceInfo, error) {
	set, devs, err := usbDevices()
	if err != nil {
		return nil, err
	}
	defer set.Close()
	var out []DeviceInfo
	for _, d := range devs {
		vid, pid, mi, serial, ok := parseInstance(d.instance)
		if !ok || mi >= 0 {
			continue
		}
		// A serial made up by Windows (no iSerialNumber) has '&' in it
		if strings.Contains(serial, "&") {
			serial = ""
		}
		out = append(out, DeviceInfo{VID: vid, PID: pid, Serial: serial, Location: d.location, Product: d.name, path: d.instance})
	}
	return out, nil
}

// classID is the compatible ID of an interface of that class triple
func classID(m Match) string {
	return fmt.Sprintf(`USB\Class_%02x&SubClass_%02x&Prot_%02x`, m.Class, m.SubClass, m.Protocol)
}

func open(d DeviceInfo, m Match) (*Device, error) {
	set, devs, err := usbDevices()
	if err != nil {
		return nil, err
	}
	defer set.Close()
	var iface *winDev
	want := classID(m)
	for i := range devs {
		c := &devs[i]
		if !strings.EqualFold(c.parent, d.path) && !strings.EqualFold(c.instance, d.path) {
			continue
		}
		if m.ByNumber {
			if _, _, mi, _, ok := parseInstance(c.instance); !ok || mi != int(m.Number) {
				continue
			}
		}
		for _, id := range c.compatible {
			if strings.EqualFold(id, want) {
				iface = c
			}
		}
		if iface != nil {
			break
		}
	}
	if iface == nil {
		return nil, newErr("open "+d.path, ErrNotFound, fmt.Errorf("no interface %s", want), "")
	}
	if !strings.EqualFold(iface.service, "WinUSB") {
		drv := iface.service
		if drv == "" {
			drv = "none"
		}
		return nil, newErr("open "+iface.instance, ErrNoDriver, fmt.Errorf("its driver is %s", drv), WinUSBHint)
	}
	path, err := interfacePath(iface)
	if err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	fh, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, newErr("open "+path, ErrBusy, err,
				"Another program (OpenOCD, another AltEsp window) has the JTAG interface open; close it")
		}
		return nil, newErr("open "+path, ErrDisconnected, err, "")
	}
	if err := procInitialize.Find(); err != nil {
		windows.CloseHandle(fh)
		return nil, newErr("open", ErrNoDriver, err, WinUSBHint)
	}
	var wh uintptr
	if r, _, e := procInitialize.Call(uintptr(fh), uintptr(unsafe.Pointer(&wh))); r == 0 {
		windows.CloseHandle(fh)
		return nil, newErr("WinUsb_Initialize", ErrNoDriver, e, WinUSBHint)
	}
	h := &winHandle{file: fh, wh: wh, timeouts: map[uint8]uint32{}}
	ep, err := h.endpoints()
	if err != nil {
		h.close()
		return nil, err
	}
	return &Device{Info: d, EP: ep, h: h}, nil
}

// interfacePath finds the device interface path of the WinUSB interface
func interfacePath(d *winDev) (string, error) {
	key, err := windows.SetupDiOpenDevRegKey(d.set, d.data, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DEV, windows.KEY_READ)
	if err != nil {
		return "", newErr("open "+d.instance, ErrNoDriver, err, WinUSBHint)
	}
	k := registry.Key(key)
	defer k.Close()
	var guids []string
	if v, _, err := k.GetStringsValue("DeviceInterfaceGUIDs"); err == nil {
		guids = v
	}
	if v, _, err := k.GetStringValue("DeviceInterfaceGUID"); err == nil {
		guids = append(guids, v)
	}
	for _, g := range guids {
		guid, err := windows.GUIDFromString(g)
		if err != nil {
			continue
		}
		paths, err := windows.CM_Get_Device_Interface_List(d.instance, &guid, cmGetInterfacePresent)
		if err == nil && len(paths) > 0 && paths[0] != "" {
			return paths[0], nil
		}
	}
	return "", newErr("open "+d.instance, ErrNoDriver, errors.New("no WinUSB device interface"), WinUSBHint)
}

type winHandle struct {
	file windows.Handle
	wh   uintptr

	mu       sync.Mutex
	timeouts map[uint8]uint32
}

// USB_INTERFACE_DESCRIPTOR, packed
type ifaceDesc struct {
	Length, Type, Number, Alt, NumEP, Class, SubClass, Protocol, IIface uint8
}

// WINUSB_PIPE_INFORMATION
type pipeInfo struct {
	PipeType      int32
	PipeID        uint8
	MaxPacketSize uint16
	Interval      uint8
}

func (h *winHandle) endpoints() (Endpoints, error) {
	var id ifaceDesc
	if r, _, e := procQueryInterface.Call(h.wh, 0, uintptr(unsafe.Pointer(&id))); r == 0 {
		return Endpoints{}, newErr("WinUsb_QueryInterfaceSettings", ErrNotFound, e, "")
	}
	ep := Endpoints{Interface: id.Number, AltSetting: id.Alt}
	for i := 0; i < int(id.NumEP); i++ {
		var pi pipeInfo
		if r, _, _ := procQueryPipe.Call(h.wh, 0, uintptr(i), uintptr(unsafe.Pointer(&pi))); r == 0 {
			continue
		}
		if pi.PipeType != usbdPipeTypeBulk {
			continue
		}
		if pi.PipeID&0x80 != 0 && ep.In == 0 {
			ep.In, ep.InPacket = pi.PipeID, int(pi.MaxPacketSize)
		} else if pi.PipeID&0x80 == 0 && ep.Out == 0 {
			ep.Out, ep.OutPacket = pi.PipeID, int(pi.MaxPacketSize)
		}
	}
	if ep.In == 0 || ep.Out == 0 {
		return ep, newErr("open", ErrNotFound, errors.New("no bulk endpoints"), "")
	}
	return ep, nil
}

func (h *winHandle) setTimeout(pipe uint8, t time.Duration) {
	v := ms(t)
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.timeouts[pipe]; ok && old == v {
		return
	}
	procSetPipePolicy.Call(h.wh, uintptr(pipe), pipeTransferTimeout, 4, uintptr(unsafe.Pointer(&v)))
	h.timeouts[pipe] = v
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

func mapErr(op string, e error) error {
	switch {
	case errors.Is(e, errorSemTimeout):
		return newErr(op, ErrTimeout, e, "")
	case errors.Is(e, windows.ERROR_GEN_FAILURE):
		return newErr(op, ErrStall, e, "")
	case errors.Is(e, errorDeviceNotConnected), errors.Is(e, windows.ERROR_BAD_COMMAND),
		errors.Is(e, windows.ERROR_FILE_NOT_FOUND), errors.Is(e, windows.ERROR_INVALID_HANDLE):
		return newErr(op, ErrDisconnected, e, "")
	}
	return newErr(op, ErrDisconnected, e, "")
}

func (h *winHandle) pipe(proc *windows.LazyProc, op string, ep uint8, buf []byte, timeout time.Duration) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	h.setTimeout(ep, timeout)
	var n uint32
	r, _, e := proc.Call(h.wh, uintptr(ep), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)), 0)
	if r == 0 {
		err := mapErr(op, e)
		if errors.Is(err, ErrStall) {
			procResetPipe.Call(h.wh, uintptr(ep))
		}
		return int(n), err
	}
	return int(n), nil
}

func (h *winHandle) bulkOut(ep uint8, data []byte, timeout time.Duration) (int, error) {
	return h.pipe(procWritePipe, "bulk out", ep, data, timeout)
}

func (h *winHandle) bulkIn(ep uint8, buf []byte, timeout time.Duration) (int, error) {
	return h.pipe(procReadPipe, "bulk in", ep, buf, timeout)
}

// control passes WINUSB_SETUP_PACKET (8 bytes) by value: one register on
// 64-bit Windows, two stack words on 32-bit x86
func (h *winHandle) control(reqType, req uint8, value, index uint16, data []byte, timeout time.Duration) (int, error) {
	h.setTimeout(0, timeout)
	setup := uint64(reqType) | uint64(req)<<8 | uint64(value)<<16 | uint64(index)<<32 | uint64(len(data))<<48
	var buf uintptr
	if len(data) > 0 {
		buf = uintptr(unsafe.Pointer(&data[0]))
	}
	var n uint32
	var r uintptr
	var e error
	if unsafe.Sizeof(uintptr(0)) == 8 {
		r, _, e = procControlTransfer.Call(h.wh, uintptr(setup), buf, uintptr(len(data)), uintptr(unsafe.Pointer(&n)), 0)
	} else {
		r, _, e = procControlTransfer.Call(h.wh, uintptr(uint32(setup)), uintptr(uint32(setup>>32)), buf, uintptr(len(data)), uintptr(unsafe.Pointer(&n)), 0)
	}
	if r == 0 {
		return int(n), mapErr("control", e)
	}
	return int(n), nil
}

func (h *winHandle) close() error {
	if h.wh != 0 {
		procFree.Call(h.wh)
		h.wh = 0
	}
	return windows.CloseHandle(h.file)
}
