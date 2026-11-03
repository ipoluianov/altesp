// Package littlefs reads and writes littlefs v2 images (disk versions 2.0
// and 2.1) held in memory: the content of a partition read from the board
// or about to be written to it.
//
// It is our own implementation, written from the littlefs specification
// (SPEC.md, and DESIGN.md for the reasoning) of
// github.com/littlefs-project/littlefs; no code of littlefs or of its
// ports was used.
//
// Writes are not power-safe and don't need to be: the image changes in
// memory and goes to the flash as a whole. Every metadata pair an
// operation changes is compacted into its other block as one commit,
// which leaves the rest of the block erased, so littlefs on the board
// keeps appending to it as usual. A failed operation leaves the image as
// it was.
package littlefs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"
)

// Disk versions
const (
	Version20 = 0x00020000
	Version21 = 0x00020001
)

// Errors besides those of io/fs (fs.ErrNotExist, fs.ErrExist,
// fs.ErrInvalid)
var (
	ErrCorrupt  = errors.New("littlefs: corrupted image")
	ErrNoSpace  = errors.New("littlefs: no space left")
	ErrNotEmpty = errors.New("littlefs: directory not empty")
	ErrNotDir   = errors.New("littlefs: not a directory")
	ErrIsDir    = errors.New("littlefs: is a directory")
	ErrNameLen  = errors.New("littlefs: name too long")
	ErrFileSize = errors.New("littlefs: file too large")
)

// Config is the geometry of a file system. The first six fields are
// stored in the superblock; ProgSize and InlineMax are how this package
// writes and are not stored.
type Config struct {
	BlockSize   uint32
	BlockCount  uint32
	NameMax     uint32 // 0: 64, the esp_littlefs default
	FileMax     uint32 // 0: 0x7FFFFFFF
	AttrMax     uint32 // 0: 0x3FE
	DiskVersion uint32 // 0: Version20, the version every littlefs reads
	// ProgSize aligns the commits. 0: 256, which suits the program sizes
	// 16..256 of ESP-IDF and Arduino
	ProgSize uint32
	// InlineMax is the largest file kept inside its directory's metadata.
	// littlefs on the board must have a cache at least this large. 0: 128
	InlineMax uint32
}

// Defaults (see Config)
const (
	DefaultNameMax   = 64
	DefaultFileMax   = 0x7FFFFFFF
	DefaultAttrMax   = 0x3FE
	DefaultProgSize  = 256
	DefaultInlineMax = 128
)

// Info describes a file or a directory
type Info struct {
	Name string
	Dir  bool
	Size int64
}

// FS is a mounted image
type FS struct {
	img  []byte
	cfg  Config
	dirs map[pair]*mdir // every metadata pair, by pair.key()
	root pair           // the first pair of the root directory
	used []bool         // blocks in use
	next uint32         // where the allocator looks first
	// repair is set when the global state is not clear: a move or an
	// orphan left by a power loss. The first write finishes it
	repair bool
}

// Mount reads an image. It works on a copy; Image returns the result of
// the changes
func Mount(img []byte) (*FS, error) {
	return mount(slices.Clone(img), Config{})
}

// Format makes an empty file system of the size of img (or of
// cfg.BlockCount blocks). img is copied, not changed
func Format(img []byte, cfg Config) (*FS, error) {
	if cfg.BlockSize < 128 {
		return nil, fmt.Errorf("littlefs: block size %d is too small", cfg.BlockSize)
	}
	if cfg.BlockCount == 0 {
		cfg.BlockCount = uint32(len(img) / int(cfg.BlockSize))
	}
	if cfg.BlockCount < 2 || uint64(cfg.BlockCount)*uint64(cfg.BlockSize) > uint64(len(img)) {
		return nil, fmt.Errorf("littlefs: %d blocks of %d bytes don't fit in %d bytes", cfg.BlockCount, cfg.BlockSize, len(img))
	}
	if cfg.NameMax == 0 {
		cfg.NameMax = DefaultNameMax
	}
	if cfg.FileMax == 0 {
		cfg.FileMax = DefaultFileMax
	}
	if cfg.AttrMax == 0 {
		cfg.AttrMax = DefaultAttrMax
	}
	if cfg.DiskVersion == 0 {
		cfg.DiskVersion = Version20
	}
	if cfg.DiskVersion != Version20 && cfg.DiskVersion != Version21 {
		return nil, fmt.Errorf("littlefs: unsupported disk version %#x", cfg.DiskVersion)
	}
	if cfg.NameMax > maxTagData || cfg.AttrMax > maxTagData || cfg.FileMax > DefaultFileMax {
		return nil, fmt.Errorf("littlefs: name max %d, attr max %d or file max %d out of range", cfg.NameMax, cfg.AttrMax, cfg.FileMax)
	}
	fsys := &FS{
		img:  bytes.Repeat([]byte{0xFF}, len(img)),
		cfg:  cfg,
		dirs: map[pair]*mdir{},
		root: pair{0, 1},
	}
	if err := fsys.setDefaults(); err != nil {
		return nil, err
	}
	fsys.used = make([]bool, cfg.BlockCount)
	fsys.used[0], fsys.used[1] = true, true
	sb := &mdir{pair: pair{0, 1}, active: 1, dirty: true}
	sb.entries = []*entry{fsys.superEntry()}
	fsys.dirs[sb.pair] = sb
	if err := fsys.sync(); err != nil {
		return nil, err
	}
	return fsys, nil
}

