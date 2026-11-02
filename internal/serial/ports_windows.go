package serial

import (
	"regexp"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// guidDevClassPorts is GUID_DEVCLASS_PORTS, the class "Ports (COM & LPT)"
var guidDevClassPorts = windows.GUID{
	Data1: 0x4d36e978, Data2: 0xe325, Data3: 0x11ce,
	Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18},
}

var (
	reVID = regexp.MustCompile(`(?i)VID_([0-9a-f]{4})`)
	rePID = regexp.MustCompile(`(?i)PID_([0-9a-f]{4})`)
	// FTDIBUS\VID_0403+PID_6001+A50285BIA\0000: the serial number and the
	// letter of the port of the chip
	reFTDISerial = regexp.MustCompile(`(?i)\+PID_[0-9a-f]{4}\+([^\\]+)`)
	reCOMSuffix  = regexp.MustCompile(`\s*\((COM\d+)\)\s*$`)
	// The interface of a composite device ends its location path with
	// "#USBMI(0)"; the device itself is the place it is plugged in
	reUSBMI = regexp.MustCompile(`(?i)#USBMI\(\d+\)$`)
)

// List returns the COM ports of the devices present, with what Windows
// knows of them, and the ports of the other drivers (virtual pairs) from
// HARDWARE\DEVICEMAP\SERIALCOMM
func List() ([]PortInfo, error) {
	ports := listSetupAPI()
	seen := map[string]bool{}
	for _, p := range ports {
		seen[strings.ToUpper(p.Name)] = true
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DEVICEMAP\SERIALCOMM`, registry.QUERY_VALUE); err == nil {
		names, _ := k.ReadValueNames(0)
		for _, n := range names {
			v, _, err := k.GetStringValue(n)
			if err != nil || seen[strings.ToUpper(v)] {
				continue
			}
			seen[strings.ToUpper(v)] = true
			desc := n
			if i := strings.LastIndex(desc, `\`); i >= 0 {
				desc = desc[i+1:]
			}
			ports = append(ports, PortInfo{Name: v, Short: v, Description: desc})
		}
		k.Close()
	}
	sortPorts(ports)
	return ports, nil
}

func listSetupAPI() []PortInfo {
	set, err := windows.SetupDiGetClassDevsEx(&guidDevClassPorts, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil
	}
	defer windows.SetupDiDestroyDeviceInfoList(set)
	var ports []PortInfo
	for i := 0; ; i++ {
		data, err := windows.SetupDiEnumDeviceInfo(set, i)
		if err != nil {
			break
		}
		name := portName(set, data)
		if !strings.HasPrefix(strings.ToUpper(name), "COM") {
			continue // a printer port
		}
		p := PortInfo{Name: name, Short: name}
		if v, err := set.DeviceRegistryProperty(data, windows.SPDRP_FRIENDLYNAME); err == nil {
			if s, ok := v.(string); ok {
				p.Description = reCOMSuffix.ReplaceAllString(s, "")
			}
		}
		if v, err := set.DeviceRegistryProperty(data, windows.SPDRP_MFG); err == nil {
			if s, ok := v.(string); ok && !strings.HasPrefix(s, "(") {
				p.Manufacturer = s
			}
		}
		if v, err := set.DeviceRegistryProperty(data, windows.SPDRP_SERVICE); err == nil {
			if s, ok := v.(string); ok {
				p.Driver = s
			}
		}
		var ids []string
		if v, err := set.DeviceRegistryProperty(data, windows.SPDRP_HARDWAREID); err == nil {
			if list, ok := v.([]string); ok {
				ids = list
			}
		}
		instance, _ := set.DeviceInstanceID(data)
		fillUSBWindows(&p, append(ids, instance), instance)
		if v, err := set.DeviceRegistryProperty(data, windows.SPDRP_LOCATION_PATHS); err == nil && p.USB {
			if list, ok := v.([]string); ok && len(list) > 0 {
				p.Location = reUSBMI.ReplaceAllString(list[0], "")
			}
		}
		ports = append(ports, p)
	}
	return ports
}

func portName(set windows.DevInfo, data *windows.DevInfoData) string {
	h, err := windows.SetupDiOpenDevRegKey(set, data, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DEV, windows.KEY_READ)
	if err != nil {
		return ""
	}
	k := registry.Key(h)
	defer k.Close()
	name, _, _ := k.GetStringValue("PortName")
	return name
}

// fillUSBWindows reads the IDs and the serial number of a USB device from
// its hardware IDs and its instance ID ("USB\VID_1A86&PID_7523\5&2E2C&0&3")
func fillUSBWindows(p *PortInfo, ids []string, instance string) {
	for _, id := range ids {
		v := reVID.FindStringSubmatch(id)
		d := rePID.FindStringSubmatch(id)
		if v == nil || d == nil {
			continue
		}
		p.VID, _ = parseHex4(v[1])
		p.PID, _ = parseHex4(d[1])
		p.USB = true
		break
	}
	if !p.USB {
		return
	}
	if m := reFTDISerial.FindStringSubmatch(instance); m != nil {
		s := m[1]
		// The letter of the port is added to the serial number
		if len(s) > 1 && s[len(s)-1] >= 'A' && s[len(s)-1] <= 'D' {
			s = s[:len(s)-1]
		}
		p.SerialNumber = s
		return
	}
	parts := strings.Split(instance, `\`)
	// Windows makes up an instance ID with '&' for a device without a serial
	if len(parts) == 3 && !strings.Contains(parts[2], "&") {
		p.SerialNumber = parts[2]
	}
}
