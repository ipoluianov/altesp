// Package loader talks to the ROM loader of an ESP chip and to the stub
// loader it runs: SLIP framing, the commands of the serial protocol, the
// resets by the control lines. It knows nothing about chips beyond what
// the protocol needs; the chip table is the chips package.
//
// Sources: the esptool documentation "Serial Protocol" and "Boot Mode
// Selection", the ESP32-C3 TRM (USB Serial/JTAG controller), and
// esp-flasher-stub (MIT) for the stub's side of the protocol.
package loader

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ipoluianov/altesp/internal/serial"
)

// Loader is a connection to a chip in download mode
type Loader struct {
	port  serial.Port
	conn  *conn
	reset Reset
	baud  int

	// stub is set once the stub runs
	stub bool
	// statusLen is the length of the status of the ROM's responses
	statusLen int
	// secureDownload is set when the chip is in Secure Download Mode
	secureDownload bool
	// encryptWord: the ROM's FLASH_BEGIN takes a fifth word (all chips but
	// the ESP32)
	encryptWord bool
}

// Options of Connect
type Options struct {
	Reset Reset
	// Attempts is how many times the chip is reset and synced; 0 is 6
	Attempts int
	// Manual is called when the automatic reset gave up and the user has
	// to put the board into download mode by hand; Connect then waits
	// for the board until the context ends. Nil gives up.
	Manual func()
}

// ErrNoSync is a chip that doesn't answer the loader's SYNC
type ErrNoSync struct {
	// Text is what the board printed meanwhile: its boot messages
	Text string
}

func (e *ErrNoSync) Error() string {
	s := "the chip did not enter download mode"
	if t := strings.TrimSpace(e.Text); t != "" {
		s += "; the board printed: " + firstLines(t, 3)
	}
	return s
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " / ")
}

// syncBaud is the rate of the first contact: every ROM loader starts at
// 115200 and the faster rate is set by a command
const syncBaud = 115200

// Connect resets the chip into download mode and syncs with its ROM
// loader. The port is the caller's: Close of the loader doesn't close it.
func Connect(ctx context.Context, port serial.Port, info serial.PortInfo, opt Options) (*Loader, error) {
	cfg := serial.DefaultConfig()
	cfg.Baud = syncBaud
	if err := port.SetConfig(cfg); err != nil {
		return nil, err
	}
	l := &Loader{port: port, conn: newConn(port), baud: syncBaud, statusLen: 4}
	l.reset = opt.Reset
	if l.reset == ResetAuto {
		l.reset = ResetFor(info)
	}
	attempts := opt.Attempts
	if attempts <= 0 {
		attempts = 6
	}
	var text strings.Builder
	for i := 0; i < attempts; i++ {
		r := l.reset
		// Auto tries the other circuit every third attempt: a board may
		// have a bridge with an Espressif ID or the other way round
		if opt.Reset == ResetAuto && i%3 == 2 {
			r = otherReset(r)
		}
		if err := enterDownload(ctx, port, r, i); err != nil {
			return nil, err
		}
		ok, err := l.sync(ctx)
		text.WriteString(l.conn.text())
		if err != nil {
			return nil, err
		}
		if ok {
			l.reset = r
			return l, nil
		}
	}
	if opt.Manual != nil {
		opt.Manual()
		for {
			ok, err := l.sync(ctx)
			if err != nil {
				return nil, err
			}
			if ok {
				l.reset = ResetManual
				return l, nil
			}
		}
	}
	return nil, &ErrNoSync{Text: text.String()}
}

func otherReset(r Reset) Reset {
	if r == ResetUSBJTAG {
		return ResetBridge
	}
	return ResetUSBJTAG
}

// syncPacket is the data of SYNC: 07 07 12 20 and 32 times 0x55, from which
// the chip also detects the baud rate
var syncPacket = append([]byte{0x07, 0x07, 0x12, 0x20}, bytesOf(0x55, 32)...)

func bytesOf(b byte, n int) []byte {
	s := make([]byte, n)
	for i := range s {
		s[i] = b
	}
	return s
}

// sync sends SYNC a few times; it is false when the chip doesn't answer.
// The ROM answers one SYNC with several responses: the extra ones are
// dropped.
func (l *Loader) sync(ctx context.Context) (bool, error) {
	l.conn.dropPackets()
	for i := 0; i < 5; i++ {
		if err := l.conn.send(request(opSync, syncPacket, 0)); err != nil {
			return false, err
		}
		deadline := time.Now().Add(100 * time.Millisecond)
		for {
			p, err := l.conn.recv(ctx, time.Until(deadline))
			if errors.Is(err, ErrTimeout) {
				break
			}
			if err != nil {
				return false, err
			}
			if len(p) >= headerSize && p[0] == dirResponse && p[1] == opSync {
				size := int(binary.LittleEndian.Uint16(p[2:]))
				if size == 2 || size == 4 {
					l.statusLen = size
				}
				l.drain(ctx, 100*time.Millisecond)
				return true, nil
			}
		}
	}
	return false, nil
}

// drain drops the packets that come during d
func (l *Loader) drain(ctx context.Context, d time.Duration) {
	deadline := time.Now().Add(d)
	for {
		if _, err := l.conn.recv(ctx, time.Until(deadline)); err != nil {
			return
		}
	}
}

// defaultTimeout is the time a simple command may take
const defaultTimeout = 3 * time.Second

// command sends a command and waits for its response, skipping the
// responses to other commands
func (l *Loader) command(ctx context.Context, op byte, data []byte, chk uint32, timeout time.Duration) (response, error) {
	if err := l.conn.send(request(op, data, chk)); err != nil {
		return response{}, err
	}
	return l.wait(ctx, op, timeout)
}