// superEntry is the superblock entry of the configuration
func (fsys *FS) superEntry() *entry {
	c := fsys.cfg
	data := make([]byte, 0, superDataSize)
	for _, v := range []uint32{c.DiskVersion, c.BlockSize, c.BlockCount, c.NameMax, c.FileMax, c.AttrMax} {
		data = binary.LittleEndian.AppendUint32(data, v)
	}
	return &entry{typ: typeSuper, name: "littlefs", stype: tagInline, sdata: data}
}

// setDefaults fills and checks the fields of the configuration that are
// not in the superblock
func (fsys *FS) setDefaults() error {
	c := &fsys.cfg
	if c.ProgSize == 0 {
		c.ProgSize = DefaultProgSize
		for c.ProgSize > 16 && c.ProgSize > c.BlockSize/16 {
			c.ProgSize /= 2
		}
	}
	if c.ProgSize > 512 || c.BlockSize%c.ProgSize != 0 {
		return fmt.Errorf("littlefs: program size %d doesn't suit block size %d", c.ProgSize, c.BlockSize)
	}
	if c.InlineMax == 0 {
		c.InlineMax = DefaultInlineMax
	}
	c.InlineMax = min(c.InlineMax, maxTagData, c.AttrMax, c.BlockSize/8)
	return nil
}

// Config returns the configuration of the file system
func (fsys *FS) Config() Config { return fsys.cfg }

// Image returns the current image. It is the file system's own buffer:
// copy it before the next change if it must stay
func (fsys *FS) Image() []byte { return fsys.img }

func (fsys *FS) block(b uint32) []byte {
	bs := int(fsys.cfg.BlockSize)
	return fsys.img[int(b)*bs : (int(b)+1)*bs]
}

// superConfig reads the superblock entry of a pair
func superConfig(d *mdir) (Config, bool) {
	if !d.hasSuper() || d.entries[0].stype != tagInline || len(d.entries[0].sdata) < superDataSize {
		return Config{}, false
	}
	s := d.entries[0].sdata
	return Config{
		DiskVersion: le32(s), BlockSize: le32(s[4:]), BlockCount: le32(s[8:]),
		NameMax: le32(s[12:]), FileMax: le32(s[16:]), AttrMax: le32(s[20:]),
	}, true
}

// findSuper reads the superblock entry without knowing the block size:
// from block 0, or from block 1 at the usual block sizes
func findSuper(img []byte) (Config, error) {
	try := func(b []byte) (Config, bool) {
		d, ok := parseBlock(b)
		if !ok {
			return Config{}, false
		}
		return superConfig(d)
	}
	if !bytes.Equal(img[min(8, len(img)):min(16, len(img))], []byte("littlefs")) {
		for bs := 128; 2*bs <= len(img); bs *= 2 {
			if c, ok := try(img[bs : 2*bs]); ok && c.BlockSize == uint32(bs) {
				return c, nil
			}
		}
	}
	if c, ok := try(img[:min(len(img), 1<<20)]); ok {
		return c, nil
	}
	return Config{}, errors.New("littlefs: no superblock")
}

