//go:build darwin

package usb

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// macOS: IOKit and CoreFoundation are loaded with purego (no cgo). The
// devices are the IOUSBHostDevice services of the IORegistry; an
// interface is an IOUSBHostInterface service whose parent is the device.
// Transfers go through the user-space plug-in of the interface: the
// IOUSBInterfaceInterface COM object (IOUSBLib.h), created with
// IOCreatePlugInInterfaceForService and QueryInterface. A COM object is a
// pointer to a pointer to its table of function pointers; purego calls
// those pointers with SyscallN. The original IOUSBInterfaceInterface
// (kIOUSBInterfaceInterfaceID) is asked for: its table is the prefix of
// every later version, so the indexes below are stable. ReadPipe has no
// timeout in that version: a timer aborts the pipe instead.
//
// The newer IOUSBHost Objective-C classes (IOUSBHostInterface,
// IOUSBHostPipe) would need the Objective-C runtime and blocks; the
// plug-in API is plain C and enough here.

var iok struct {
	once sync.Once
	err  error

	serviceMatching       func(name string) uintptr
	serviceGetMatching    func(mainPort uint32, matching uintptr, iter *uint32) int32
	iteratorNext          func(iter uint32) uint32
	objectRelease         func(obj uint32) int32
	entryCreateCFProperty func(entry uint32, key, alloc uintptr, options uint32) uintptr
	entryGetParentEntry   func(entry uint32, plane string, parent *uint32) int32
	createPlugIn          func(service uint32, pluginType, interfaceType uintptr, plugin *uintptr, score *int32) int32
	cfStringCreate        func(alloc uintptr, s string, encoding uint32) uintptr
	cfStringGetCString    func(s uintptr, buf *byte, size int64, encoding uint32) bool
	cfNumberGetValue      func(n uintptr, kind int64, value *int64) bool
	cfGetTypeID           func(cf uintptr) uint64
	cfStringGetTypeID     func() uint64
	cfNumberGetTypeID     func() uint64
	cfRelease             func(cf uintptr)
	cfUUIDCreateFromStr   func(alloc, s uintptr) uintptr
}

const (
	cfStringEncodingUTF8 = 0x08000100
	cfNumberSInt64Type   = 4
	ioKitFramework       = "/System/Library/Frameworks/IOKit.framework/IOKit"
	cfFramework          = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"

	// The UUIDs of IOUSBLib.h and IOCFPlugIn.h
	uuidInterfaceUserClient = "2D9786C6-9EF3-11D4-AD51-000A27052861" // kIOUSBInterfaceUserClientTypeID
	uuidCFPlugInInterface   = "C244E858-109C-11D4-91D4-0050E4C6426F" // kIOCFPlugInInterfaceID
	uuidUSBInterface        = "73C97AE8-9EF3-11D4-B1D0-000A27052861" // kIOUSBInterfaceInterfaceID

	// IOReturn values (IOKit/IOReturn.h, IOKit/usb/USB.h)
	kIOReturnSuccess         = 0
	kIOReturnNoDevice        = 0xe00002c0
	kIOReturnExclusiveAccess = 0xe00002c5
	kIOReturnNotOpen         = 0xe00002cd
	kIOReturnTimeout         = 0xe00002d6
	kIOReturnAborted         = 0xe00002eb
	kIOReturnNotResponding   = 0xe00002ed
	kIOUSBPipeStalled        = 0xe000404f
	kIOUSBTransactionTimeout = 0xe0004051
)

// The function indexes of IOUSBInterfaceInterface (IOUSBLib.h,
// IOUSBInterfaceStruct100: IUNKNOWN_C_GUTS takes 0..3)
const (
	vtQueryInterface    = 1
	vtRelease           = 3
	vtUSBInterfaceOpen  = 8
	vtUSBInterfaceClose = 9
	vtGetInterfaceNum   = 17
	vtGetNumEndpoints   = 19
	vtControlRequest    = 24
	vtGetPipeProperties = 26
	vtAbortPipe         = 28
	vtClearPipeStall    = 30
	vtReadPipe          = 31
	vtWritePipe         = 32
)

