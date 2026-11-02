package loader

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	// The data blocks of a flash write: the ROM takes small ones, the
	// stub writes one block while the next comes
	romFlashBlock  = 0x400
	stubFlashBlock = 0x4000
	// sectorSize is the erase sector of the SPI flash
	sectorSize = 0x1000
	// mb is the unit of the timeouts that grow with the size
	mb = 1 << 20
)

// timeoutFor gives an operation on size bytes per a time per megabyte,
// never less than the default
func timeoutFor(size int, perMB time.Duration) time.Duration {
	t := time.Duration(int64(perMB) * int64(size) / mb)
	return max(t, defaultTimeout)
}

const (
	eraseTimeoutPerMB = 30 * time.Second
	md5TimeoutPerMB   = 8 * time.Second
	// eraseChipTimeout: a whole 16 MB chip takes up to two minutes
	eraseChipTimeout = 120 * time.Second
)

// Progress is told how many bytes of the total are done
type Progress func(done, total int)

// WriteFlash writes data at addr, compressed. The flash under it is erased
// as it is written. Compress is off for the rare chip whose ROM can't
// inflate; then the data goes as is.
func (l *Loader) WriteFlash(ctx context.Context, addr uint32, data []byte, compress bool, progress Progress) error {
	if compress {
		return l.writeDeflated(ctx, addr, data, progress)
	}
	return l.writePlain(ctx, addr, data, progress)
}

func (l *Loader) flashBlock() int {
	if l.stub {
		return stubFlashBlock
	}
	return romFlashBlock
}

// beginWords are the words of FLASH_BEGIN and FLASH_DEFL_BEGIN: the ROM of
// the chips after the ESP32 takes a fifth one, "encrypted" (0)
func (l *Loader) beginWords(size, blocks, block, addr uint32) []byte {
	if !l.stub && l.encryptWord {
		return words(size, blocks, block, addr, 0)
	}
	return words(size, blocks, block, addr)
}

func (l *Loader) writePlain(ctx context.Context, addr uint32, data []byte, progress Progress) error {
	block := l.flashBlock()
	blocks := (len(data) + block - 1) / block
	if _, err := l.command(ctx, opFlashBegin, l.beginWords(uint32(len(data)), uint32(blocks), uint32(block), addr),
		0, timeoutFor(len(data), eraseTimeoutPerMB)); err != nil {
		return err
	}
	for seq := 0; seq < blocks; seq++ {
		chunk := make([]byte, block)
		n := copy(chunk, data[seq*block:])
		// The last block is padded with 0xFF, which is erased flash
		for i := n; i < block; i++ {
			chunk[i] = 0xFF
		}
		payload := append(words(uint32(block), uint32(seq), 0, 0), chunk...)
		if _, err := l.command(ctx, opFlashData, payload, checksum(chunk), timeoutFor(block, eraseTimeoutPerMB)); err != nil {
			return err
		}
		if progress != nil {
			progress(min((seq+1)*block, len(data)), len(data))
		}
	}
	return nil
}

func (l *Loader) writeDeflated(ctx context.Context, addr uint32, data []byte, progress Progress) error {
	var buf bytes.Buffer
	w, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	w.Write(data)
	w.Close()
	comp := buf.Bytes()

	block := l.flashBlock()
	blocks := (len(comp) + block - 1) / block
	size := uint32(len(data))
	if !l.stub {
		// The ROM takes the size to erase, whole sectors
		size = (size + sectorSize - 1) / sectorSize * sectorSize
	}
	if _, err := l.command(ctx, opFlashDeflBegin, l.beginWords(size, uint32(blocks), uint32(block), addr),
		0, timeoutFor(len(data), eraseTimeoutPerMB)); err != nil {
		return err
	}
	for seq := 0; seq < blocks; seq++ {
		chunk := comp[seq*block : min((seq+1)*block, len(comp))]
		payload := append(words(uint32(len(chunk)), uint32(seq), 0, 0), chunk...)
		// A block of compressed data may stand for much more flash
		written := len(data) * (seq + 1) / blocks
		timeout := timeoutFor(len(data)/blocks+1, eraseTimeoutPerMB)
		if _, err := l.command(ctx, opFlashDeflData, payload, checksum(chunk), timeout); err != nil {
			return err
		}
		if progress != nil {
			progress(written, len(data))
		}
	}
	return nil
}

// The write has no end command here: FLASH_END would leave the loader.
// The stub writes the last block while the next command comes, and a
// failure of it is the answer to that command — the MD5 check that
// follows every write.

