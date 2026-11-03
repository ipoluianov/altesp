package efuse

import (
	"fmt"

	"github.com/ipoluianov/altesp/internal/chips"
)

// Model is an in-memory eFuse controller of a chip, for tests and dry
// runs. It implements chips.Regs with the registers Burn and Read use: the
// programming registers (PGM_DATA and PGM_CHECK_VALUE, or the ESP32's
// BLKn_WDATA), CONF with its op codes, CMD with PGM and READ and the
// block number, the read registers, the error registers (RD_REPEAT_ERR,
// RD_RS_ERR, the ESP32's DEC_STATUS) and the timing registers as plain
// memory. Programming ORs bits into the eFuses; bits under a set WR_DIS
// bit don't change; blocks under a set RD_DIS bit read as zeros. The read
// registers change only on a READ command, as on the chips. RS-coded
// blocks keep their check bytes and report a failed block when they don't
// match the data; the ESP32's 3/4 blocks are stored coded and decoded on
// read, a group with wrong check bytes setting DEC_STATUS.
//
// unverified, the model's assumptions about the hardware: what the ESP32
// reads in BLKn_RDATA6-7 with the 3/4 coding (zero here) and in the
// repeat coding (the OR of both copies here), which bit of a block's
// DEC_STATUS nibble a group sets (bit g for group g here), that a write-
// protected block takes no bits at all, and that RS errors are not
// corrected (the chips correct up to 6 bytes).
type Model struct {
	chip *chips.Chip
	l    *layout
	t    *tableDef

	fuses  [][]uint32 // the eFuse bits; the ESP32's BLOCK1-3 coded
	parity [][3]uint32
	rd     [][]uint32 // the read registers
	errs   map[uint32]uint32
	regs   map[uint32]uint32
	busy   int

	// Busy is how many reads of CMD show a command still running
	Busy int
	// Programs counts the PGM commands, Reads the READ commands
	Programs, Reads int
	// Reprogrammed counts the bits programmed again while already 1,
	// which the TRMs forbid
	Reprogrammed int
	// BadCommands counts commands with a wrong op code in CONF
	BadCommands int
	// Stuck are bits that don't burn, per block and word: a defective
	// chip for the tests
	Stuck map[[2]int]uint32
}

// NewModel is a blank chip: all eFuses 0; nil for a chip without a layout
func NewModel(chip *chips.Chip, rev chips.Revision) *Model {
	l, t := layoutFor(chip), tableFor(chip, rev)
	if l == nil || t == nil {
		return nil
	}
	m := &Model{chip: chip, l: l, t: t, regs: map[uint32]uint32{}, errs: map[uint32]uint32{},
		Stuck: map[[2]int]uint32{}}
	for _, b := range l.blocks {
		n := b.words
		if l.kind == kindESP32 {
			n = 8
			if len(m.fuses) == 0 {
				n = b.words
			}
		}
		m.fuses = append(m.fuses, make([]uint32, n))
		m.rd = append(m.rd, make([]uint32, b.words))
	}
	m.parity = make([][3]uint32, len(l.blocks))
	m.latch()
	return m
}

// Set burns data into a block directly, as the factory does: the words as
// the read registers show them, coded as the block's scheme needs
func (m *Model) Set(blk int, words []uint32) {
	m.setRead(blk, words)
}

// setRead stores a block so that it reads as words
func (m *Model) setRead(blk int, words []uint32) {
	if m.l.kind == kindESP32 && blk > 0 {
		var w [8]uint32
		switch m.fuses[0][6] & 3 {
		case 1:
			w = encode34(words)
		case 2:
			w = encodeRepeat(words)
		default:
			copy(w[:], words)
		}
		for i := range w {
			m.fuses[blk][i] |= w[i]
		}
	} else {
		for i := range m.fuses[blk] {
			if i < len(words) {
				m.fuses[blk][i] |= words[i]
			}
		}
		if m.l.kind == kindRS && blk > 0 {
			m.parity[blk] = rsEncode(m.fuses[blk])
		}
	}
	m.latch()
}

// CorruptParity flips a check byte of an RS block: the chip then reports
// the block as failed
func (m *Model) CorruptParity(blk int) {
	m.parity[blk][0] ^= 1
	m.latch()
}

// Fuses are the stored bits of a block (the ESP32's BLOCK1-3 coded)
func (m *Model) Fuses(blk int) []uint32 { return append([]uint32(nil), m.fuses[blk]...) }

// Reg is a register as last written (the timing and programming registers)
func (m *Model) Reg(addr uint32) uint32 { return m.regs[addr] }

func (m *Model) ReadReg(addr uint32) (uint32, error) {
	l := m.l
	if addr == l.cmd {
		if m.busy > 0 {
			m.busy--
			return m.regs[addr], nil
		}
		m.regs[addr] = 0
		return 0, nil
	}
	for b, bl := range l.blocks {
		if addr >= bl.rd && addr < bl.rd+uint32(4*bl.words) && (addr-bl.rd)%4 == 0 {
			return m.rd[b][(addr-bl.rd)/4], nil
		}
	}
	if v, ok := m.errs[addr]; ok {
		return v, nil
	}
	return m.regs[addr], nil
}

