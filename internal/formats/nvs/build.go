package nvs

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Building lays the items out page after page, the way the library and
// nvs_partition_gen do: a namespace item, then the values of that
// namespace; a string goes whole into one page; a blob is split into
// BLOB_DATA chunks that fill the rest of a page (nvs_storage.cpp
// writeMultiPageBlob) and is closed by a BLOB_IDX item. The pages that
// got items are FULL but the last, which is ACTIVE; the rest stay erased,
// and at least one of them must remain.

// writer places items into the image
type writer struct {
	img   []byte
	pages int // pages in the image
	used  int // pages opened
	next  int // next free entry of the last opened page
	v1    bool
}

// open starts a new page
func (w *writer) open() error {
	if w.used >= w.pages-1 {
		return ErrNoSpace
	}
	w.used++
	w.next = 0
	return nil
}

// room makes sure the current page has span free entries
func (w *writer) room(span int) error {
	if w.used == 0 || w.next+span > entryCount {
		return w.open()
	}
	return nil
}

// tailroom is how many data bytes a variable-length item can still take
// in the current page (Page::getVarDataTailroom)
func (w *writer) tailroom() int {
	if w.used == 0 || w.next >= entryCount-1 {
		return 0
	}
	return (entryCount - w.next - 1) * entrySize
}

// put writes an item header and its data entries into the current page,
// which must have room for them
func (w *writer) put(h []byte, data []byte) {
	span := 1 + (len(data)+entrySize-1)/entrySize
	h[2] = byte(span)
	binary.LittleEndian.PutUint32(h[4:], crc(0xFFFFFFFF, h[0:4], h[8:32]))
	page := w.img[(w.used-1)*pageSize : w.used*pageSize]
	off := entryDataOff + w.next*entrySize
	copy(page[off:], h)
	copy(page[off+entrySize:], data)
	for i := w.next; i < w.next+span; i++ {
		// EMPTY 0b11 -> WRITTEN 0b10
		page[entryTableOff+i/4] &^= 1 << (uint(i%4) * 2)
	}
	w.next += span
}

// header makes an item header with an empty (0xFF) data field
func header(ns byte, t byte, chunk byte, key string) []byte {
	h := make([]byte, entrySize)
	h[0], h[1], h[3] = ns, t, chunk
	copy(h[8:24], key) // the rest of the key stays 0, as strncpy pads it
	for i := 24; i < 32; i++ {
		h[i] = 0xFF
	}
	return h
}

// varItem writes a string or blob chunk: the header holds the data size
// and CRC
func (w *writer) varItem(ns, t, chunk byte, key string, data []byte) error {
	h := header(ns, t, chunk, key)
	binary.LittleEndian.PutUint16(h[24:], uint16(len(data)))
	binary.LittleEndian.PutUint32(h[28:], crc(0xFFFFFFFF, data))
	if err := w.room(1 + (len(data)+entrySize-1)/entrySize); err != nil {
		return err
	}
	w.put(h, data)
	return nil
}

// blob writes BLOB_DATA chunks and the BLOB_IDX
func (w *writer) blob(ns byte, key string, data []byte) error {
	count := 0
	rest := data
	for {
		tail := w.tailroom()
		// A first chunk that would be small goes to a new page, as in
		// writeMultiPageBlob
		if tail == 0 || count == 0 && tail < len(rest) && tail < smallTailroom {
			if err := w.open(); err != nil {
				return err
			}
			continue
		}
		n := min(len(rest), tail)
		if count >= blobChunksMax {
			return fmt.Errorf("nvs: blob %q needs more than %d chunks", key, blobChunksMax)
		}
		if err := w.varItem(ns, itemBlobData, byte(count), key, rest[:n]); err != nil {
			return err
		}
		count++
		rest = rest[n:]
		if len(rest) == 0 {
			break
		}
		if err := w.open(); err != nil {
			return err
		}
	}
	h := header(ns, itemBlobIdx, chunkAny, key)
	binary.LittleEndian.PutUint32(h[24:], uint32(len(data)))
	h[28] = byte(count)
	h[29] = 0 // chunkStart: VerOffset::VER_0_OFFSET
	if err := w.room(1); err != nil {
		return err
	}
	w.put(h, nil)
	return nil
}

