// Package spiffs reads and writes SPIFFS images as ESP-IDF and the Arduino
// cores build them: lists and reads the files, creates, replaces, renames
// and deletes them, formats an image and builds one from a folder.
//
// SPIFFS keeps no superblock: the page and block sizes and the lengths of
// the object header are those the firmware was built with. Detect tries
// the common sets and checks the block magic and the object lookup pages;
// other sets are given to Mount by hand.
//
// SPIFFS has no directories: a name is a whole path ("/www/index.html").
// ReadDir shows the parts before a "/" as directories.
//
// Sources: the SPIFFS technical specification (docs/TECH_SPEC) and the
// layout in spiffs_nucleus.h of pellepl/spiffs (MIT), implemented here from
// the layout, not ported; the parameters of ESP-IDF (CONFIG_SPIFFS_*,
// components/spiffs) and of the mkspiffs builds for Arduino (mkspiffs
// --version prints them).
package spiffs

import (
	"encoding/binary"
	"errors"
	"fmt"
	iofs "io/fs"
	"sort"
	"strings"
)

// Params are the build-time settings of SPIFFS; the image does not keep
// them
type Params struct {
	PageSize   int // logical page: 256 on ESP-IDF and Arduino
	BlockSize  int // logical block: 4096 on ESP-IDF and Arduino-ESP32
	ObjNameLen int // SPIFFS_OBJ_NAME_LEN, the terminating zero included: 32
	MetaLen    int // SPIFFS_OBJ_META_LEN: 4 on ESP-IDF and Arduino-ESP32, 0 on ESP8266
	// UseMagic: every block keeps a magic word in its lookup pages
	UseMagic bool
	// UseMagicLen: the magic depends on the block count too
	UseMagicLen bool
	// AlignedIndex is SPIFFS_ALIGNED_OBJECT_INDEX_TABLES (the ESP8266
	// Arduino core): the page indexes of the index header start at an even
	// offset
	AlignedIndex bool
}

var (
	// IDF is ESP-IDF's default configuration (CONFIG_SPIFFS_PAGE_SIZE 256,
	// OBJ_NAME_LEN 32, META_LENGTH 4, USE_MAGIC, USE_MAGIC_LENGTH) with
	// the 4 KB flash sector as the block. Arduino-ESP32 and its mkspiffs
	// build are the same.
	IDF = Params{PageSize: 256, BlockSize: 4096, ObjNameLen: 32, MetaLen: 4, UseMagic: true, UseMagicLen: true}
	// ESP8266 is the Arduino ESP8266 core (mkspiffs build arduino-esp8266):
	// no metadata, no magic length, aligned index tables; the block is
	// 8 KB on most of its flash layouts
	ESP8266 = Params{PageSize: 256, BlockSize: 8192, ObjNameLen: 32, MetaLen: 0, UseMagic: true, AlignedIndex: true}
)

// Info is a file or a directory
type Info struct {
	Name string
	Dir  bool
	Size int64
}

var (
	ErrNoSpace = errors.New("no free space")
	ErrName    = errors.New("bad name")
	ErrDamaged = errors.New("damaged file system")
)

// The page header: object id (2), span index (2), flags (1). A flag is set
// when its bit is cleared: flash writes only clear bits.
const (
	flagUsed  = 1 << 0
	flagFinal = 1 << 1
	flagIndex = 1 << 2
	flagIxDel = 1 << 6 // an index page being deleted
	flagDel   = 1 << 7

	phLen = 5
	// ixPhLen is the header of an index page past the first: the page
	// header aligned to 4
	ixPhLen = 8

	idFree    = 0xFFFF
	idDeleted = 0x0000
	idIndex   = 0x8000 // the index pages of an object have this bit

	typeFile     = 1
	undefinedLen = 0xFFFFFFFF
	magicBase    = 0x20140529
)

