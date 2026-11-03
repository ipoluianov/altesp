package jtagsim

import (
	"errors"
	"fmt"
)

// FTDI is a simulated FT2232H channel in front of the same TAP as the
// USB-JTAG controller: the vendor requests and the MPSSE commands of
// FTDI AN_108 used for JTAG, written from the FTDI documents (see package
// ftdi for the list), not from the host code. Bulk IN data comes in
// packets that start with two status bytes (AN232B-04).
type FTDI struct {
	d      *Device
	packet int
	// mode is the bit mode: 0 UART (after reset), 2 MPSSE
	mode    byte
	latency uint16
	in      []byte // commands not complete yet
	out     []byte // MPSSE answers not read yet
	low     [2]byte
	divisor uint16
	div5    bool
	// Errors records misuse the host must not do
	Errors []string
	// Requests counts the vendor requests by number
	Requests map[uint8]int
}

// FTDI gives the device an FTDI MPSSE front; packet is the bulk packet
// size (512 at high speed)
func (d *Device) FTDI(packet int) *FTDI {
	return &FTDI{d: d, packet: packet, div5: true, Requests: map[uint8]int{}}
}

// Status bytes of a bulk IN packet: modem status (CTS, DSR) and line
// status, as an idle channel reports them (AN232B-04)
const ftdiStatus0, ftdiStatus1 = 0x32, 0x60

// Control is a vendor request (FreeBSD uftdi_reg.h: RESET 0, SET_LATENCY
// 9, SET_BITMODE 11, wIndex the port, 1 = channel A)
func (f *FTDI) Control(req uint8, value, index uint16) error {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if f.d.gone {
		return ErrGone
	}
	f.Requests[req]++
	if index != 1 {
		f.Errors = append(f.Errors, fmt.Sprintf("request %d for port %d, not channel A", req, index))
	}
	switch req {
	case 0:
		switch value {
		case 0:
			f.in, f.out = nil, nil
		case 1:
			f.out = nil
		case 2:
			f.in = nil
		}
	case 9:
		f.latency = value
	case 11:
		f.mode = byte(value >> 8)
		if f.mode == 0 {
			f.in, f.out = nil, nil
		}
	default:
		return fmt.Errorf("jtagsim: vendor request %d not modeled", req)
	}
	return nil
}

// Write takes MPSSE commands from the bulk OUT endpoint
func (f *FTDI) Write(p []byte) error {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if f.d.gone {
		return ErrGone
	}
	if f.mode != 2 {
		f.Errors = append(f.Errors, "data written while not in MPSSE mode")
		return nil
	}
	f.in = append(f.in, p...)
	for len(f.in) > 0 && !f.d.gone {
		n := f.command(f.in)
		if n == 0 {
			break
		}
		f.in = f.in[n:]
	}
	return nil
}

// Read gives whole packets, each with the two status bytes; a packet with
// no data when there is nothing to send (the latency timer)
func (f *FTDI) Read(p []byte) (int, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if f.d.gone {
		return 0, ErrGone
	}
	if len(p) < f.packet {
		return 0, errors.New("jtagsim: bulk IN read smaller than a packet")
	}
	n := 0
	for {
		if n+2 > len(p) {
			break
		}
		k := min(len(f.out), f.packet-2, len(p)-n-2)
		p[n], p[n+1] = ftdiStatus0, ftdiStatus1
		copy(p[n+2:], f.out[:k])
		f.out = f.out[k:]
		n += 2 + k
		if len(f.out) == 0 || n+f.packet > len(p) {
			break
		}
	}
	return n, nil
}

// jtagReady tells whether the pins are set for JTAG: TCK, TDI, TMS
// outputs (AN_129)
func (f *FTDI) jtagReady() bool { return f.low[1]&0x0B == 0x0B && f.low[1]&0x04 == 0 }

// clock is one TCK cycle: TDO sampled on the rising edge, before the TAP
// moves
func (f *FTDI) clock(tms, tdi bool) bool {
	if !f.jtagReady() {
		f.Errors = append(f.Errors, "TCK clocked before the JTAG pins were set as outputs")
	}
	tdo := f.d.tdo()
	f.d.clock(tms, tdi)
	return tdo
}

// command runs the command at the start of b and returns its length, 0
// when b doesn't hold all of it yet
func (f *FTDI) command(b []byte) int {
	op := b[0]
	need := func(n int) bool { return len(b) >= n }
	switch op {
	case 0x19, 0x39: // bytes out (and in), LSB first
		if !need(3) {
			return 0
		}
		nb := int(b[1]) | int(b[2])<<8 + 1
		if !need(3 + nb) {
			return 0
		}
		for i := 0; i < nb; i++ {
			var in byte
			for j := 0; j < 8; j++ {
				if f.clock(false, b[3+i]>>uint(j)&1 != 0) {
					in |= 1 << uint(j)
				}
			}
			if op == 0x39 {
				f.out = append(f.out, in)
			}
		}
		return 3 + nb
	case 0x1B, 0x3B: // bits out (and in), LSB first
		if !need(3) {
			return 0
		}
		var in byte
		for j := 0; j <= int(b[1]&7); j++ {
			in >>= 1
			if f.clock(false, b[2]>>uint(j)&1 != 0) {
				in |= 0x80
			}
		}
		if op == 0x3B {
			f.out = append(f.out, in)
		}
		return 3
	case 0x4B, 0x6B: // TMS bits, bit 7 held on TDI
		if !need(3) {
			return 0
		}
		n := int(b[1]&7) + 1
		if n > 7 {
			f.Errors = append(f.Errors, "TMS command longer than 7 bits")
			n = 7
		}
		var in byte
		for j := 0; j < n; j++ {
			in >>= 1
			if f.clock(b[2]>>uint(j)&1 != 0, b[2]&0x80 != 0) {
				in |= 0x80
			}
		}
		if op == 0x6B {
			f.out = append(f.out, in)
		}
		return 3
	case 0x80:
		if !need(3) {
			return 0
		}
		f.low = [2]byte{b[1], b[2]}
		return 3
	case 0x82:
		if !need(3) {
			return 0
		}
		return 3
	case 0x81:
		f.out = append(f.out, f.low[0])
		return 1
	case 0x86:
		if !need(3) {
			return 0
		}
		f.divisor = uint16(b[1]) | uint16(b[2])<<8
		return 3
	case 0x8A:
		f.div5 = false
		return 1
	case 0x8B:
		f.div5 = true
		return 1
	case 0x84, 0x85, 0x87, 0x8C, 0x8D, 0x96, 0x97:
		return 1
	}
	// an invalid command: 0xFA and the command (AN_108 3.1)
	f.out = append(f.out, 0xFA, op)
	return 1
}

// TCKHz is the TCK frequency the host set
func (f *FTDI) TCKHz() int {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	base := 60000000
	if f.div5 {
		base = 12000000
	}
	return base / (2 * (1 + int(f.divisor)))
}
