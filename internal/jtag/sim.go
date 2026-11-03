package jtag

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"hash"
	"sync"
	"time"

	"github.com/ipoluianov/altesp/internal/jtag/ftdi"
	"github.com/ipoluianov/altesp/internal/jtag/jtagsim"
)

// SimBoard describes the board a Sim pretends to be: a chip of the table
// with its flash, behind a simulated USB-JTAG controller
type SimBoard struct {
	Chip string // "esp32c3"
	// Flash is the content of the flash, changed by the writes; 4 MB of
	// 0xFF when nil
	Flash []byte
	// FlashID is the JEDEC ID the SPI flash answers; by the size of Flash
	// when 0
	FlashID uint32
	MAC     [6]byte
	// IDCode overrides the IDCODE of the chip table
	IDCode uint32
	// JTAGDisabled: the TAP doesn't answer (eFuse DIS_USB_JTAG)
	JTAGDisabled bool
	// DMDisabled: the TAP answers, the debug module doesn't
	// (SOFT_DIS_JTAG)
	DMDisabled bool
	// SBA: the DM has system bus access; AbstractMem: abstract memory
	// access; neither: the program buffer is used
	SBA, AbstractMem bool
	ProgBufSize      int // 16 when 0
	ImpEbreak        bool
	// BusyCycles is how long a DMI access takes in TCK cycles
	BusyCycles int
	// HaltPolls is how many dmstatus reads a ROM function takes
	HaltPolls int
	// Reenumerate: a reset drops the USB device, as a system reset of the
	// USB-Serial-JTAG controller would
	Reenumerate bool
	// NoWatchdogs: the watchdogs are off from the start; otherwise they
	// run and reset the chip at the first ROM call unless disabled
	NoWatchdogs bool
	// Adapter is what the board is reached through: its built-in
	// USB-JTAG or an FT2232H (ESP-Prog) on its JTAG pins
	Adapter Adapter
	// OCDID is what the OCD_ID register of an Xtensa core reads
	OCDID uint32
}

// The registers the chip model needs that the flash routine itself does
// not know: BLK1 of the eFuse (the MAC: EFUSE_RD_MAC_SPI_SYS_0_REG /
// EFUSE_RD_MAC_SYS_0_REG, efuse_reg.h) and the SPI1 controller of the
// flash (spi_mem_reg.h), as in the table of package chips
var simRegs = map[string]struct{ blk1, spi uint32 }{
	"esp32c3": {0x60008844, 0x60002000},
	"esp32c6": {0x600B0844, 0x60003000},
	"esp32h2": {0x600B0844, 0x60003000},
}

// Sim is a simulated board on the other end of the JTAG link
type Sim struct {
	*jtagsim.Device
	board SimBoard
	t     *target

	mu       sync.Mutex
	ram      []byte
	regs     map[uint32]uint32
	flash    []byte
	attached bool
	chipSize uint32
	md5s     map[uint32]hash.Hash
	// BootMode is what the last reset booted: "download" or "app"
	BootMode string
	// Calls counts the ROM function calls by address
	Calls map[uint32]int
	// WatchdogResets counts the resets by a watchdog
	WatchdogResets int
	// FTDI is the FT2232H front when Adapter is AdapterFTDI
	FTDI *jtagsim.FTDI
}

// ftdiPacket is the bulk packet size of the FT2232H at high speed
const ftdiPacket = 512

