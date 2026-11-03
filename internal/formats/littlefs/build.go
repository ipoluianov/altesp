package littlefs

// The image generator: a littlefs image of a host folder

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// FromDir makes an image of size bytes from the files of a host folder,
// with the default configuration (see Config)
func FromDir(dir string, size int, blockSize uint32) ([]byte, error) {
	return Build(dir, size, Config{BlockSize: blockSize})
}

// Build makes an image of size bytes from the files of a host folder.
// Only regular files and folders are taken; links are followed
func Build(dir string, size int, cfg Config) ([]byte, error) {
	fsys, err := Format(make([]byte, size), cfg)
	if err != nil {
		return nil, err
	}
	if err := fsys.AddDir(dir, "/"); err != nil {
		return nil, err
	}
	return fsys.Image(), nil
}

// AddDir copies a host folder into the directory to of the image,
// creating it if needed
func (fsys *FS) AddDir(dir, to string) error {
	if err := fsys.mkdirAll(to); err != nil {
		return err
	}
	list, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, de := range list {
		src := filepath.Join(dir, de.Name())
		dst := path.Join(to, de.Name())
		st, err := os.Stat(src)
		if err != nil {
			return err
		}
		switch {
		case st.IsDir():
			if err := fsys.AddDir(src, dst); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := fsys.WriteFile(dst, data); err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
		}
	}
	return nil
}
