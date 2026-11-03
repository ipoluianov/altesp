// Package ftdi drives a JTAG TAP through an FTDI FT2232H in MPSSE mode:
// the adapter of ESP-Prog (and of the boards that carry the same
// circuit), whose channel A is the JTAG port and channel B a UART.
//
// Sources and their licenses:
//
//   - FTDI AN_108 "Command Processor for MPSSE and MCU Host Bus Emulation
//     Modes" (the MPSSE opcodes, the length encodings, the bad-command
//     answer 0xFA used to synchronize), AN_135 "FTDI MPSSE Basics" (the
//     initialization order: reset, latency timer, bit mode MPSSE, then
//     the MPSSE setup commands), AN_129 (JTAG over MPSSE: TCK ADBUS0,
//     TDI ADBUS1, TDO ADBUS2, TMS ADBUS3), AN232B-04 (every bulk IN
//     packet starts with two status bytes; the latency timer), the
//     FT2232H datasheet (60 MHz MPSSE clock, 512-byte packets at high
//     speed, 4 KB buffers per channel). FTDI documents, published for
//     developers of software for FTDI chips.
//   - The wire format of the vendor requests (request numbers, wValue and
//     wIndex layout) is not in the FTDI application notes; it is taken
//     from FreeBSD sys/dev/usb/serial/uftdi_reg.h and uftdi.c (BSD-2-Clause):
//     RESET 0 (wValue 0 reset, 1 purge RX, 2 purge TX), SET_LATENCY 9
//     (wValue the latency in ms), SET_BITMODE 11 (wValue mode<<8 | pin
//     mask, mode 0x02 = MPSSE, 0 = reset to the UART mode), wIndex the
//     port: 1 for channel A, 2 for B.
//   - ESP-Prog documentation (Espressif, docs.espressif.com "ESP-Prog
//     Introduction"): VID 0x0403 PID 0x6010, channel A is JTAG, B is the
//     program UART.
//
// unverified (TZ.md "Hardware checks"): everything here was checked only
// against the simulator in package jtagsim, written from the same
// documents. In particular: the bit order of the LSB-first bit reads
// (the bits come in at bit 7 and move down, so k bits read end in the top
// k bits of the byte), and that the class triple of the FT2232H
// interfaces is 0xFF/0xFF/0xFF.
package ftdi

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// USB identity of the FT2232H (FT2232H datasheet, default EEPROM values;
// ESP-Prog keeps them)
const (
	VID      = 0x0403
	PID2232H = 0x6010
	// Class triple of its interfaces: vendor specific
	Class, SubClass, Protocol = 0xFF, 0xFF, 0xFF
	// JTAGInterface is channel A, interface 0 (ESP-Prog docs)
	JTAGInterface = 0
)

// Vendor requests (FreeBSD uftdi_reg.h, see the package comment)
const (
	ReqReset      = 0x00
	ReqSetLatency = 0x09
	ReqSetBitmode = 0x0B

	ResetSIO = 0
	PurgeRX  = 1
	PurgeTX  = 2

	ModeReset = 0x00
	ModeMPSSE = 0x02

	// LatencyMS is the latency timer: how long the chip holds a part
	// packet before it sends it. Run asks for the data by "send
	// immediate", so this only bounds the status-only packets.
	LatencyMS = 2
)

// Port is one interface of the FTDI device as the USB layer gives it
type Port interface {
	// Write is a bulk OUT transfer
	Write(p []byte) error
	// Read is a bulk IN transfer: whole packets, each starting with the
	// two status bytes (AN232B-04); len(p) is a multiple of the packet
	// size
	Read(p []byte) (int, error)
	// Control is a vendor request to the device, host to device, no
	// data stage (bmRequestType 0x40)
	Control(req uint8, value, index uint16) error
}

// ErrTimeout: the MPSSE sent no data in time
var ErrTimeout = errors.New("the FTDI adapter sent no data")

// Link is an FTDI interface in MPSSE mode: Write sends MPSSE commands,
// Read returns what the MPSSE answered, without the status bytes. It is
// an espjtag.Link (and so a jtag.Link).
type Link struct {
	port   Port
	packet int
	index  uint16
	rest   []byte
	buf    []byte
	// Timeout bounds a Read that gets only status packets
	Timeout time.Duration
}

// Open puts interface iface of the device into MPSSE mode: reset, purge,
// latency timer, bit mode (AN_135 section 4.2). packet is the bulk IN
// packet size: 512 at high speed.
func Open(p Port, iface uint8, packet int) (*Link, error) {
	if packet <= 2 {
		packet = 512
	}
	l := &Link{port: p, packet: packet, index: uint16(iface) + 1, Timeout: 2 * time.Second}
	l.buf = make([]byte, 8*packet)
	steps := []struct {
		what  string
		req   uint8
		value uint16
	}{
		{"reset", ReqReset, ResetSIO},
		{"purge RX", ReqReset, PurgeRX},
		{"purge TX", ReqReset, PurgeTX},
		{"latency timer", ReqSetLatency, LatencyMS},
		{"bit mode reset", ReqSetBitmode, ModeReset << 8},
		{"bit mode MPSSE", ReqSetBitmode, ModeMPSSE << 8},
	}
	for _, s := range steps {
		if err := p.Control(s.req, s.value, l.index); err != nil {
			return nil, fmt.Errorf("FTDI %s: %w", s.what, err)
		}
	}
	return l, nil
}

// Write sends MPSSE commands
func (l *Link) Write(b []byte) error { return l.port.Write(b) }

// Read returns MPSSE data as it comes, at least one byte, or ErrTimeout
func (l *Link) Read(p []byte) (int, error) {
	deadline := time.Now().Add(l.Timeout)
	for len(l.rest) == 0 {
		n, err := l.port.Read(l.buf)
		if err != nil {
			return 0, err
		}
		for i := 0; i < n; i += l.packet {
			end := min(i+l.packet, n)
			if end-i > 2 {
				l.rest = append(l.rest, l.buf[i+2:end]...)
			}
		}
		if len(l.rest) == 0 && time.Now().After(deadline) {
			return 0, ErrTimeout
		}
	}
	n := copy(p, l.rest)
	l.rest = l.rest[n:]
	if len(l.rest) == 0 {
		l.rest = nil
	}
	return n, nil
}

// Discard drops data received and not read
func (l *Link) Discard() { l.rest = nil }

// Close returns the interface to its UART mode and closes the port
func (l *Link) Close() error {
	l.port.Control(ReqSetBitmode, ModeReset<<8, l.index)
	if c, ok := l.port.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
