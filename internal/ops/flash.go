package ops

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"sort"

	"github.com/ipoluianov/altesp/internal/chips"
	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/loader"
)

// Item is a file to write at an address
type Item struct {
	Name string // shown in the messages
	Addr uint32
	Data []byte
}

// Severity of a Problem
type Severity int

const (
	// Warning: the write may go on after the user saw it
	Warning Severity = iota
	// Fatal: the write would break the board or fail
	Fatal
)

// Problem is something wrong with what is about to be written
type Problem struct {
	Severity Severity
	Item     string // the name of the file, "" for the whole set
	Text     string
}

func (p Problem) String() string {
	if p.Item == "" {
		return p.Text
	}
	return p.Item + ": " + p.Text
}

// sector is the erase unit of the flash
const sector = 0x1000

// Check looks at the files before they are written: that they fit into the
// flash and don't overlap, that the images are built for this chip and its
// revision, that the bootloader goes where the ROM looks for it
func Check(b Board, items []Item) []Problem {
	var ps []Problem
	add := func(sev Severity, item, format string, a ...any) {
		ps = append(ps, Problem{Severity: sev, Item: item, Text: fmt.Sprintf(format, a...)})
	}
	sorted := append([]Item(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Addr < sorted[j].Addr })
	for i, it := range sorted {
		end := uint64(it.Addr) + uint64(len(it.Data))
		if end > uint64(b.FlashSize) {
			sev := Fatal
			if !b.FlashSizeKnown {
				sev = Warning
			}
			add(sev, it.Name, "ends at 0x%X, past the end of the %d MB flash", end, b.FlashSize>>20)
		}
		if i > 0 {
			prev := sorted[i-1]
			if uint64(prev.Addr)+uint64(len(prev.Data)) > uint64(it.Addr) {
				add(Fatal, it.Name, "overlaps %s", prev.Name)
			}
		}
		if it.Addr%sector != 0 {
			add(Warning, it.Name, "0x%X is not at a 4 KB sector boundary: the start of its sector is erased too", it.Addr)
		}
		ps = append(ps, checkImage(b, it)...)
	}
	if b.SecureDownload {
		add(Warning, "", "Secure Download Mode: the written data can't be verified")
	}
	if b.Security.FlashEncryption {
		add(Warning, "", "Flash encryption is on: plain images written as they are won't boot")
	}
	return ps
}

// checkImage checks an application or bootloader image against the chip
func checkImage(b Board, it Item) []Problem {
	kind := formats.Detect(it.Data)
	if kind != formats.App && kind != formats.Bootloader {
		return nil
	}
	img, err := formats.ParseImage(it.Data)
	if err != nil {
		return []Problem{{Warning, it.Name, "looks like an image but can't be read: " + err.Error()}}
	}
	var ps []Problem
	c := b.Chip
	if c != nil && img.ChipID != c.ChipID {
		other := fmt.Sprintf("chip ID %d", img.ChipID)
		if oc := chips.ByChipID(uint32(img.ChipID)); oc != nil {
			other = oc.Name
		}
		ps = append(ps, Problem{Fatal, it.Name, fmt.Sprintf("is built for %s, the board is %s", other, c.Name)})
	}
	if rev := b.Info.Revision.Full(); c != nil && b.Info.Revision != (chips.Revision{}) {
		if img.MinRevFull != 0 && rev < int(img.MinRevFull) {
			ps = append(ps, Problem{Fatal, it.Name, fmt.Sprintf("needs chip revision v%d.%d or newer, the chip is %s",
				img.MinRevFull/100, img.MinRevFull%100, b.Info.Revision)})
		}
		if img.MaxRevFull != 0 && img.MaxRevFull != 0xFFFF && rev > int(img.MaxRevFull) {
			ps = append(ps, Problem{Fatal, it.Name, fmt.Sprintf("supports chip revisions up to v%d.%d, the chip is %s",
				img.MaxRevFull/100, img.MaxRevFull%100, b.Info.Revision)})
		}
	}
	if kind == formats.Bootloader && c != nil && it.Addr != c.BootloaderAddr {
		ps = append(ps, Problem{Warning, it.Name, fmt.Sprintf("is a bootloader at 0x%X; the %s ROM loads it from 0x%X", it.Addr, c.Name, c.BootloaderAddr)})
	}
	if !img.ChecksumOK || img.HashAppended && !img.HashOK {
		ps = append(ps, Problem{Warning, it.Name, "its checksum or hash doesn't match: the file may be damaged"})
	}
	return ps
}

// FlashOptions of Flash
type FlashOptions struct {
	// Verify compares the MD5 of the flash with the file after writing
	Verify bool
	// OnlyChanged skips the files already on the board
	OnlyChanged bool
	// NoCompress sends the data as it is
	NoCompress bool
}

// FileProgress tells the progress of the file number index
type FileProgress func(index, done, total int)

// ErrVerify is a file whose MD5 on the board differs after writing
var ErrVerify = errors.New("verification failed: the flash differs from the file")

// Flash writes the files one by one
func (s *Session) Flash(ctx context.Context, items []Item, opt FlashOptions, progress FileProgress) error {
	b := &s.board
	// Secure Download Mode has neither compressed writing nor MD5
	compress := !opt.NoCompress && !b.SecureDownload
	canMD5 := !b.SecureDownload
	for i, it := range items {
		report := func(done, total int) {
			if progress != nil {
				progress(i, done, total)
			}
		}
		want := md5.Sum(it.Data)
		if opt.OnlyChanged && canMD5 {
			if got, err := s.l.FlashMD5(ctx, it.Addr, len(it.Data)); err == nil && got == want {
				report(len(it.Data), len(it.Data))
				continue
			}
		}
		if err := s.l.WriteFlash(ctx, it.Addr, it.Data, compress, report); err != nil {
			return fmt.Errorf("%s: %w", it.Name, err)
		}
		if opt.Verify && canMD5 {
			got, err := s.l.FlashMD5(ctx, it.Addr, len(it.Data))
			if err != nil {
				return fmt.Errorf("%s: %w", it.Name, err)
			}
			if got != want {
				return fmt.Errorf("%s: %w", it.Name, ErrVerify)
			}
		}
	}
	return nil
}

// ErrCantRead is a board whose flash can't be read
var ErrCantRead = errors.New("the flash of this board can't be read: the stub loader is not running and the ROM can't read")

// Read reads a region of the flash
func (s *Session) Read(ctx context.Context, addr uint32, size int, progress loader.Progress) ([]byte, error) {
	if !s.board.CanRead || s.board.SecureDownload {
		return nil, ErrCantRead
	}
	return s.l.ReadFlash(ctx, addr, size, progress)
}

// EraseAll erases the whole flash. Without the stub the ROM erases it
// region by region as it would before a write.
func (s *Session) EraseAll(ctx context.Context) error {
	if s.board.SecureDownload {
		return errors.New("Secure Download Mode doesn't allow erasing")
	}
	if s.board.Stub {
		return s.l.EraseFlash(ctx)
	}
	return s.l.EraseRegion(ctx, 0, s.board.FlashSize)
}

// Erase erases whole sectors
func (s *Session) Erase(ctx context.Context, addr uint32, size int) error {
	if s.board.SecureDownload {
		return errors.New("Secure Download Mode doesn't allow erasing")
	}
	return s.l.EraseRegion(ctx, addr, size)
}
