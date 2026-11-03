package littlefs

// Metadata pairs (SPEC.md "Directories / Metadata pairs", "Metadata
// tags"). A pair is two blocks; each block is a revision count and a log
// of commits. A commit is a run of tags, every tag stored big-endian and
// XORed with the previous one, ending with a CRC tag. The newest valid
// commit wins, and of the two blocks the one with the newer revision.

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"slices"
)

const blockNull = 0xFFFFFFFF

// Tag types (type3, 11 bits)
const (
	typeName      = 0x000 // 0x0xx: the chunk is the file type
	typeReg       = 0x01
	typeDir       = 0x02
	typeSuper     = 0xFF
	tagCreate     = 0x401
	tagDelete     = 0x4FF
	tagStruct     = 0x200 // 0x2xx
	tagDirStruct  = 0x200
	tagInline     = 0x201
	tagCTZ        = 0x202
	tagUserAttr   = 0x300 // 0x3xx: the chunk is the attribute type
	tagCRC        = 0x500 // 0x5xx: the low bit of the chunk is the valid state
	tagFCRC       = 0x5FF
	tagTail       = 0x600
	tagSoftTail   = 0x600
	tagHardTail   = 0x601
	tagGState     = 0x700
	tagMoveState  = 0x7FF
	idNone        = 0x3FF
	lenDeleted    = 0x3FF
	maxTagData    = 0x3FE
	gstateSize    = 12
	superDataSize = 24
)

func mkTag(typ uint16, id uint16, size int) uint32 {
	return uint32(typ)<<20 | uint32(id&0x3FF)<<10 | uint32(size&0x3FF)
}

func tagType(t uint32) uint16 { return uint16(t>>20) & 0x7FF }
func tagID(t uint32) uint16   { return uint16(t>>10) & 0x3FF }
func tagSize(t uint32) uint32 { return t & 0x3FF }

// tagDataSize is the bytes of data after the tag: a deleted tag has none
func tagDataSize(t uint32) int {
	if tagSize(t) == lenDeleted {
		return 0
	}
	return int(tagSize(t))
}

// crc is the CRC-32 of littlefs: polynomial 0x04C11DB7 (reflected, as
// IEEE), started with 0xFFFFFFFF and not inverted at the end
func crc(c uint32, p []byte) uint32 {
	return ^crc32.Update(^c, crc32.IEEETable, p)
}

func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

// pair is a pointer to a metadata pair. {a,b} and {b,a} are the same pair
type pair [2]uint32

func (p pair) key() pair {
	if p[0] > p[1] {
		return pair{p[1], p[0]}
	}
	return p
}

func (p pair) same(q pair) bool { return p.key() == q.key() }

func (p pair) shares(q pair) bool {
	return p[0] == q[0] || p[0] == q[1] || p[1] == q[0] || p[1] == q[1]
}

func (p pair) bytes() []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, p[0])
	binary.LittleEndian.PutUint32(b[4:], p[1])
	return b
}

func pairFrom(b []byte) pair { return pair{le32(b), le32(b[4:])} }

// entry is one id of a metadata pair: a file, a directory or the
// superblock
type entry struct {
	typ   uint8 // chunk of the name tag: typeReg, typeDir, typeSuper
	name  string
	stype uint16 // type3 of the struct tag, 0 when there is none
	sdata []byte
	attrs []attr // sorted by type
}

type attr struct {
	typ  uint8
	data []byte
}

func (e *entry) size() int64 {
	switch e.stype {
	case tagInline:
		return int64(len(e.sdata))
	case tagCTZ:
		if len(e.sdata) >= 8 {
			return int64(le32(e.sdata[4:]))
		}
	}
	return 0
}

// mdir is the state of a metadata pair after replaying its commits
type mdir struct {
	pair     pair
	rev      uint32
	active   int // index in pair of the block with the newest commits
	entries  []*entry
	tail     pair
	tailType uint16 // 0 (no tail), tagSoftTail or tagHardTail
	gdelta   [gstateSize]byte
	dirty    bool
}