func mount(img []byte, keep Config) (*FS, error) {
	cfg, err := findSuper(img)
	if err != nil {
		return nil, err
	}
	fsys := &FS{img: img, cfg: cfg, dirs: map[pair]*mdir{}}
	if err := fsys.checkConfig(); err != nil {
		return nil, err
	}
	fsys.cfg.ProgSize, fsys.cfg.InlineMax = keep.ProgSize, keep.InlineMax
	if err := fsys.setDefaults(); err != nil {
		return nil, err
	}
	if err := fsys.load(); err != nil {
		return nil, err
	}
	return fsys, nil
}

func (fsys *FS) checkConfig() error {
	c := fsys.cfg
	if c.DiskVersion>>16 != 2 || c.DiskVersion&0xFFFF > 1 {
		return fmt.Errorf("littlefs: unsupported disk version %d.%d", c.DiskVersion>>16, c.DiskVersion&0xFFFF)
	}
	if c.BlockSize < 128 || c.BlockCount < 2 {
		return fmt.Errorf("%w: block size %d, block count %d", ErrCorrupt, c.BlockSize, c.BlockCount)
	}
	if uint64(c.BlockSize)*uint64(c.BlockCount) > uint64(len(fsys.img)) {
		return fmt.Errorf("littlefs: the image has %d bytes, the superblock says %d blocks of %d", len(fsys.img), c.BlockCount, c.BlockSize)
	}
	if c.NameMax == 0 || c.NameMax > maxTagData {
		fsys.cfg.NameMax = 255
	}
	if c.AttrMax == 0 || c.AttrMax > maxTagData {
		fsys.cfg.AttrMax = maxTagData
	}
	if c.FileMax == 0 || c.FileMax > DefaultFileMax {
		fsys.cfg.FileMax = DefaultFileMax
	}
	return nil
}

