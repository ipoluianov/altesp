package littlefs

// CTZ skip-lists (SPEC.md "0x202 LFS_TYPE_CTZSTRUCT", DESIGN.md "CTZ
// skip-lists"). A file is a list of blocks stored backwards: block n of
// the file (n > 0) starts with ctz(n)+1 pointers, pointer x to block
// n-2^x, and the data follows. Block 0 is all data. The struct keeps the
// last block (the head) and the size.

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

// ctzPointers is the number of pointers at the start of block n
func ctzPointers(n int) int {
	if n == 0 {
		return 0
	}
	return bits.TrailingZeros(uint(n)) + 1
}

// ctzCount is the number of blocks of a file of size bytes. It stops
// counting past max
func ctzCount(size uint64, blockSize, max int) int {
	n := 0
	for size > 0 {
		c := uint64(blockSize - 4*ctzPointers(n))
		size -= min(c, size)
		n++
		if n > max {
			break
		}
	}
	return n
}

// ctzBlocks lists the blocks of a file from the first to the head
func (fsys *FS) ctzBlocks(head, size uint32) ([]uint32, error) {
	if size == 0 {
		return nil, nil
	}
	bs := int(fsys.cfg.BlockSize)
	n := ctzCount(uint64(size), bs, int(fsys.cfg.BlockCount))
	if n > int(fsys.cfg.BlockCount) {
		return nil, fmt.Errorf("%w: file of %d bytes is larger than the image", ErrCorrupt, size)
	}
	blocks := make([]uint32, n)
	b := head
	for i := n - 1; ; i-- {
		if b >= fsys.cfg.BlockCount {
			return nil, fmt.Errorf("%w: file block %d out of range", ErrCorrupt, b)
		}
		blocks[i] = b
		if i == 0 {
			break
		}
		b = le32(fsys.block(b))
	}
	return blocks, nil
}

// readCTZ reads a whole file
func (fsys *FS) readCTZ(head, size uint32) ([]byte, error) {
	blocks, err := fsys.ctzBlocks(head, size)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, size)
	for i, b := range blocks {
		data := fsys.block(b)[4*ctzPointers(i):]
		out = append(out, data[:min(len(data), int(size)-len(out))]...)
	}
	return out, nil
}

// writeCTZ stores data in newly allocated blocks and returns the head
func (fsys *FS) writeCTZ(data []byte) (uint32, error) {
	var blocks []uint32
	for off := 0; off < len(data); {
		n := len(blocks)
		b, err := fsys.alloc()
		if err != nil {
			return 0, err
		}
		blk := fsys.block(b)
		p := 0
		for x := 0; x < ctzPointers(n); x++ {
			binary.LittleEndian.PutUint32(blk[p:], blocks[n-1<<x])
			p += 4
		}
		off += copy(blk[p:], data[off:])
		blocks = append(blocks, b)
	}
	return blocks[len(blocks)-1], nil
}
