package ops

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/formats/fatfs"
	"github.com/ipoluianov/altesp/internal/formats/littlefs"
	"github.com/ipoluianov/altesp/internal/formats/nvs"
	"github.com/ipoluianov/altesp/internal/formats/spiffs"
)

// The image generators of the "Tools" menu

// BuildNVS makes an NVS partition image of size bytes from a CSV in the
// format of ESP-IDF's nvs_partition_gen; files the CSV names are found
// next to it. version 1 is for firmware built before ESP-IDF v3.2, 0 or 2
// for the others.
func BuildNVS(csvPath string, size, version int) ([]byte, error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := nvs.ReadCSV(f, filepath.Dir(csvPath))
	if err != nil {
		return nil, err
	}
	if version == 0 {
		version = nvs.Version2
	}
	return nvs.BuildWith(entries, size, nvs.Options{Version: version})
}

// FS kinds the generator makes
const (
	FSLittleFS = "LittleFS"
	FSSPIFFS   = "SPIFFS"
	FSFAT      = "FAT"
)

// BuildFS makes a file system image of size bytes from a folder, with the
// parameters ESP-IDF and Arduino use by default (FAT with wear levelling)
func BuildFS(kind, dir string, size int) ([]byte, error) {
	switch kind {
	case FSLittleFS:
		return littlefs.FromDir(dir, size, 4096)
	case FSSPIFFS:
		return spiffs.FromDir(dir, size, spiffs.IDF)
	case FSFAT:
		return fatfs.FromDir(dir, size, fatfs.IDF)
	}
	return nil, fmt.Errorf("unknown file system %s", kind)
}

// BuildMerged puts the files at their addresses into one image that is
// flashed at 0: the gaps are erased flash (0xFF). Files must not overlap.
func BuildMerged(files []File) ([]byte, error) {
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Addr < sorted[j].Addr })
	var out []byte
	for _, f := range sorted {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, err
		}
		if int(f.Addr) < len(out) {
			return nil, fmt.Errorf("%s at 0x%X overlaps the file before it", filepath.Base(f.Path), f.Addr)
		}
		out = append(out, bytes.Repeat([]byte{0xFF}, int(f.Addr)-len(out))...)
		out = append(out, data...)
	}
	return out, nil
}

// BuildPartitionTable writes a partition table binary
func BuildPartitionTable(parts []Partition) ([]byte, error) {
	return formats.BuildPartitionTable(parts)
}
