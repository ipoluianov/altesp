// Package fatfs reads and writes FAT partitions as ESP-IDF makes them: FAT12
// and FAT16 (FAT32 is read and written, not formatted) with VFAT long
// names, on top of ESP-IDF's wear levelling layer or without it. It lists
// directories, reads files, creates, replaces, renames and deletes files
// and directories, formats a partition and builds one from a folder, like
// fatfsgen.py and wl_fatfsgen.py.
//
// With wear levelling the file system is not at the start of the
// partition: the WL state says where every 4 KB sector is (see wl.go). The
// FAT sector is 4096 bytes (CONFIG_WL_SECTOR_SIZE_4096, the default) or
// 512 bytes; with 512 the "safe" mode (CONFIG_WL_SECTOR_MODE_SAFE) keeps
// the last two 4 KB sectors for itself, the "performance" mode does not.
//
// Sources: Microsoft FAT32 File System Specification 1.03; the ESP-IDF
// Programming Guide (FAT Filesystem Support, Wear Levelling API, FATFS
// partition generator) and the parameters of fatfsgen.py; FatFs (the FAT
// driver ESP-IDF runs) for how it tells FAT12 from FAT16 and makes 8.3
// aliases.
package fatfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	iofs "io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Params describe a FAT partition
type Params struct {
	// SectorSize is the FAT sector: 4096 or 512 (CONFIG_WL_SECTOR_SIZE);
	// 0 is 4096
	SectorSize int
	// WL: the partition has the wear levelling layer
	WL bool
	// Safe: WL with 512-byte sectors in safe mode, which keeps two 4 KB
	// sectors at the end of the logical space
	Safe bool

	// Format and FromDir make the file system with these; Detect and
	// Mount give those of the image. 0 is the default.
	FATs           int    // FAT copies: 2, as ESP-IDF's f_mkfs and fatfsgen.py
	ClusterSectors int    // sectors per cluster: 1
	RootEntries    int    // entries of the root directory: 512
	Label          string // the volume label; "" is none ("NO NAME")
}

var (
	// IDF is a FAT partition as esp_vfs_fat_spiflash_mount_rw_wl and
	// wl_fatfsgen.py make it by default
	IDF = Params{SectorSize: 4096, WL: true}
	// Raw is FAT without wear levelling (esp_vfs_fat_spiflash_mount_ro,
	// fatfsgen.py, mkfatfs)
	Raw = Params{SectorSize: 4096}
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
)

// now is the time new entries get
var now = time.Now

// FS is a FAT partition in memory
type FS struct {
	p    Params
	phys []byte // the partition, with wear levelling
	wl   *wl
	// logical is the space under wear levelling; the partition without it
	logical []byte
	v       *vol
}

// fatLen is the bytes of the logical space the volume may take
func fatLen(logical int, p Params) int {
	if p.WL && p.Safe {
		return logical - 2*wlSector
	}
	return logical
}

// Mount opens a FAT partition; the image is copied. With p.WL the wear
// levelling state is read as the chip reads it; a partition without a
// state is taken as the chip would take it, with the dummy sector first.
// A p.SectorSize other than the boot sector's is an error.
func Mount(img []byte, p Params) (*FS, error) {
	f := &FS{p: p}
	if p.WL {
		w, err := openWL(img)
		if err != nil {
			return nil, err
		}
		f.wl = w
		f.phys = append([]byte(nil), img...)
		f.logical = w.unwrap(img)
		if p.Safe {
			if err := recoverSafe(f.logical); err != nil {
				return nil, err
			}
		}
	} else {
		f.logical = append([]byte(nil), img...)
	}
	v, err := openVol(f.logical[:max(fatLen(len(f.logical), p), 0)])
	if err != nil {
		return nil, err
	}
	if p.SectorSize != 0 && v.bps != p.SectorSize {
		return nil, fmt.Errorf("fatfs: the volume has sectors of %d bytes, not %d", v.bps, p.SectorSize)
	}
	f.v = v
	f.p.SectorSize = v.bps
	f.p.FATs, f.p.ClusterSectors, f.p.RootEntries, f.p.Label = v.nFATs, v.spc, v.rootEnt, v.label
	return f, nil
}

// Detect tells a FAT partition and its parameters: wear levelling by the
// config sector at its end (its CRC), else a boot sector at its start.
// Safe mode is told by the volume ending two sectors short of the logical
// space.
func Detect(img []byte) (Params, bool) {
	if w, err := openWL(img); err == nil && w.hasCfg {
		logical := w.unwrap(img)
		v, err := openVol(logical)
		if err != nil {
			return Params{}, false
		}
		p := Params{SectorSize: v.bps, WL: true}
		p.Safe = v.bps < wlSector && v.tot*v.bps <= len(logical)-2*wlSector
		f, err := Mount(img, p)
		if err != nil {
			return Params{}, false
		}
		return f.p, true
	}
	f, err := Mount(img, Params{})
	if err != nil {
		return Params{}, false
	}
	return f.p, true
}

