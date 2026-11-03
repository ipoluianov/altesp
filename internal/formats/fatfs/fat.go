package fatfs

// The FAT volume: the boot sector, the FATs, the directories and the
// files, read and written in memory. Based on the FAT reader of AltHex
// (core/fs/fat.go, same author, MIT), with writing added.

import (
	"encoding/binary"
	"errors"
	"fmt"
	iofs "io/fs"
	"slices"
	"strings"
	"time"
)

const (
	// maxDirEntries caps the entries of a directory: damaged volumes may
	// claim endless ones
	maxDirEntries = 1 << 16

	attrReadOnly = 0x01
	attrVolume   = 0x08
	attrDir      = 0x10
	attrArchive  = 0x20
	attrLFN      = 0x0F

	// FatFs' limits of the cluster counts (MAX_FAT12, MAX_FAT16): it
	// takes 4085 clusters for FAT12, where the Microsoft specification
	// says FAT16. The chip runs FatFs; Format never makes those counts.
	maxFAT12 = 0xFF5
	maxFAT16 = 0xFFF5
)

var (
	errChain = errors.New("damaged cluster chain")
	errDir   = errors.New("damaged directory")
)

// vol is a FAT volume in memory
type vol struct {
	b        []byte
	bps      int // bytes per sector
	spc      int // sectors per cluster
	rsv      int
	nFATs    int
	fatSz    int // sectors of a FAT
	rootEnt  int
	tot      int // sectors
	bits     int // 12, 16 or 32
	clusSz   int
	rootOff  int // the fixed root directory of FAT12/16
	rootLen  int
	rootClus uint32 // the first cluster of the root of FAT32
	dataOff  int
	nClus    int // the clusters are 2..nClus+1
	label    string
}

// openVol checks the boot sector and its sizes; the type comes from the
// count of clusters
func openVol(b []byte) (*vol, error) {
	bad := func(why string) (*vol, error) { return nil, fmt.Errorf("fatfs: no FAT volume: %s", why) }
	if len(b) < 512 {
		return bad("too small")
	}
	if b[0] != 0xEB && b[0] != 0xE9 {
		return bad("no jump instruction")
	}
	le16 := func(o int) int { return int(binary.LittleEndian.Uint16(b[o:])) }
	le32 := func(o int) int { return int(binary.LittleEndian.Uint32(b[o:])) }
	v := &vol{b: b, bps: le16(11), spc: int(b[13]), rsv: le16(14), nFATs: int(b[16]), rootEnt: le16(17)}
	v.tot = le16(19)
	if v.tot == 0 {
		v.tot = le32(32)
	}
	v.fatSz = le16(22)
	if v.fatSz == 0 {
		v.fatSz = le32(36)
	}
	media := b[21]
	switch {
	case v.bps != 512 && v.bps != 1024 && v.bps != 2048 && v.bps != 4096:
		return bad(fmt.Sprintf("%d bytes per sector", v.bps))
	case v.spc == 0 || v.spc&(v.spc-1) != 0 || v.rsv == 0 || v.nFATs == 0 || v.nFATs > 4:
		return bad("bad BPB")
	case media != 0xF0 && media < 0xF8:
		return bad("bad media byte")
	case v.tot == 0 || v.fatSz == 0:
		return bad("no sectors")
	}
	if v.tot > len(b)/v.bps {
		return nil, fmt.Errorf("fatfs: the volume has %d sectors of %d bytes, the image only %d bytes", v.tot, v.bps, len(b))
	}
	rootSecs := (v.rootEnt*32 + v.bps - 1) / v.bps
	meta := v.rsv + v.nFATs*v.fatSz + rootSecs
	if meta >= v.tot {
		return bad("no data area")
	}
	n := (v.tot - meta) / v.spc
	switch {
	case n <= maxFAT12:
		v.bits = 12
	case n <= maxFAT16:
		v.bits = 16
	default:
		v.bits = 32
	}
	// The FAT holds 2 more entries than there are clusters; a count that
	// does not fit is cut to what the FAT can say
	n = min(n, v.fatSz*v.bps*8/v.bits-2, 0x0FFFFFF5)
	if n < 1 {
		return bad("no clusters")
	}
	v.nClus = n
	v.clusSz = v.bps * v.spc
	v.dataOff = meta * v.bps
	if v.bits == 32 {
		if v.rootEnt != 0 || le16(22) != 0 {
			return bad("bad FAT32 BPB")
		}
		v.rootClus = uint32(le32(44))
		if b[66] == 0x29 {
			v.label = fatLabel(b[71:82])
		}
	} else {
		if v.rootEnt == 0 {
			return bad("no root directory")
		}
		v.rootOff = (v.rsv + v.nFATs*v.fatSz) * v.bps
		v.rootLen = v.rootEnt * 32
		if b[38] == 0x29 {
			v.label = fatLabel(b[43:54])
		}
	}
	// The label of the root directory wins over the copy in the BPB
	if d, err := v.openDir(v.rootClus); err == nil {
		for _, e := range d.entries {
			if e.attr&attrVolume != 0 && e.attr&attrDir == 0 {
				v.label = fatLabel(d.buf[e.slot : e.slot+11])
				break
			}
		}
	}
	return v, nil
}

