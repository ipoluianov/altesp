package spiffs

import (
	"encoding/binary"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Writing works as SPIFFS on the chip does: a file is written to free
// pages (lookup entry 0xFFFF), a deleted page has its lookup entry and its
// deleted flag cleared, so only bits go from 1 to 0 within a block. When
// the free pages run out, the image is compacted: the files are written
// anew into erased blocks, each block's erase count one more.
//
// SPIFFS on the chip needs two free blocks to allocate anything (it
// returns SPIFFS_ERR_FULL otherwise); a write that would leave fewer
// fails with ErrNoSpace, so an image built here can be written to on the
// chip.

// spareBlocks is how many blocks are kept free
const spareBlocks = 2

// Format makes an empty SPIFFS of the size of img with the parameters, as
// SPIFFS_format does: erased blocks with their erase count 0 and the
// magic. img is not changed.
func Format(img []byte, p Params) (*FS, error) {
	g, err := p.geom(len(img))
	if err != nil {
		return nil, err
	}
	f := &FS{g: g, img: make([]byte, len(img))}
	for i := range f.img {
		f.img[i] = 0xFF
	}
	for b := 0; b < g.blocks; b++ {
		f.eraseBlock(b, 0)
	}
	return f, nil
}

// eraseBlock erases a block and writes its erase count and magic
func (f *FS) eraseBlock(b int, count uint16) {
	blk := f.img[b*f.g.BlockSize : (b+1)*f.g.BlockSize]
	for i := range blk {
		blk[i] = 0xFF
	}
	f.put16(f.g.eraseOff(b), count&^idIndex)
	if f.g.UseMagic {
		f.put16(f.g.magicOff(b), f.g.magic(b))
	}
}

// WriteFile creates a file or replaces its contents
func (f *FS) WriteFile(name string, data []byte) error {
	return f.atomic("write", name, func() error {
		c, err := f.checkName(name)
		if err != nil {
			return err
		}
		if o := f.scan().find(c); o != nil {
			f.deleteObject(o.id)
		}
		return f.create(c, data, spareBlocks)
	})
}

// Remove deletes a file, or every file under a directory
func (f *FS) Remove(name string) error {
	return f.atomic("remove", name, func() error {
		if canon(name) == "/" {
			return ErrName
		}
		t := f.scan()
		if o := t.find(name); o != nil {
			f.deleteObject(o.id)
			return nil
		}
		prefix := canon(name) + "/"
		n := 0
		for _, o := range t.objs {
			if strings.HasPrefix(canon(o.name), prefix) {
				f.deleteObject(o.id)
				n++
			}
		}
		if n == 0 {
			return iofs.ErrNotExist
		}
		return nil
	})
}

// Rename gives a file a new name, or every file under a directory a new
// prefix; an existing file of the new name is an error
func (f *FS) Rename(from, to string) error {
	return f.atomic("rename", from, func() error {
		t := f.scan()
		type move struct {
			o    *object
			name string
		}
		var moves []move
		if o := t.find(from); o != nil {
			moves = append(moves, move{o, canon(to)})
		} else {
			prefix := canon(from) + "/"
			for _, o := range t.objs {
				if rest, ok := strings.CutPrefix(canon(o.name), prefix); ok && prefix != "//" {
					moves = append(moves, move{o, canon(to) + "/" + rest})
				}
			}
		}
		if len(moves) == 0 {
			return iofs.ErrNotExist
		}
		for _, m := range moves {
			if _, err := f.checkName(m.name); err != nil {
				return err
			}
			if o := t.find(m.name); o != nil && !slices.ContainsFunc(moves, func(x move) bool { return x.o == o }) {
				return iofs.ErrExist
			}
		}
		// The data is read first: moving a file deletes its pages
		data := make([][]byte, len(moves))
		for i, m := range moves {
			d, err := f.ReadFile(m.o.name)
			if err != nil {
				return err
			}
			data[i] = d
		}
		for _, m := range moves {
			f.deleteObject(m.o.id)
		}
		for i, m := range moves {
			if err := f.create(m.name, data[i], spareBlocks); err != nil {
				return err
			}
		}
		return nil
	})
}

// atomic runs a change and puts the image back when it fails
func (f *FS) atomic(op, name string, fn func() error) error {
	saved := append([]byte(nil), f.img...)
	if err := fn(); err != nil {
		f.img = saved
		if _, ok := err.(*iofs.PathError); ok {
			return err
		}
		return &iofs.PathError{Op: op, Path: name, Err: err}
	}
	return nil
}

// checkName gives the canonical name: it has to fit OBJ_NAME_LEN with its
// terminating zero
func (f *FS) checkName(name string) (string, error) {
	c := canon(name)
	if c == "/" || strings.Contains(c, "//") || strings.IndexByte(c, 0) >= 0 {
		return "", ErrName
	}
	if len(c) > f.g.ObjNameLen-1 {
		return "", fmt.Errorf("%w: %q is longer than %d bytes", ErrName, c, f.g.ObjNameLen-1)
	}
	return c, nil
}

// deleteObject marks every page of an object deleted
func (f *FS) deleteObject(id uint16) {
	for p := 0; p < f.g.pages(); p++ {
		if !f.g.isData(p) {
			continue
		}
		lu := f.lookup(p)
		if lu == idFree || lu == idDeleted || lu&^idIndex != id {
			continue
		}
		f.put16(f.g.luOff(p), idDeleted)
		fl := &f.img[p*f.g.PageSize+4]
		*fl &^= flagDel
		if lu&idIndex != 0 {
			*fl &^= flagIxDel
		}
	}
}

// pagesFor is how many pages a file of n bytes takes: its data pages and
// index pages
func (g *geom) pagesFor(n int) (data, index int) {
	data = (n + g.data - 1) / g.data
	index = 1
	if data > g.hdrIx {
		index += (data - g.hdrIx + g.ixIx - 1) / g.ixIx
	}
	return data, index
}

// create writes a new file; its name is canonical and not taken. spare is
// how many free blocks have to remain.
func (f *FS) create(name string, data []byte, spare int) error {
	nData, nIndex := f.g.pagesFor(len(data))
	pages, ok := f.alloc(nData+nIndex, spare)
	if !ok && spare > 0 {
		if err := f.compact(); err != nil {
			return err
		}
		pages, ok = f.alloc(nData+nIndex, spare)
	}
	if !ok {
		return ErrNoSpace
	}
	id := f.freeID()
	ps := f.g.PageSize
	// Index pages first: the header, then the others by span
	for k := 0; k < nIndex; k++ {
		p := pages[k]
		o := p * ps
		f.put16(f.g.luOff(p), id|idIndex)
		f.put16(o, id|idIndex)
		f.put16(o+2, uint16(k))
		f.img[o+4] = 0xFF &^ (flagUsed | flagFinal | flagIndex)
		if k == 0 {
			binary.LittleEndian.PutUint32(f.img[o+ixPhLen:], uint32(len(data)))
			f.img[o+ixPhLen+4] = typeFile
			nm := f.img[o+ixPhLen+5:][:f.g.ObjNameLen]
			clear(nm)
			copy(nm, name)
		}
	}
	for s := 0; s < nData; s++ {
		p := pages[nIndex+s]
		o := p * ps
		f.put16(f.g.luOff(p), id)
		f.put16(o, id)
		f.put16(o+2, uint16(s))
		f.img[o+4] = 0xFF &^ (flagUsed | flagFinal)
		copy(f.img[o+phLen:o+ps], data[s*f.g.data:])
		// The page index goes to the header or to index page k
		ix, off := pages[0], f.g.hdrLen+2*s
		if s >= f.g.hdrIx {
			k := s - f.g.hdrIx
			ix, off = pages[1+k/f.g.ixIx], ixPhLen+2*(k%f.g.ixIx)
		}
		f.put16(ix*ps+off, uint16(p))
	}
	return nil
}

// freeID is the smallest object id no lookup entry has
func (f *FS) freeID() uint16 {
	used := map[uint16]bool{}
	for p := 0; p < f.g.pages(); p++ {
		if f.g.isData(p) {
			used[f.lookup(p)&^idIndex] = true
		}
	}
	for id := uint16(1); ; id++ {
		if !used[id] {
			return id
		}
	}
}

// alloc finds n free pages, from blocks in use first, so that whole free
// blocks stay; it fails when fewer than spare free blocks would remain. A
// page whose lookup entry is free but which is not erased (a write cut
// short) is not used.
func (f *FS) alloc(n, spare int) ([]int, bool) {
	var partial, empty [][]int
	for b := 0; b < f.g.blocks; b++ {
		var free []int
		for i := f.g.lu; i < f.g.ppb; i++ {
			p := b*f.g.ppb + i
			if f.lookup(p) == idFree && f.erased(p) {
				free = append(free, p)
			}
		}
		switch len(free) {
		case 0:
		case f.g.luEntries:
			empty = append(empty, free)
		default:
			partial = append(partial, free)
		}
	}
	var out []int
	for _, l := range partial {
		out = append(out, l...)
	}
	keep := len(empty)
	for _, l := range empty {
		if len(out) >= n {
			break
		}
		out = append(out, l...)
		keep--
	}
	if len(out) < n || keep < spare {
		return nil, false
	}
	return out[:n], true
}

// erased tells a page that is all 0xFF
func (f *FS) erased(p int) bool {
	for _, b := range f.img[p*f.g.PageSize : (p+1)*f.g.PageSize] {
		if b != 0xFF {
			return false
		}
	}
	return true
}

// compact writes the files anew into erased blocks, like the garbage
// collection of SPIFFS does a block at a time; every block's erase count
// grows by one. The files are packed with no spare blocks asked for: an
// image filled by another tool loses nothing.
func (f *FS) compact() error {
	t := f.scan()
	type file struct {
		name string
		data []byte
	}
	var files []file
	for _, o := range t.objs {
		if d, err := f.ReadFile(o.name); err == nil {
			files = append(files, file{canon(o.name), d})
		}
	}
	slices.SortFunc(files, func(a, b file) int { return strings.Compare(a.name, b.name) })
	for b := 0; b < f.g.blocks; b++ {
		f.eraseBlock(b, f.u16(f.g.eraseOff(b))+1)
	}
	for _, x := range files {
		if err := f.create(x.name, x.data, 0); err != nil {
			return err
		}
	}
	return nil
}

// FromDir builds a SPIFFS image of size bytes from the files of a folder,
// as mkspiffs and spiffsgen.py do: a file "dir/sub/a.txt" is named
// "/sub/a.txt". Like mkspiffs, .DS_Store files and .git folders are left
// out.
func FromDir(dir string, size int, p Params) ([]byte, error) {
	f, err := Format(make([]byte, size), p)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(dir, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" {
			return nil
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return f.WriteFile("/"+filepath.ToSlash(rel), data)
	})
	if err != nil {
		return nil, err
	}
	return f.Image(), nil
}