// fetch reads a metadata pair from the image
func (fsys *FS) fetch(p pair) (*mdir, error) {
	if p[0] >= fsys.cfg.BlockCount || p[1] >= fsys.cfg.BlockCount {
		return nil, fmt.Errorf("%w: metadata pair {%d,%d} out of range", ErrCorrupt, p[0], p[1])
	}
	var best *mdir
	for i := range 2 {
		d, ok := parseBlock(fsys.block(p[i]))
		if !ok {
			continue
		}
		d.active = i
		if best == nil || int32(d.rev-best.rev) > 0 {
			best = d
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%w: metadata pair {%d,%d} has no valid commit", ErrCorrupt, p[0], p[1])
	}
	best.pair = p
	return best, nil
}

// get returns a metadata pair, reading it if it is not known yet
func (fsys *FS) get(p pair) (*mdir, error) {
	if d := fsys.dirs[p.key()]; d != nil {
		return d, nil
	}
	d, err := fsys.fetch(p)
	if err != nil {
		return nil, err
	}
	fsys.dirs[p.key()] = d
	return d, nil
}

// load reads every metadata pair: the list threaded through the tails
// from blocks 0 and 1, then the directory tree. It finds the root, the
// global state and the blocks in use
func (fsys *FS) load() error {
	var gstate [gstateSize]byte
	var superChain []pair
	inSuper := true
	n := 0
	for p := (pair{0, 1}); ; {
		if fsys.dirs[p.key()] != nil || n > int(fsys.cfg.BlockCount) {
			return fmt.Errorf("%w: the metadata list loops", ErrCorrupt)
		}
		n++
		d, err := fsys.get(p)
		if err != nil {
			return err
		}
		for i := range gstate {
			gstate[i] ^= d.gdelta[i]
		}
		if inSuper {
			if c, ok := superConfig(d); ok {
				superChain = append(superChain, p)
				// The last superblock in the list is the newest
				if c.BlockSize != fsys.cfg.BlockSize || c.BlockCount > fsys.cfg.BlockCount {
					return fmt.Errorf("%w: superblocks disagree", ErrCorrupt)
				}
				c.ProgSize, c.InlineMax = fsys.cfg.ProgSize, fsys.cfg.InlineMax
				fsys.cfg = c
				if err := fsys.checkConfig(); err != nil {
					return err
				}
				fsys.root = p
			} else {
				inSuper = false
			}
		}
		if d.tailType == 0 || d.tail[0] == blockNull {
			break
		}
		p = d.tail
	}
	if gstate != [gstateSize]byte{} {
		fsys.repair = true
		// A move cut by a power loss: the source is a deleted file
		t := le32(gstate[:])
		if tagType(t) == tagDelete {
			if d := fsys.dirs[pairFrom(gstate[4:]).key()]; d != nil && int(tagID(t)) < len(d.entries) {
				d.entries = slices.Delete(d.entries, int(tagID(t)), int(tagID(t))+1)
				d.dirty = true
			}
		}
	}

	// The tree. Pairs met here and not in the list replaced a pair of the
	// list that shares a block with them (a relocation cut by a power loss)
	reach := map[pair]bool{}
	for _, p := range superChain {
		reach[p.key()] = true
	}
	var walk func(p pair, depth int) error
	walk = func(p pair, depth int) error {
		if depth > int(fsys.cfg.BlockCount) {
			return fmt.Errorf("%w: directories loop", ErrCorrupt)
		}
		for steps := 0; ; steps++ {
			if steps > int(fsys.cfg.BlockCount) {
				return fmt.Errorf("%w: a directory loops", ErrCorrupt)
			}
			if fsys.dirs[p.key()] == nil {
				if err := fsys.relink(p); err != nil {
					return err
				}
			}
			d, err := fsys.get(p)
			if err != nil {
				return err
			}
			if reach[p.key()] && steps > 0 {
				return fmt.Errorf("%w: a directory loops", ErrCorrupt)
			}
			reach[p.key()] = true
			for _, e := range d.entries {
				if e.typ == typeDir && e.stype == tagDirStruct && len(e.sdata) >= 8 {
					if q := pairFrom(e.sdata); !reach[q.key()] {
						if err := walk(q, depth+1); err != nil {
							return err
						}
					}
				}
			}
			if d.tailType != tagHardTail {
				return nil
			}
			p = d.tail
		}
	}
	if err := walk(fsys.root, 0); err != nil {
		return err
	}
	if fsys.repair {
		fsys.dropOrphans(reach)
	}

	fsys.used = make([]bool, fsys.cfg.BlockCount)
	for _, d := range fsys.dirs {
		fsys.used[d.pair[0]], fsys.used[d.pair[1]] = true, true
		for _, e := range d.entries {
			if e.typ == typeReg && e.stype == tagCTZ && len(e.sdata) >= 8 {
				blocks, _ := fsys.ctzBlocks(le32(e.sdata), le32(e.sdata[4:]))
				for _, b := range blocks {
					fsys.used[b] = true
				}
			}
		}
	}
	return nil
}

// relink puts a directory pair that is not in the metadata list in the
// place of the listed pair it shares a block with
func (fsys *FS) relink(p pair) error {
	d, err := fsys.fetch(p)
	if err != nil {
		return err
	}
	for _, old := range fsys.dirs {
		if !old.pair.shares(p) {
			continue
		}
		pred := fsys.pred(old.pair)
		if pred == nil {
			break
		}
		pred.tail = p
		pred.dirty = true
		delete(fsys.dirs, old.pair.key())
		fsys.repair = true
		break
	}
	fsys.dirs[p.key()] = d
	return nil
}

// dropOrphans removes from the metadata list the pairs no directory
// points to (a removal cut by a power loss)
func (fsys *FS) dropOrphans(reach map[pair]bool) {
	d := fsys.dirs[pair{0, 1}.key()]
	for d != nil && d.tailType != 0 {
		next := fsys.dirs[d.tail.key()]
		if next == nil {
			return
		}
		if reach[next.pair.key()] {
			d = next
			continue
		}
		d.tail = next.tail
		d.tailType = 0
		if next.tailType != 0 {
			d.tailType = tagSoftTail
		}
		d.dirty = true
		delete(fsys.dirs, next.pair.key())
	}
}

// pred is the pair whose tail points to p
func (fsys *FS) pred(p pair) *mdir {
	for _, d := range fsys.dirs {
		if d.tailType != 0 && d.tail.same(p) {
			return d
		}
	}
	return nil
}

// alloc takes a free block and erases it
func (fsys *FS) alloc() (uint32, error) {
	n := fsys.cfg.BlockCount
	for i := range n {
		b := (fsys.next + i) % n
		if !fsys.used[b] {
			fsys.used[b] = true
			fsys.next = (b + 1) % n
			blk := fsys.block(b)
			for j := range blk {
				blk[j] = 0xFF
			}
			return b, nil
		}
	}
	return 0, ErrNoSpace
}

func (fsys *FS) free(b uint32) {
	if b < fsys.cfg.BlockCount {
		fsys.used[b] = false
	}
}

// newDir allocates an empty metadata pair
func (fsys *FS) newDir() (*mdir, error) {
	a, err := fsys.alloc()
	if err != nil {
		return nil, err
	}
	b, err := fsys.alloc()
	if err != nil {
		return nil, err
	}
	// active 1 and revision 0: the first commit goes to block a with
	// revision 1, block b stays erased
	d := &mdir{pair: pair{a, b}, active: 1, dirty: true}
	fsys.dirs[d.pair.key()] = d
	return d, nil
}

// dropDir removes a pair from the metadata list; pred is the pair before
// it. The global state delta it held moves to pred
func (fsys *FS) dropDir(d, pred *mdir) {
	pred.tail, pred.tailType = d.tail, d.tailType
	for i := range pred.gdelta {
		pred.gdelta[i] ^= d.gdelta[i]
	}
	pred.dirty = true
	fsys.free(d.pair[0])
	fsys.free(d.pair[1])
	delete(fsys.dirs, d.pair.key())
}

// write compacts a pair into its other block
func (fsys *FS) write(d *mdir) error {
	bs := int(fsys.cfg.BlockSize)
	buf, err := encode(d, d.rev+1, bs, bs, int(fsys.cfg.ProgSize), fsys.cfg.DiskVersion >= Version21)
	if errors.Is(err, errNoRoom) {
		return fmt.Errorf("%w: an entry doesn't fit in a metadata block", ErrNoSpace)
	}
	if err != nil {
		return err
	}
	target := 1 - d.active
	blk := fsys.block(d.pair[target])
	copy(blk, buf)
	for i := len(buf); i < len(blk); i++ {
		blk[i] = 0xFF
	}
	d.rev++
	d.active = target
	d.dirty = false
	return nil
}

// commitDir writes a pair, splitting it while its compacted commit takes
// more than half a block, so that littlefs on the board has room to
// append
func (fsys *FS) commitDir(d *mdir) error {
	prog := int(fsys.cfg.ProgSize)
	limit := max(int(fsys.cfg.BlockSize)/2, overhead(prog)) - overhead(prog)
	// The next pair of the directory joins this one if both fit
	for d.tailType == tagHardTail {
		next := fsys.dirs[d.tail.key()]
		if next == nil || entriesSize(d.entries)+entriesSize(next.entries) > limit {
			break
		}
		d.entries = append(d.entries, next.entries...)
		fsys.dropDir(next, d)
	}
	for {
		size, k := 0, 0
		for k < len(d.entries) && size+entrySize(d.entries[k]) <= limit {
			size += entrySize(d.entries[k])
			k++
		}
		if k == len(d.entries) || len(d.entries) == 1 {
			return fsys.write(d)
		}
		k = max(k, 1)
		nd, err := fsys.newDir()
		if err != nil {
			return err
		}
		nd.entries = slices.Clone(d.entries[k:])
		nd.tail, nd.tailType = d.tail, d.tailType
		d.entries = d.entries[:k]
		d.tail, d.tailType = nd.pair, tagHardTail
		if err := fsys.write(d); err != nil {
			return err
		}
		d = nd
	}
}

// sync writes every changed pair
func (fsys *FS) sync() error {
	var keys []pair
	for k, d := range fsys.dirs {
		if d.dirty {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i][0] < keys[j][0] })
	for _, k := range keys {
		if d := fsys.dirs[k]; d != nil && d.dirty {
			if err := fsys.commitDir(d); err != nil {
				return err
			}
		}
	}
	return nil
}

