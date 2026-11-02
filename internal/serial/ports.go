package serial

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// PortInfo describes a port of the computer
type PortInfo struct {
	Name  string // the path to open: "/dev/ttyUSB0", "COM5"
	Short string // the name shown: "ttyUSB0", "COM5"

	// Description is what the system tells about the device: the product
	// name of a USB device, the friendly name on Windows
	Description string

	USB          bool
	VID, PID     uint16
	SerialNumber string
	Manufacturer string
	Product      string
	Driver       string // "ftdi_sio", "cp210x", "cdc_acm"... when known

	// Location is where the USB device is plugged in: the sysfs name on
	// Linux ("1-1.4"), the location path on Windows, the locationID on
	// macOS. It stays the same while the device is re-enumerated after a
	// reset, even when its port name or its product ID changes.
	Location string
}

// Key identifies the device across reconnections: a USB device by its IDs
// and serial number (its port name may change after a reset), one without
// a serial number (CH340) by its IDs and the place it is plugged in, any
// other by the port name
func (p PortInfo) Key() string {
	if p.USB && p.SerialNumber != "" {
		return fmt.Sprintf("usb:%04x:%04x:%s", p.VID, p.PID, p.SerialNumber)
	}
	if p.USB && p.Location != "" {
		return fmt.Sprintf("usbloc:%04x:%04x:%s", p.VID, p.PID, p.Location)
	}
	return "port:" + p.Name
}

// IDs returns "1a86:7523" for a USB device, "" when they aren't known
func (p PortInfo) IDs() string {
	if !p.USB || p.VID == 0 {
		return ""
	}
	return fmt.Sprintf("%04x:%04x", p.VID, p.PID)
}

// Chip names the USB-UART chip or the board family by the vendor ID
func (p PortInfo) Chip() string {
	if !p.USB || p.VID == 0 {
		return ""
	}
	if name, ok := knownProducts[uint32(p.VID)<<16|uint32(p.PID)]; ok {
		return name
	}
	return knownVendors[p.VID]
}

// Title is the short text of a port in a list: "ttyUSB0 · CH340"
func (p PortInfo) Title() string {
	what := p.Chip()
	if what == "" {
		what = p.Description
	}
	if what == "" || strings.EqualFold(what, p.Short) {
		return p.Short
	}
	return p.Short + " · " + what
}

// Details is a full description of the port, one fact per line
func (p PortInfo) Details() []string {
	var lines []string
	add := func(name, value string) {
		if value != "" {
			lines = append(lines, name+": "+value)
		}
	}
	add("Port", p.Name)
	add("Description", p.Description)
	add("Chip", p.Chip())
	add("USB ID", p.IDs())
	add("Manufacturer", p.Manufacturer)
	add("Product", p.Product)
	add("Serial number", p.SerialNumber)
	add("Driver", p.Driver)
	add("USB location", p.Location)
	return lines
}

var knownVendors = map[uint16]string{
	0x0403: "FTDI",
	0x1a86: "CH340",
	0x10c4: "CP210x",
	0x067b: "PL2303",
	0x2341: "Arduino",
	0x2a03: "Arduino",
	0x1b4f: "SparkFun",
	0x239a: "Adafruit",
	0x303a: "ESP32",
	0x0483: "STM32",
	0x2e8a: "Raspberry Pi Pico",
	0x1366: "J-Link",
	0x0d28: "DAPLink",
	0x16c0: "Teensy",
	0x1915: "Nordic",
	0x1fc9: "NXP",
	0x03eb: "Microchip",
	0x04d8: "Microchip",
	0x0451: "TI",
	0x1d50: "OpenMoko",
	0x2886: "Seeed",
	0x1209: "pid.codes",
	0x0e8d: "MediaTek",
	0x12d1: "Huawei",
	0x2c7c: "Quectel",
	0x1e0e: "SIMCom",
	0x05c6: "Qualcomm",
	0x1546: "u-blox",
}

var knownProducts = map[uint32]string{
	0x04036001: "FT232R",
	0x04036010: "FT2232",
	0x04036011: "FT4232",
	0x04036014: "FT232H",
	0x04036015: "FT231X",
	0x1a867523: "CH340",
	0x1a865523: "CH341",
	0x1a8655d4: "CH9102",
	0x1a8655d3: "CH343",
	0x10c4ea60: "CP2102",
	0x10c4ea70: "CP2105",
	0x10c4ea71: "CP2108",
	0x067b2303: "PL2303",
	0x067b23a3: "PL2303GC",
	0x303a1001: "ESP32 USB JTAG/serial",
	0x303a0002: "ESP32 USB OTG (download)",
	0x0483374b: "ST-LINK/V2-1",
	0x0483374e: "ST-LINK/V3",
	0x04835740: "STM32 Virtual COM",
	0x2e8a000a: "Raspberry Pi Pico",
	0x2e8a0005: "Raspberry Pi Pico (MicroPython)",
	0x2e8a000c: "Raspberry Pi Debug Probe",
}

// sortPorts orders the ports by kind (USB first) and by name, the numbers
// in names compared as numbers: COM2 before COM10
func sortPorts(ports []PortInfo) {
	sort.SliceStable(ports, func(i, j int) bool {
		a, b := ports[i], ports[j]
		if a.USB != b.USB {
			return a.USB
		}
		return naturalLess(a.Short, b.Short)
	})
}

func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := unicode.IsDigit(rune(a[0])), unicode.IsDigit(rune(b[0]))
		if da && db {
			na, ra := leadingNumber(a)
			nb, rb := leadingNumber(b)
			if na != nb {
				return na < nb
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func leadingNumber(s string) (int, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n, s[i:]
}

// parseHex4 reads a USB ID written in hex ("1a86")
func parseHex4(s string) (uint16, bool) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 16, 16)
	return uint16(v), err == nil
}
