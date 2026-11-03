package ops

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"os"

	"github.com/ipoluianov/altesp/internal/formats"
)

// Partition is a row of the partition table
type Partition = formats.Partition

// OTASlots are the OTA app partitions in the order of their slots
func OTASlots(parts []Partition) []Partition {
	var slots []Partition
	for sub := byte(formats.SubOTAMin); sub <= formats.SubOTAMax; sub++ {
		if p, ok := formats.FindPartition(parts, formats.PartApp, sub); ok {
			slots = append(slots, p)
		}
	}
	return slots
}

// ActivePartition is the app partition the bootloader starts: the OTA slot
// the otadata selects, else factory, else the first OTA slot
func ActivePartition(parts []Partition, ota formats.OTASelect) (Partition, bool) {
	slots := OTASlots(parts)
	if slot, ok := ota.Slot(len(slots)); ok {
		return slots[slot], true
	}
	return formats.AppPartition(parts)
}

// readOTA reads the otadata partition of the board, when there is one
func (s *Session) readOTA(ctx context.Context) {
	b := &s.board
	p, ok := formats.FindPartition(b.Partitions, formats.PartData, formats.SubDataOTA)
	if !ok {
		return
	}
	data, err := s.t.ReadFlash(ctx, p.Offset, int(min(p.Size, 2*sector)), nil)
	if err == nil {
		b.OTA = formats.ParseOTAData(data)
	}
}

// RefreshPartitions reads the partition table and the apps again, after
// something was written
func (s *Session) RefreshPartitions(ctx context.Context) {
	if s.board.CanRead && !s.board.Security.FlashEncryption {
		s.board.Partitions, s.board.Apps = nil, nil
		s.readPartitions(ctx)
	}
}

// partition finds a partition of the board by its name
func (s *Session) partition(name string) (Partition, error) {
	for _, p := range s.board.Partitions {
		if p.Name == name {
			return p, nil
		}
	}
	return Partition{}, fmt.Errorf("no partition %s", name)
}

// ReadPartition reads a whole partition
func (s *Session) ReadPartition(ctx context.Context, name string, progress func(done, total int)) ([]byte, error) {
	p, err := s.partition(name)
	if err != nil {
		return nil, err
	}
	return s.Read(ctx, p.Offset, int(p.Size), progress)
}

// WritePartition writes data at the start of a partition; the rest of the
// partition is erased when erase is set
func (s *Session) WritePartition(ctx context.Context, name string, data []byte, erase bool, progress func(done, total int)) error {
	p, err := s.partition(name)
	if err != nil {
		return err
	}
	if len(data) > int(p.Size) {
		return fmt.Errorf("%s is %s, the file is %s", name, SizeText(int(p.Size)), SizeText(len(data)))
	}
	if erase {
		if err := s.Erase(ctx, p.Offset, int(p.Size)); err != nil {
			return err
		}
	}
	err = s.Flash(ctx, []Item{{Name: name, Addr: p.Offset, Data: data}}, FlashOptions{Verify: true}, func(_, done, total int) {
		if progress != nil {
			progress(done, total)
		}
	})
	s.RefreshPartitions(ctx)
	return err
}

// ErasePartition erases a whole partition
func (s *Session) ErasePartition(ctx context.Context, name string) error {
	p, err := s.partition(name)
	if err != nil {
		return err
	}
	err = s.Erase(ctx, p.Offset, int(p.Size))
	s.RefreshPartitions(ctx)
	return err
}

// SetActiveOTA makes the bootloader start OTA slot n next
func (s *Session) SetActiveOTA(ctx context.Context, slot int) error {
	b := &s.board
	p, ok := formats.FindPartition(b.Partitions, formats.PartData, formats.SubDataOTA)
	if !ok {
		return errors.New("the board has no otadata partition")
	}
	sec, content, err := formats.SelectOTA(b.OTA, slot, len(OTASlots(b.Partitions)))
	if err != nil {
		return err
	}
	addr := p.Offset + uint32(sec)*sector
	if err := s.Erase(ctx, addr, sector); err != nil {
		return err
	}
	if err := s.Flash(ctx, []Item{{Name: "otadata", Addr: addr, Data: content}}, FlashOptions{Verify: true}, nil); err != nil {
		return err
	}
	s.readOTA(ctx)
	if got, ok := b.OTA.Slot(len(OTASlots(b.Partitions))); !ok || got != slot {
		return errors.New("the otadata did not take the new slot")
	}
	return nil
}

