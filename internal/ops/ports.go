package ops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ipoluianov/altesp/internal/serial"
)

// listPorts is serial.List; tests replace it
var listPorts = serial.List

// Ports lists the serial ports, the likely boards first
func Ports() ([]serial.PortInfo, error) {
	ports, err := listPorts()
	if err != nil {
		return nil, err
	}
	var boards, others []serial.PortInfo
	for _, p := range ports {
		if LikelyBoard(p) {
			boards = append(boards, p)
		} else {
			others = append(others, p)
		}
	}
	return append(boards, others...), nil
}

// LikelyBoard tells a port an ESP board may be on: a USB-UART bridge or
// the chip's own USB, not a Bluetooth port or a UART of the computer
func LikelyBoard(p serial.PortInfo) bool {
	if !p.USB {
		return false
	}
	switch p.VID {
	case 0x303a, 0x10c4, 0x1a86, 0x0403, 0x067b:
		return true
	}
	return false
}

// ErrNoBoard is no port that looks like a board
var ErrNoBoard = errors.New("no board found: connect one or give the port")

// PickPort finds the port by its name ("COM5", "/dev/ttyUSB0",
// "ttyUSB0"); with no name, the only likely board
func PickPort(name string) (serial.PortInfo, error) {
	ports, err := Ports()
	if err != nil {
		return serial.PortInfo{}, err
	}
	if name != "" {
		for _, p := range ports {
			if strings.EqualFold(p.Name, name) || strings.EqualFold(p.Short, name) {
				return p, nil
			}
		}
		// A port the list doesn't show may still open (a symlink, a
		// virtual port)
		return serial.PortInfo{Name: name, Short: name}, nil
	}
	var boards []serial.PortInfo
	for _, p := range ports {
		if LikelyBoard(p) {
			boards = append(boards, p)
		}
	}
	switch len(boards) {
	case 0:
		return serial.PortInfo{}, ErrNoBoard
	case 1:
		return boards[0], nil
	}
	names := make([]string, len(boards))
	for i, b := range boards {
		names[i] = b.Short
	}
	return serial.PortInfo{}, fmt.Errorf("several boards are connected (%s): give the port", strings.Join(names, ", "))
}
