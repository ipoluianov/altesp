package fatfs

// The wear levelling layer of ESP-IDF (components/wear_levelling). The
// partition is cut into 4 KB sectors; at its end are two copies of the
// state and the config sector, before them the data sectors and one spare
// "dummy" sector. After every wl_update_rate erases the dummy moves one
// sector on (the sector after it is copied into it), and once it went
// round the whole partition the data turns by one sector
// (wl_dummy_sec_move_count). A record of 16 bytes after the state tells
// each move, so the state itself is rewritten only once a round.
//
// The logical address a of the file system is at
//
//	r = (flash_size - move_count*4096 + a) mod flash_size,
//	r + 4096 if r >= dummy_pos*4096
//
// in the partition (WL_Flash::calcAddr), flash_size being the data sectors
// without the dummy.
//
// Sources: the ESP-IDF Programming Guide "Wear Levelling API", and the
// Apache-2.0 sources of ESP-IDF v5.3: WL_State.h, WL_Config.h,
// WL_Flash.cpp, WL_Ext_Safe.cpp, wear_levelling.cpp; wl_fatfsgen.py for
// the values the image generator writes.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math/rand/v2"
)

const (
	wlSector     = 4096 // flash sector and WL page; ESP-IDF uses no other
	wlStateLen   = 64   // sizeof(wl_state_t)
	wlCfgLen     = 48   // sizeof(wl_config_t)
	wlRecord     = 16   // wl_pos_update_record_size
	wlVersion    = 2    // WL_CURRENT_VERSION
	wlUpdateRate = 16   // WL_DEFAULT_UPDATERATE
	wlTempBuf    = 32   // WL_DEFAULT_TEMP_BUFF_SIZE
	// The CRC of the state covers the fields before crc32 (V2), or before
	// wl_device_id (V1, whose crc was where wl_device_id is now)
	wlCRCLenV2  = 60
	wlCRCLenV1  = 28
	wlCfgCRCLen = 32

	// safeOK marks a sector copy of WL_Ext_Safe not finished
	safeOK = 0x12345678
)

// wlCRC is crc32_le(UINT32_MAX, ...) of ESP-IDF: the zlib CRC-32 going on
// from 0xFFFFFFFF
func wlCRC(b []byte) uint32 { return crc32.Update(0xFFFFFFFF, crc32.IEEETable, b) }

// wlLayout is where the parts of a WL partition are (WL_Flash::config)
type wlLayout struct {
	size      int // the partition
	stateSize int // one copy of the state: a sector, more for big partitions
	state1    int
	state2    int
	cfg       int
	flashSize int // the logical bytes: the data sectors without the dummy
	maxPos    int // the positions of the dummy: the data sectors and it
}

func newWLLayout(size int) (wlLayout, error) {
	l := wlLayout{size: size}
	if size%wlSector != 0 {
		return l, fmt.Errorf("fatfs: a wear levelling partition is a multiple of %d bytes, not %d", wlSector, size)
	}
	l.stateSize = wlSector
	if need := wlStateLen + size/wlSector*wlRecord; need > l.stateSize {
		l.stateSize = (need + wlSector - 1) / wlSector * wlSector
	}
	l.cfg = size - wlSector
	l.state1 = size - 2*l.stateSize - wlSector
	l.state2 = size - l.stateSize - wlSector
	l.flashSize = ((size-2*l.stateSize-wlSector)/wlSector - 1) * wlSector
	if l.flashSize <= 0 {
		return l, fmt.Errorf("fatfs: %d bytes is too small for wear levelling", size)
	}
	l.maxPos = 1 + l.flashSize/wlSector
	return l, nil
}

// wl is the mapping of a WL partition as the chip sees it after mounting
type wl struct {
	wlLayout
	pos   int // the dummy sector
	moves int // wl_dummy_sec_move_count
	// hasCfg: the config sector has a good CRC
	hasCfg bool
	// fresh: no state the chip would take; it writes a new one, with the
	// dummy at 0 and no moves
	fresh bool
}

// wlState is wl_state_t
type wlState struct {
	pos, maxPos, moves, access, maxCount, blockSize, version, deviceID uint32
	crc                                                                uint32
	raw                                                                []byte
}

