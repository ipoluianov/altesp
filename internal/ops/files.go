package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ipoluianov/altesp/internal/formats"
)

// ParseNumber reads an address or a size: "0x10000", "65536", "4MB",
// "64K"
func ParseNumber(s string) (uint32, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := uint64(1)
	for _, u := range []struct {
		suffix string
		mult   uint64
	}{{"MB", 1 << 20}, {"M", 1 << 20}, {"KB", 1 << 10}, {"K", 1 << 10}} {
		if strings.HasSuffix(t, u.suffix) && !strings.HasPrefix(t, "0X") {
			t, mult = strings.TrimSuffix(t, u.suffix), u.mult
			break
		}
	}
	v, err := strconv.ParseUint(strings.TrimSpace(t), 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	v *= mult
	if v > 1<<32-1 {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return uint32(v), nil
}

// LoadItems reads the files of a build into items
func LoadItems(files []formats.FlashFile) ([]Item, error) {
	items := make([]Item, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, err
		}
		name := f.Name
		if name == "" {
			name = filepath.Base(f.Path)
		}
		items = append(items, Item{Name: name, Addr: f.Addr, Data: data})
	}
	return items, nil
}

// ItemsFromArgs reads the files of the command line: a build folder, or
// pairs of an address and a file. A single file without an address goes
// where its kind says: an application to its partition on the board is
// decided by the caller (AppAddress), a merged image to 0.
func ItemsFromArgs(args []string) ([]Item, *formats.Build, error) {
	if len(args) == 1 {
		if st, err := os.Stat(args[0]); err == nil && st.IsDir() {
			b, err := formats.LoadBuild(args[0])
			if err != nil {
				return nil, nil, err
			}
			items, err := LoadItems(b.Files)
			return items, b, err
		}
	}
	if len(args)%2 != 0 {
		return nil, nil, fmt.Errorf("give a build folder, or pairs of an address and a file")
	}
	var items []Item
	for i := 0; i < len(args); i += 2 {
		addr, err := ParseNumber(args[i])
		if err != nil {
			return nil, nil, err
		}
		data, err := os.ReadFile(args[i+1])
		if err != nil {
			return nil, nil, err
		}
		items = append(items, Item{Name: filepath.Base(args[i+1]), Addr: addr, Data: data})
	}
	return items, nil, nil
}

// AppAddress is where a single application image goes on this board: its
// factory or first OTA partition, else 0x10000, where ESP-IDF puts it by
// default
func AppAddress(b Board) uint32 {
	if p, ok := formats.AppPartition(b.Partitions); ok {
		return p.Offset
	}
	return 0x10000
}
