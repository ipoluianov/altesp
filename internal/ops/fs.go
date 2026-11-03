package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/formats/fatfs"
	"github.com/ipoluianov/altesp/internal/formats/littlefs"
	"github.com/ipoluianov/altesp/internal/formats/spiffs"
)

// FileInfo is a file or a folder of a partition's file system
type FileInfo struct {
	Name string
	Dir  bool
	Size int64
}

// FileSystem is the file system of a partition, opened in memory: the
// browser changes it there, and Save writes the image back
type FileSystem interface {
	Kind() string // "LittleFS", "SPIFFS", "FAT"
	ReadDir(path string) ([]FileInfo, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	Remove(path string) error
	Rename(from, to string) error
	Mkdir(path string) error
	Image() []byte
}

// ErrNoFS is a partition whose file system is not recognized
var ErrNoFS = errors.New("no known file system in this partition")

// openers mount an image by the kind formats.Detect gives; the packages
// of the other file systems add theirs
var openers = map[formats.Kind]func(img []byte) (FileSystem, error){
	formats.LittleFS: func(img []byte) (FileSystem, error) {
		f, err := littlefs.Mount(img)
		if err != nil {
			return nil, err
		}
		return lfs{f}, nil
	},
}

// OpenFS mounts the file system of a partition image
func OpenFS(img []byte) (FileSystem, error) {
	open, ok := openers[formats.Detect(img)]
	if !ok {
		return nil, ErrNoFS
	}
	return open(img)
}

// ReadFS reads a partition of the board and mounts its file system
func (s *Session) ReadFS(ctx context.Context, name string, progress func(done, total int)) (FileSystem, error) {
	data, err := s.ReadPartition(ctx, name, progress)
	if err != nil {
		return nil, err
	}
	fs, err := OpenFS(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return fs, nil
}

// WriteFS writes the changed file system back into its partition; only
// the sectors that changed are written
func (s *Session) WriteFS(ctx context.Context, name string, fs FileSystem, progress func(done, total int)) error {
	p, err := s.partition(name)
	if err != nil {
		return err
	}
	img := fs.Image()
	diff, err := s.Compare(ctx, p.Offset, img, nil)
	if err != nil {
		return err
	}
	var items []Item
	for _, r := range diff {
		off := r.Addr - p.Offset
		items = append(items, Item{Name: name, Addr: r.Addr, Data: img[off : off+r.Size]})
	}
	for _, it := range items {
		if err := s.Erase(ctx, it.Addr, len(it.Data)); err != nil {
			return err
		}
	}
	return s.Flash(ctx, items, FlashOptions{Verify: true}, func(_, done, total int) {
		if progress != nil {
			progress(done, total)
		}
	})
}

// lfs is LittleFS as a FileSystem
type lfs struct{ f *littlefs.FS }

func (l lfs) Kind() string                          { return "LittleFS" }
func (l lfs) ReadFile(path string) ([]byte, error)  { return l.f.ReadFile(path) }
func (l lfs) WriteFile(path string, d []byte) error { return l.f.WriteFile(path, d) }
func (l lfs) Remove(path string) error              { return l.f.Remove(path) }
func (l lfs) Rename(from, to string) error          { return l.f.Rename(from, to) }
func (l lfs) Mkdir(path string) error               { return l.f.Mkdir(path) }
func (l lfs) Image() []byte                         { return l.f.Image() }
func (l lfs) ReadDir(path string) ([]FileInfo, error) {
	list, err := l.f.ReadDir(path)
	out := make([]FileInfo, len(list))
	for i, e := range list {
		out[i] = FileInfo{e.Name, e.Dir, e.Size}
	}
	return out, err
}

func init() {
	openers[formats.SPIFFS] = func(img []byte) (FileSystem, error) {
		p, ok := spiffs.Detect(img)
		if !ok {
			return nil, ErrNoFS
		}
		f, err := spiffs.Mount(img, p)
		if err != nil {
			return nil, err
		}
		return spiffsFS{f}, nil
	}
	openers[formats.FAT] = func(img []byte) (FileSystem, error) {
		p, ok := fatfs.Detect(img)
		if !ok {
			return nil, ErrNoFS
		}
		f, err := fatfs.Mount(img, p)
		if err != nil {
			return nil, err
		}
		return fatFS{f}, nil
	}
}

// spiffsFS is SPIFFS as a FileSystem. SPIFFS has no folders: the "/" in
// the names make them.
type spiffsFS struct{ f *spiffs.FS }

// ErrNoFolders is a folder asked of SPIFFS
var ErrNoFolders = errors.New("SPIFFS has no folders: a file name with a / makes one")

func (s spiffsFS) Kind() string                          { return "SPIFFS" }
func (s spiffsFS) ReadFile(path string) ([]byte, error)  { return s.f.ReadFile(path) }
func (s spiffsFS) WriteFile(path string, d []byte) error { return s.f.WriteFile(path, d) }
func (s spiffsFS) Remove(path string) error              { return s.f.Remove(path) }
func (s spiffsFS) Rename(from, to string) error          { return s.f.Rename(from, to) }
func (s spiffsFS) Mkdir(string) error                    { return ErrNoFolders }
func (s spiffsFS) Image() []byte                         { return s.f.Image() }
func (s spiffsFS) ReadDir(path string) ([]FileInfo, error) {
	list, err := s.f.ReadDir(path)
	out := make([]FileInfo, len(list))
	for i, e := range list {
		out[i] = FileInfo{e.Name, e.Dir, e.Size}
	}
	return out, err
}

// fatFS is FAT, with or without wear levelling, as a FileSystem
type fatFS struct{ f *fatfs.FS }

func (s fatFS) Kind() string                          { return "FAT" }
func (s fatFS) ReadFile(path string) ([]byte, error)  { return s.f.ReadFile(path) }
func (s fatFS) WriteFile(path string, d []byte) error { return s.f.WriteFile(path, d) }
func (s fatFS) Remove(path string) error              { return s.f.Remove(path) }
func (s fatFS) Rename(from, to string) error          { return s.f.Rename(from, to) }
func (s fatFS) Mkdir(path string) error               { return s.f.Mkdir(path) }
func (s fatFS) Image() []byte                         { return s.f.Image() }
func (s fatFS) ReadDir(path string) ([]FileInfo, error) {
	list, err := s.f.ReadDir(path)
	out := make([]FileInfo, len(list))
	for i, e := range list {
		out[i] = FileInfo{e.Name, e.Dir, e.Size}
	}
	return out, err
}