// update runs a change and writes it, or restores the image if it fails
func (fsys *FS) update(change func() error) error {
	backup := slices.Clone(fsys.img)
	err := change()
	if err == nil && fsys.repair {
		// Clear the global state: the sum of all deltas becomes zero
		var g [gstateSize]byte
		for _, d := range fsys.dirs {
			for i := range g {
				g[i] ^= d.gdelta[i]
			}
		}
		sb := fsys.dirs[pair{0, 1}.key()]
		for i := range g {
			sb.gdelta[i] ^= g[i]
		}
		sb.dirty = true
	}
	if err == nil {
		err = fsys.sync()
	}
	if err != nil {
		copy(fsys.img, backup)
		if back, merr := mount(fsys.img, fsys.cfg); merr == nil {
			*fsys = *back
		}
		return err
	}
	fsys.repair = false
	return nil
}

// splitPath turns a path into its names. "/" and "" are the root
func splitPath(path string) ([]string, error) {
	var names []string
	for _, s := range strings.Split(path, "/") {
		switch s {
		case "", ".":
		case "..":
			return nil, fs.ErrInvalid
		default:
			names = append(names, s)
		}
	}
	return names, nil
}

// chain is the pairs of a directory
func (fsys *FS) chain(p pair) ([]*mdir, error) {
	var out []*mdir
	for {
		d, err := fsys.get(p)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
		if d.tailType != tagHardTail || len(out) > int(fsys.cfg.BlockCount) {
			return out, nil
		}
		p = d.tail
	}
}

