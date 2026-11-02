package ops

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// permissionHint tells how to get access to the serial ports, where the
// system needs it: on Linux the user must be in the group of the devices
func permissionHint() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	group := "dialout"
	if _, err := os.Stat("/etc/arch-release"); err == nil {
		group = "uucp"
	}
	return "\nAdd yourself to the " + group + " group and log in again:\n  sudo usermod -aG " + group + " $USER"
}

// LinuxObstacles lists the programs known to grab the ports of ESP boards:
// ModemManager probes ttyACM ports (the chip's own USB) with AT commands,
// brltty takes CH340 adapters for braille displays. Empty elsewhere.
func LinuxObstacles() []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	var found []string
	for _, p := range []struct{ name, fix string }{
		{"ModemManager", "ModemManager may grab the board's port: sudo systemctl disable --now ModemManager, or install the udev rules of the .deb/.rpm package"},
		{"brltty", "brltty may grab CH340 adapters: sudo apt remove brltty (if no braille display is used)"},
	} {
		if running(p.name) {
			found = append(found, p.fix)
		}
	}
	return found
}

// running tells whether a process of that name runs, by /proc/*/comm
func running(name string) bool {
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, c := range comms {
		b, err := os.ReadFile(c)
		if err == nil && strings.TrimSpace(string(b)) == name {
			return true
		}
	}
	return false
}