func parseWLState(b []byte) wlState {
	le := func(i int) uint32 { return binary.LittleEndian.Uint32(b[i*4:]) }
	return wlState{pos: le(0), maxPos: le(1), moves: le(2), access: le(3), maxCount: le(4),
		blockSize: le(5), version: le(6), deviceID: le(7), crc: le(15), raw: b[:wlStateLen]}
}

func (s wlState) okV2() bool { return wlCRC(s.raw[:wlCRCLenV2]) == s.crc }

// okV1: the V1 state kept its CRC where wl_device_id is now
func (s wlState) okV1() bool { return s.version == 1 && wlCRC(s.raw[:wlCRCLenV1]) == s.deviceID }

// openWL reads the state of a WL partition the way WL_Flash::init does,
// without writing anything: which state it takes, and where it finds the
// dummy sector
func openWL(img []byte) (*wl, error) {
	l, err := newWLLayout(len(img))
	if err != nil {
		return nil, err
	}
	w := &wl{wlLayout: l}
	c := img[l.cfg:]
	if wlCRC(c[:wlCfgCRCLen]) == binary.LittleEndian.Uint32(c[wlCfgCRCLen:]) {
		page := binary.LittleEndian.Uint32(c[8:])
		sector := binary.LittleEndian.Uint32(c[12:])
		if page != wlSector || sector != wlSector {
			return nil, fmt.Errorf("fatfs: wear levelling with pages of %d and sectors of %d is not supported", page, sector)
		}
		w.hasCfg = true
	}
	s1 := parseWLState(img[l.state1:])
	s2 := parseWLState(img[l.state2:])
	ok1, ok2 := s1.okV2(), s2.okV2()
	var s wlState
	switch {
	case ok1 && ok2:
		s = s1
		if s.version == wlVersion {
			w.moves = int(s.moves)
			w.pos = w.recoverPos(img, s, w.maxPosOf(s))
		}
	case ok1:
		// The chip copies state 1 over state 2 and keeps its fields: the
		// position records are not read
		s = s1
		w.pos, w.moves = int(s.pos), int(s.moves)
	case ok2:
		// State 1 is rebuilt from state 2, and the dummy is taken to be
		// at the last position
		s = s2
		w.pos, w.moves = w.maxPosOf(s)-1, int(s.moves)
	case s1.okV1() && s2.okV1() && s1.deviceID == s2.deviceID:
		// V1 is updated to V2: the records up to the first erased one
		// are written anew, and the position found after them
		s = s1
		s.version = wlVersion
		max := w.maxPosOf(s1)
		pos := max - 1
		for i := 0; i < max; i++ {
			if img[l.state1+wlStateLen+i*wlRecord] == 0xFF {
				pos = i
				break
			}
		}
		w.pos, w.moves = min(pos+1, max-1), int(s1.moves)
	default:
		w.fresh = true
		return w, nil
	}
	if s.version != wlVersion {
		w.fresh, w.pos, w.moves = true, 0, 0
	}
	if w.pos < 0 || w.pos >= w.maxPos {
		w.pos = w.maxPos - 1
	}
	w.moves %= w.maxPos - 1
	return w, nil
}

// maxPosOf is the dummy positions of a state, within what the partition
// has
func (w *wl) maxPosOf(s wlState) int {
	if s.maxPos == 0 || int(s.maxPos) > w.maxPos {
		return w.maxPos
	}
	return int(s.maxPos)
}

// recoverPos finds the dummy: the first position without its record
// (WL_Flash::recoverPos), the last one when all have it
func (w *wl) recoverPos(img []byte, s wlState, max int) int {
	for i := 0; i < max; i++ {
		rec := img[w.state1+wlStateLen+i*wlRecord:]
		if !recordOK(rec, s.deviceID, i) {
			return i
		}
	}
	return max - 1
}

// recordOK checks the record of position i (WL_Flash::OkBuffSet): four
// CRCs of device_id + 4*i + k
func recordOK(rec []byte, id uint32, i int) bool {
	var b [4]byte
	for k := 0; k < 4; k++ {
		binary.LittleEndian.PutUint32(b[:], id+uint32(i*4+k))
		if wlCRC(b[:]) != binary.LittleEndian.Uint32(rec[k*4:]) {
			return false
		}
	}
	return true
}