// Params gives the parameters of the partition
func (f *FS) Params() Params { return f.p }

// Type is "FAT12", "FAT16" or "FAT32"
func (f *FS) Type() string { return fmt.Sprintf("FAT%d", f.v.bits) }

// Image gives the bytes of the partition
func (f *FS) Image() []byte {
	if f.wl == nil {
		return f.logical
	}
	f.phys = f.wl.wrap(f.phys, f.logical)
	return f.phys
}

// Usage gives the bytes the clusters in use take and those of all
// clusters
func (f *FS) Usage() (used, total int64) {
	total = int64(f.v.nClus) * int64(f.v.clusSz)
	return total - int64(f.v.freeClusters())*int64(f.v.clusSz), total
}

func pathErr(op, path string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*iofs.PathError); ok {
		return err
	}
	return &iofs.PathError{Op: op, Path: path, Err: err}
}

// ReadDir lists a directory, sorted by name
func (f *FS) ReadDir(path string) ([]Info, error) {
	d, err := f.v.openPath(path)
	if err != nil {
		return nil, pathErr("readdir", path, err)
	}
	var out []Info
	for _, e := range d.entries {
		if e.attr&attrVolume != 0 {
			continue
		}
		out = append(out, info(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func info(e dirent) Info {
	i := Info{Name: e.name, Dir: e.isDir()}
	if !i.Dir {
		i.Size = int64(e.size)
	}
	return i
}

// Stat gives the entry of a path
func (f *FS) Stat(path string) (Info, error) {
	_, e, err := f.v.lookup(path)
	if err != nil {
		return Info{}, pathErr("stat", path, err)
	}
	if e == nil {
		return Info{Name: "/", Dir: true}, nil
	}
	return info(*e), nil
}

// ReadFile gives the contents of a file
func (f *FS) ReadFile(path string) ([]byte, error) {
	_, e, err := f.v.lookup(path)
	if err == nil && (e == nil || e.isDir()) {
		err = errIsDir
	}
	if err != nil {
		return nil, pathErr("read", path, err)
	}
	b, err := f.v.readFile(e)
	return b, pathErr("read", path, err)
}

var errIsDir = errors.New("is a directory")

// atomic runs a change and puts the volume back when it fails
func (f *FS) atomic(op, path string, fn func() error) error {
	saved := append([]byte(nil), f.v.b...)
	if err := fn(); err != nil {
		copy(f.v.b, saved)
		return pathErr(op, path, err)
	}
	if f.v.bits == 32 {
		f.invalidateFSInfo()
	}
	return nil
}

// invalidateFSInfo sets the free count and the next free cluster of the
// FSInfo sector of FAT32 to unknown: they are hints, and stale hints
// mislead
func (f *FS) invalidateFSInfo() {
	b := f.v.b
	s := int(binary.LittleEndian.Uint16(b[48:]))
	o := s * f.v.bps
	if s == 0 || s >= f.v.rsv || binary.LittleEndian.Uint32(b[o:]) != 0x41615252 {
		return
	}
	binary.LittleEndian.PutUint32(b[o+488:], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(b[o+492:], 0xFFFFFFFF)
}

// WriteFile creates a file or replaces its contents; its directory has to
// exist
func (f *FS) WriteFile(path string, data []byte) error {
	return f.writeFile(path, data, now())
}

func (f *FS) writeFile(path string, data []byte, t time.Time) error {
	return f.atomic("write", path, func() error {
		if len(data) > 0xFFFFFFFF {
			return ErrNoSpace
		}
		dirPath, name, err := parentOf(path)
		if err != nil {
			return err
		}
		if name, err = cleanName(name); err != nil {
			return err
		}
		d, err := f.v.openPath(dirPath)
		if err != nil {
			return err
		}
		if e := d.find(name); e != nil {
			if e.isDir() {
				return errIsDir
			}
			if e.clus != 0 {
				f.v.free(e.clus)
			}
			c, err := f.v.writeData(data)
			if err != nil {
				return err
			}
			s := d.buf[e.slot : e.slot+32]
			setClus(s, c)
			binary.LittleEndian.PutUint32(s[28:], uint32(len(data)))
			date, tm := dosTime(t)
			binary.LittleEndian.PutUint16(s[22:], tm)
			binary.LittleEndian.PutUint16(s[24:], date)
			binary.LittleEndian.PutUint16(s[18:], date)
			s[11] |= attrArchive
			f.v.flush(d)
			return nil
		}
		c, err := f.v.writeData(data)
		if err != nil {
			return err
		}
		if err := f.v.add(d, name, newEntry(attrArchive, c, uint32(len(data)), t)); err != nil {
			return err
		}
		f.v.flush(d)
		return nil
	})
}

// Mkdir makes a directory; its parent has to exist
func (f *FS) Mkdir(path string) error {
	return f.mkdir(path, now())
}

func (f *FS) mkdir(path string, t time.Time) error {
	return f.atomic("mkdir", path, func() error {
		dirPath, name, err := parentOf(path)
		if err != nil {
			return err
		}
		if name, err = cleanName(name); err != nil {
			return err
		}
		d, err := f.v.openPath(dirPath)
		if err != nil {
			return err
		}
		if d.find(name) != nil {
			return iofs.ErrExist
		}
		ch, err := f.v.alloc(1)
		if err != nil {
			return err
		}
		b := f.v.clus(ch[0])
		clear(b)
		dot := newEntry(attrDir, ch[0], 0, t)
		copy(dot, ".          ")
		copy(b, dot)
		dotdot := newEntry(attrDir, d.clus, 0, t)
		copy(dotdot, "..         ")
		copy(b[32:], dotdot)
		if err := f.v.add(d, name, newEntry(attrDir, ch[0], 0, t)); err != nil {
			return err
		}
		f.v.flush(d)
		return nil
	})
}

// Remove deletes a file, or a directory with everything in it
func (f *FS) Remove(path string) error {
	return f.atomic("remove", path, func() error {
		d, e, err := f.v.lookup(path)
		if err != nil {
			return err
		}
		if e == nil {
			return iofs.ErrInvalid // the root
		}
		if err := f.v.removeTree(e, map[uint32]bool{}); err != nil {
			return err
		}
		d.remove(e)
		f.v.flush(d)
		return nil
	})
}

// Rename moves a file or a directory to a new path; nothing may be there
// but itself (a change of case)
func (f *FS) Rename(from, to string) error {
	return f.atomic("rename", from, func() error {
		src, e, err := f.v.lookup(from)
		if err != nil {
			return err
		}
		if e == nil {
			return iofs.ErrInvalid
		}
		fromNames, _ := split(from)
		toNames, err := split(to)
		if err != nil || len(toNames) == 0 {
			return iofs.ErrInvalid
		}
		toDir, name, _ := parentOf(to)
		if name, err = cleanName(name); err != nil {
			return err
		}
		sameDir := isAncestor(fromNames[:len(fromNames)-1], toNames[:len(toNames)-1]) &&
			len(fromNames) == len(toNames)
		if e.isDir() && isAncestor(fromNames, toNames) && !sameDir {
			return fmt.Errorf("%w: a directory into itself", iofs.ErrInvalid)
		}
		entry := append([]byte(nil), src.buf[e.slot:e.slot+32]...)
		isDir, clus := e.isDir(), e.clus
		dst, err := f.v.openPath(toDir)
		if err != nil {
			return err
		}
		if x := dst.find(name); x != nil && !(sameDir && x.slot == e.slot) {
			return iofs.ErrExist
		}
		src.remove(e)
		f.v.flush(src)
		if sameDir {
			dst = src
		} else if dst, err = f.v.openPath(toDir); err != nil {
			return err
		}
		entry[12] = 0 // the lowercase flags belong to the old name
		if err := f.v.add(dst, name, entry); err != nil {
			return err
		}
		f.v.flush(dst)
		if isDir && !sameDir && clus != 0 {
			// ".." of the directory moved points to its new parent; the
			// root is 0 there, on FAT32 too
			p := dst.clus
			if p == f.v.rootClus {
				p = 0
			}
			b := f.v.clus(clus)
			if string(b[32:34]) == ".." {
				setClus(b[32:64], p)
			}
		}
		return nil
	})
}

// Format makes an empty FAT of the size of img as ESP-IDF's f_mkfs and
// fatfsgen.py do: one reserved sector, the FATs, the root directory, one
// sector per cluster unless asked; FAT12 or FAT16 by the cluster count.
// With p.WL the wear levelling sectors are made as the chip makes them on
// its first mount. img is not changed.
func Format(img []byte, p Params) (*FS, error) {
	if p.SectorSize == 0 {
		p.SectorSize = 4096
	}
	if p.SectorSize != 512 && p.SectorSize != 4096 {
		return nil, fmt.Errorf("fatfs: sectors of %d bytes; ESP-IDF has 512 and 4096", p.SectorSize)
	}
	if p.Safe && (!p.WL || p.SectorSize != 512) {
		return nil, fmt.Errorf("fatfs: safe mode is wear levelling with 512-byte sectors")
	}
	if p.WL {
		l, err := newWLLayout(len(img))
		if err != nil {
			return nil, err
		}
		logical := make([]byte, l.flashSize)
		for i := range logical {
			logical[i] = 0xFF
		}
		n := fatLen(len(logical), p)
		if n <= 0 {
			return nil, fmt.Errorf("fatfs: %d bytes is too small", len(img))
		}
		if err := mkfs(logical[:n], p); err != nil {
			return nil, err
		}
		return Mount(buildWL(len(img), logical), p)
	}
	b := make([]byte, len(img))
	if err := mkfs(b, p); err != nil {
		return nil, err
	}
	return Mount(b, p)
}

// mkfs writes an empty FAT12/16 volume over all of b. The data area is
// left erased (0xFF) as fatfsgen.py leaves it.
func mkfs(b []byte, p Params) error {
	ss := p.SectorSize
	spc := max(p.ClusterSectors, 1)
	nf := p.FATs
	if nf == 0 {
		nf = 2
	}
	rootEnt := p.RootEntries
	if rootEnt == 0 {
		rootEnt = 512
	}
	if spc&(spc-1) != 0 || spc > 128 || nf > 2 || rootEnt < 0 {
		return fmt.Errorf("fatfs: bad format parameters")
	}
	// The root directory fills whole sectors
	rootSecs := (rootEnt*32 + ss - 1) / ss
	rootEnt = rootSecs * ss / 32
	if rootEnt > 0xFFFF {
		return fmt.Errorf("fatfs: too many root entries")
	}
	tot := len(b) / ss
	rsv, fatSz, n, bits := 1, 1, 0, 0
	for i := 0; ; i++ {
		if i > 64 {
			return fmt.Errorf("fatfs: no FAT layout for %d bytes", len(b))
		}
		data := tot - rsv - nf*fatSz - rootSecs
		if data < spc*2 {
			return fmt.Errorf("fatfs: %d bytes is too small for FAT", len(b))
		}
		n = data / spc
		// Counts where FatFs and the specification disagree are skipped
		if n == maxFAT12 || n == maxFAT16 {
			rsv++
			continue
		}
		switch {
		case n < maxFAT12:
			bits = 12
		case n < maxFAT16:
			bits = 16
		default:
			return fmt.Errorf("fatfs: %d clusters need FAT32, which ESP-IDF does not format: use bigger clusters", n)
		}
		need := ((n+2)*bits + 7) / 8
		need = (need + ss - 1) / ss
		if need <= fatSz {
			break
		}
		fatSz = need
	}
	meta := (rsv + nf*fatSz + rootSecs) * ss
	clear(b[:meta])
	for i := meta; i < tot*ss; i++ {
		b[i] = 0xFF
	}
	le16 := func(o, v int) { binary.LittleEndian.PutUint16(b[o:], uint16(v)) }
	copy(b, "\xEB\xFE\x90MSDOS5.0")
	le16(11, ss)
	b[13] = byte(spc)
	le16(14, rsv)
	b[16] = byte(nf)
	le16(17, rootEnt)
	if tot < 0x10000 {
		le16(19, tot)
	} else {
		binary.LittleEndian.PutUint32(b[32:], uint32(tot))
	}
	b[21] = 0xF8
	le16(22, fatSz)
	le16(24, 63)  // sectors per track, as FatFs
	le16(26, 255) // heads
	b[36] = 0x80  // drive number
	b[38] = 0x29
	binary.LittleEndian.PutUint32(b[39:], rand.Uint32())
	label := strings.ToUpper(p.Label)
	if label == "" {
		label = "NO NAME"
	}
	copy(b[43:54], fmt.Sprintf("%-11.11s", label))
	copy(b[54:62], fmt.Sprintf("FAT%-5d", bits))
	b[510], b[511] = 0x55, 0xAA
	for k := 0; k < nf; k++ {
		t := b[(rsv+k*fatSz)*ss:]
		if bits == 12 {
			copy(t, "\xF8\xFF\xFF")
		} else {
			copy(t, "\xF8\xFF\xFF\xFF")
		}
	}
	if p.Label != "" {
		e := newEntry(attrVolume, 0, 0, now())
		copy(e, b[43:54])
		copy(b[(rsv+nf*fatSz)*ss:], e)
	}
	return nil
}

// FromDir builds a FAT partition of size bytes from the files and folders
// of a folder, as fatfsgen.py and wl_fatfsgen.py do; the files keep their
// modification times
func FromDir(dir string, size int, p Params) ([]byte, error) {
	f, err := Format(make([]byte, size), p)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(dir, func(path string, d iofs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		switch {
		case st.IsDir():
			return f.mkdir(rel, st.ModTime())
		case st.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return f.writeFile(rel, data, st.ModTime())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return f.Image(), nil
}