// geom is the layout that follows from Params and the size of the image
type geom struct {
	Params
	blocks    int
	ppb       int // pages per block
	lu        int // object lookup pages at the start of every block
	luEntries int // data pages of a block: one lookup entry each
	hdrLen    int // the object index header
	hdrIx     int // page indexes in the index header
	ixIx      int // page indexes in the other index pages
	data      int // data bytes of a page
}

func (p Params) geom(size int) (geom, error) {
	g := geom{Params: p}
	if p.PageSize < 64 || p.PageSize&(p.PageSize-1) != 0 || p.PageSize > 65536 {
		return g, fmt.Errorf("spiffs: bad page size %d", p.PageSize)
	}
	if p.BlockSize < 2*p.PageSize || p.BlockSize%p.PageSize != 0 {
		return g, fmt.Errorf("spiffs: bad block size %d", p.BlockSize)
	}
	if p.ObjNameLen < 2 || p.MetaLen < 0 {
		return g, fmt.Errorf("spiffs: bad object name or metadata length")
	}
	g.blocks = size / p.BlockSize
	g.ppb = p.BlockSize / p.PageSize
	if g.blocks < 1 || g.blocks*g.ppb > 0xFFFF {
		return g, fmt.Errorf("spiffs: %d bytes is not a SPIFFS size for blocks of %d", size, p.BlockSize)
	}
	g.lu = max(1, g.ppb*2/p.PageSize)
	g.luEntries = g.ppb - g.lu
	// The erase count, and the magic before it, take the last ids of the
	// lookup pages; SPIFFS refuses a geometry where entries need them
	// (SPIFFS_CHECK_MAGIC_POSSIBLE)
	reserved := 2
	if p.UseMagic {
		reserved = 4
	}
	if g.luEntries*2+reserved > g.lu*p.PageSize {
		return g, fmt.Errorf("spiffs: no room for the magic with pages of %d in blocks of %d", p.PageSize, p.BlockSize)
	}
	g.hdrLen = ixPhLen + 4 + 1 + p.ObjNameLen + p.MetaLen
	if p.AlignedIndex {
		g.hdrLen = (g.hdrLen + 1) &^ 1
	}
	g.hdrIx = (p.PageSize - g.hdrLen) / 2
	g.ixIx = (p.PageSize - ixPhLen) / 2
	g.data = p.PageSize - phLen
	if g.hdrIx < 1 {
		return g, fmt.Errorf("spiffs: the object header does not fit a page")
	}
	return g, nil
}

// pages is the count of pages of the image
func (g *geom) pages() int { return g.blocks * g.ppb }

// isData tells a page that has a lookup entry
func (g *geom) isData(p int) bool { return p >= 0 && p < g.pages() && p%g.ppb >= g.lu }

// luOff is where the lookup entry of a data page is
func (g *geom) luOff(p int) int {
	return p/g.ppb*g.BlockSize + (p%g.ppb-g.lu)*2
}

// magicOff and eraseOff are the magic and the erase count of a block
func (g *geom) magicOff(b int) int { return b*g.BlockSize + g.lu*g.PageSize - 4 }
func (g *geom) eraseOff(b int) int { return b*g.BlockSize + g.lu*g.PageSize - 2 }

// magic of block b (SPIFFS_MAGIC)
func (g *geom) magic(b int) uint16 {
	m := uint32(magicBase) ^ uint32(g.PageSize)
	if g.UseMagicLen {
		m ^= uint32(g.blocks - b)
	}
	return uint16(m)
}

// FS is a SPIFFS image in memory
type FS struct {
	g   geom
	img []byte
}

// Mount opens an image with the parameters; the image is copied. With the
// magic on, an image with no block of the right magic is refused.
func Mount(img []byte, p Params) (*FS, error) {
	g, err := p.geom(len(img))
	if err != nil {
		return nil, err
	}
	f := &FS{g: g, img: append([]byte(nil), img...)}
	if p.UseMagic && f.magicBlocks() == 0 {
		return nil, fmt.Errorf("spiffs: not formatted with these parameters (no block magic)")
	}
	return f, nil
}