// addr is where logical address a is in the partition
// (WL_Flash::calcAddr)
func (w *wl) addr(a int) int {
	r := (w.flashSize - w.moves*wlSector + a) % w.flashSize
	if r >= w.pos*wlSector {
		r += wlSector
	}
	return r
}

// unwrap gives the logical bytes of the partition
func (w *wl) unwrap(img []byte) []byte {
	out := make([]byte, w.flashSize)
	for a := 0; a < w.flashSize; a += wlSector {
		p := w.addr(a)
		copy(out[a:a+wlSector], img[p:p+wlSector])
	}
	return out
}

// wrap puts the logical bytes back into the partition; the dummy and the
// WL sectors are kept. A partition the chip would take as fresh gets a
// new state, so the chip finds its data where it was put.
func (w *wl) wrap(img, logical []byte) []byte {
	out := append([]byte(nil), img...)
	if w.fresh {
		return buildWL(w.size, logical)
	}
	for a := 0; a < w.flashSize; a += wlSector {
		p := w.addr(a)
		copy(out[p:p+wlSector], logical[a:a+wlSector])
	}
	return out
}

// buildWL makes a WL partition of the logical bytes as the chip leaves it
// after its first mount (WL_Flash::initSections) and as wl_fatfsgen.py
// writes it: the dummy at sector 0, the data from sector 1, two copies of
// the state with no position records, the config.
func buildWL(size int, logical []byte) []byte {
	l, _ := newWLLayout(size)
	img := make([]byte, size)
	for i := range img {
		img[i] = 0xFF
	}
	copy(img[wlSector:], logical[:l.flashSize])
	st := make([]byte, wlStateLen)
	le := func(i int, v uint32) { binary.LittleEndian.PutUint32(st[i*4:], v) }
	le(0, 0)
	le(1, uint32(l.maxPos))
	le(2, 0)
	le(3, 0)
	le(4, wlUpdateRate)
	le(5, wlSector)
	le(6, wlVersion)
	le(7, rand.Uint32())
	le(15, wlCRC(st[:wlCRCLenV2]))
	copy(img[l.state1:], st)
	copy(img[l.state2:], st)
	cfg := make([]byte, wlCfgLen)
	for i, v := range []uint32{0, uint32(size), wlSector, wlSector, wlUpdateRate, wlRecord, wlVersion, wlTempBuf} {
		binary.LittleEndian.PutUint32(cfg[i*4:], v)
	}
	binary.LittleEndian.PutUint32(cfg[wlCfgCRCLen:], wlCRC(cfg[:wlCfgCRCLen]))
	copy(img[l.cfg:], cfg)
	return img
}

var errSafe = errors.New("fatfs: damaged safe mode state")

// recoverSafe finishes a sector copy of WL_Ext_Safe that power cut short,
// as WL_Ext_Safe::recover does on mount: the 512-byte sectors of the 4 KB
// sector outside the range being written come back from the dump sector.
// The logical bytes end with the state sector and the dump sector.
func recoverSafe(logical []byte) error {
	n := len(logical)
	if n < 3*wlSector {
		return nil
	}
	st := logical[n-2*wlSector:]
	if binary.LittleEndian.Uint32(st) != safeOK {
		return nil
	}
	base := int(binary.LittleEndian.Uint32(st[4:]))
	off := int(binary.LittleEndian.Uint32(st[8:]))
	count := int(binary.LittleEndian.Uint32(st[12:]))
	if base < 0 || base >= n/wlSector-2 {
		return errSafe
	}
	dump := logical[n-wlSector:]
	sec := logical[base*wlSector : (base+1)*wlSector]
	const fat = 512
	for i := 0; i < wlSector/fat; i++ {
		if i < off || i >= off+count {
			copy(sec[i*fat:(i+1)*fat], dump[i*fat:])
		} else {
			for k := i * fat; k < (i+1)*fat; k++ {
				sec[k] = 0xFF
			}
		}
	}
	for i := range wlSector {
		st[i] = 0xFF
	}
	return nil
}
