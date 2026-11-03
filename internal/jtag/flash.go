package jtag

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
)

// Flash geometry the ROM functions work in (esp_rom_spiflash.h: sector
// 4 KB, block 64 KB, page 256 bytes, as passed to config_param)
const (
	sectorSize = 0x1000
	blockSize  = 0x10000
)

func words(b []byte) []uint32 {
	w := make([]uint32, (len(b)+3)/4)
	var pad [4]byte
	for i := range w {
		if 4*i+4 <= len(b) {
			w[i] = binary.LittleEndian.Uint32(b[4*i:])
		} else {
			copy(pad[:], []byte{0xFF, 0xFF, 0xFF, 0xFF})
			copy(pad[:], b[4*i:])
			w[i] = binary.LittleEndian.Uint32(pad[:])
		}
	}
	return w
}

func wordBytes(w []uint32) []byte {
	b := make([]byte, 4*len(w))
	for i, v := range w {
		binary.LittleEndian.PutUint32(b[4*i:], v)
	}
	return b
}

func (s *Session) romResult(name string, r uint32, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if r != 0 {
		what := "error"
		if r == 2 {
			what = "timeout"
		}
		return fmt.Errorf("%w: %s returned %d (%s)", ErrFlash, name, r, what)
	}
	return nil
}

// readAligned reads [addr, addr+n) with addr and n multiples of 4 and n
// at most bufSize: the ROM copies the flash into the buffer, then the
// buffer is read out
func (s *Session) readAligned(ctx context.Context, addr uint32, n int) ([]byte, error) {
	buf := s.t.work + workBuf
	r, err := s.call(ctx, s.t.rom.read, callShort, addr, buf, uint32(n))
	if err := s.romResult("esp_rom_spiflash_read", r, err); err != nil {
		return nil, err
	}
	w, err := s.cpu.readMem(buf, n/4)
	if err != nil {
		return nil, err
	}
	return wordBytes(w), nil
}

// readFlash reads any range
func (s *Session) readFlash(ctx context.Context, addr uint32, size int, progress func(done, total int)) ([]byte, error) {
	out := make([]byte, 0, size)
	start := addr &^ 3
	skip := int(addr - start)
	end := (int(addr) + size + 3) &^ 3
	for a := int(start); a < end; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := min(bufSize, end-a)
		b, err := s.readAligned(ctx, uint32(a), n)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		a += n
		if progress != nil {
			progress(min(len(out)-skip, size), size)
		}
	}
	return out[skip : skip+size], nil
}

// ReadFlash reads size bytes of the flash at addr
func (s *Session) ReadFlash(ctx context.Context, addr uint32, size int, progress func(done, total int)) ([]byte, error) {
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if size <= 0 {
		return nil, nil
	}
	return s.readFlash(ctx, addr, size, progress)
}

// erase erases a sector-aligned range: 64 KB blocks where aligned,
// 4 KB sectors elsewhere
func (s *Session) erase(ctx context.Context, addr uint32, size int) error {
	if addr%sectorSize != 0 || size%sectorSize != 0 {
		return fmt.Errorf("erase 0x%x+0x%x: not aligned to 4 KB sectors", addr, size)
	}
	end := addr + uint32(size)
	for a := addr; a < end; {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a%blockSize == 0 && end-a >= blockSize {
			r, err := s.call(ctx, s.t.rom.eraseBlock, callErase, a/blockSize)
			if err := s.romResult("esp_rom_spiflash_erase_block", r, err); err != nil {
				return err
			}
			a += blockSize
			continue
		}
		r, err := s.call(ctx, s.t.rom.eraseSector, callErase, a/sectorSize)
		if err := s.romResult("esp_rom_spiflash_erase_sector", r, err); err != nil {
			return err
		}
		a += sectorSize
	}
	return nil
}

// EraseRegion erases [addr, addr+size); both must be multiples of 4 KB
func (s *Session) EraseRegion(ctx context.Context, addr uint32, size int) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.erase(ctx, addr, size)
}

// EraseFlash erases the whole flash
func (s *Session) EraseFlash(ctx context.Context) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	s.status("Erasing the flash")
	r, err := s.call(ctx, s.t.rom.eraseChip, callEraseChip)
	return s.romResult("esp_rom_spiflash_erase_chip", r, err)
}

