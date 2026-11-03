package loader

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"io"
	"sync"
	"time"

	"github.com/ipoluianov/altesp/internal/serial"
)

// SimBoard describes the board a Sim pretends to be
type SimBoard struct {
	// Circuit is how the control lines reach the chip: ResetBridge (the
	// auto-reset transistors) or ResetUSBJTAG (the CDC-ACM of the chip)
	Circuit Reset
	// SecurityInfo answers GET_SECURITY_INFO; nil is a chip without the
	// command (ESP32)
	SecurityInfo []byte
	// Magic is the word at 0x40001000 (ESP32, ESP32-S2 detection)
	Magic uint32
	// ROMStatusLen is the length of the ROM's status: 4 or 2
	ROMStatusLen int
	// EncryptWord: the ROM's FLASH_BEGIN takes five words
	EncryptWord bool
	// ReadSlow: the ROM knows READ_FLASH_SLOW
	ReadSlow bool
	// SecureDownload: only the commands of Secure Download Mode work
	SecureDownload bool
	// Flash is the content of the flash, changed by the writes
	Flash []byte
	// AppLog is printed when the application boots
	AppLog string
	// NoStub: the stub never says OHAI
	NoStub bool
	// MaxBaud makes the line lose what the board sends above this rate, as
	// an adapter that can't keep up; 0 is no limit
	MaxBaud int
	// Regs answers READ_REG and takes WRITE_REG (an eFuse model); nil
	// reads zeros and ignores the writes
	Regs interface {
		ReadReg(addr uint32) (uint32, error)
		WriteReg(addr, value, mask, delayUS uint32) error
	}
	// ReadStallAfter makes READ_FLASH stall after that many packets, as
	// the stub on an ESP32 did: the acknowledgements are not seen until a
	// frame of another length comes, which ends the read
	ReadStallAfter int
	// LoseReadReg is the number of READ_REG answers lost on the line
	// before the board answers again
	LoseReadReg int
	// BreakAfter makes the line go dead after that many commands that
	// carry flash data (FLASH_DATA, FLASH_DEFL_DATA, READ_FLASH): nothing
	// is answered any more, as a cable with a bad contact; 0 never
	BreakAfter int
}

type simMode int

const (
	simOff simMode = iota // in reset or not powered yet
	simApp
	simROM
	simStub
)

// Sim is a simulated ESP board on the other end of a port: the auto-reset
// circuit, the ROM loader and the stub loader, over an in-memory flash.
// It is a serial.Port for the tests of this package and of the operations
// above it.
type Sim struct {
	board SimBoard

	mu     sync.Mutex
	cond   *sync.Cond
	out    bytes.Buffer
	closed bool
	// dataCmds counts the commands of flash data; dead: the line broke
	// (see SimBoard.BreakAfter)
	dataCmds int
	dead     bool

	mode     simMode
	dtr, rts bool
	baud     int
	// bridge: EN was released and the chip boots when the timer fires,
	// reading GPIO0 then (the EN capacitor delays it)
	bootTimer *time.Timer
	// usbFlag is the download flag of the USB-Serial-JTAG controller
	usbFlag bool

	dec slipDecoder

	// flash write in progress
	writeAddr uint32
	writeSeq  int
	defl      bool
	encrypt   bool
	deflBuf   bytes.Buffer
	deflLeft  int

	// READ_FLASH in progress
	reading           bool
	readAddr, readEnd uint32
	readPacket        uint32
	readDigest        []byte
	readSent          int
	readStalled       bool

	// Commands counts the commands received, by opcode
	Commands map[byte]int
	// Boots counts the boots into the application and into download mode
	AppBoots, DownloadBoots int
}

// NewSim powers the board on: it boots the application
func NewSim(b SimBoard) *Sim {
	if b.ROMStatusLen == 0 {
		b.ROMStatusLen = 4
	}
	s := &Sim{board: b, Commands: map[byte]int{}}
	s.cond = sync.NewCond(&s.mu)
	s.mu.Lock()
	s.boot(false)
	s.mu.Unlock()
	return s
}

// BreakIn makes the line go dead after n more commands of flash data
func (s *Sim) BreakIn(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.board.BreakAfter = s.dataCmds + n
}

// DataCommands is the number of commands of flash data handled
func (s *Sim) DataCommands() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataCmds
}

// broken counts a command of flash data and tells the line gone dead
func (s *Sim) broken() bool {
	s.dataCmds++
	s.dead = s.board.BreakAfter > 0 && s.dataCmds > s.board.BreakAfter
	return s.dead
}

