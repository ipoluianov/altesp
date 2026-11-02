package serial

import (
	"context"
	"time"
)

// A board with built-in USB disappears on every reset and comes back as a
// new device: on Windows maybe with another COM number, an ESP32-S2 in
// download mode with another product ID. Find and Wait tell it among the
// ports by what doesn't change.

// Find returns the port of the device prev among ports: the same device
// key (IDs and serial number), else the same place in USB, else the same
// port name of the same vendor
func Find(prev PortInfo, ports []PortInfo) (PortInfo, bool) {
	key := prev.Key()
	for _, p := range ports {
		if p.Key() == key {
			return p, true
		}
	}
	if prev.USB && prev.Location != "" {
		for _, p := range ports {
			if p.USB && p.Location == prev.Location {
				return p, true
			}
		}
	}
	for _, p := range ports {
		if p.Name == prev.Name && p.VID == prev.VID {
			return p, true
		}
	}
	return PortInfo{}, false
}

// listPorts is List; tests replace it
var listPorts = List

// pollInterval is how often Wait looks at the ports
const pollInterval = 100 * time.Millisecond

// Wait waits for the device prev to come back after a reset and returns
// its port. When gone is set, the device must first disappear: right
// after the reset it may still be listed for a moment.
func Wait(ctx context.Context, prev PortInfo, gone bool) (PortInfo, error) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		ports, err := listPorts()
		if err == nil {
			p, ok := Find(prev, ports)
			switch {
			case gone && !ok:
				gone = false
			case !gone && ok:
				return p, nil
			}
		}
		select {
		case <-ctx.Done():
			return PortInfo{}, ctx.Err()
		case <-t.C:
		}
	}
}
