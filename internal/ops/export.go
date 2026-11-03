package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ipoluianov/altesp/internal/formats"
)

// ExportKind is how the firmware of the board is saved
type ExportKind int

const (
	// ExportWhole: the whole flash, one file
	ExportWhole ExportKind = iota
	// ExportParts: a folder of the parts — bootloader, partition table,
	// partitions — with flasher_args.json and flash_args, as an ESP-IDF
	// build: it is flashed back as one
	ExportParts
	// ExportTrimmed: one file from 0x0 to the last byte that isn't FF
	ExportTrimmed
	// ExportApp: the application that boots, alone: for OTA
	ExportApp
)

// ExportOptions of Export
type ExportOptions struct {
	Kind ExportKind
	// UserData adds to the parts the data of the board: NVS, file
	// systems, PHY calibration, coredump and the other data partitions
	UserData bool
}

// ExportFile is a file of an export and where it goes in the flash
type ExportFile struct {
	Name string
	Addr uint32
	Data []byte
}

// Export reads what the kind saves. The warnings tell what the files can't
// do, such as an encrypted flash read as ciphertext.
func (s *Session) Export(ctx context.Context, opt ExportOptions, progress func(done, total int)) (files []ExportFile, warnings []string, err error) {
	b := s.board
	if !b.CanRead || b.SecureDownload {
		return nil, nil, ErrCantRead
	}
	if b.Security.FlashEncryption {
		warnings = append(warnings, "Flash encryption is on: the files are ciphertext and can only be flashed back onto this chip")
	}
	switch opt.Kind {
	case ExportWhole:
		data, err := s.Read(ctx, 0, b.FlashSize, progress)
		if err != nil {
			return nil, nil, err
		}
		return []ExportFile{{"flash.bin", 0, data}}, warnings, nil
	case ExportTrimmed:
		end, err := s.usedEnd(ctx, 0, b.FlashSize)
		if err != nil {
			return nil, nil, err
		}
		if end == 0 {
			return nil, nil, errors.New("the flash is empty: nothing to save")
		}
		data, err := s.Read(ctx, 0, end, progress)
		if err != nil {
			return nil, nil, err
		}
		return []ExportFile{{"flash.bin", 0, trimFF(data)}}, warnings, nil
	case ExportApp:
		p, ok := ActivePartition(b.Partitions, b.OTA)
		if !ok {
			return nil, nil, errors.New("no application boots on this board")
		}
		data, err := s.readImage(ctx, p.Offset, int(p.Size), progress)
		if err != nil {
			return nil, nil, err
		}
		if data == nil {
			return nil, nil, fmt.Errorf("the partition %s holds no application", p.Name)
		}
		return []ExportFile{{p.Name + ".bin", p.Offset, data}}, warnings, nil
	case ExportParts:
		files, err := s.exportParts(ctx, opt, progress)
		return files, warnings, err
	}
	return nil, nil, fmt.Errorf("unknown export %d", opt.Kind)
}