// magicBlocks counts the blocks with the right magic
func (f *FS) magicBlocks() int {
	n := 0
	for b := 0; b < f.g.blocks; b++ {
		if f.u16(f.g.magicOff(b)) == f.g.magic(b) {
			n++
		}
	}
	return n
}

// Params gives the parameters the image was opened with
func (f *FS) Params() Params { return f.g.Params }

// Image gives the bytes of the image
func (f *FS) Image() []byte { return f.img }

func (f *FS) u16(off int) uint16 { return binary.LittleEndian.Uint16(f.img[off:]) }

func (f *FS) put16(off int, v uint16) { binary.LittleEndian.PutUint16(f.img[off:], v) }

// lookup is the lookup entry of a data page
func (f *FS) lookup(p int) uint16 { return f.u16(f.g.luOff(p)) }

// live tells a data page that is written completely and not deleted, and
// whose header agrees with its lookup entry
func (f *FS) live(p int) (id uint16, span int, ok bool) {
	lu := f.lookup(p)
	if lu == idFree || lu == idDeleted {
		return 0, 0, false
	}
	o := p * f.g.PageSize
	if f.u16(o) != lu {
		return 0, 0, false
	}
	fl := f.img[o+4]
	if fl&flagUsed != 0 || fl&flagFinal != 0 || fl&flagDel == 0 {
		return 0, 0, false
	}
	index := lu&idIndex != 0
	if index != (fl&flagIndex == 0) || (index && fl&flagIxDel == 0) {
		return 0, 0, false
	}
	return lu, int(f.u16(o + 2)), true
}

// object is a file
type object struct {
	id   uint16 // without idIndex
	name string
	size int
	hdr  int // the page of the index header
}

// table is what a scan of the lookup pages finds
type table struct {
	objs  map[uint16]*object
	index map[[2]int]int // (id, span) -> index page
	data  map[[2]int]int // (id, span) -> data page
	used  int            // live pages
}

func (f *FS) scan() *table {
	t := &table{objs: map[uint16]*object{}, index: map[[2]int]int{}, data: map[[2]int]int{}}
	for p := 0; p < f.g.pages(); p++ {
		if !f.g.isData(p) {
			continue
		}
		id, span, ok := f.live(p)
		if !ok {
			continue
		}
		t.used++
		key := [2]int{int(id &^ idIndex), span}
		if id&idIndex == 0 {
			if _, dup := t.data[key]; !dup {
				t.data[key] = p
			}
			continue
		}
		if _, dup := t.index[key]; dup {
			continue
		}
		t.index[key] = p
		if span != 0 {
			continue
		}
		o := p * f.g.PageSize
		if f.img[o+ixPhLen+4] != typeFile {
			continue
		}
		size := binary.LittleEndian.Uint32(f.img[o+ixPhLen:])
		if size == undefinedLen {
			size = 0
		}
		name := f.img[o+ixPhLen+5:][:f.g.ObjNameLen]
		if i := strings.IndexByte(string(name), 0); i >= 0 {
			name = name[:i]
		}
		t.objs[uint16(key[0])] = &object{id: uint16(key[0]), name: string(name), size: int(size), hdr: p}
	}
	return t
}

// canon makes "/a/b" of a path or a name
func canon(p string) string {
	return "/" + strings.Trim(p, "/")
}

func (t *table) find(name string) *object {
	c := canon(name)
	for _, o := range t.objs {
		if canon(o.name) == c {
			return o
		}
	}
	return nil
}