// lookup finds a name in a directory
func (fsys *FS) lookup(dir pair, name string) (*mdir, int, error) {
	ch, err := fsys.chain(dir)
	if err != nil {
		return nil, 0, err
	}
	for _, d := range ch {
		for i, e := range d.entries {
			if (e.typ == typeReg || e.typ == typeDir) && e.name == name {
				return d, i, nil
			}
		}
	}
	return nil, 0, fs.ErrNotExist
}

func dirPair(e *entry) (pair, error) {
	if e.typ != typeDir {
		return pair{}, ErrNotDir
	}
	if e.stype != tagDirStruct || len(e.sdata) < 8 {
		return pair{}, ErrCorrupt
	}
	return pairFrom(e.sdata), nil
}

// walkDir finds the first pair of a directory
func (fsys *FS) walkDir(names []string) (pair, error) {
	p := fsys.root
	for _, n := range names {
		d, i, err := fsys.lookup(p, n)
		if err != nil {
			return pair{}, err
		}
		if p, err = dirPair(d.entries[i]); err != nil {
			return pair{}, err
		}
	}
	return p, nil
}

// target is a resolved path: its parent directory and, if it exists, the
// pair and id of its entry
type target struct {
	parent pair
	name   string
	d      *mdir
	id     int
}

func (t *target) entry() *entry { return t.d.entries[t.id] }

// resolve finds a path that is not the root
func (fsys *FS) resolve(path string) (target, error) {
	names, err := splitPath(path)
	if err != nil {
		return target{}, err
	}
	if len(names) == 0 {
		return target{}, fs.ErrInvalid
	}
	parent, err := fsys.walkDir(names[:len(names)-1])
	if err != nil {
		return target{}, err
	}
	t := target{parent: parent, name: names[len(names)-1]}
	t.d, t.id, err = fsys.lookup(parent, t.name)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	return t, err
}

func info(e *entry) Info {
	return Info{Name: e.name, Dir: e.typ == typeDir, Size: e.size()}
}