// NewSim makes a simulated board; the chip runs its application
func NewSim(b SimBoard) (*Sim, error) {
	t := targetByKey(b.Chip)
	if t == nil {
		return nil, fmt.Errorf("no simulation of %q", b.Chip)
	}
	if b.Flash == nil {
		b.Flash = make([]byte, 4<<20)
		for i := range b.Flash {
			b.Flash[i] = 0xFF
		}
	}
	if b.FlashID == 0 {
		cap := 0
		for 1<<cap < len(b.Flash) {
			cap++
		}
		b.FlashID = 0xC84000 | uint32(cap) // GigaDevice, log2 of the size
	}
	if b.ProgBufSize == 0 {
		b.ProgBufSize = 16
	}
	// the chip model keeps its own copy of the table entry: the tests
	// change the host's
	tc := *t
	s := &Sim{
		board: b, t: &tc, flash: b.Flash, BootMode: "app",
		ram:   make([]byte, t.sramHigh-t.sramLow),
		regs:  map[uint32]uint32{},
		md5s:  map[uint32]hash.Hash{},
		Calls: map[uint32]int{},
	}
	id := t.idcode
	if b.IDCode != 0 {
		id = b.IDCode
	}
	cfg := jtagsim.Config{
		IDCode: id, Disabled: b.JTAGDisabled, NoDTM: b.DMDisabled,
		BusyCycles: b.BusyCycles, SBA: b.SBA, AbstractMem: b.AbstractMem,
		ProgBufSize: b.ProgBufSize, ImpEbreak: b.ImpEbreak, HaltPolls: b.HaltPolls,
		Reenumerate: b.Reenumerate, AppPC: 0x42000100,
	}
	if t.arch == archXtensa {
		cfg.Xtensa = &jtagsim.XtensaConfig{Cores: t.taps, OCDID: b.OCDID}
	} else {
		s.powerOn()
	}
	s.Device = jtagsim.New(cfg, s)
	if b.Adapter == AdapterFTDI {
		s.FTDI = s.Device.FTDI(ftdiPacket)
	}
	return s, nil
}

// powerOn sets the registers as the chip has them out of reset: the eFuse
// MAC, the watchdogs running
func (s *Sim) powerOn() {
	r := simRegs[s.t.key]
	m := s.board.MAC
	s.regs[r.blk1] = uint32(m[2])<<24 | uint32(m[3])<<16 | uint32(m[4])<<8 | uint32(m[5])
	s.regs[r.blk1+4] = uint32(m[0])<<8 | uint32(m[1])
	if !s.board.NoWatchdogs {
		for _, w := range s.t.wdts {
			s.regs[w.config] = 1 << 31
		}
	}
}

// Flash returns the content of the simulated flash
func (s *Sim) Flash() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flash
}

// Link is the USB side of the board: the USB-JTAG controller, or the
// FT2232H put into MPSSE mode
func (s *Sim) Link() Link {
	if s.FTDI != nil {
		l, err := ftdi.Open(s.FTDI, ftdi.JTAGInterface, ftdiPacket)
		if err != nil {
			panic(err) // the simulated vendor requests don't fail
		}
		return l
	}
	return s.Device
}

// Options are the options OpenLink needs for the board
func (s *Sim) Options() Options {
	return Options{BootWait: time.Millisecond, Reopen: s.Reopen, Adapter: s.board.Adapter}
}

// Reopen is Options.Reopen for the board
func (s *Sim) Reopen() (Link, error) {
	s.Device.Reopen()
	return s.Link(), nil
}

func (s *Sim) ramOff(addr uint32) (int, bool) {
	if addr >= s.t.sramLow && addr+4 <= s.t.sramHigh {
		return int(addr - s.t.sramLow), true
	}
	return 0, false
}

// Read32 implements jtagsim.Bus
func (s *Sim) Read32(addr uint32) (uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if addr%4 != 0 {
		return 0, false
	}
	if off, ok := s.ramOff(addr); ok {
		return binary.LittleEndian.Uint32(s.ram[off:]), true
	}
	if addr >= 0x60000000 && addr < 0x60100000 {
		return s.regs[addr], true
	}
	return 0, false
}

// Write32 implements jtagsim.Bus
func (s *Sim) Write32(addr, v uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if addr%4 != 0 {
		return false
	}
	if off, ok := s.ramOff(addr); ok {
		binary.LittleEndian.PutUint32(s.ram[off:], v)
		return true
	}
	if addr >= 0x60000000 && addr < 0x60100000 {
		s.writeReg(addr, v)
		return true
	}
	return false
}

// writeReg models the registers with behavior: the write-protected
// watchdogs and the SPI user command that reads the flash ID
func (s *Sim) writeReg(addr, v uint32) {
	for _, w := range s.t.wdts {
		if addr == w.config && s.regs[w.protect] != w.key {
			return
		}
	}
	if sw := s.t.swd; addr == sw.config && s.regs[sw.protect] != sw.key {
		return
	}
	spi := simRegs[s.t.key].spi
	if addr == spi && v&(1<<18) != 0 {
		// USR: the RDID command answers at once, the bytes in W0 from
		// its lowest one
		id := s.board.FlashID
		s.regs[spi+0x58] = id>>16&0xFF | (id>>8&0xFF)<<8 | (id&0xFF)<<16
		s.regs[spi] = v &^ (1 << 18)
		return
	}
	s.regs[addr] = v
}