// FlashData returns a copy of the flash
func (s *Sim) FlashData() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.board.Flash...)
}

// Mode tells what runs: "app", "rom", "stub" or "off"
func (s *Sim) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return [...]string{"off", "app", "rom", "stub"}[s.mode]
}

// Baud is the rate the host set last
func (s *Sim) Baud() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baud
}

func (s *Sim) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.out.Len() == 0 && !s.closed {
		s.cond.Wait()
	}
	if s.closed {
		return 0, serial.ErrClosed
	}
	return s.out.Read(p)
}

func (s *Sim) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, serial.ErrClosed
	}
	if s.mode == simROM || s.mode == simStub {
		s.dec.feed(p, s.handle)
	}
	return len(p), nil
}

func (s *Sim) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.bootTimer != nil {
		s.bootTimer.Stop()
	}
	s.cond.Broadcast()
	return nil
}

func (s *Sim) SetConfig(c serial.Config) error {
	s.mu.Lock()
	s.baud = c.Baud
	s.mu.Unlock()
	return nil
}

func (s *Sim) Signals() (serial.Signals, error) { return serial.Signals{}, nil }
func (s *Sim) Break(d time.Duration) error      { return nil }
func (s *Sim) SetDTR(on bool) error             { return s.setLines(on, s.rtsNow()) }
func (s *Sim) SetRTS(on bool) error             { return s.setLines(s.dtrNow(), on) }
func (s *Sim) dtrNow() bool                     { s.mu.Lock(); defer s.mu.Unlock(); return s.dtr }
func (s *Sim) rtsNow() bool                     { s.mu.Lock(); defer s.mu.Unlock(); return s.rts }
func (s *Sim) emit(b []byte) {
	if s.board.MaxBaud > 0 && s.baud > s.board.MaxBaud {
		return
	}
	s.out.Write(b)
	s.cond.Broadcast()
}
func (s *Sim) emitPacket(p []byte)           { s.emit(slipEncode(p)) }
func (s *Sim) io0Low() bool                  { return s.dtr && !s.rts }
func (s *Sim) enLow() bool                   { return s.rts && !s.dtr }
func (s *Sim) count(op byte)                 { s.Commands[op]++ }
func (s *Sim) stubStatus(code uint16) []byte { return []byte{byte(code >> 8), byte(code)} }
func (s *Sim) romStatus(ok bool, code byte) []byte {
	st := make([]byte, s.board.ROMStatusLen)
	if !ok {
		st[0], st[1] = 1, code
	}
	return st
}

// simBootDelay is how long EN takes to rise on a board with a capacitor
const simBootDelay = 5 * time.Millisecond

func (s *Sim) setLines(dtr, rts bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wasEN := s.enLow()
	prevDTR, prevRTS := s.dtr, s.rts
	s.dtr, s.rts = dtr, rts
	switch s.board.Circuit {
	case ResetUSBJTAG:
		// TRM ESP32-C3 table 30.3-2
		switch {
		case !rts && !dtr:
			s.usbFlag = false
		case !rts && dtr:
			s.usbFlag = true
		case rts && !dtr && !(prevRTS && !prevDTR):
			s.boot(s.usbFlag)
		}
	default:
		if s.enLow() {
			s.mode = simOff
			if s.bootTimer != nil {
				s.bootTimer.Stop()
				s.bootTimer = nil
			}
		} else if wasEN {
			s.bootTimer = time.AfterFunc(simBootDelay, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if !s.closed && s.mode == simOff {
					s.boot(s.io0Low())
				}
			})
		}
	}
	return nil
}

// boot starts the chip: the ROM loader when download is set, else the
// application
func (s *Sim) boot(download bool) {
	s.dec = slipDecoder{}
	s.reading = false
	if download {
		s.mode = simROM
		s.DownloadBoots++
		s.emit([]byte("ESP-ROM:sim\r\nrst:0x1 (POWERON),boot:0x3 (DOWNLOAD(USB/UART0))\r\nwaiting for download\r\n"))
		return
	}
	s.mode = simApp
	s.AppBoots++
	s.emit([]byte("ESP-ROM:sim\r\nrst:0x1 (POWERON),boot:0x8 (SPI_FAST_FLASH_BOOT)\r\n" + s.board.AppLog))
}

// reply sends the response to a command: data, then the status
func (s *Sim) reply(op byte, value uint32, data []byte, ok bool, code byte) {
	var st []byte
	if s.mode == simStub {
		c := uint16(0)
		if !ok {
			c = uint16(code)<<8 | 0
		}
		st = s.stubStatus(c)
	} else {
		st = s.romStatus(ok, code)
	}
	body := append(append([]byte(nil), data...), st...)
	p := make([]byte, headerSize, headerSize+len(body))
	p[0], p[1] = dirResponse, op
	binary.LittleEndian.PutUint16(p[2:], uint16(len(body)))
	binary.LittleEndian.PutUint32(p[4:], value)
	s.emitPacket(append(p, body...))
}