// ReadDir lists a directory: the files whose names start with it and the
// directories their names imply
func (f *FS) ReadDir(dir string) ([]Info, error) {
	d := canon(dir)
	prefix := d + "/"
	if d == "/" {
		prefix = "/"
	}
	t := f.scan()
	seen := map[string]bool{}
	var out []Info
	for _, o := range t.objs {
		n := canon(o.name)
		if n == d && d != "/" {
			return nil, &iofs.PathError{Op: "readdir", Path: dir, Err: errNotDir}
		}
		rest, ok := strings.CutPrefix(n, prefix)
		if !ok || rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			if !seen[rest[:i]] {
				seen[rest[:i]] = true
				out = append(out, Info{Name: rest[:i], Dir: true})
			}
			continue
		}
		out = append(out, Info{Name: rest, Size: int64(o.size)})
	}
	if len(out) == 0 && d != "/" {
		return nil, &iofs.PathError{Op: "readdir", Path: dir, Err: iofs.ErrNotExist}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

var errNotDir = errors.New("not a directory")

// Stat gives the entry of a file, or of a directory implied by names
func (f *FS) Stat(name string) (Info, error) {
	c := canon(name)
	base := c[strings.LastIndexByte(c, '/')+1:]
	if c == "/" {
		return Info{Name: "/", Dir: true}, nil
	}
	t := f.scan()
	if o := t.find(c); o != nil {
		return Info{Name: base, Size: int64(o.size)}, nil
	}
	for _, o := range t.objs {
		if strings.HasPrefix(canon(o.name), c+"/") {
			return Info{Name: base, Dir: true}, nil
		}
	}
	return Info{}, &iofs.PathError{Op: "stat", Path: name, Err: iofs.ErrNotExist}
}

// ReadFile gives the contents of a file
func (f *FS) ReadFile(name string) ([]byte, error) {
	t := f.scan()
	o := t.find(name)
	if o == nil {
		return nil, &iofs.PathError{Op: "read", Path: name, Err: iofs.ErrNotExist}
	}
	if o.size > len(f.img) {
		return nil, &iofs.PathError{Op: "read", Path: name, Err: fmt.Errorf("%w: a size of %d bytes", ErrDamaged, o.size)}
	}
	out := make([]byte, 0, o.size)
	for s := 0; len(out) < o.size; s++ {
		p, ok := f.dataPage(t, o, s)
		if !ok {
			return nil, &iofs.PathError{Op: "read", Path: name, Err: fmt.Errorf("%w: no page %d of the object", ErrDamaged, s)}
		}
		n := min(f.g.data, o.size-len(out))
		off := p*f.g.PageSize + phLen
		out = append(out, f.img[off:off+n]...)
	}
	return out, nil
}

// dataPage finds the data page of span s of an object: by the index, or
// by the scan when the index entry is not a live page of the object
func (f *FS) dataPage(t *table, o *object, s int) (int, bool) {
	ixPage, off := o.hdr, f.g.hdrLen+2*s
	if s >= f.g.hdrIx {
		k := s - f.g.hdrIx
		var ok bool
		ixPage, ok = t.index[[2]int{int(o.id), 1 + k/f.g.ixIx}]
		off = ixPhLen + 2*(k%f.g.ixIx)
		if !ok {
			ixPage = -1
		}
	}
	if ixPage >= 0 {
		p := int(f.u16(ixPage*f.g.PageSize + off))
		if f.g.isData(p) {
			if id, span, ok := f.live(p); ok && id == o.id && span == s {
				return p, true
			}
		}
	}
	p, ok := t.data[[2]int{int(o.id), s}]
	return p, ok
}

// Usage gives the bytes the files take and the bytes SPIFFS can hold, as
// SPIFFS_info counts them: two blocks are kept free for the garbage
// collection
func (f *FS) Usage() (used, total int64) {
	t := f.scan()
	pages := (f.g.blocks-2)*f.g.luEntries + 1
	return int64(t.used) * int64(f.g.data), int64(max(pages, 0)) * int64(f.g.data)
}

// Detect finds the parameters of a SPIFFS image: the page and block
// sizes from the block magic (or, without it, from lookup pages that
// agree with the pages), the header layout from the object index headers
// whose page indexes lead to the object's data pages. ok is false for
// anything else, an erased partition included.
func Detect(img []byte) (Params, bool) {
	pages := []int{256, 512, 128, 1024}
	blocks := []int{4096, 8192, 16384, 32768, 65536}
	for _, magicLen := range []bool{true, false} {
		for _, ps := range pages {
			for _, bs := range blocks {
				p := IDF
				if !magicLen {
					p = ESP8266
				}
				p.PageSize, p.BlockSize, p.UseMagicLen = ps, bs, magicLen
				g, err := p.geom(len(img))
				if err != nil || g.blocks < 2 || len(img)%bs != 0 {
					continue
				}
				f := &FS{g: g, img: img}
				n := f.magicBlocks()
				if n == g.blocks || (n >= g.blocks-1 && g.blocks >= 4) {
					return f.headerLayout(), true
				}
			}
		}
	}
	// No magic: the smallest geometry whose lookup pages agree with the
	// pages
	for _, base := range []Params{IDF, ESP8266} {
		for _, ps := range pages {
			for _, bs := range blocks {
				p := base
				p.PageSize, p.BlockSize, p.UseMagic, p.UseMagicLen = ps, bs, false, false
				g, err := p.geom(len(img))
				if err != nil || g.blocks < 2 || len(img)%bs != 0 {
					continue
				}
				f := &FS{g: g, img: img}
				if good, bad := f.lookupAgrees(); good > 0 && bad == 0 {
					return f.headerLayout(), true
				}
			}
		}
	}
	return Params{}, false
}

// lookupAgrees counts the lookup entries whose page has the id, and the
// entries that disagree with their page: an id on a page of another id,
// a free entry on a written page
func (f *FS) lookupAgrees() (good, bad int) {
	for p := 0; p < f.g.pages(); p++ {
		if !f.g.isData(p) {
			continue
		}
		lu := f.lookup(p)
		o := p * f.g.PageSize
		switch {
		case lu == idDeleted:
		case lu == idFree:
			for _, b := range f.img[o : o+phLen] {
				if b != 0xFF {
					bad++
					break
				}
			}
		case f.u16(o) == lu:
			good++
		default:
			bad++
		}
	}
	return good, bad
}

// headerLayout picks the metadata length and the alignment of the index
// header under which the index headers lead to their data pages. Without
// files there is nothing to tell them by: the preset of the magic kind is
// kept.
func (f *FS) headerLayout() Params {
	base := f.g.Params
	type layout struct {
		meta    int
		aligned bool
	}
	order := []layout{{4, false}, {0, true}, {0, false}, {4, true}}
	if !base.UseMagicLen {
		order = []layout{{0, true}, {4, false}, {0, false}, {4, true}}
	}
	best, bestScore := base, 0
	for i, l := range order {
		p := base
		p.MetaLen, p.AlignedIndex = l.meta, l.aligned
		g, err := p.geom(len(f.img))
		if err != nil {
			continue
		}
		c := &FS{g: g, img: f.img}
		s := c.indexScore()
		if i == 0 || s > bestScore {
			best, bestScore = p, s
		}
	}
	return best
}

// indexScore counts the page indexes of the index headers that lead to a
// live data page of their object and span, less those that do not
func (f *FS) indexScore() int {
	t := f.scan()
	score := 0
	for _, o := range t.objs {
		n := min((o.size+f.g.data-1)/f.g.data, f.g.hdrIx)
		for s := 0; s < n; s++ {
			p := int(f.u16(o.hdr*f.g.PageSize + f.g.hdrLen + 2*s))
			if f.g.isData(p) {
				if id, span, ok := f.live(p); ok && id == o.id && span == s {
					score++
					continue
				}
			}
			score--
		}
	}
	return score
}