func loadIOKit() error {
	iok.once.Do(func() {
		cf, err := purego.Dlopen(cfFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			iok.err = err
			return
		}
		io, err := purego.Dlopen(ioKitFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			iok.err = err
			return
		}
		purego.RegisterLibFunc(&iok.serviceMatching, io, "IOServiceMatching")
		purego.RegisterLibFunc(&iok.serviceGetMatching, io, "IOServiceGetMatchingServices")
		purego.RegisterLibFunc(&iok.iteratorNext, io, "IOIteratorNext")
		purego.RegisterLibFunc(&iok.objectRelease, io, "IOObjectRelease")
		purego.RegisterLibFunc(&iok.entryCreateCFProperty, io, "IORegistryEntryCreateCFProperty")
		purego.RegisterLibFunc(&iok.entryGetParentEntry, io, "IORegistryEntryGetParentEntry")
		purego.RegisterLibFunc(&iok.createPlugIn, io, "IOCreatePlugInInterfaceForService")
		purego.RegisterLibFunc(&iok.cfStringCreate, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&iok.cfStringGetCString, cf, "CFStringGetCString")
		purego.RegisterLibFunc(&iok.cfNumberGetValue, cf, "CFNumberGetValue")
		purego.RegisterLibFunc(&iok.cfGetTypeID, cf, "CFGetTypeID")
		purego.RegisterLibFunc(&iok.cfStringGetTypeID, cf, "CFStringGetTypeID")
		purego.RegisterLibFunc(&iok.cfNumberGetTypeID, cf, "CFNumberGetTypeID")
		purego.RegisterLibFunc(&iok.cfRelease, cf, "CFRelease")
		purego.RegisterLibFunc(&iok.cfUUIDCreateFromStr, cf, "CFUUIDCreateFromString")
	})
	return iok.err
}

func cfProperty(entry uint32, key string) uintptr {
	k := iok.cfStringCreate(0, key, cfStringEncodingUTF8)
	if k == 0 {
		return 0
	}
	defer iok.cfRelease(k)
	return iok.entryCreateCFProperty(entry, k, 0, 0)
}

func strProp(entry uint32, key string) string {
	v := cfProperty(entry, key)
	if v == 0 {
		return ""
	}
	defer iok.cfRelease(v)
	if iok.cfGetTypeID(v) != iok.cfStringGetTypeID() {
		return ""
	}
	var buf [512]byte
	if !iok.cfStringGetCString(v, &buf[0], int64(len(buf)), cfStringEncodingUTF8) {
		return ""
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n])
}

func numProp(entry uint32, key string) (int64, bool) {
	v := cfProperty(entry, key)
	if v == 0 {
		return 0, false
	}
	defer iok.cfRelease(v)
	if iok.cfGetTypeID(v) != iok.cfNumberGetTypeID() {
		return 0, false
	}
	var n int64
	return n, iok.cfNumberGetValue(v, cfNumberSInt64Type, &n)
}

// services calls f for each service of the class; f keeps the service
// by returning true (it releases it then)
func services(class string, f func(s uint32) bool) error {
	if err := loadIOKit(); err != nil {
		return newErr("IOKit", ErrUnsupported, err, "")
	}
	matching := iok.serviceMatching(class)
	if matching == 0 {
		return newErr("IOServiceMatching", ErrUnsupported, nil, "")
	}
	var iter uint32
	if r := iok.serviceGetMatching(0, matching, &iter); r != 0 {
		return newErr("IOServiceGetMatchingServices", ErrUnsupported, fmt.Errorf("IOReturn 0x%08x", uint32(r)), "")
	}
	defer iok.objectRelease(iter)
	for {
		s := iok.iteratorNext(iter)
		if s == 0 {
			return nil
		}
		if !f(s) {
			iok.objectRelease(s)
		}
	}
}

func deviceInfo(s uint32) (DeviceInfo, bool) {
	vid, ok1 := numProp(s, "idVendor")
	pid, ok2 := numProp(s, "idProduct")
	if !ok1 || !ok2 {
		return DeviceInfo{}, false
	}
	d := DeviceInfo{
		VID: uint16(vid), PID: uint16(pid),
		Serial:       strProp(s, "USB Serial Number"),
		Manufacturer: strProp(s, "USB Vendor Name"),
		Product:      strProp(s, "USB Product Name"),
	}
	if loc, ok := numProp(s, "locationID"); ok {
		d.Location = fmt.Sprintf("0x%08x", uint32(loc))
	}
	d.path = d.Location
	return d, true
}

func list() ([]DeviceInfo, error) {
	var out []DeviceInfo
	err := services("IOUSBHostDevice", func(s uint32) bool {
		if d, ok := deviceInfo(s); ok {
			out = append(out, d)
		}
		return false
	})
	return out, err
}