// FlashMD5 computes the MD5 of a region of flash on the chip
func (l *Loader) FlashMD5(ctx context.Context, addr uint32, size int) ([16]byte, error) {
	var sum [16]byte
	r, err := l.command(ctx, opSPIFlashMD5, words(addr, uint32(size), 0, 0), 0, timeoutFor(size, md5TimeoutPerMB))
	if err != nil {
		return sum, err
	}
	switch len(r.data) {
	case 16: // the stub: raw bytes
		copy(sum[:], r.data)
	case 32: // the ROM: hex text
		if _, err := hex.Decode(sum[:], r.data); err != nil {
			return sum, fmt.Errorf("SPI_FLASH_MD5: %w", err)
		}
	default:
		return sum, fmt.Errorf("SPI_FLASH_MD5: unexpected answer of %d bytes", len(r.data))
	}
	return sum, nil
}

// ErrNeedsStub is an operation the ROM loader can't do
var ErrNeedsStub = errors.New("the ROM loader can't do this; the stub loader is required")

// EraseFlash erases the whole flash. The ROM can't: callers write 0xFF.
func (l *Loader) EraseFlash(ctx context.Context) error {
	if !l.stub {
		return ErrNeedsStub
	}
	_, err := l.command(ctx, opEraseFlash, nil, 0, eraseChipTimeout)
	return err
}

// EraseRegion erases whole sectors. The ROM can't erase on its own, but
// FLASH_BEGIN erases what it covers: without the stub the region is
// erased that way, writing nothing.
func (l *Loader) EraseRegion(ctx context.Context, addr uint32, size int) error {
	if addr%sectorSize != 0 || size%sectorSize != 0 {
		return fmt.Errorf("erase region 0x%X+0x%X is not aligned to sectors", addr, size)
	}
	timeout := timeoutFor(size, eraseTimeoutPerMB)
	if l.stub {
		_, err := l.command(ctx, opEraseRegion, words(addr, uint32(size)), 0, timeout)
		return err
	}
	block := romFlashBlock
	blocks := (size + block - 1) / block
	_, err := l.command(ctx, opFlashBegin, l.beginWords(uint32(size), uint32(blocks), uint32(block), addr), 0, timeout)
	return err
}

// readPacket is the size of the READ_FLASH data packets (at most 4096)
const readPacket = 0x1000

// readInFlight is how many packets the stub may send before an
// acknowledgement; the stub sends one at a time anyway
const readInFlight = 64

// ReadFlash reads a region of flash. The stub reads fast and checks the
// data with an MD5; the ROM can only read with the slow command, which not
// every chip has.
func (l *Loader) ReadFlash(ctx context.Context, addr uint32, size int, progress Progress) ([]byte, error) {
	if !l.stub {
		return l.readFlashSlow(ctx, addr, size, progress)
	}
	if _, err := l.command(ctx, opReadFlash, words(addr, uint32(size), readPacket, readInFlight), 0, defaultTimeout); err != nil {
		return nil, err
	}
	data := make([]byte, 0, size)
	for len(data) < size {
		p, err := l.conn.recv(ctx, defaultTimeout)
		if err != nil {
			return nil, fmt.Errorf("READ_FLASH at 0x%X: %w", addr+uint32(len(data)), err)
		}
		data = append(data, p...)
		if len(data) > size {
			return nil, fmt.Errorf("READ_FLASH: %d bytes more than asked", len(data)-size)
		}
		ack := make([]byte, 4)
		binary.LittleEndian.PutUint32(ack, uint32(len(data)))
		if err := l.conn.send(ack); err != nil {
			return nil, err
		}
		if progress != nil {
			progress(len(data), size)
		}
	}
	p, err := l.conn.recv(ctx, defaultTimeout)
	if err != nil {
		return nil, fmt.Errorf("READ_FLASH digest: %w", err)
	}
	if sum := md5.Sum(data); !bytes.Equal(p, sum[:]) {
		return nil, errors.New("READ_FLASH: the data doesn't match its MD5")
	}
	return data, nil
}

// slowBlock is what READ_FLASH_SLOW returns at a time
const slowBlock = 64

// readFlashSlow reads through the ROM. The command is not in the protocol
// documentation: it is known for the ESP32 ROM and tried on the others
// (see "Hardware checks" in TZ.md).
func (l *Loader) readFlashSlow(ctx context.Context, addr uint32, size int, progress Progress) ([]byte, error) {
	data := make([]byte, 0, size)
	for len(data) < size {
		r, err := l.command(ctx, opReadFlashSlow, words(addr+uint32(len(data)), slowBlock), 0, defaultTimeout)
		if err != nil {
			var e *Error
			if errors.As(err, &e) && e.Unsupported() || errors.Is(err, ErrTimeout) {
				return nil, fmt.Errorf("%w: %v", ErrNeedsStub, err)
			}
			return nil, err
		}
		if len(r.data) < slowBlock {
			return nil, fmt.Errorf("READ_FLASH_SLOW: short answer (%d bytes)", len(r.data))
		}
		data = append(data, r.data[:min(slowBlock, size-len(data))]...)
		if progress != nil {
			progress(len(data), size)
		}
	}
	return data, nil
}
