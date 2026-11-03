//go:build linux

package usb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListSysfs(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, attrs map[string]string) {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0o755)
		for k, v := range attrs {
			os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644)
		}
	}
	mk("1-2", map[string]string{"idVendor": "303a", "idProduct": "1001", "busnum": "1", "devnum": "7", "serial": "84:F7:03:12:34:56", "product": "USB JTAG/serial debug unit"})
	mk("1-2:1.2", map[string]string{"bInterfaceNumber": "02"})
	mk("usb1", map[string]string{"idVendor": "1d6b", "idProduct": "0002", "busnum": "1", "devnum": "1"})
	all, err := listSysfs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d devices: %+v", len(all), all)
	}
	var d DeviceInfo
	for _, x := range all {
		if x.VID == 0x303A {
			d = x
		}
	}
	if d.PID != 0x1001 || d.Serial != "84:F7:03:12:34:56" || d.Location != "1-2" || d.path != "/dev/bus/usb/001/007" {
		t.Fatalf("%+v", d)
	}
}
