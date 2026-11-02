package serial

import (
	"os"
	"path/filepath"
	"strings"
)

const sysTTY = "/sys/class/tty"

// List returns the serial ports of the computer from sysfs: USB adapters
// and boards (ttyUSB, ttyACM), the UARTs of the board (ttyS, ttyAMA...),
// Bluetooth (rfcomm). The consoles and the pseudo terminals are left out.
func List() ([]PortInfo, error) {
	entries, err := os.ReadDir(sysTTY)
	if err != nil {
		return nil, err
	}
	var ports []PortInfo
	for _, e := range entries {
		name := e.Name()
		dir := filepath.Join(sysTTY, name)
		dev, err := filepath.EvalSymlinks(filepath.Join(dir, "device"))
		if err != nil {
			continue // a virtual terminal
		}
		// A UART of the serial core with the type 0 is a placeholder the
		// kernel makes for ttyS0..ttyS31 without hardware behind them
		if t, ok := readSys(filepath.Join(dir, "type")); ok && t == "0" {
			continue
		}
		p := PortInfo{Name: "/dev/" + name, Short: name}
		if _, err := os.Stat(p.Name); err != nil {
			continue
		}
		if drv, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			p.Driver = filepath.Base(drv)
		}
		fillUSB(&p, dev)
		if p.Description == "" {
			p.Description = describeLinux(name, p.Driver)
		}
		ports = append(ports, p)
	}
	sortPorts(ports)
	return ports, nil
}

// fillUSB finds the USB device above the tty in sysfs and reads its IDs
// and strings
func fillUSB(p *PortInfo, dev string) {
	for d := dev; d != "/" && d != "." && strings.HasPrefix(d, "/sys/devices"); d = filepath.Dir(d) {
		vid, ok := readSys(filepath.Join(d, "idVendor"))
		if !ok {
			continue
		}
		pid, _ := readSys(filepath.Join(d, "idProduct"))
		p.VID, _ = parseHex4(vid)
		p.PID, _ = parseHex4(pid)
		p.USB = true
		p.Location = filepath.Base(d)
		p.Manufacturer, _ = readSys(filepath.Join(d, "manufacturer"))
		p.Product, _ = readSys(filepath.Join(d, "product"))
		p.SerialNumber, _ = readSys(filepath.Join(d, "serial"))
		// The interface of a composite device has a name of its own
		if iface, ok := readSys(filepath.Join(dev, "interface")); ok {
			p.Description = iface
		} else {
			p.Description = p.Product
		}
		return
	}
}

func describeLinux(name, driver string) string {
	switch {
	case strings.HasPrefix(name, "rfcomm"):
		return "Bluetooth"
	case strings.HasPrefix(name, "ttyAMA"), strings.HasPrefix(name, "ttyS"):
		return "UART"
	}
	return driver
}

func readSys(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}
