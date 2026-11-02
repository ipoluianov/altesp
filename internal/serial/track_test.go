package serial

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestFind(t *testing.T) {
	jtag := PortInfo{Name: "COM5", USB: true, VID: 0x303a, PID: 0x1001, SerialNumber: "7C:DF:A1:00:12:34", Location: "PCIROOT(0)#USB(2)"}
	ch340 := PortInfo{Name: "/dev/ttyUSB0", USB: true, VID: 0x1a86, PID: 0x7523, Location: "1-1.4"}
	s2 := PortInfo{Name: "/dev/ttyACM0", USB: true, VID: 0x303a, PID: 0x0080, Location: "1-2"}
	uart := PortInfo{Name: "/dev/ttyS0"}

	tests := []struct {
		name  string
		prev  PortInfo
		ports []PortInfo
		want  string
		found bool
	}{
		{"new COM number, same serial", jtag,
			[]PortInfo{{Name: "COM3"}, {Name: "COM7", USB: true, VID: 0x303a, PID: 0x1001, SerialNumber: "7C:DF:A1:00:12:34"}}, "COM7", true},
		{"no serial, same place", ch340,
			[]PortInfo{{Name: "/dev/ttyUSB1", USB: true, VID: 0x1a86, PID: 0x7523, Location: "1-1.3"},
				{Name: "/dev/ttyUSB2", USB: true, VID: 0x1a86, PID: 0x7523, Location: "1-1.4"}}, "/dev/ttyUSB2", true},
		{"download mode has another PID", s2,
			[]PortInfo{{Name: "/dev/ttyACM1", USB: true, VID: 0x303a, PID: 0x0002, Location: "1-2"}}, "/dev/ttyACM1", true},
		{"plain UART by name", uart, []PortInfo{{Name: "/dev/ttyS1"}, {Name: "/dev/ttyS0"}}, "/dev/ttyS0", true},
		{"gone", jtag, []PortInfo{{Name: "COM3"}}, "", false},
		{"other board in the same place of another kind is still found", ch340,
			[]PortInfo{{Name: "/dev/ttyACM0", USB: true, VID: 0x303a, PID: 0x1001, Location: "1-1.4"}}, "/dev/ttyACM0", true},
	}
	for _, tt := range tests {
		got, ok := Find(tt.prev, tt.ports)
		if ok != tt.found || got.Name != tt.want {
			t.Errorf("%s: got %q %v, want %q %v", tt.name, got.Name, ok, tt.want, tt.found)
		}
	}
}

func TestWaitGoneAndBack(t *testing.T) {
	dev := PortInfo{Name: "COM5", USB: true, VID: 0x303a, PID: 0x1001, SerialNumber: "A"}
	back := dev
	back.Name = "COM9"
	// The device is listed, then gone, then back with another name
	steps := [][]PortInfo{{dev}, {dev}, nil, nil, {back}}
	var mu sync.Mutex
	i := 0
	old := listPorts
	listPorts = func() ([]PortInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		s := steps[min(i, len(steps)-1)]
		i++
		return s, nil
	}
	defer func() { listPorts = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := Wait(ctx, dev, true)
	if err != nil || p.Name != "COM9" {
		t.Fatalf("Wait = %q, %v; want COM9", p.Name, err)
	}
}

func TestWaitTimeout(t *testing.T) {
	old := listPorts
	listPorts = func() ([]PortInfo, error) { return nil, nil }
	defer func() { listPorts = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Wait(ctx, PortInfo{Name: "COM5"}, false); err == nil {
		t.Fatal("Wait returned without the device")
	}
}