// fatLabel trims a label padded with spaces; "NO NAME" is none
func fatLabel(b []byte) string {
	s := strings.TrimRight(oem(b), " ")
	if s == "NO NAME" {
		return ""
	}
	return s
}

// ---- The FAT ----

func (v *vol) fatOff(k int) int { return (v.rsv + k*v.fatSz) * v.bps }

func (v *vol) eoc() uint32 {
	switch v.bits {
	case 12:
		return 0xFFF
	case 16:
		return 0xFFFF
	}
	return 0x0FFFFFFF
}

func (v *vol) isEOC(x uint32) bool {
	switch v.bits {
	case 12:
		return x >= 0xFF8
	case 16:
		return x >= 0xFFF8
	}
	return x >= 0x0FFFFFF8
}

// get gives the FAT entry of cluster c from the first FAT
func (v *vol) get(c uint32) uint32 {
	t := v.b[v.fatOff(0):]
	switch v.bits {
	case 12:
		w := binary.LittleEndian.Uint16(t[c+c/2:])
		if c&1 != 0 {
			return uint32(w >> 4)
		}
		return uint32(w & 0xFFF)
	case 16:
		return uint32(binary.LittleEndian.Uint16(t[c*2:]))
	}
	return binary.LittleEndian.Uint32(t[c*4:]) & 0x0FFFFFFF
}

// set writes the FAT entry of cluster c into every FAT
func (v *vol) set(c, x uint32) {
	for k := 0; k < v.nFATs; k++ {
		t := v.b[v.fatOff(k):]
		switch v.bits {
		case 12:
			o := c + c/2
			w := binary.LittleEndian.Uint16(t[o:])
			if c&1 != 0 {
				w = w&0x000F | uint16(x)<<4
			} else {
				w = w&0xF000 | uint16(x)&0xFFF
			}
			binary.LittleEndian.PutUint16(t[o:], w)
		case 16:
			binary.LittleEndian.PutUint16(t[c*2:], uint16(x))
		default:
			w := binary.LittleEndian.Uint32(t[c*4:])
			binary.LittleEndian.PutUint32(t[c*4:], w&0xF0000000|x&0x0FFFFFFF)
		}
	}
}

// chain follows the FAT from first to the end mark; a cluster out of the
// volume or seen twice is an error
func (v *vol) chain(first uint32) ([]uint32, error) {
	var out []uint32
	seen := map[uint32]bool{}
	for c := first; ; {
		if c < 2 || int(c) > v.nClus+1 || seen[c] {
			return nil, fmt.Errorf("%w: cluster %d", errChain, c)
		}
		seen[c] = true
		out = append(out, c)
		n := v.get(c)
		if v.isEOC(n) {
			return out, nil
		}
		c = n
	}
}