// value writes one value
func (w *writer) value(ns byte, e Entry) error {
	switch e.Type {
	case String:
		return w.varItem(ns, byte(String), chunkAny, e.Key, append([]byte(e.Value.(string)), 0))
	case Blob:
		if w.v1 {
			return w.varItem(ns, byte(Blob), chunkAny, e.Key, e.Value.([]byte))
		}
		return w.blob(ns, e.Key, e.Value.([]byte))
	}
	h := header(ns, byte(e.Type), chunkAny, e.Key)
	var v uint64
	switch x := e.Value.(type) {
	case int64:
		v = uint64(x)
	case uint64:
		v = x
	case float64:
		if e.Type == Float {
			v = uint64(math.Float32bits(float32(x)))
		} else {
			v = math.Float64bits(x)
		}
	}
	for i := 0; i < e.Type.size(); i++ {
		h[24+i] = byte(v >> (8 * i))
	}
	if err := w.room(1); err != nil {
		return err
	}
	w.put(h, nil)
	return nil
}

// build makes the image; spaces gives the namespace order (more are
// taken from the entries), k encrypts it when set
func build(spaces []string, entries []Entry, size int, o Options) ([]byte, error) {
	k := o.Keys
	ver := byte(version2)
	switch o.Version {
	case 0, Version2:
	case Version1:
		ver = version1
	default:
		return nil, fmt.Errorf("nvs: unknown format version %d", o.Version)
	}
	if size%pageSize != 0 || size < minPages*pageSize {
		return nil, fmt.Errorf("nvs: size 0x%X is not a multiple of 0x%X of at least 0x%X", size, pageSize, minPages*pageSize)
	}
	order := []string{}
	index := map[string]int{}
	addNS := func(s string) {
		if _, ok := index[s]; !ok {
			index[s] = len(order)
			order = append(order, s)
		}
	}
	for _, s := range spaces {
		if err := checkName("namespace", s); err != nil {
			return nil, err
		}
		addNS(s)
	}
	byNS := map[string][]Entry{}
	seen := map[[2]string]bool{}
	for _, e := range entries {
		e, err := e.Normalize()
		if err != nil {
			return nil, err
		}
		id := [2]string{e.Namespace, e.Key}
		if seen[id] {
			return nil, fmt.Errorf("nvs: key %s/%s is given twice", e.Namespace, e.Key)
		}
		seen[id] = true
		if ver == version1 {
			if err := fitsV1(e); err != nil {
				return nil, err
			}
		}
		addNS(e.Namespace)
		byNS[e.Namespace] = append(byNS[e.Namespace], e)
	}
	if len(order) > nsAny-1 {
		return nil, fmt.Errorf("nvs: %d namespaces, at most %d are possible", len(order), nsAny-1)
	}

	w := &writer{img: make([]byte, size), pages: size / pageSize, v1: ver == version1}
	for i := range w.img {
		w.img[i] = 0xFF
	}
	for i, s := range order {
		ns := byte(i + 1)
		h := header(nsIndex, byte(U8), chunkAny, s)
		h[24] = ns
		if err := w.room(1); err != nil {
			return nil, err
		}
		w.put(h, nil)
		for _, e := range byNS[s] {
			if err := w.value(ns, e); err != nil {
				return nil, err
			}
		}
	}

	var x *xts
	if k != nil {
		var err error
		if x, err = newXTS(*k); err != nil {
			return nil, err
		}
	}
	for p := 0; p < w.used; p++ {
		page := w.img[p*pageSize : (p+1)*pageSize]
		state := uint32(pageFull)
		if p == w.used-1 {
			state = pageActive
		}
		binary.LittleEndian.PutUint32(page[0:], state)
		binary.LittleEndian.PutUint32(page[4:], uint32(p))
		page[8] = ver
		binary.LittleEndian.PutUint32(page[28:], crc(0xFFFFFFFF, page[pageHeaderCRC0:pageHeaderCRC1]))
		if x != nil {
			for i := 0; i < entryCount; i++ {
				if entryState(page, i) == entryWritten {
					off := entryDataOff + i*entrySize
					x.encrypt(page[off:off+entrySize], uint32(p*pageSize+off))
				}
			}
		}
	}
	return w.img, nil
}

// fitsV1 checks that a string or blob fits one item of format version 1,
// as Page::writeItem of ESP-IDF v3.0 does
func fitsV1(e Entry) error {
	n := 0
	switch v := e.Value.(type) {
	case string:
		n = len(v) + 1
	case []byte:
		n = len(v)
	default:
		if e.Type == Float || e.Type == Double {
			return fmt.Errorf("nvs: %s/%s: format version 1 has no %v type", e.Namespace, e.Key, e.Type)
		}
		return nil
	}
	if n > v1VarMax {
		return fmt.Errorf("nvs: %s/%s: %d bytes, format version 1 (ESP-IDF before v3.2) takes up to %d bytes for a %v", e.Namespace, e.Key, n, v1VarMax, e.Type)
	}
	return nil
}

// entryState reads the 2-bit state of an entry from the page bitmap
func entryState(page []byte, i int) int {
	return int(page[entryTableOff+i/4]>>(uint(i%4)*2)) & 3
}