// deref reads the pointer-sized word at address p (memory owned by
// IOKit); the double conversion keeps go vet's unsafeptr check quiet
func deref(p uintptr) uintptr { return **(**uintptr)(unsafe.Pointer(&p)) }

// call calls function idx of the COM object obj. The pointers among the
// arguments are converted in the calls to it: uintptrescapes keeps their
// objects on the heap and alive during the call.
//
//go:uintptrescapes
func call(obj uintptr, idx int, args ...uintptr) uint32 {
	fn := deref(deref(obj) + uintptr(idx)*unsafe.Sizeof(uintptr(0)))
	r, _, _ := purego.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return uint32(r)
}

// uuidRegs is a UUID passed by value (CFUUIDBytes, 16 bytes): in two
// integer registers on arm64 and amd64
func uuidRegs(s string) (uintptr, uintptr) {
	var b [16]byte
	h := strings.ReplaceAll(s, "-", "")
	for i := range b {
		fmt.Sscanf(h[2*i:2*i+2], "%02x", &b[i])
	}
	var lo, hi uint64
	for i := 7; i >= 0; i-- {
		lo = lo<<8 | uint64(b[i])
		hi = hi<<8 | uint64(b[8+i])
	}
	return uintptr(lo), uintptr(hi)
}

func cfUUID(s string) uintptr {
	str := iok.cfStringCreate(0, s, cfStringEncodingUTF8)
	if str == 0 {
		return 0
	}
	defer iok.cfRelease(str)
	return iok.cfUUIDCreateFromStr(0, str)
}

func open(d DeviceInfo, m Match) (*Device, error) {
	var found uint32
	err := services("IOUSBHostInterface", func(s uint32) bool {
		if found != 0 {
			return false
		}
		c, _ := numProp(s, "bInterfaceClass")
		sc, _ := numProp(s, "bInterfaceSubClass")
		p, _ := numProp(s, "bInterfaceProtocol")
		num, _ := numProp(s, "bInterfaceNumber")
		if !m.matches(uint8(num), uint8(c), uint8(sc), uint8(p)) {
			return false
		}
		var parent uint32
		if iok.entryGetParentEntry(s, "IOService", &parent) != 0 {
			return false
		}
		pd, ok := deviceInfo(parent)
		iok.objectRelease(parent)
		if !ok || pd.VID != d.VID || pd.PID != d.PID || (d.path != "" && pd.path != d.path) {
			return false
		}
		found = s
		return true
	})
	if err != nil {
		return nil, err
	}
	if found == 0 {
		return nil, newErr("open", ErrNotFound, fmt.Errorf("no interface %02x/%02x/%02x", m.Class, m.SubClass, m.Protocol), "")
	}
	defer iok.objectRelease(found)

	userClient, plugType := cfUUID(uuidInterfaceUserClient), cfUUID(uuidCFPlugInInterface)
	if userClient == 0 || plugType == 0 {
		return nil, newErr("open", ErrUnsupported, nil, "")
	}
	defer iok.cfRelease(userClient)
	defer iok.cfRelease(plugType)
	var plugin uintptr
	var score int32
	if r := iok.createPlugIn(found, userClient, plugType, &plugin, &score); r != 0 || plugin == 0 {
		return nil, newErr("IOCreatePlugInInterfaceForService", ErrBusy, fmt.Errorf("IOReturn 0x%08x", uint32(r)), "")
	}
	lo, hi := uuidRegs(uuidUSBInterface)
	var intf uintptr
	r := call(plugin, vtQueryInterface, lo, hi, uintptr(unsafe.Pointer(&intf)))
	call(plugin, vtRelease)
	if r != 0 || intf == 0 {
		return nil, newErr("QueryInterface", ErrUnsupported, fmt.Errorf("HRESULT 0x%08x", r), "")
	}
	if r := call(intf, vtUSBInterfaceOpen); r != kIOReturnSuccess {
		call(intf, vtRelease)
		if r == kIOReturnExclusiveAccess {
			return nil, newErr("USBInterfaceOpen", ErrBusy, fmt.Errorf("IOReturn 0x%08x", r),
				"Another program (OpenOCD, another AltEsp window) has the JTAG interface open; close it")
		}
		return nil, newErr("USBInterfaceOpen", ErrBusy, fmt.Errorf("IOReturn 0x%08x", r), "")
	}
	h := &macHandle{intf: intf, pipes: map[uint8]uint8{}}
	ep, err := h.endpoints()
	if err != nil {
		h.close()
		return nil, err
	}
	return &Device{Info: d, EP: ep, h: h}, nil
}