// alloc takes n free clusters, links them and ends the chain
func (v *vol) alloc(n int) ([]uint32, error) {
	var out []uint32
	for c := uint32(2); len(out) < n && int(c) <= v.nClus+1; c++ {
		if v.get(c) == 0 {
			out = append(out, c)
		}
	}
	if len(out) < n {
		return nil, ErrNoSpace
	}
	for i, c := range out {
		if i+1 < len(out) {
			v.set(c, out[i+1])
		} else {
			v.set(c, v.eoc())
		}
	}
	return out, nil
}

// free releases a chain; a damaged one is released up to the damage
func (v *vol) free(first uint32) {
	seen := map[uint32]bool{}
	for c := first; c >= 2 && int(c) <= v.nClus+1 && !seen[c]; {
		seen[c] = true
		n := v.get(c)
		v.set(c, 0)
		if v.isEOC(n) {
			break
		}
		c = n
	}
}

func (v *vol) freeClusters() int {
	n := 0
	for c := uint32(2); int(c) <= v.nClus+1; c++ {
		if v.get(c) == 0 {
			n++
		}
	}
	return n
}

func (v *vol) clus(c uint32) []byte {
	o := v.dataOff + int(c-2)*v.clusSz
	return v.b[o : o+v.clusSz]
}

// ---- Directories ----

// dirent is an entry of a directory
type dirent struct {
	name  string
	attr  byte
	clus  uint32
	size  uint32
	slot  int // the offset of the 8.3 entry in the directory
	first int // the offset of its first long name entry; slot without one
}

func (e *dirent) isDir() bool { return e.attr&attrDir != 0 }

// dir is a directory read into memory; flush writes it back
type dir struct {
	clus    uint32   // 0: the fixed root of FAT12/16
	chain   []uint32 // its clusters
	fat32   bool
	buf     []byte
	entries []dirent // files and directories, the volume label too
}

func (v *vol) openDir(clus uint32) (*dir, error) {
	d := &dir{clus: clus, fat32: v.bits == 32}
	if clus == 0 {
		if v.bits == 32 {
			return nil, errDir
		}
		d.buf = append([]byte(nil), v.b[v.rootOff:v.rootOff+v.rootLen]...)
	} else {
		ch, err := v.chain(clus)
		if err != nil {
			return nil, err
		}
		if len(ch)*v.clusSz/32 > maxDirEntries {
			return nil, fmt.Errorf("%w: too long", errDir)
		}
		d.chain = ch
		for _, c := range ch {
			d.buf = append(d.buf, v.clus(c)...)
		}
	}
	d.entries = parseDir(d.buf, d.fat32)
	return d, nil
}

func (v *vol) flush(d *dir) {
	if d.clus == 0 {
		copy(v.b[v.rootOff:], d.buf)
		return
	}
	for i, c := range d.chain {
		copy(v.clus(c), d.buf[i*v.clusSz:])
	}
}

// parseDir parses the 32-byte entries of a directory. Deleted entries,
// "." and ".." are skipped; a broken long name gives the 8.3 one. The
// high half of the cluster is FAT32's only.
func parseDir(b []byte, fat32 bool) []dirent {
	var list []dirent
	var lfn []uint16 // the long name being collected
	var lfnSum byte
	first := 0
	need := -1 // the ordinal of the long name entry expected next; 0 - complete
	for i := 0; i+32 <= len(b); i += 32 {
		d := b[i : i+32]
		if d[0] == 0 {
			break
		}
		if d[0] == 0xE5 {
			need = -1
			continue
		}
		attr := d[11]
		if attr&0x3F == attrLFN {
			ord := int(d[0] & 0x1F)
			if d[0]&0x40 != 0 {
				if ord == 0 || ord > 20 {
					need = -1
					continue
				}
				lfn = make([]uint16, ord*13)
				lfnSum = d[13]
				need = ord
				first = i
			}
			if need <= 0 || ord != need || d[13] != lfnSum {
				need = -1
				continue
			}
			p := lfn[(ord-1)*13:]
			for k, o := range lfnOffsets {
				p[k] = binary.LittleEndian.Uint16(d[o:])
			}
			need--
			continue
		}
		long := need == 0 && checksum(d[:11]) == lfnSum
		need = -1
		e := dirent{attr: attr, slot: i, first: i}
		e.clus = uint32(binary.LittleEndian.Uint16(d[26:]))
		if fat32 {
			e.clus |= uint32(binary.LittleEndian.Uint16(d[20:])) << 16
		}
		e.size = binary.LittleEndian.Uint32(d[28:])
		e.name = shortName(d)
		if long {
			if s := longName(lfn); s != "" {
				e.name = s
				e.first = first
			}
		}
		if e.name == "." || e.name == ".." || e.name == "" {
			continue
		}
		list = append(list, e)
	}
	return list
}

