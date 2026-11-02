package serial

import (
	"path/filepath"
	"strings"
)

// List returns the callout devices (/dev/cu.*): opening them doesn't wait
// for the carrier, as the tty.* ones do. The USB IDs, strings and location
// come from the IORegistry (iokit_darwin.go); when it can't be read, the
// kind of a port is told by its name.
func List() ([]PortInfo, error) {
	paths, err := filepath.Glob("/dev/cu.*")
	if err != nil {
		return nil, err
	}
	usb := ioRegistryPorts()
	var ports []PortInfo
	for _, path := range paths {
		short := strings.TrimPrefix(path, "/dev/")
		p := PortInfo{Name: path, Short: short}
		if u, ok := usb[path]; ok {
			p.USB, p.VID, p.PID = true, u.VID, u.PID
			p.SerialNumber, p.Manufacturer, p.Product = u.SerialNumber, u.Manufacturer, u.Product
			p.Location, p.Description = u.Location, u.Product
		}
		if p.Description == "" {
			var isUSB bool
			p.Description, isUSB = describeDarwin(short)
			p.USB = p.USB || isUSB
		}
		ports = append(ports, p)
	}
	sortPorts(ports)
	return ports, nil
}

func describeDarwin(short string) (string, bool) {
	n := strings.ToLower(short)
	switch {
	case strings.Contains(n, "wchusbserial"):
		return "CH340", true
	case strings.Contains(n, "slab_usbtouart"):
		return "CP210x", true
	case strings.Contains(n, "usbserial"):
		return "USB serial", true
	case strings.Contains(n, "usbmodem"):
		return "USB CDC", true
	case strings.Contains(n, "bluetooth"):
		return "Bluetooth", false
	}
	return "", false
}