func (l *Loader) wait(ctx context.Context, op byte, timeout time.Duration) (response, error) {
	deadline := time.Now().Add(timeout)
	statusLen := l.statusLen
	if l.stub {
		statusLen = 2
	}
	for {
		p, err := l.conn.recv(ctx, time.Until(deadline))
		if err != nil {
			if errors.Is(err, ErrTimeout) {
				return response{}, fmt.Errorf("%s: %w", opName(op), err)
			}
			return response{}, err
		}
		r, ok := parseResponse(p, statusLen)
		if !ok || r.op != op {
			continue
		}
		if r.status != 0 {
			return r, &Error{Op: op, Status: r.status, Code: r.code, Stub: l.stub}
		}
		return r, nil
	}
}

// Stub tells whether the stub loader runs
func (l *Loader) Stub() bool { return l.stub }

// Reset is the way the chip was reset into download mode
func (l *Loader) Reset() Reset { return l.reset }

// Baud is the current rate of the port
func (l *Loader) Baud() int { return l.baud }

// SecureDownload tells whether the chip is in Secure Download Mode: only
// a few commands work, the stub can't be loaded, flash can't be read
func (l *Loader) SecureDownload() bool { return l.secureDownload }

// SetEncryptWord tells that the ROM's FLASH_BEGIN takes the fifth word
// (the encryption flag): all chips but the ESP32
func (l *Loader) SetEncryptWord(on bool) { l.encryptWord = on }

// ReadReg reads a 32-bit word of the chip's memory
func (l *Loader) ReadReg(ctx context.Context, addr uint32) (uint32, error) {
	r, err := l.command(ctx, opReadReg, words(addr), 0, defaultTimeout)
	return r.value, err
}

// WriteReg writes a 32-bit word under a mask and waits delay microseconds
func (l *Loader) WriteReg(ctx context.Context, addr, value, mask, delayUS uint32) error {
	_, err := l.command(ctx, opWriteReg, words(addr, value, mask, delayUS), 0, defaultTimeout)
	return err
}

// SecurityInfo is the answer of GET_SECURITY_INFO
type SecurityInfo struct {
	Flags         uint32
	FlashCryptCnt byte
	KeyPurposes   [7]byte
	// ChipID is the chip's ID as in the image header (esp_chip_id_t);
	// HasChipID is false for the chips whose answer lacks it (ESP32-S2)
	ChipID     uint32
	HasChipID  bool
	ECOVersion uint32
}

// SecurityInfo asks the chip for its security state and ID. The ESP32
// doesn't know the command: the error is then Unsupported.
func (l *Loader) SecurityInfo(ctx context.Context) (SecurityInfo, error) {
	r, err := l.command(ctx, opGetSecurityInfo, nil, 0, defaultTimeout)
	if err != nil {
		return SecurityInfo{}, err
	}
	d := r.data
	if len(d) < 12 {
		return SecurityInfo{}, fmt.Errorf("GET_SECURITY_INFO: short answer (%d bytes)", len(d))
	}
	var s SecurityInfo
	s.Flags = binary.LittleEndian.Uint32(d)
	s.FlashCryptCnt = d[4]
	copy(s.KeyPurposes[:], d[5:12])
	if len(d) >= 20 {
		s.ChipID = binary.LittleEndian.Uint32(d[12:])
		s.ECOVersion = binary.LittleEndian.Uint32(d[16:])
		s.HasChipID = true
	}
	return s, nil
}

// SetSecureDownload is set by the caller once it has found the chip in
// Secure Download Mode (a magic register can't be read)
func (l *Loader) SetSecureDownload(on bool) { l.secureDownload = on }

// ChangeBaud switches the chip and the port to another rate
func (l *Loader) ChangeBaud(ctx context.Context, baud int) error {
	old := uint32(0)
	if l.stub {
		old = uint32(l.baud)
	}
	if _, err := l.command(ctx, opChangeBaud, words(uint32(baud), old), 0, defaultTimeout); err != nil {
		return err
	}
	cfg := serial.DefaultConfig()
	cfg.Baud = baud
	if err := l.port.SetConfig(cfg); err != nil {
		return err
	}
	l.baud = baud
	// The chip switches after its answer: what comes meanwhile is noise
	if err := sleep(ctx, 50*time.Millisecond); err != nil {
		return err
	}
	l.conn.flush()
	return nil
}

// SPIAttach enables the SPI flash with the default pins
func (l *Loader) SPIAttach(ctx context.Context) error {
	data := words(0)
	if !l.stub {
		// The ROM takes a second word, the legacy flash mode flag
		data = words(0, 0)
	}
	_, err := l.command(ctx, opSPIAttach, data, 0, defaultTimeout)
	return err
}

// SetFlashSize tells the loader the size of the flash. The other values
// are the usual ones and are not used for writing.
func (l *Loader) SetFlashSize(ctx context.Context, size uint32) error {
	const (
		blockSize  = 64 * 1024
		sectorSize = 4 * 1024
		pageSize   = 256
		statusMask = 0xFFFF
	)
	_, err := l.command(ctx, opSPISetParams, words(0, size, blockSize, sectorSize, pageSize, statusMask), 0, defaultTimeout)
	return err
}

// RunApp resets the chip into the application. The loader can't be used
// after it.
func (l *Loader) RunApp(ctx context.Context) error {
	if l.reset == ResetManual {
		return nil
	}
	return runApp(ctx, l.port, l.reset)
}

// Text returns what the chip printed outside the protocol since the last
// call
func (l *Loader) Text() string { return l.conn.text() }