// lfnOffsets are where the 13 characters of a long name entry are
var lfnOffsets = [13]int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}

// find gives the file or directory of a name, the case ignored
func (d *dir) find(name string) *dirent {
	for i := range d.entries {
		e := &d.entries[i]
		if e.attr&attrVolume == 0 && strings.EqualFold(e.name, name) {
			return e
		}
	}
	return nil
}

// remove marks the entries of e deleted
func (d *dir) remove(e *dirent) {
	for o := e.first; o <= e.slot; o += 32 {
		d.buf[o] = 0xE5
	}
	d.entries = parseDir(d.buf, d.fat32)
}

// add writes the entries of a new name; sfn is the 8.3 entry with its
// name set by add. A directory of clusters grows by a cluster when it has
// no room; the fixed root does not.
func (v *vol) add(d *dir, name string, sfn []byte) error {
	short, lfn, nt, err := makeShortName(name, func(s []byte) bool {
		for _, e := range d.entries {
			if string(d.buf[e.slot:e.slot+11]) == string(s) {
				return true
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	copy(sfn[:11], short)
	sfn[12] = nt
	var u []uint16
	n := 1
	if lfn {
		u = utf16Name(name)
		n += (len(u) + 12) / 13
	}
	at := -1
	for run, i := 0, 0; i+32 <= len(d.buf); i += 32 {
		if c := d.buf[i]; c == 0 || c == 0xE5 {
			run++
			if run == n {
				at = i - (n-1)*32
				break
			}
		} else {
			run = 0
		}
	}
	if at < 0 {
		if d.clus == 0 || len(d.buf)/32+v.clusSz/32 > maxDirEntries {
			return fmt.Errorf("%w: the directory is full", ErrNoSpace)
		}
		c, err := v.alloc(1)
		if err != nil {
			return err
		}
		v.set(d.chain[len(d.chain)-1], c[0])
		d.chain = append(d.chain, c[0])
		d.buf = append(d.buf, make([]byte, v.clusSz)...)
		return v.add(d, name, sfn)
	}
	sum := checksum(short)
	for k := n - 1; k >= 1; k-- {
		e := d.buf[at+(n-1-k)*32:][:32]
		clear(e)
		e[0] = byte(k)
		if k == n-1 {
			e[0] |= 0x40
		}
		e[11] = attrLFN
		e[13] = sum
		for j, o := range lfnOffsets {
			c := uint16(0xFFFF)
			switch p := (k-1)*13 + j; {
			case p < len(u):
				c = u[p]
			case p == len(u):
				c = 0
			}
			binary.LittleEndian.PutUint16(e[o:], c)
		}
	}
	copy(d.buf[at+(n-1)*32:], sfn[:32])
	d.entries = parseDir(d.buf, d.fat32)
	return nil
}

// newEntry makes the 8.3 entry of a file or a directory, its name left
// for add
func newEntry(attr byte, clus, size uint32, t time.Time) []byte {
	e := make([]byte, 32)
	e[11] = attr
	date, tm := dosTime(t)
	binary.LittleEndian.PutUint16(e[14:], tm)
	binary.LittleEndian.PutUint16(e[16:], date)
	binary.LittleEndian.PutUint16(e[18:], date)
	binary.LittleEndian.PutUint16(e[22:], tm)
	binary.LittleEndian.PutUint16(e[24:], date)
	setClus(e, clus)
	binary.LittleEndian.PutUint32(e[28:], size)
	return e
}

func setClus(e []byte, c uint32) {
	binary.LittleEndian.PutUint16(e[20:], uint16(c>>16))
	binary.LittleEndian.PutUint16(e[26:], uint16(c))
}

// dosTime encodes a time as FAT keeps it: local time, no zone, from 1980
func dosTime(t time.Time) (date, tm uint16) {
	if t.Year() < 1980 {
		return 0x21, 0 // 1980-01-01
	}
	if t.Year() > 2107 {
		t = time.Date(2107, 12, 31, 23, 59, 58, 0, time.Local)
	}
	date = uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day())
	tm = uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)
	return date, tm
}

// ---- Paths ----

// split splits a "/" separated path into names; "." and empty parts are
// dropped
func split(p string) ([]string, error) {
	var out []string
	for _, s := range strings.Split(p, "/") {
		switch s {
		case "", ".":
		case "..":
			return nil, iofs.ErrInvalid
		default:
			out = append(out, s)
		}
	}
	return out, nil
}

// lookup finds the directory a path is in and its entry there; the root
// has no entry (e is nil)
func (v *vol) lookup(p string) (parent *dir, e *dirent, err error) {
	names, err := split(p)
	if err != nil {
		return nil, nil, err
	}
	d, err := v.openDir(v.rootClus)
	if err != nil {
		return nil, nil, err
	}
	for i, name := range names {
		e = d.find(name)
		if e == nil {
			return nil, nil, iofs.ErrNotExist
		}
		if i == len(names)-1 {
			return d, e, nil
		}
		if !e.isDir() {
			return nil, nil, errNotDir
		}
		if d, err = v.openDir(e.clus); err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, nil
}

var errNotDir = errors.New("not a directory")

// openPath opens the directory of a path
func (v *vol) openPath(p string) (*dir, error) {
	_, e, err := v.lookup(p)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return v.openDir(v.rootClus)
	}
	if !e.isDir() {
		return nil, errNotDir
	}
	return v.openDir(e.clus)
}

// parentOf splits a path into its directory and its last name
func parentOf(p string) (string, string, error) {
	names, err := split(p)
	if err != nil {
		return "", "", err
	}
	if len(names) == 0 {
		return "", "", iofs.ErrInvalid
	}
	return strings.Join(names[:len(names)-1], "/"), names[len(names)-1], nil
}

// ---- Files ----

func (v *vol) readFile(e *dirent) ([]byte, error) {
	if e.size == 0 {
		return []byte{}, nil
	}
	ch, err := v.chain(e.clus)
	if err != nil {
		return nil, err
	}
	need := (int(e.size) + v.clusSz - 1) / v.clusSz
	if len(ch) < need {
		return nil, fmt.Errorf("%w: %d clusters of %d", errChain, len(ch), need)
	}
	out := make([]byte, 0, e.size)
	for _, c := range ch[:need] {
		out = append(out, v.clus(c)...)
	}
	return out[:e.size], nil
}

// writeData takes clusters for data and writes it; the rest of the last
// cluster is zeros. An empty file has no cluster (0).
func (v *vol) writeData(data []byte) (uint32, error) {
	if len(data) == 0 {
		return 0, nil
	}
	ch, err := v.alloc((len(data) + v.clusSz - 1) / v.clusSz)
	if err != nil {
		return 0, err
	}
	for i, c := range ch {
		b := v.clus(c)
		n := copy(b, data[i*v.clusSz:])
		clear(b[n:])
	}
	return ch[0], nil
}

// removeTree frees a file, or a directory with everything in it. A
// damaged volume may have a directory inside itself: seen stops that.
func (v *vol) removeTree(e *dirent, seen map[uint32]bool) error {
	if e.isDir() && e.clus != 0 {
		if seen[e.clus] {
			return fmt.Errorf("%w: a loop of directories", errDir)
		}
		seen[e.clus] = true
		d, err := v.openDir(e.clus)
		if err != nil {
			return err
		}
		for i := range d.entries {
			if c := &d.entries[i]; c.attr&attrVolume == 0 {
				if err := v.removeTree(c, seen); err != nil {
					return err
				}
			}
		}
	}
	if e.clus != 0 {
		v.free(e.clus)
	}
	return nil
}

// isAncestor tells whether directory a is b or holds it
func isAncestor(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	return slices.EqualFunc(a, b[:len(a)], strings.EqualFold)
}
