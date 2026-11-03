package ops

import (
	"context"

	"github.com/ipoluianov/altesp/internal/loader"
	"github.com/ipoluianov/altesp/internal/serial"
)

// Monitor is the port of a running board: its log and its input
type Monitor struct {
	serial.Port
	Info  serial.PortInfo
	reset loader.Reset
}

// DefaultMonitorBaud is the rate of the ESP-IDF console
const DefaultMonitorBaud = 115200

// OpenMonitor opens the port of the board without resetting it: the log of
// the running application goes on. DTR and RTS are released so that the
// board isn't held in reset. RTS goes first: Linux raises both lines on
// open, which the auto-reset circuit of a bridge ignores, while RTS alone
// pulls EN low and would restart the application.
func OpenMonitor(info serial.PortInfo, baud int) (*Monitor, error) {
	cfg := serial.DefaultConfig()
	if baud > 0 {
		cfg.Baud = baud
	}
	p, err := openPortConfig(info, cfg)
	if err != nil {
		return nil, portError(info, err)
	}
	p.SetRTS(false)
	p.SetDTR(false)
	return &Monitor{Port: p, Info: info, reset: loader.ResetFor(info)}, nil
}

// openPortConfig opens a port with the settings; tests replace it
var openPortConfig = func(info serial.PortInfo, cfg serial.Config) (serial.Port, error) {
	return serial.Open(info.Name, cfg)
}

// Reset restarts the application. With built-in USB the port goes away
// and comes back: the caller reopens it (serial.Wait).
func (m *Monitor) Reset(ctx context.Context) error {
	return loader.ResetToApp(ctx, m.Port, m.reset)
}

// Bootloader resets the board into download mode and leaves it there
func (m *Monitor) Bootloader(ctx context.Context) error {
	return loader.ResetToDownload(ctx, m.Port, m.reset)
}