func (d *mdir) hasSuper() bool {
	return len(d.entries) > 0 && d.entries[0].typ == typeSuper && d.entries[0].name == "littlefs"
}

// at returns the entry of an id, adding empty ones up to it
func (d *mdir) at(id uint16) *entry {
	for int(id) >= len(d.entries) {
		d.entries = append(d.entries, &entry{})
	}
	return d.entries[id]
}

// apply replays one tag of a valid commit
func (d *mdir) apply(t uint32, data []byte) {
	typ := tagType(t)
	id := tagID(t)
	data = slices.Clone(data)
	deleted := tagSize(t) == lenDeleted
	switch {
	case typ&0x700 == tagTail:
		if len(data) >= 8 && !deleted {
			d.tail = pairFrom(data)
			d.tailType = typ
		}
		return
	case typ&0x700 == tagGState:
		for i := 0; i < len(data) && i < gstateSize; i++ {
			d.gdelta[i] ^= data[i]
		}
		return
	}
	if id == idNone {
		return
	}
	switch {
	case typ == tagCreate:
		if int(id) > len(d.entries) {
			d.at(id - 1)
		}
		d.entries = slices.Insert(d.entries, int(id), &entry{})
	case typ == tagDelete:
		if int(id) < len(d.entries) {
			d.entries = slices.Delete(d.entries, int(id), int(id)+1)
		}
	case typ&0x700 == typeName:
		e := d.at(id)
		e.typ = uint8(typ)
		e.name = string(data)
	case typ&0x700 == tagStruct:
		e := d.at(id)
		if deleted {
			e.stype, e.sdata = 0, nil
		} else {
			e.stype, e.sdata = typ, data
		}
	case typ&0x700 == tagUserAttr:
		e := d.at(id)
		e.setAttr(uint8(typ), data, deleted)
	}
}

func (e *entry) setAttr(typ uint8, data []byte, deleted bool) {
	i, found := slices.BinarySearchFunc(e.attrs, typ, func(a attr, t uint8) int { return int(a.typ) - int(t) })
	switch {
	case deleted && found:
		e.attrs = slices.Delete(e.attrs, i, i+1)
	case deleted:
	case found:
		e.attrs[i].data = data
	default:
		e.attrs = slices.Insert(e.attrs, i, attr{typ, data})
	}
}

// parseBlock replays the commits of one metadata block. ok is false when
// the block holds no valid commit
func parseBlock(b []byte) (d *mdir, ok bool) {
	if len(b) < 4 {
		return nil, false
	}
	d = &mdir{rev: le32(b)}
	type rawTag struct {
		t    uint32
		data []byte
	}
	var pend []rawTag
	ptag := uint32(0xFFFFFFFF)
	sum := crc(0xFFFFFFFF, b[:4])
	off := 4
	for off+4 <= len(b) {
		t := binary.BigEndian.Uint32(b[off:]) ^ ptag
		if t>>31 != 0 {
			// The valid bit: the end of the log
			break
		}
		end := off + 4 + tagDataSize(t)
		if end > len(b) {
			break
		}
		typ := tagType(t)
		if typ&0x700 == tagCRC && typ != tagFCRC {
			if end-off < 8 {
				break
			}
			sum = crc(sum, b[off:off+4])
			if sum != le32(b[off+4:]) {
				// A torn commit: it and everything after it are ignored
				break
			}
			for _, p := range pend {
				d.apply(p.t, p.data)
			}
			pend = pend[:0]
			ok = true
			// The chunk's low bit flips the expected valid bit of the
			// next commit
			ptag = t ^ uint32(typ&1)<<31
			sum = 0xFFFFFFFF
			off = end
			continue
		}
		sum = crc(sum, b[off:end])
		pend = append(pend, rawTag{t, b[off+4 : end]})
		ptag = t
		off = end
	}
	return d, ok
}

