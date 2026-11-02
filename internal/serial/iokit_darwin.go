package serial

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// The IORegistry is read through IOKit and CoreFoundation, loaded with
// purego (no cgo). Each serial port is an IOSerialBSDClient service; the
// USB device it belongs to is one of its parents, so the USB properties
// are searched up the IOService plane.

var iokit struct {
	once sync.Once
	err  error

	serviceMatching        func(name string) uintptr
	serviceGetMatching     func(mainPort uint32, matching uintptr, iter *uint32) int32
	iteratorNext           func(iter uint32) uint32
	objectRelease          func(obj uint32) int32
	entryCreateCFProperty  func(entry uint32, key, alloc uintptr, options uint32) uintptr
	entrySearchCFProperty  func(entry uint32, plane string, key, alloc uintptr, options uint32) uintptr
	cfStringCreateWithCStr func(alloc uintptr, s string, encoding uint32) uintptr
	cfStringGetCString     func(s uintptr, buf *byte, size int64, encoding uint32) bool
	cfNumberGetValue       func(n uintptr, kind int64, value *int64) bool
	cfGetTypeID            func(cf uintptr) uint64
	cfStringGetTypeID      func() uint64
	cfNumberGetTypeID      func() uint64
	cfRelease              func(cf uintptr)
}

const (
	cfStringEncodingUTF8     = 0x08000100
	cfNumberSInt64Type       = 4
	ioRegistryIterateRecurse = 1
	ioRegistryIterateParents = 2
	ioServicePlane           = "IOService"
	ioSerialBSDServiceValue  = "IOSerialBSDClient"
	ioCalloutDeviceKey       = "IOCalloutDevice"
	ioKitFramework           = "/System/Library/Frameworks/IOKit.framework/IOKit"
	coreFoundationFramework  = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
)

func loadIOKit() error {
	iokit.once.Do(func() {
		cf, err := purego.Dlopen(coreFoundationFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			iokit.err = err
			return
		}
		io, err := purego.Dlopen(ioKitFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			iokit.err = err
			return
		}
		purego.RegisterLibFunc(&iokit.serviceMatching, io, "IOServiceMatching")
		purego.RegisterLibFunc(&iokit.serviceGetMatching, io, "IOServiceGetMatchingServices")
		purego.RegisterLibFunc(&iokit.iteratorNext, io, "IOIteratorNext")
		purego.RegisterLibFunc(&iokit.objectRelease, io, "IOObjectRelease")
		purego.RegisterLibFunc(&iokit.entryCreateCFProperty, io, "IORegistryEntryCreateCFProperty")
		purego.RegisterLibFunc(&iokit.entrySearchCFProperty, io, "IORegistryEntrySearchCFProperty")
		purego.RegisterLibFunc(&iokit.cfStringCreateWithCStr, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&iokit.cfStringGetCString, cf, "CFStringGetCString")
		purego.RegisterLibFunc(&iokit.cfNumberGetValue, cf, "CFNumberGetValue")
		purego.RegisterLibFunc(&iokit.cfGetTypeID, cf, "CFGetTypeID")
		purego.RegisterLibFunc(&iokit.cfStringGetTypeID, cf, "CFStringGetTypeID")
		purego.RegisterLibFunc(&iokit.cfNumberGetTypeID, cf, "CFNumberGetTypeID")
		purego.RegisterLibFunc(&iokit.cfRelease, cf, "CFRelease")
	})
	return iokit.err
}

// usbInfo is what the IORegistry tells about the USB device of a port
type usbInfo struct {
	VID, PID                            uint16
	SerialNumber, Manufacturer, Product string
	Location                            string
}

// ioRegistryPorts maps the callout device of each USB serial port to its
// USB device. It is empty when IOKit can't be loaded.
func ioRegistryPorts() map[string]usbInfo {
	ports := map[string]usbInfo{}
	if loadIOKit() != nil {
		return ports
	}
	matching := iokit.serviceMatching(ioSerialBSDServiceValue)
	if matching == 0 {
		return ports
	}
	var iter uint32
	// The matching dictionary is consumed by the call
	if iokit.serviceGetMatching(0, matching, &iter) != 0 {
		return ports
	}
	defer iokit.objectRelease(iter)
	for {
		service := iokit.iteratorNext(iter)
		if service == 0 {
			break
		}
		path := stringProperty(service, ioCalloutDeviceKey, false)
		vid, okV := numberProperty(service, "idVendor")
		pid, okP := numberProperty(service, "idProduct")
		if path != "" && okV && okP {
			u := usbInfo{
				VID: uint16(vid), PID: uint16(pid),
				SerialNumber: stringProperty(service, "USB Serial Number", true),
				Manufacturer: stringProperty(service, "USB Vendor Name", true),
				Product:      stringProperty(service, "USB Product Name", true),
			}
			if loc, ok := numberProperty(service, "locationID"); ok {
				u.Location = fmt.Sprintf("0x%08x", uint32(loc))
			}
			ports[path] = u
		}
		iokit.objectRelease(service)
	}
	return ports
}

// property reads a property of the entry, or of its nearest parent that
// has it when search is set; the result is released by the caller
func property(entry uint32, key string, search bool) uintptr {
	k := iokit.cfStringCreateWithCStr(0, key, cfStringEncodingUTF8)
	if k == 0 {
		return 0
	}
	defer iokit.cfRelease(k)
	if search {
		return iokit.entrySearchCFProperty(entry, ioServicePlane, k, 0,
			ioRegistryIterateRecurse|ioRegistryIterateParents)
	}
	return iokit.entryCreateCFProperty(entry, k, 0, 0)
}

func stringProperty(entry uint32, key string, search bool) string {
	v := property(entry, key, search)
	if v == 0 {
		return ""
	}
	defer iokit.cfRelease(v)
	if iokit.cfGetTypeID(v) != iokit.cfStringGetTypeID() {
		return ""
	}
	var buf [512]byte
	if !iokit.cfStringGetCString(v, &buf[0], int64(len(buf)), cfStringEncodingUTF8) {
		return ""
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n])
}

// numberProperty searches the parents always: the USB numbers are never
// on the serial service itself
func numberProperty(entry uint32, key string) (int64, bool) {
	v := property(entry, key, true)
	if v == 0 {
		return 0, false
	}
	defer iokit.cfRelease(v)
	if iokit.cfGetTypeID(v) != iokit.cfNumberGetTypeID() {
		return 0, false
	}
	var n int64
	if !iokit.cfNumberGetValue(v, cfNumberSInt64Type, &n) {
		return 0, false
	}
	return n, true
}