// Fetch implements jtagsim.Bus: the instruction bus sees the SRAM at
// iOffset
func (s *Sim) Fetch(pc uint32) (uint32, bool) { return s.Read32(pc - s.t.iOffset) }

// Reset implements jtagsim.Bus: a system reset boots download mode when
// the force-download bit is set (it stays: it is in the RTC/LP domain)
func (s *Sim) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = false
	s.chipSize = 0
	fd := s.t.forceDownload
	if s.regs[fd.addr]&fd.bit != 0 {
		s.BootMode = "download"
	} else {
		s.BootMode = "app"
	}
	keep := s.regs[fd.addr]
	s.regs = map[uint32]uint32{}
	s.regs[fd.addr] = keep
	s.powerOn()
}

func (s *Sim) watchdogRunning() bool {
	for _, w := range s.t.wdts {
		if s.regs[w.config]&(1<<31) != 0 {
			return true
		}
	}
	return false
}

// Call implements jtagsim.Bus: the ROM functions of the flash routine,
// over a NOR flash (programming only clears bits)
func (s *Sim) Call(h *jtagsim.Hart) jtagsim.CallResult {
	s.mu.Lock()
	rom := s.t.rom
	pc := h.PC
	known := false
	for _, f := range []uint32{rom.attach, rom.configParam, rom.unlock, rom.eraseChip, rom.eraseBlock,
		rom.eraseSector, rom.write, rom.read, rom.md5Init, rom.md5Update, rom.md5Final} {
		if f == pc {
			known = true
		}
	}
	if !known {
		s.mu.Unlock()
		return jtagsim.NotROM
	}
	s.Calls[pc]++
	if s.watchdogRunning() {
		s.WatchdogResets++
		s.mu.Unlock()
		return jtagsim.ChipReset
	}
	a := h.X[10:18]
	res := uint32(0)
	const errResult = 1
	flashOK := func(addr, n uint32) bool {
		return s.attached && addr+n <= s.chipSize && addr+n <= uint32(len(s.flash)) && addr+n >= addr
	}
	ramSlice := func(addr, n uint32) []byte {
		off, ok := s.ramOff(addr)
		if !ok || off+int(n) > len(s.ram) {
			return nil
		}
		return s.ram[off : off+int(n)]
	}
	switch pc {
	case rom.attach:
		s.attached = true
	case rom.configParam:
		s.chipSize = a[1]
		if a[2] != 0x10000 || a[3] != 0x1000 || a[4] != 0x100 {
			res = errResult
		}
	case rom.unlock:
		if !s.attached {
			res = errResult
		}
	case rom.eraseChip:
		if s.attached {
			for i := range s.flash {
				s.flash[i] = 0xFF
			}
		} else {
			res = errResult
		}
	case rom.eraseBlock, rom.eraseSector:
		size := uint32(0x10000)
		if pc == rom.eraseSector {
			size = 0x1000
		}
		addr := a[0] * size
		if flashOK(addr, size) {
			for i := addr; i < addr+size; i++ {
				s.flash[i] = 0xFF
			}
		} else {
			res = errResult
		}
	case rom.write:
		dst, n := a[0], a[2]
		src := ramSlice(a[1], n)
		if dst%4 != 0 || n%4 != 0 || src == nil || !flashOK(dst, n) {
			res = errResult
			break
		}
		for i, b := range src {
			s.flash[dst+uint32(i)] &= b
		}
	case rom.read:
		src, n := a[0], a[2]
		dst := ramSlice(a[1], n)
		if src%4 != 0 || n%4 != 0 || dst == nil || !flashOK(src, n) {
			res = errResult
			break
		}
		copy(dst, s.flash[src:src+n])
	case rom.md5Init:
		s.md5s[a[0]] = md5.New()
	case rom.md5Update:
		if m, b := s.md5s[a[0]], ramSlice(a[1], a[2]); m != nil && b != nil {
			m.Write(b)
		}
	case rom.md5Final:
		if m, b := s.md5s[a[1]], ramSlice(a[0], 16); m != nil && b != nil {
			copy(b, m.Sum(nil))
			delete(s.md5s, a[1])
		}
	}
	h.X[10] = res
	s.mu.Unlock()
	return jtagsim.Returned
}