// WriteFlash writes data at addr. The 4 KB sectors it touches are erased
// first; what they held outside the data is read before and written back.
// The result is verified by MD5.
func (s *Session) WriteFlash(ctx context.Context, addr uint32, data []byte, progress func(done, total int)) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if len(data) == 0 {
		return nil
	}
	start := addr &^ (sectorSize - 1)
	end := (addr + uint32(len(data)) + sectorSize - 1) &^ (sectorSize - 1)
	img := make([]byte, end-start)
	if start < addr {
		head, err := s.readFlash(ctx, start, int(addr-start), nil)
		if err != nil {
			return err
		}
		copy(img, head)
	}
	tail := addr + uint32(len(data))
	if tail < end {
		b, err := s.readFlash(ctx, tail, int(end-tail), nil)
		if err != nil {
			return err
		}
		copy(img[tail-start:], b)
	}
	copy(img[addr-start:], data)

	total := 2 * len(img)
	report := func(done int) {
		if progress != nil {
			progress(done, total)
		}
	}
	s.status(fmt.Sprintf("Erasing 0x%x..0x%x", start, end))
	if err := s.erase(ctx, start, len(img)); err != nil {
		return err
	}
	buf := s.t.work + workBuf
	blank := bytes.Repeat([]byte{0xFF}, bufSize)
	for off := 0; off < len(img); off += bufSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := img[off:min(off+bufSize, len(img))]
		if !bytes.Equal(chunk, blank[:len(chunk)]) {
			if err := s.cpu.writeMem(buf, words(chunk)); err != nil {
				return err
			}
			r, err := s.call(ctx, s.t.rom.write, callErase, start+uint32(off), buf, uint32(len(chunk)))
			if err := s.romResult("esp_rom_spiflash_write", r, err); err != nil {
				return err
			}
		}
		report(off + len(chunk))
	}
	sum, err := s.flashMD5(ctx, start, len(img), func(done, _ int) { report(len(img) + done) })
	if err != nil {
		return err
	}
	if sum != md5Of(img) {
		return fmt.Errorf("%w (0x%x..0x%x)", ErrVerify, start, end)
	}
	return nil
}

// flashMD5 computes the MD5 of a flash range on the chip with the ROM's
// MD5 functions, over the RAM buffer, so that only the digest comes back
// over JTAG. Ranges not aligned to 4 bytes are read back instead.
func (s *Session) flashMD5(ctx context.Context, addr uint32, size int, progress func(done, total int)) ([16]byte, error) {
	var sum [16]byte
	rom := s.t.rom
	if addr%4 != 0 || size%4 != 0 || rom.md5Init == 0 {
		b, err := s.readFlash(ctx, addr, size, progress)
		if err != nil {
			return sum, err
		}
		return md5Of(b), nil
	}
	mctx, buf, digest := s.t.work+workMD5, s.t.work+workBuf, s.t.work+workDigest
	if _, err := s.call(ctx, rom.md5Init, callShort, mctx); err != nil {
		return sum, fmt.Errorf("MD5Init: %w", err)
	}
	for off := 0; off < size; off += bufSize {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		n := min(bufSize, size-off)
		r, err := s.call(ctx, rom.read, callShort, addr+uint32(off), buf, uint32(n))
		if err := s.romResult("esp_rom_spiflash_read", r, err); err != nil {
			return sum, err
		}
		if _, err := s.call(ctx, rom.md5Update, callShort, mctx, buf, uint32(n)); err != nil {
			return sum, fmt.Errorf("MD5Update: %w", err)
		}
		if progress != nil {
			progress(off+n, size)
		}
	}
	if _, err := s.call(ctx, rom.md5Final, callShort, digest, mctx); err != nil {
		return sum, fmt.Errorf("MD5Final: %w", err)
	}
	w, err := s.cpu.readMem(digest, 4)
	if err != nil {
		return sum, err
	}
	copy(sum[:], wordBytes(w))
	return sum, nil
}

// FlashMD5 is the MD5 of a flash range, computed on the chip
func (s *Session) FlashMD5(ctx context.Context, addr uint32, size int) ([16]byte, error) {
	if err := s.lock(ctx); err != nil {
		return [16]byte{}, err
	}
	defer s.mu.Unlock()
	return s.flashMD5(ctx, addr, size, nil)
}