// fail answers with an error: the ROM's "invalid message", the stub's
// "invalid command"
func (s *Sim) fail(op byte, romCode byte) {
	if s.mode == simStub {
		s.reply(op, 0, nil, false, 0xC3)
		return
	}
	s.reply(op, 0, nil, false, romCode)
}

// secureAllowed are the commands of Secure Download Mode
var secureAllowed = map[byte]bool{
	opSync: true, opSPIAttach: true, opSPISetParams: true, opChangeBaud: true,
	opFlashBegin: true, opFlashData: true, opFlashEnd: true, opGetSecurityInfo: true,
}

func (s *Sim) handle(p []byte) {
	if s.dead {
		return
	}
	if s.reading {
		s.readAck(p)
		return
	}
	if len(p) < headerSize || p[0] != dirRequest {
		return
	}
	op := p[1]
	size := int(binary.LittleEndian.Uint16(p[2:]))
	chk := binary.LittleEndian.Uint32(p[4:])
	data := p[headerSize:]
	if size != len(data) {
		s.fail(op, 0x05)
		return
	}
	s.count(op)
	if s.board.SecureDownload && s.mode == simROM && !secureAllowed[op] {
		s.fail(op, 0x05)
		return
	}
	u32 := func(i int) uint32 {
		if len(data) < 4*(i+1) {
			return 0
		}
		return binary.LittleEndian.Uint32(data[4*i:])
	}
	switch op {
	case opSync:
		if len(data) != 36 {
			s.fail(op, 0x05)
			return
		}
		// The ROM answers one SYNC several times
		n := 1
		if s.mode == simROM {
			n = 8
		}
		for i := 0; i < n; i++ {
			s.reply(op, 0x20120707, nil, true, 0)
		}
	case opReadReg:
		v := uint32(0)
		if u32(0) == 0x40001000 {
			v = s.board.Magic
		} else if s.board.Regs != nil {
			v, _ = s.board.Regs.ReadReg(u32(0))
		}
		if s.board.LoseReadReg > 0 {
			s.board.LoseReadReg--
			break
		}
		s.reply(op, v, nil, true, 0)
	case opWriteReg:
		// addr, value, mask, delay: one or more groups of four words
		if s.board.Regs != nil {
			for i := 0; 4*(i+4) <= len(data); i += 4 {
				s.board.Regs.WriteReg(u32(i), u32(i+1), u32(i+2), u32(i+3))
			}
		}
		s.reply(op, 0, nil, true, 0)
	case opSPIAttach, opSPISetParams, opChangeBaud, opFlashEnd:
		s.reply(op, 0, nil, true, 0)
	case opGetSecurityInfo:
		if s.board.SecurityInfo == nil {
			s.fail(op, 0x05)
			return
		}
		s.reply(op, 0, s.board.SecurityInfo, true, 0)
	case opMemBegin, opMemData:
		if op == opMemData && checksum(data[16:]) != chk {
			s.fail(op, 0x07)
			return
		}
		s.reply(op, 0, nil, true, 0)
	case opMemEnd:
		s.reply(op, 0, nil, true, 0)
		if !s.board.NoStub && s.mode == simROM {
			s.mode = simStub
			s.emitPacket(stubGreeting)
		}
	case opFlashBegin, opFlashDeflBegin:
		want := 16
		if s.mode == simROM && s.board.EncryptWord {
			want = 20
		}
		if len(data) != want && !(s.mode == simStub && len(data) == 20) {
			s.fail(op, 0x05)
			return
		}
		eraseSize, addr := u32(0), u32(3)
		if int(addr)+int(eraseSize) > len(s.board.Flash) {
			s.fail(op, 0x01)
			return
		}
		end := min((addr+eraseSize+sectorSize-1)/sectorSize*sectorSize, uint32(len(s.board.Flash)))
		for i := addr / sectorSize * sectorSize; i < end; i++ {
			s.board.Flash[i] = 0xFF
		}
		s.writeAddr, s.writeSeq = addr, 0
		s.encrypt = len(data) == 20 && u32(4) == 1
		s.defl = op == opFlashDeflBegin
		s.deflBuf.Reset()
		s.deflLeft = int(u32(1))
		s.reply(op, 0, nil, true, 0)
	case opFlashData, opFlashDeflData:
		if s.broken() {
			return
		}
		if len(data) < 16 || int(u32(0)) != len(data)-16 || int(u32(1)) != s.writeSeq {
			s.fail(op, 0x05)
			return
		}
		block := data[16:]
		if checksum(block) != chk {
			s.fail(op, 0x07)
			return
		}
		s.writeSeq++
		if op == opFlashData {
			copy(s.board.Flash[s.writeAddr:], s.cipher(block))
			s.writeAddr += uint32(len(block))
		} else {
			s.deflBuf.Write(block)
			s.deflLeft--
			if s.deflLeft == 0 {
				r, err := zlib.NewReader(&s.deflBuf)
				if err != nil {
					s.fail(op, 0x0b)
					return
				}
				plain, err := io.ReadAll(r)
				if err != nil {
					s.fail(op, 0x0b)
					return
				}
				copy(s.board.Flash[s.writeAddr:], s.cipher(plain))
			}
		}
		s.reply(op, 0, nil, true, 0)
	case opSPIFlashMD5:
		addr, n := u32(0), u32(1)
		if int(addr+n) > len(s.board.Flash) {
			s.fail(op, 0x01)
			return
		}
		sum := md5.Sum(s.board.Flash[addr : addr+n])
		if s.mode == simStub {
			s.reply(op, 0, sum[:], true, 0)
		} else {
			s.reply(op, 0, []byte(hex.EncodeToString(sum[:])), true, 0)
		}
	case opEraseFlash:
		if s.mode != simStub {
			s.fail(op, 0x05)
			return
		}
		for i := range s.board.Flash {
			s.board.Flash[i] = 0xFF
		}
		s.reply(op, 0, nil, true, 0)
	case opEraseRegion:
		addr, n := u32(0), u32(1)
		if s.mode != simStub || addr%sectorSize != 0 || n%sectorSize != 0 || int(addr+n) > len(s.board.Flash) {
			s.fail(op, 0x05)
			return
		}
		for i := addr; i < addr+n; i++ {
			s.board.Flash[i] = 0xFF
		}
		s.reply(op, 0, nil, true, 0)
	case opReadFlashSlow:
		addr, n := u32(0), u32(1)
		if s.mode != simROM || !s.board.ReadSlow || int(addr+n) > len(s.board.Flash) {
			s.fail(op, 0x05)
			return
		}
		s.reply(op, 0, s.board.Flash[addr:addr+n], true, 0)
	case opReadFlash:
		if s.broken() {
			return
		}
		addr, n, packet := u32(0), u32(1), u32(2)
		if s.mode != simStub || int(addr+n) > len(s.board.Flash) || packet == 0 || packet > 4096 {
			s.fail(op, 0x05)
			return
		}
		s.reply(op, 0, nil, true, 0)
		sum := md5.Sum(s.board.Flash[addr : addr+n])
		s.reading, s.readAddr, s.readEnd, s.readPacket, s.readDigest = true, addr, addr+n, packet, sum[:]
		s.readSent, s.readStalled = 0, false
		s.readNext()
	default:
		s.fail(op, 0x05)
	}
}

