package usb

import (
	"errors"
	"strings"
	"testing"
)

// espConfig is a configuration descriptor shaped like the one of the
// USB-Serial-JTAG controller: a CDC-ACM function (interfaces 0 and 1)
// and the vendor JTAG interface 2 with bulk OUT 0x02 and bulk IN 0x83
var espConfig = []byte{
	9, 2, 98, 0, 3, 1, 0, 0xC0, 250, // configuration
	8, 11, 0, 2, 2, 2, 0, 0, // interface association
	9, 4, 0, 0, 1, 2, 2, 0, 0, // CDC control
	5, 0x24, 0, 0x10, 1,
	5, 0x24, 1, 3, 1,
	4, 0x24, 2, 6,
	5, 0x24, 6, 0, 1,
	7, 5, 0x82, 3, 64, 0, 1, // interrupt IN
	9, 4, 1, 0, 2, 0x0A, 0, 0, 0, // CDC data
	7, 5, 0x01, 2, 64, 0, 0,
	7, 5, 0x81, 2, 64, 0, 0,
	9, 4, 2, 0, 2, 0xFF, 0xFF, 0x01, 0, // JTAG
	7, 5, 0x02, 2, 64, 0, 0,
	7, 5, 0x83, 2, 64, 0, 0,
}

func TestFindEndpoints(t *testing.T) {
	ep, err := FindEndpoints(espConfig, Match{Class: 0xFF, SubClass: 0xFF, Protocol: 0x01})
	if err != nil {
		t.Fatal(err)
	}
	want := Endpoints{Interface: 2, In: 0x83, Out: 0x02, InPacket: 64, OutPacket: 64}
	if ep != want {
		t.Fatalf("got %+v, want %+v", ep, want)
	}
	ep, err = FindEndpoints(espConfig, Match{Class: 0x0A})
	if err != nil || ep.Interface != 1 || ep.In != 0x81 || ep.Out != 0x01 {
		t.Fatalf("CDC data: %+v %v", ep, err)
	}
	if _, err := FindEndpoints(espConfig, Match{Class: 0xFF, SubClass: 0x42, Protocol: 0x01}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing interface: %v", err)
	}
	if _, err := FindEndpoints([]byte{9, 2, 0}, Match{}); err == nil {
		t.Fatal("truncated descriptor accepted")
	}
}

// ft2232Config is shaped like the configuration of an FT2232H: two
// vendor interfaces of the same class, A (0) with bulk 0x81/0x02 and B
// (1) with 0x83/0x04, 512-byte packets (FT2232H datasheet, "USB
// descriptors"; unverified: the subclass and protocol 0xFF)
var ft2232Config = []byte{
	9, 2, 55, 0, 2, 1, 0, 0x80, 50,
	9, 4, 0, 0, 2, 0xFF, 0xFF, 0xFF, 2,
	7, 5, 0x81, 2, 0, 2, 0,
	7, 5, 0x02, 2, 0, 2, 0,
	9, 4, 1, 0, 2, 0xFF, 0xFF, 0xFF, 2,
	7, 5, 0x83, 2, 0, 2, 0,
	7, 5, 0x04, 2, 0, 2, 0,
}

func TestFindEndpointsByNumber(t *testing.T) {
	m := Match{Class: 0xFF, SubClass: 0xFF, Protocol: 0xFF, ByNumber: true, Number: 1}
	ep, err := FindEndpoints(ft2232Config, m)
	if err != nil || ep.Interface != 1 || ep.In != 0x83 || ep.Out != 0x04 || ep.InPacket != 512 {
		t.Fatalf("interface B: %+v %v", ep, err)
	}
	m.Number = 0
	if ep, err = FindEndpoints(ft2232Config, m); err != nil || ep.Interface != 0 || ep.In != 0x81 {
		t.Fatalf("interface A: %+v %v", ep, err)
	}
	m.Number = 2
	if _, err := FindEndpoints(ft2232Config, m); !errors.Is(err, ErrNotFound) {
		t.Fatalf("interface 2: %v", err)
	}
}

func TestErrorText(t *testing.T) {
	err := newErr("open /dev/bus/usb/001/004", ErrPermission, errors.New("permission denied"), UdevHint)
	if !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), "60-altesp-usbjtag.rules") {
		t.Fatalf("%v", err)
	}
}