// ReadDir lists a directory, sorted by name
func (fsys *FS) ReadDir(path string) ([]Info, error) {
	names, err := splitPath(path)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: path, Err: err}
	}
	p, err := fsys.walkDir(names)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: path, Err: err}
	}
	ch, err := fsys.chain(p)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: path, Err: err}
	}
	var out []Info
	for _, d := range ch {
		for _, e := range d.entries {
			if e.typ == typeReg || e.typ == typeDir {
				out = append(out, info(e))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Stat describes a path
func (fsys *FS) Stat(path string) (Info, error) {
	names, err := splitPath(path)
	if err == nil && len(names) == 0 {
		return Info{Name: "/", Dir: true}, nil
	}
	t, err := fsys.resolve(path)
	if err == nil && t.d == nil {
		err = fs.ErrNotExist
	}
	if err != nil {
		return Info{}, &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	return info(t.entry()), nil
}

// ReadFile reads a whole file
func (fsys *FS) ReadFile(path string) ([]byte, error) {
	t, err := fsys.resolve(path)
	if err == nil && t.d == nil {
		err = fs.ErrNotExist
	}
	if err == nil && t.entry().typ == typeDir {
		err = ErrIsDir
	}
	if err != nil {
		return nil, &fs.PathError{Op: "read", Path: path, Err: err}
	}
	e := t.entry()
	switch {
	case e.stype == tagInline:
		return slices.Clone(e.sdata), nil
	case e.stype == tagCTZ && len(e.sdata) >= 8:
		data, err := fsys.readCTZ(le32(e.sdata), le32(e.sdata[4:]))
		if err != nil {
			return nil, &fs.PathError{Op: "read", Path: path, Err: err}
		}
		return data, nil
	case e.stype == 0:
		return []byte{}, nil
	}
	return nil, &fs.PathError{Op: "read", Path: path, Err: ErrCorrupt}
}

// freeFile releases the data blocks of a file
func (fsys *FS) freeFile(e *entry) {
	if e.typ == typeReg && e.stype == tagCTZ && len(e.sdata) >= 8 {
		blocks, _ := fsys.ctzBlocks(le32(e.sdata), le32(e.sdata[4:]))
		for _, b := range blocks {
			fsys.free(b)
		}
	}
}

// insert puts an entry into a directory, in name order
func (fsys *FS) insert(dir pair, e *entry) error {
	ch, err := fsys.chain(dir)
	if err != nil {
		return err
	}
	// The pair whose names it falls between: names in a later pair are
	// all greater
	d := ch[len(ch)-1]
	for i := 0; i+1 < len(ch); i++ {
		if first := firstName(ch[i+1]); first != "" && e.name < first {
			d = ch[i]
			break
		}
	}
	if len(d.entries) >= idNone-1 {
		return ErrNoSpace
	}
	pos := len(d.entries)
	for i, x := range d.entries {
		if x.typ != typeSuper && x.name > e.name {
			pos = i
			break
		}
	}
	d.entries = slices.Insert(d.entries, pos, e)
	d.dirty = true
	return nil
}

func firstName(d *mdir) string {
	for _, e := range d.entries {
		if e.typ != typeSuper {
			return e.name
		}
	}
	return ""
}

// unlink takes an entry out of its pair. A pair left empty that is not
// the first of its directory leaves the list
func (fsys *FS) unlink(t target) error {
	d := t.d
	d.entries = slices.Delete(d.entries, t.id, t.id+1)
	d.dirty = true
	if len(d.entries) > 0 {
		return nil
	}
	ch, err := fsys.chain(t.parent)
	if err != nil {
		return err
	}
	for i := 1; i < len(ch); i++ {
		if ch[i] == d {
			fsys.dropDir(d, ch[i-1])
			break
		}
	}
	return nil
}

func (fsys *FS) checkName(name string) error {
	if len(name) > int(fsys.cfg.NameMax) {
		return ErrNameLen
	}
	return nil
}

// WriteFile creates or replaces a file. Its directory must exist
func (fsys *FS) WriteFile(path string, data []byte) error {
	err := fsys.update(func() error {
		if uint64(len(data)) > uint64(fsys.cfg.FileMax) {
			return ErrFileSize
		}
		t, err := fsys.resolve(path)
		if err != nil {
			return err
		}
		if err := fsys.checkName(t.name); err != nil {
			return err
		}
		e := &entry{typ: typeReg, name: t.name}
		if t.d != nil {
			old := t.entry()
			if old.typ == typeDir {
				return ErrIsDir
			}
			fsys.freeFile(old)
			e = old
			t.d.dirty = true
		}
		if len(data) <= int(fsys.cfg.InlineMax) {
			e.stype, e.sdata = tagInline, slices.Clone(data)
		} else {
			head, err := fsys.writeCTZ(data)
			if err != nil {
				return err
			}
			e.stype = tagCTZ
			e.sdata = binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, head), uint32(len(data)))
		}
		if t.d == nil {
			return fsys.insert(t.parent, e)
		}
		return nil
	})
	if err != nil {
		return &fs.PathError{Op: "write", Path: path, Err: err}
	}
	return nil
}

// Mkdir creates a directory. Its parent must exist
func (fsys *FS) Mkdir(path string) error {
	err := fsys.update(func() error { return fsys.mkdir(path) })
	if err != nil {
		return &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

func (fsys *FS) mkdir(path string) error {
	t, err := fsys.resolve(path)
	if err != nil {
		return err
	}
	if t.d != nil {
		return fs.ErrExist
	}
	if err := fsys.checkName(t.name); err != nil {
		return err
	}
	ch, err := fsys.chain(t.parent)
	if err != nil {
		return err
	}
	nd, err := fsys.newDir()
	if err != nil {
		return err
	}
	// The new pair joins the list after the last pair of its parent
	last := ch[len(ch)-1]
	nd.tail, nd.tailType = last.tail, last.tailType
	last.tail, last.tailType = nd.pair, tagSoftTail
	last.dirty = true
	return fsys.insert(t.parent, &entry{typ: typeDir, name: t.name, stype: tagDirStruct, sdata: nd.pair.bytes()})
}

// mkdirAll creates a directory and its missing parents
func (fsys *FS) mkdirAll(path string) error {
	names, err := splitPath(path)
	if err != nil {
		return err
	}
	for i := range names {
		p := strings.Join(names[:i+1], "/")
		if st, err := fsys.Stat(p); err == nil {
			if !st.Dir {
				return &fs.PathError{Op: "mkdir", Path: p, Err: ErrNotDir}
			}
			continue
		}
		if err := fsys.Mkdir(p); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes a file or an empty directory
func (fsys *FS) Remove(path string) error {
	err := fsys.update(func() error {
		t, err := fsys.resolve(path)
		if err == nil && t.d == nil {
			err = fs.ErrNotExist
		}
		if err != nil {
			return err
		}
		return fsys.remove(t)
	})
	if err != nil {
		return &fs.PathError{Op: "remove", Path: path, Err: err}
	}
	return nil
}

func (fsys *FS) remove(t target) error {
	e := t.entry()
	if e.typ == typeDir {
		p, err := dirPair(e)
		if err != nil {
			return err
		}
		ch, err := fsys.chain(p)
		if err != nil {
			return err
		}
		for _, d := range ch {
			if len(d.entries) > 0 {
				return ErrNotEmpty
			}
		}
		if err := fsys.unlink(t); err != nil {
			return err
		}
		// Its pairs leave the list
		pred := fsys.pred(ch[0].pair)
		if pred == nil {
			return fmt.Errorf("%w: directory {%d,%d} is not in the metadata list", ErrCorrupt, p[0], p[1])
		}
		last := ch[len(ch)-1]
		pred.tail, pred.tailType = last.tail, 0
		if last.tailType != 0 {
			pred.tailType = tagSoftTail
		}
		pred.dirty = true
		for _, d := range ch {
			for i := range pred.gdelta {
				pred.gdelta[i] ^= d.gdelta[i]
			}
			fsys.free(d.pair[0])
			fsys.free(d.pair[1])
			delete(fsys.dirs, d.pair.key())
		}
		return nil
	}
	fsys.freeFile(e)
	return fsys.unlink(t)
}

// Rename moves a file or a directory. An existing file at the
// destination is replaced, as is an empty directory when a directory
// moves
func (fsys *FS) Rename(from, to string) error {
	err := fsys.update(func() error {
		src, err := fsys.resolve(from)
		if err == nil && src.d == nil {
			err = fs.ErrNotExist
		}
		if err != nil {
			return err
		}
		fromNames, _ := splitPath(from)
		toNames, err := splitPath(to)
		if err != nil {
			return err
		}
		if slices.Equal(fromNames, toNames) {
			return nil
		}
		e := src.entry()
		if e.typ == typeDir && len(toNames) > len(fromNames) && slices.Equal(toNames[:len(fromNames)], fromNames) {
			return fmt.Errorf("%w: a directory can't move into itself", fs.ErrInvalid)
		}
		dst, err := fsys.resolve(to)
		if err != nil {
			return err
		}
		if err := fsys.checkName(dst.name); err != nil {
			return err
		}
		if dst.d != nil {
			old := dst.entry()
			switch {
			case old.typ == typeDir && e.typ != typeDir:
				return ErrIsDir
			case old.typ != typeDir && e.typ == typeDir:
				return ErrNotDir
			}
			if err := fsys.remove(dst); err != nil {
				return err
			}
			// The removal may have shifted the source
			if src, err = fsys.resolve(from); err != nil {
				return err
			}
		}
		if err := fsys.unlink(src); err != nil {
			return err
		}
		e.name = dst.name
		return fsys.insert(dst.parent, e)
	})
	if err != nil {
		return &fs.PathError{Op: "rename", Path: from, Err: err}
	}
	return nil
}