type macHandle struct {
	intf  uintptr
	pipes map[uint8]uint8 // endpoint address -> pipeRef
}

func (h *macHandle) endpoints() (Endpoints, error) {
	var num, n uint8
	call(h.intf, vtGetInterfaceNum, uintptr(unsafe.Pointer(&num)))
	if r := call(h.intf, vtGetNumEndpoints, uintptr(unsafe.Pointer(&n))); r != 0 {
		return Endpoints{}, newErr("GetNumEndpoints", ErrNotFound, fmt.Errorf("IOReturn 0x%08x", r), "")
	}
	ep := Endpoints{Interface: num}
	for ref := uint8(1); ref <= n; ref++ {
		var dir, number, typ, interval uint8
		var size uint16
		if call(h.intf, vtGetPipeProperties, uintptr(ref), uintptr(unsafe.Pointer(&dir)), uintptr(unsafe.Pointer(&number)),
			uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&interval))) != 0 {
			continue
		}
		if typ != xferBulk {
			continue
		}
		// direction: kUSBOut 0, kUSBIn 1 (USB.h)
		if dir == 1 && ep.In == 0 {
			ep.In, ep.InPacket = 0x80|number, int(size)
			h.pipes[ep.In] = ref
		} else if dir == 0 && ep.Out == 0 {
			ep.Out, ep.OutPacket = number, int(size)
			h.pipes[ep.Out] = ref
		}
	}
	if ep.In == 0 || ep.Out == 0 {
		return ep, newErr("open", ErrNotFound, fmt.Errorf("no bulk endpoints"), "")
	}
	return ep, nil
}

func (h *macHandle) mapErr(op string, ref uint8, r uint32) error {
	e := fmt.Errorf("IOReturn 0x%08x", r)
	switch r {
	case kIOReturnAborted, kIOReturnTimeout, kIOUSBTransactionTimeout:
		return newErr(op, ErrTimeout, e, "")
	case kIOUSBPipeStalled:
		call(h.intf, vtClearPipeStall, uintptr(ref))
		return newErr(op, ErrStall, e, "")
	}
	return newErr(op, ErrDisconnected, e, "")
}

// withTimeout aborts the pipe when f takes longer than t
func (h *macHandle) withTimeout(ref uint8, t time.Duration, f func() uint32) uint32 {
	if t <= 0 {
		return f()
	}
	timer := time.AfterFunc(t, func() { call(h.intf, vtAbortPipe, uintptr(ref)) })
	defer timer.Stop()
	return f()
}

func (h *macHandle) bulkOut(ep uint8, data []byte, timeout time.Duration) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	ref := h.pipes[ep]
	r := h.withTimeout(ref, timeout, func() uint32 {
		return call(h.intf, vtWritePipe, uintptr(ref), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	})
	if r != 0 {
		return 0, h.mapErr("bulk out", ref, r)
	}
	return len(data), nil
}

func (h *macHandle) bulkIn(ep uint8, buf []byte, timeout time.Duration) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	ref := h.pipes[ep]
	size := uint32(len(buf))
	r := h.withTimeout(ref, timeout, func() uint32 {
		return call(h.intf, vtReadPipe, uintptr(ref), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	})
	if r != 0 {
		return 0, h.mapErr("bulk in", ref, r)
	}
	return int(size), nil
}

// IOUSBDevRequest (IOUSBLib.h / USB.h)
type devRequest struct {
	reqType, req         uint8
	value, index, length uint16
	data                 unsafe.Pointer
	lenDone              uint32
}

func (h *macHandle) control(reqType, req uint8, value, index uint16, data []byte, timeout time.Duration) (int, error) {
	dr := devRequest{reqType: reqType, req: req, value: value, index: index, length: uint16(len(data))}
	if len(data) > 0 {
		dr.data = unsafe.Pointer(&data[0])
	}
	r := h.withTimeout(0, timeout, func() uint32 {
		return call(h.intf, vtControlRequest, 0, uintptr(unsafe.Pointer(&dr)))
	})
	if r != 0 {
		return 0, h.mapErr("control", 0, r)
	}
	return int(dr.lenDone), nil
}

func (h *macHandle) close() error {
	if h.intf != 0 {
		call(h.intf, vtUSBInterfaceClose)
		call(h.intf, vtRelease)
		h.intf = 0
	}
	return nil
}

var _ = kIOReturnNoDevice + kIOReturnNotOpen + kIOReturnNotResponding