func (m *Model) WriteReg(addr, value, mask, delayUS uint32) error {
	if mask == 0 {
		mask = 0xFFFFFFFF
	}
	l := m.l
	for _, bl := range l.blocks {
		if addr >= bl.rd && addr < bl.rd+uint32(4*bl.words) {
			return fmt.Errorf("model: 0x%08X is a read-only register", addr)
		}
	}
	v := m.regs[addr]&^mask | value&mask
	m.regs[addr] = v
	if addr != l.cmd {
		return nil
	}
	op := m.regs[l.conf] & 0xFFFF
	switch {
	case v&cmdPgm != 0:
		if op != opWrite {
			m.BadCommands++
			break
		}
		m.Programs++
		if l.kind == kindESP32 {
			m.programESP32()
		} else {
			m.programRS(int(v >> cmdBlkNumS & 0xF))
		}
	case v&cmdRead != 0:
		if op != opRead {
			m.BadCommands++
			break
		}
		m.Reads++
		m.latch()
	}
	m.busy = m.Busy
	if m.busy == 0 {
		m.regs[addr] = 0
	}
	return nil
}

// wrProtected are the bits of a word of a block that WR_DIS protects now
func (m *Model) wrProtected(blk, word int) uint32 {
	var p uint32
	for _, r := range m.t.wr {
		if !bitOf(m.fuses, 0, r.bit) || r.blk != blk {
			continue
		}
		if blk > 0 {
			return 0xFFFFFFFF
		}
		for bit := max(r.lo, 32*word); bit < min(r.hi, 32*word+32); bit++ {
			p |= 1 << uint(bit-32*word)
		}
	}
	return p
}

func (m *Model) burnWord(blk, word int, v uint32) {
	v &^= m.wrProtected(blk, word)
	v &^= m.Stuck[[2]int{blk, word}]
	m.Reprogrammed += popcount(v & m.fuses[blk][word])
	m.fuses[blk][word] |= v
}

func popcount(v uint32) int {
	n := 0
	for ; v != 0; v &= v - 1 {
		n++
	}
	return n
}

func (m *Model) programRS(blk int) {
	if blk >= len(m.fuses) {
		return
	}
	// the words follow each other from PGM_DATA0 (BLOCK0 of the ESP32-S31
	// reaches PGM_CHECK_VALUE0)
	for w := range m.fuses[blk] {
		m.burnWord(blk, w, m.regs[m.l.pgmData+uint32(4*w)])
	}
	if blk > 0 && m.wrProtected(blk, 0) == 0 {
		for i := range m.parity[blk] {
			p := m.regs[m.l.pgmData+pgmCheckOff+uint32(4*i)]
			m.Reprogrammed += popcount(p & m.parity[blk][i])
			m.parity[blk][i] |= p
		}
	}
}

func (m *Model) programESP32() {
	for b, bl := range m.l.blocks {
		for w := range m.fuses[b] {
			m.burnWord(b, w, m.regs[bl.wr+uint32(4*w)])
		}
	}
}

// latch is the READ command: the read registers and error registers from
// the eFuses
func (m *Model) latch() {
	l := m.l
	clear(m.errs)
	for b := range m.rd {
		clear(m.rd[b])
	}
	if l.kind == kindESP32 {
		copy(m.rd[0], m.fuses[0])
		var dec uint32
		for b := 1; b < len(m.rd); b++ {
			var raw [8]uint32
			copy(raw[:], m.fuses[b])
			switch m.fuses[0][6] & 3 {
			case 1:
				data, bad := decode34(raw)
				copy(m.rd[b], data[:])
				for g, x := range bad {
					if x {
						dec |= 1 << uint(4*(b-1)+g)
					}
				}
			case 2:
				for i := 0; i < 4; i++ {
					m.rd[b][i] = raw[i] | raw[i+4]
				}
			default:
				copy(m.rd[b], raw[:])
			}
		}
		m.errs[l.decStatus] = dec
	} else {
		for b := range m.rd {
			copy(m.rd[b], m.fuses[b])
		}
		for _, a := range l.repeatErr {
			m.errs[a] = 0
		}
		rs := make([]uint32, len(l.rsErr))
		for b := 1; b < len(m.rd); b++ {
			if rsCheck(m.fuses[b], m.parity[b]) {
				continue
			}
			if l.rsErrKind == rsErrC3 {
				if i := b / 8; i < len(rs) {
					rs[i] |= 8 << (4 * uint(b%8))
				}
			} else if i := (b - 1) / 8; i < len(rs) {
				rs[i] |= 8 << (4 * uint((b-1)%8))
			}
		}
		for i, a := range l.rsErr {
			m.errs[a] = rs[i]
		}
	}
	// read protection: the whole block, or the half of the ESP32-C2's key
	for _, r := range m.t.rd {
		if r.blk == 0 || r.blk >= len(m.rd) || !bitOf(m.fuses, 0, r.bit) {
			continue
		}
		for bit := r.lo; bit < r.hi && bit/32 < len(m.rd[r.blk]); bit++ {
			m.rd[r.blk][bit/32] &^= 1 << (uint(bit) % 32)
		}
	}
}
