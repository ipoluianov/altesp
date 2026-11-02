package loader

import (
	"context"
	"time"

	"github.com/ipoluianov/altesp/internal/serial"
)

// Reset is the way the board is reset by the control lines of the port
type Reset int

const (
	// ResetAuto picks the way by the adapter and tries the others
	ResetAuto Reset = iota
	// ResetBridge is the auto-reset circuit of the boards with a USB-UART
	// bridge: DTR drives GPIO0 and RTS drives EN through two transistors,
	// so that DTR and RTS asserted together don't reset the chip
	ResetBridge
	// ResetUSBJTAG is the USB-Serial-JTAG controller of the chip: its
	// CDC-ACM port reads RTS and DTR as a reset and a download flag
	ResetUSBJTAG
	// ResetManual doesn't touch the lines: the user holds BOOT and
	// presses RESET
	ResetManual
)

func (r Reset) String() string {
	switch r {
	case ResetBridge:
		return "bridge"
	case ResetUSBJTAG:
		return "usb-jtag"
	case ResetManual:
		return "manual"
	}
	return "auto"
}

// ResetFor is the reset of the adapter of a port
func ResetFor(p serial.PortInfo) Reset {
	if p.USB && p.VID == 0x303a && p.PID == 0x1001 {
		return ResetUSBJTAG
	}
	return ResetBridge
}

// lines sets DTR and RTS. True asserts a line, that is drives it low.
type lines interface {
	SetDTR(on bool) error
	SetRTS(on bool) error
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// step sets the lines and waits
type step struct {
	dtr, rts bool
	wait     time.Duration
}

func run(ctx context.Context, l lines, steps []step) error {
	for _, s := range steps {
		// DTR first, then RTS: some drivers send the state of both lines
		// only when RTS changes
		if err := l.SetDTR(s.dtr); err != nil {
			return err
		}
		if err := l.SetRTS(s.rts); err != nil {
			return err
		}
		if err := sleep(ctx, s.wait); err != nil {
			return err
		}
	}
	return nil
}

// bridgeDownload holds EN low, then releases it with GPIO0 low, then
// releases GPIO0. hold is how long EN is held: boards with a large EN
// capacitor need longer.
//
// The circuit (ESP32-DevKitC schematic): RTS asserted alone pulls EN low,
// DTR asserted alone pulls GPIO0 low, both or none leave both high.
func bridgeDownload(hold time.Duration) []step {
	return []step{
		{dtr: false, rts: true, wait: hold},                  // EN low: reset
		{dtr: true, rts: false, wait: 50 * time.Millisecond}, // EN high, GPIO0 low: download boot
		{dtr: false, rts: false, wait: 0},                    // GPIO0 released
	}
}

// bridgeRun resets the chip with GPIO0 high: it boots the application
var bridgeRun = []step{
	{dtr: false, rts: true, wait: 100 * time.Millisecond},
	{dtr: false, rts: false, wait: 0},
}

// The USB-Serial-JTAG sequences are tables 30.4-1 and 30.4-2 of the
// ESP32-C3 TRM, line by line: most systems set DTR and RTS one at a time,
// and the Windows CDC-ACM driver sends DTR only when RTS is set.

type lineStep struct {
	dtr  bool // false: the line is RTS
	on   bool
	wait time.Duration
}

var usbJTAGDownload = []lineStep{
	{dtr: true, on: false},                               // clear DTR
	{dtr: false, on: false},                              // clear RTS
	{dtr: true, on: true},                                // set DTR: download flag
	{dtr: false, on: false},                              // clear RTS: propagate DTR
	{dtr: false, on: true},                               // set RTS
	{dtr: true, on: false},                               // clear DTR: reset
	{dtr: false, on: true, wait: 100 * time.Millisecond}, // set RTS: propagate DTR
	{dtr: false, on: false},                              // clear RTS: clear download flag
}

var usbJTAGRun = []lineStep{
	{dtr: true, on: false},                               // clear DTR
	{dtr: false, on: false},                              // clear RTS: clear download flag
	{dtr: false, on: true, wait: 100 * time.Millisecond}, // set RTS: reset
	{dtr: false, on: false},                              // clear RTS: exit reset
}

func runLines(ctx context.Context, l lines, steps []lineStep) error {
	for _, s := range steps {
		var err error
		if s.dtr {
			err = l.SetDTR(s.on)
		} else {
			err = l.SetRTS(s.on)
		}
		if err != nil {
			return err
		}
		if err := sleep(ctx, s.wait); err != nil {
			return err
		}
	}
	return nil
}

// enterDownload resets the chip into download mode the way r says;
// attempt picks the variant of the bridge sequence
func enterDownload(ctx context.Context, l lines, r Reset, attempt int) error {
	switch r {
	case ResetUSBJTAG:
		return runLines(ctx, l, usbJTAGDownload)
	case ResetBridge:
		hold := 100 * time.Millisecond
		if attempt%2 == 1 {
			hold = 500 * time.Millisecond
		}
		return run(ctx, l, bridgeDownload(hold))
	}
	return nil
}

// runApp resets the chip into the application
func runApp(ctx context.Context, l lines, r Reset) error {
	switch r {
	case ResetUSBJTAG:
		return runLines(ctx, l, usbJTAGRun)
	case ResetBridge:
		return run(ctx, l, bridgeRun)
	}
	return nil
}