var errNoRoom = errors.New("littlefs: metadata does not fit in a block")

// commit builds the single commit of a compacted metadata block
type commit struct {
	buf  []byte
	ptag uint32
	err  error
}

func newCommit(rev uint32) *commit {
	c := &commit{ptag: 0xFFFFFFFF}
	c.buf = binary.LittleEndian.AppendUint32(c.buf, rev)
	return c
}

func (c *commit) add(typ uint16, id uint16, data []byte) {
	if len(data) > maxTagData {
		c.err = errNoRoom
		return
	}
	t := mkTag(typ, id, len(data))
	c.buf = binary.BigEndian.AppendUint32(c.buf, t^c.ptag)
	c.buf = append(c.buf, data...)
	c.ptag = t
}

// finish ends the commit with an optional FCRC and the CRC tag, padded to
// prog, and returns it. erased is the value of the bytes after the commit.
// The commit must fit in limit bytes
func (c *commit) finish(limit, blockSize, prog int, fcrc bool, erased byte) ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	end := alignUp(len(c.buf)+8, prog)
	if fcrc {
		// The FCRC tells a 2.1 reader the next prog bytes are erased, so
		// it may append there; a commit at the end of the block needs none
		e := alignUp(len(c.buf)+12+8, prog)
		if e+prog <= blockSize {
			ff := make([]byte, prog)
			for i := range ff {
				ff[i] = erased
			}
			data := binary.LittleEndian.AppendUint32(nil, uint32(prog))
			data = binary.LittleEndian.AppendUint32(data, crc(0xFFFFFFFF, ff))
			c.add(tagFCRC, idNone, data)
			end = e
		}
	}
	if end > limit || end > blockSize {
		return nil, errNoRoom
	}
	size := end - len(c.buf) - 4
	if size > maxTagData {
		return nil, errNoRoom
	}
	// The valid state makes an erased word after the commit read as an
	// invalid tag
	var valid uint16
	if end < blockSize && erased&0x80 == 0 {
		valid = 1
	}
	t := mkTag(tagCRC|valid, idNone, size)
	c.buf = binary.BigEndian.AppendUint32(c.buf, t^c.ptag)
	c.buf = binary.LittleEndian.AppendUint32(c.buf, crc(0xFFFFFFFF, c.buf))
	for len(c.buf) < end {
		c.buf = append(c.buf, erased)
	}
	return c.buf, nil
}

func alignUp(n, a int) int { return (n + a - 1) / a * a }

// encode compacts a metadata pair into one commit
func encode(d *mdir, rev uint32, limit, blockSize, prog int, fcrc bool) ([]byte, error) {
	c := newCommit(rev)
	for i, e := range d.entries {
		id := uint16(i)
		c.add(typeName|uint16(e.typ), id, []byte(e.name))
		if e.stype != 0 {
			c.add(e.stype, id, e.sdata)
		}
		for _, a := range e.attrs {
			c.add(tagUserAttr|uint16(a.typ), id, a.data)
		}
	}
	if d.tailType != 0 {
		c.add(d.tailType, idNone, d.tail.bytes())
	}
	if d.gdelta != [gstateSize]byte{} {
		c.add(tagMoveState, idNone, d.gdelta[:])
	}
	return c.finish(limit, blockSize, prog, fcrc, 0xFF)
}

// entrySize is the bytes an entry takes in a compacted commit
func entrySize(e *entry) int {
	n := 4 + len(e.name)
	if e.stype != 0 {
		n += 4 + len(e.sdata)
	}
	for _, a := range e.attrs {
		n += 4 + len(a.data)
	}
	return n
}

func entriesSize(list []*entry) int {
	n := 0
	for _, e := range list {
		n += entrySize(e)
	}
	return n
}

// overhead is the most a compacted commit takes besides its entries
func overhead(prog int) int {
	// revision, tail, move state, FCRC, CRC, padding
	return 4 + 12 + 16 + 12 + 8 + prog
}
