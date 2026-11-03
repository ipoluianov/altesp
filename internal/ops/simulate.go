package ops

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"sync"

	"github.com/ipoluianov/altesp/internal/loader"
	"github.com/ipoluianov/altesp/internal/serial"
)

// SimulateBoard makes the package see one simulated ESP32-C3 board with
// built-in USB on the port "sim0" instead of the ports of the computer:
// for the tests of the window and the command line, and for trying the
// program without a board. The board has a partition table and an
// application, and prints a boot log each time its port is opened. end
// puts the real ports back.
func SimulateBoard() (end func()) {
	return simulate(c3Board())
}

// simulate puts the simulated board in place of the ports
func simulate(board loader.SimBoard) (end func()) {
	st := &simState{board: board}
	oldList, oldOpen, oldOpenCfg, oldSim := listPorts, openPort, openPortConfig, simulated
	oldJTAG := listJTAG
	// No USB-JTAG of the computer: only the simulated board
	listJTAG = func() ([]JTAGDevice, error) { return nil, nil }
	listPorts = func() ([]serial.PortInfo, error) { return []serial.PortInfo{simPort}, nil }
	openPort = func(serial.PortInfo) (serial.Port, error) { return st.open() }
	openPortConfig = func(serial.PortInfo, serial.Config) (serial.Port, error) { return st.open() }
	simulated = true
	return func() {
		listPorts, openPort, openPortConfig, simulated = oldList, oldOpen, oldOpenCfg, oldSim
		listJTAG = oldJTAG
		st.mu.Lock()
		if st.cur != nil {
			st.cur.Close()
		}
		st.mu.Unlock()
	}
}

// simulated is set while SimulateBoard is in effect: the simulated port
// never disappears on a reset (see WaitPort)
var simulated bool

// simState is the simulated board across the openings of its port: each
// opening is a new Sim over the flash the last one left, as a board that
// stays powered keeps its flash. The port is taken exclusively, as a real
// one.
type simState struct {
	mu    sync.Mutex
	board loader.SimBoard
	cur   *loader.Sim
}

func (st *simState) open() (serial.Port, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cur != nil {
		return nil, serial.ErrBusy
	}
	b := st.board
	b.Flash = append([]byte(nil), st.board.Flash...)
	st.cur = loader.NewSim(b)
	return &simHandle{Sim: st.cur, st: st}, nil
}

// simHandle is an opened simulated port: closing it keeps the flash and
// frees the port
type simHandle struct {
	*loader.Sim
	st   *simState
	once sync.Once
}

func (h *simHandle) Close() error {
	h.once.Do(func() {
		h.st.mu.Lock()
		h.st.board.Flash = h.Sim.FlashData()
		if h.st.cur == h.Sim {
			h.st.cur = nil
		}
		h.st.mu.Unlock()
	})
	return h.Sim.Close()
}

// simPort is the port of the simulated board
var simPort = serial.PortInfo{Name: "sim0", Short: "sim0", USB: true, VID: 0x303a, PID: 0x1001,
	SerialNumber: "SIM", Description: "Simulated ESP32-C3"}

// partitionTable builds a table as ESP-IDF writes it: 32-byte entries
// (magic AA 50, type, subtype, offset, size, name, flags), the MD5 entry
// (EB EB, 14 x FF, the MD5 of the entries) and 0xFF after
func partitionTable() []byte {
	entry := func(name string, typ, sub byte, off, size uint32) []byte {
		e := make([]byte, 32)
		e[0], e[1], e[2], e[3] = 0xAA, 0x50, typ, sub
		binary.LittleEndian.PutUint32(e[4:], off)
		binary.LittleEndian.PutUint32(e[8:], size)
		copy(e[12:28], name)
		return e
	}
	var t []byte
	t = append(t, entry("nvs", 1, 0x02, 0x9000, 0x6000)...)
	t = append(t, entry("phy_init", 1, 0x01, 0xF000, 0x1000)...)
	t = append(t, entry("factory", 0, 0x00, 0x10000, 0x100000)...)
	sum := md5.Sum(t)
	m := make([]byte, 32)
	m[0], m[1] = 0xEB, 0xEB
	for i := 2; i < 16; i++ {
		m[i] = 0xFF
	}
	copy(m[16:], sum[:])
	t = append(t, m...)
	return append(t, bytes.Repeat([]byte{0xFF}, 0xC00-len(t))...)
}

// appHead builds the start of an application image (esp_image_header_t,
// esp_image_segment_header_t, esp_app_desc_t of ESP-IDF esp_app_format.h
// and esp_app_desc.h) for the chip ID
func appHead(chipID uint16, project, version string) []byte {
	h := make([]byte, 0x120)
	h[0], h[1] = 0xE9, 1                          // magic, one segment
	binary.LittleEndian.PutUint16(h[12:], chipID) // extended header: chip_id
	binary.LittleEndian.PutUint16(h[17:], 0xFFFF) // max_chip_rev_full
	binary.LittleEndian.PutUint32(h[24:], 0x3C000020)
	binary.LittleEndian.PutUint32(h[28:], 0x100)
	d := h[32:]
	binary.LittleEndian.PutUint32(d[0:], 0xABCD5432)
	copy(d[16:48], version)
	copy(d[48:80], project)
	copy(d[80:96], "12:00:00")
	copy(d[96:112], "Nov  2 2026")
	copy(d[112:144], "v5.4")
	return h
}

// appImage is a whole application image: appHead, the padding and the
// checksum byte (0xEF XOR the segment data) at the end of a 16-byte block
func appImage(chipID uint16) []byte {
	img := appHead(chipID, "app", "1.0")
	sum := byte(0xEF)
	for _, b := range img[32:] {
		sum ^= b
	}
	for (len(img)+1)%16 != 0 {
		img = append(img, 0)
	}
	return append(img, sum)
}

func c3Board() loader.SimBoard {
	si := make([]byte, 20)
	binary.LittleEndian.PutUint32(si[12:], 5) // ESP32-C3
	flash := bytes.Repeat([]byte{0xFF}, 4<<20)
	copy(flash[0x8000:], partitionTable())
	copy(flash[0x10000:], appHead(5, "sensor-node", "1.4.2"))
	return loader.SimBoard{Circuit: loader.ResetUSBJTAG, SecurityInfo: si, EncryptWord: true, Flash: flash,
		AppLog: "I (31) boot: ESP-IDF v5.4 2nd stage bootloader\r\nW (120) wifi: no AP configured\r\nE (130) app: sensor not found\r\nI (140) app: running\r\n"}
}