// exportParts reads the parts of the flash. An application is cut to its
// image: the bootloader reads no further, and what is left after it on
// another board does no harm. A data partition is kept whole — a shorter
// file would leave the old pages of the other board after it — and an
// erased one is left out, but otadata, which tells the slot that boots.
func (s *Session) exportParts(ctx context.Context, opt ExportOptions, progress func(done, total int)) ([]ExportFile, error) {
	b := s.board
	if b.Partitions == nil {
		return nil, errors.New("no partition table was found on the board: save the whole flash instead")
	}
	type part struct {
		name       string
		addr, size uint32
		app, keep  bool
	}
	boot := b.Chip.BootloaderAddr
	parts := []part{{name: "bootloader", addr: boot, size: b.PartitionTableAddr - boot, app: true}}
	parts = append(parts, part{name: "partition-table", addr: b.PartitionTableAddr, size: 0xC00, keep: true})
	for _, p := range b.Partitions {
		firmware := p.IsApp() || p.SubTypeName() == "ota"
		if !firmware && !opt.UserData {
			continue
		}
		parts = append(parts, part{name: p.Name, addr: p.Offset, size: p.Size, app: p.IsApp(), keep: p.SubTypeName() == "ota"})
	}
	total := 0
	for _, p := range parts {
		total += int(p.size)
	}
	done := 0
	report := func(d, _ int) {
		if progress != nil {
			progress(done+d, total)
		}
	}
	var files []ExportFile
	names := map[string]bool{}
	for _, p := range parts {
		var data []byte
		var err error
		switch {
		case p.app:
			data, err = s.readImage(ctx, p.addr, int(p.size), report)
		case p.keep:
			data, err = s.Read(ctx, p.addr, int(p.size), report)
		default:
			var erased bool
			if erased, err = s.Erased(ctx, p.addr, int(p.size)); err == nil && !erased {
				data, err = s.Read(ctx, p.addr, int(p.size), report)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
		done += int(p.size)
		report(0, total)
		if data == nil {
			continue
		}
		files = append(files, ExportFile{Name: fileName(p.name, names), Addr: p.addr, Data: data})
	}
	return files, nil
}

// readImage reads the image at the address, up to size: only up to the
// last sector that isn't erased, then cut to the image with its signature
// block. Data that isn't an image is kept up to that sector; an erased
// region is nil.
func (s *Session) readImage(ctx context.Context, addr uint32, size int, progress func(done, total int)) ([]byte, error) {
	end, err := s.usedEnd(ctx, addr, size)
	if err != nil || end == 0 {
		return nil, err
	}
	data, err := s.Read(ctx, addr, end, progress)
	if err != nil {
		return nil, err
	}
	img, err := formats.ParseImage(data)
	if err != nil || img.Size <= 0 || img.Size > len(data) {
		return data, nil
	}
	n := img.Size
	if img.Signed {
		// The signature block sits in the 4 KB sector after the image
		n = min(len(data), (n+sector-1)/sector*sector+sector)
	}
	return data[:n], nil
}

// usedEnd is the length of the region up to the end of its last sector
// that isn't erased: the chip tells by the MD5 of 64 KB blocks, from the
// end, then of the sectors of the last block with data. 0 is all erased.
func (s *Session) usedEnd(ctx context.Context, addr uint32, size int) (int, error) {
	const block = 0x10000
	for end := size; end > 0; {
		from := max(0, (end-1)/block*block)
		erased, err := s.Erased(ctx, addr+uint32(from), end-from)
		if err != nil {
			return 0, err
		}
		if !erased {
			for e := end; e > from; e -= sector {
				f := max(from, e-sector)
				erased, err := s.Erased(ctx, addr+uint32(f), e-f)
				if err != nil {
					return 0, err
				}
				if !erased {
					return e, nil
				}
			}
		}
		end = from
	}
	return 0, nil
}

// trimFF cuts the bytes FF at the end
func trimFF(data []byte) []byte {
	n := len(data)
	for n > 0 && data[n-1] == 0xFF {
		n--
	}
	return data[:n]
}

// fileName is the file of a part: its name in letters, digits and _-., once
func fileName(name string, used map[string]bool) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, name)
	if clean == "" {
		clean = "part"
	}
	out := clean + ".bin"
	for i := 2; used[out]; i++ {
		out = fmt.Sprintf("%s-%d.bin", clean, i)
	}
	used[out] = true
	return out
}

// WriteExport writes the files of an export. One file goes to path; the
// parts go into a new folder under path named name (name-2 ... if taken),
// with flasher_args.json and flash_args: it is a build folder. Returns
// the file or the folder written.
func (s *Session) WriteExport(files []ExportFile, kind ExportKind, path, name string) (string, error) {
	if kind != ExportParts {
		if len(files) != 1 {
			return "", errors.New("one file expected")
		}
		return path, os.WriteFile(path, files[0].Data, 0o644)
	}
	dir := filepath.Join(path, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(path, fmt.Sprintf("%s-%d", name, i))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Addr < files[j].Addr })
	flashFiles := map[string]string{}
	var args strings.Builder
	mode, freq, size := s.flashSettings(files)
	fmt.Fprintf(&args, "--flash_mode %s --flash_freq %s --flash_size %s\n", mode, freq, size)
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644); err != nil {
			return "", err
		}
		addr := fmt.Sprintf("0x%x", f.Addr)
		flashFiles[addr] = f.Name
		fmt.Fprintf(&args, "%s %s\n", addr, f.Name)
	}
	chip := ""
	if s.board.Chip != nil {
		chip = s.board.Chip.Key
	}
	fa := map[string]any{
		"write_flash_args": []string{"--flash_mode", mode, "--flash_size", size, "--flash_freq", freq},
		"flash_settings":   map[string]string{"flash_mode": mode, "flash_size": size, "flash_freq": freq},
		"flash_files":      flashFiles,
		"extra_esptool_args": map[string]any{
			"after": "hard_reset", "before": "default_reset", "stub": true, "chip": chip,
		},
	}
	raw, _ := json.MarshalIndent(fa, "", "    ")
	if err := os.WriteFile(filepath.Join(dir, "flasher_args.json"), append(raw, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "flash_args"), []byte(args.String()), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// flashSettings are the flash mode, frequency and size the bootloader's
// header tells, in the words of esptool; "keep" for what isn't known
func (s *Session) flashSettings(files []ExportFile) (mode, freq, size string) {
	mode, freq, size = "keep", "keep", "keep"
	if s.board.Chip == nil {
		return
	}
	for _, f := range files {
		if f.Addr != s.board.Chip.BootloaderAddr || len(f.Data) < 4 || f.Data[0] != 0xE9 {
			continue
		}
		for n, c := range flashModes {
			if c == f.Data[2] {
				mode = strings.ToLower(n)
			}
		}
		for n, c := range flashFreqs[s.board.Chip.Key] {
			if c == f.Data[3]&0x0F {
				freq = strings.ToLower(strings.SplitN(strings.TrimSuffix(n, "MHz"), "MHz", 2)[0]) + "m"
			}
		}
		if c := f.Data[3] >> 4; c <= flashSizeMax[s.board.Chip.Key] {
			size = fmt.Sprintf("%dMB", 1<<c)
		}
	}
	return
}
