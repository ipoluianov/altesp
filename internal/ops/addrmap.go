package ops

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

// Where an address of the running application is in the flash: the
// segments of the application image (esp_image_segment_header_t: load
// address, length, then the data) map each loaded address to the bytes of
// the image it came from, inside the app partition. Used by "Show in hex"
// of the monitor.

// segment is a loaded segment of an image: its load address, length and
// the flash address of its data
type segment struct{ addr, size, flash uint32 }

// ErrNotInApp is an address no segment of the application holds
var ErrNotInApp = errors.New("the address is not in a segment of the application")

// readAt reads bytes of the flash or of an image
type readAt func(addr uint32, n int) ([]byte, error)

// imageSegments walks the segment headers of the image at base
func imageSegments(read readAt, base uint32) ([]segment, error) {
	h, err := read(base, 24)
	if err != nil {
		return nil, err
	}
	if h[0] != 0xE9 || h[1] == 0 || h[1] > 16 {
		return nil, errors.New("no application image there")
	}
	var segs []segment
	off := uint32(24)
	for i := 0; i < int(h[1]); i++ {
		sh, err := read(base+off, 8)
		if err != nil {
			return nil, err
		}
		addr, size := binary.LittleEndian.Uint32(sh), binary.LittleEndian.Uint32(sh[4:])
		if size > 16<<20 {
			return nil, fmt.Errorf("segment %d is damaged", i)
		}
		segs = append(segs, segment{addr, size, base + off + 8})
		off += 8 + size
	}
	return segs, nil
}

func findSegment(segs []segment, addr uint32) (uint32, error) {
	for _, s := range segs {
		if addr >= s.addr && addr < s.addr+s.size {
			return s.flash + addr - s.addr, nil
		}
	}
	return 0, ErrNotInApp
}

// FlashAddrOf finds where an address of the application that boots is in
// the flash
func (s *Session) FlashAddrOf(ctx context.Context, addr uint32) (uint32, error) {
	app, ok := ActivePartition(s.board.Partitions, s.board.OTA)
	if !ok {
		return 0, errors.New("no application partition is known")
	}
	if s.segs == nil || s.segsOf != app.Offset {
		segs, err := imageSegments(func(a uint32, n int) ([]byte, error) { return s.Read(ctx, a, n, nil) }, app.Offset)
		if err != nil {
			return 0, err
		}
		s.segs, s.segsOf = segs, app.Offset
	}
	return findSegment(s.segs, addr)
}

// FlashAddrOf finds where an address of the application that boots is in
// the image
func (im *Image) FlashAddrOf(addr uint32) (uint32, error) {
	app, ok := ActivePartition(im.Partitions, im.OTA)
	if !ok {
		return 0, errors.New("no application partition is known")
	}
	segs, err := imageSegments(func(a uint32, n int) ([]byte, error) {
		if int(a)+n > len(im.Data) {
			return nil, errors.New("past the end of the image")
		}
		return im.Data[a : int(a)+n], nil
	}, app.Offset)
	if err != nil {
		return 0, err
	}
	return findSegment(segs, addr)
}