// cipher stands for the encryption of an encrypted write: the simulated
// flash holds the data XOR 0x5A
func (s *Sim) cipher(b []byte) []byte {
	if !s.encrypt {
		return b
	}
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = c ^ 0x5A
	}
	return out
}

// readNext sends the next READ_FLASH packet; one is in flight at a time,
// as esp-flasher-stub does
func (s *Sim) readNext() {
	if s.readAddr >= s.readEnd {
		s.emitPacket(s.readDigest)
		s.reading = false
		return
	}
	if s.board.ReadStallAfter > 0 && s.readSent == s.board.ReadStallAfter && !s.readStalled {
		s.readStalled = true
		return
	}
	end := min(s.readAddr+s.readPacket, s.readEnd)
	s.emitPacket(s.board.Flash[s.readAddr:end])
	s.readAddr = end
	s.readSent++
}

// readAck takes the host's acknowledgement of a READ_FLASH packet. A frame
// of another length ends the read: the stub sends what it holds back and
// the digest, and the frame is lost.
func (s *Sim) readAck(p []byte) {
	if len(p) != 4 {
		if s.readStalled && s.readAddr < s.readEnd {
			end := min(s.readAddr+s.readPacket, s.readEnd)
			s.emitPacket(s.board.Flash[s.readAddr:end])
		}
		s.emitPacket(s.readDigest)
		s.reading = false
		return
	}
	if s.readStalled {
		return
	}
	s.readNext()
}