// WritePartitionTable checks a new table and writes it in place of the
// board's (or at 0x8000 when the board has none)
func (s *Session) WritePartitionTable(ctx context.Context, parts []Partition) error {
	addr := s.board.PartitionTableAddr
	if addr == 0 {
		addr = 0x8000
	}
	if problems := formats.CheckPartitions(parts, addr, s.board.FlashSize); len(problems) > 0 {
		return fmt.Errorf("the table is not valid: %s", problems[0])
	}
	bin, err := formats.BuildPartitionTable(parts)
	if err != nil {
		return err
	}
	if err := s.Erase(ctx, addr, sector); err != nil {
		return err
	}
	if err := s.Flash(ctx, []Item{{Name: "partition table", Addr: addr, Data: bin}}, FlashOptions{Verify: true}, nil); err != nil {
		return err
	}
	s.RefreshPartitions(ctx)
	return nil
}

// Range is a region of the flash
type Range struct{ Addr, Size uint32 }

// compareBlock is the unit of the comparison
const compareBlock = sector

// Compare finds the blocks of the flash at addr that differ from data, by
// the MD5 the loader computes on the chip: nothing is read back
func (s *Session) Compare(ctx context.Context, addr uint32, data []byte, progress func(done, total int)) ([]Range, error) {
	if s.board.SecureDownload {
		return nil, errors.New("Secure Download Mode can't compare")
	}
	var diff []Range
	for off := 0; off < len(data); off += compareBlock {
		n := min(compareBlock, len(data)-off)
		got, err := s.t.FlashMD5(ctx, addr+uint32(off), n)
		if err != nil {
			return nil, err
		}
		if got != md5.Sum(data[off:off+n]) {
			if k := len(diff); k > 0 && diff[k-1].Addr+diff[k-1].Size == addr+uint32(off) {
				diff[k-1].Size += uint32(n)
			} else {
				diff = append(diff, Range{addr + uint32(off), uint32(n)})
			}
		}
		if progress != nil {
			progress(off+n, len(data))
		}
	}
	return diff, nil
}

// WriteBytes changes bytes in place: the sectors under them are read,
// patched, erased and written again
func (s *Session) WriteBytes(ctx context.Context, addr uint32, data []byte) error {
	start := addr / sector * sector
	end := (addr + uint32(len(data)) + sector - 1) / sector * sector
	old, err := s.Read(ctx, start, int(end-start), nil)
	if err != nil {
		return err
	}
	copy(old[addr-start:], data)
	if err := s.Erase(ctx, start, int(end-start)); err != nil {
		return err
	}
	return s.Flash(ctx, []Item{{Name: "edit", Addr: start, Data: old}}, FlashOptions{Verify: true}, nil)
}

// Image is a flash or partition image opened from a file: what the tabs
// show when no board is connected
type Image struct {
	Path               string
	Data               []byte
	Kind               string
	Partitions         []Partition
	PartitionTableAddr uint32
	Apps               map[string]*formats.Image
	OTA                formats.OTASelect
}

// OpenImage reads a flash dump, a merged image or a partition image
func OpenImage(path string) (*Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	im := &Image{Path: path, Data: data, Kind: formats.Detect(data).String(), Apps: map[string]*formats.Image{}}
	if off, ok := formats.FindPartitionTable(data); ok {
		parts, err := formats.ParsePartitionTable(data[off:min(int(off)+0xC00, len(data))])
		if err == nil {
			im.Partitions, im.PartitionTableAddr = parts, off
		}
	}
	for _, p := range im.Partitions {
		end := int(p.Offset) + int(p.Size)
		if end > len(data) {
			continue
		}
		if p.IsApp() && int(p.Offset)+formats.AppHeadSize <= len(data) {
			if img, ok := formats.ParseAppHead(data[p.Offset:]); ok {
				im.Apps[p.Name] = img
			}
		}
		if p.Type == formats.PartData && p.SubType == formats.SubDataOTA {
			im.OTA = formats.ParseOTAData(data[p.Offset:end])
		}
	}
	return im, nil
}
