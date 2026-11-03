package nvs

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// item is one item read from a page
type item struct {
	ns, typ, chunk byte
	key            string
	hdr            []byte // the 32-byte header
	data           []byte // variable-length data
	where          string // page and entry, for problems
}

// Parse reads an NVS image. A wrong size is an error; damaged pages and
// items are reported in Problems and skipped. An encrypted image gives
// Encrypted and no entries.
func Parse(data []byte) (*Image, error) {
	return parse(data, nil)
}

// ParseEncrypted reads an encrypted NVS image with its keys. The image
// keeps the keys, so Bytes encrypts the edited image back.
func ParseEncrypted(data []byte, k Keys) (*Image, error) {
	return parse(data, &k)
}

func parse(data []byte, k *Keys) (*Image, error) {
	if len(data) == 0 || len(data)%pageSize != 0 {
		return nil, fmt.Errorf("nvs: size %d is not a multiple of 0x%X", len(data), pageSize)
	}
	im := &Image{Size: len(data), Pages: make([]Page, len(data)/pageSize)}
	var x *xts
	if k != nil {
		var err error
		if x, err = newXTS(*k); err != nil {
			return nil, err
		}
		kc := *k
		im.keys = &kc
		im.Encrypted = true
	}
	problem := func(format string, a ...any) {
		im.Problems = append(im.Problems, fmt.Sprintf(format, a...))
	}

	// Pages that hold data, in the order of their sequence numbers
	type loaded struct {
		n    int
		seq  uint32
		page []byte
	}
	var pages []loaded
	headers := 0
	for n := range im.Pages {
		page := data[n*pageSize : (n+1)*pageSize]
		info := &im.Pages[n]
		state := binary.LittleEndian.Uint32(page)
		switch state {
		case pageUninit:
			info.State = "empty"
			info.Free = entryCount
			for _, b := range page {
				if b != 0xFF {
					problem("page %d: not initialized but not erased", n)
					break
				}
			}
			im.FreeEntries += entryCount
			continue
		case pageActive:
			info.State = "active"
		case pageFull:
			info.State = "full"
		case pageFreeing:
			info.State = "freeing"
		case pageCorrupt:
			info.State = "corrupt"
			problem("page %d: marked corrupt", n)
			im.FreeEntries += entryCount
			continue
		default:
			info.State = "invalid"
			problem("page %d: unknown state 0x%08X", n, state)
			continue
		}
		if crc(0xFFFFFFFF, page[pageHeaderCRC0:pageHeaderCRC1]) != binary.LittleEndian.Uint32(page[28:]) {
			info.State = "corrupt"
			problem("page %d: bad header CRC", n)
			continue
		}
		headers++
		info.Seq = binary.LittleEndian.Uint32(page[4:])
		switch page[8] {
		case version2:
			info.Version = 2
		case version1:
			info.Version = 1
		default:
			problem("page %d: unknown format version 0x%02X", n, page[8])
		}
		if x != nil {
			// Decrypt a copy: the written and erased entries are
			// ciphertext, empty ones are plain 0xFF
			cp := append([]byte{}, page...)
			for i := 0; i < entryCount; i++ {
				if entryState(cp, i) != entryEmpty {
					off := entryDataOff + i*entrySize
					x.decrypt(cp[off:off+entrySize], uint32(n*pageSize+off))
				}
			}
			page = cp
		}
		pages = append(pages, loaded{n, info.Seq, page})
	}
	if headers == 0 && len(im.Pages) > 0 && im.FreeEntries == 0 {
		return nil, fmt.Errorf("nvs: no NVS pages found")
	}
	im.Version = Version2
	v1, v2 := false, false
	for _, p := range im.Pages {
		v1 = v1 || p.Version == 1
		v2 = v2 || p.Version == 2
	}
	if v1 && !v2 {
		im.Version = Version1
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].seq < pages[j].seq })

	// Items of all pages, oldest first
	var items []item
	good, bad := 0, 0
	for _, p := range pages {
		info := &im.Pages[p.n]
		for i := 0; i < entryCount; {
			switch entryState(p.page, i) {
			case entryEmpty:
				info.Free++
				i++
				continue
			case entryErased:
				info.Erased++
				i++
				continue
			case entryIllegal:
				problem("page %d entry %d: illegal state", p.n, i)
				info.Erased++
				i++
				continue
			}
			info.Used++
			off := entryDataOff + i*entrySize
			h := p.page[off : off+entrySize]
			it, span, err := readItem(p.page, i)
			if err != "" {
				if err == "bad CRC" {
					bad++
				}
				problem("page %d entry %d: %s", p.n, i, err)
				info.Used += span - 1 // a bad data CRC skips the data entries
				i += span
				continue
			}
			good++
			for j := i + 1; j < i+span; j++ {
				info.Used++
				if entryState(p.page, j) != entryWritten {
					problem("page %d entry %d: data entry of %q is not written", p.n, j, it.key)
				}
			}
			it.hdr = h
			it.where = fmt.Sprintf("page %d entry %d", p.n, i)
			items = append(items, it)
			i += span
		}
		im.UsedEntries += info.Used
		if info.State != "freeing" {
			im.FreeEntries += entryCount - info.Used
		}
	}

	if good == 0 && bad > 0 {
		if x != nil {
			return nil, ErrWrongKeys
		}
		// Every item fails its CRC: the entries are ciphertext
		im.Encrypted = true
		im.Problems = []string{"encrypted: keys from the nvs_keys partition are needed"}
		return im, nil
	}

	im.collect(items, problem)
	return im, nil
}

// readItem checks the item at entry i as Item::checkHeaderConsistency
// does and reads its data; it gives the span, or a problem
func readItem(page []byte, i int) (item, int, string) {
	off := entryDataOff + i*entrySize
	h := page[off : off+entrySize]
	if crc(0xFFFFFFFF, h[0:4], h[8:32]) != binary.LittleEndian.Uint32(h[4:]) {
		return item{}, 1, "bad CRC"
	}
	it := item{ns: h[0], typ: h[1], chunk: h[3], key: cstr(h[8:24])}
	span := int(h[2])
	switch Type(it.typ) {
	case U8, I8, U16, I16, U32, I32, U64, I64, Float, Double:
		if span != 1 {
			return it, 1, fmt.Sprintf("span %d of a %v item", span, Type(it.typ))
		}
		return it, 1, ""
	}
	switch it.typ {
	case itemBlobIdx:
		if span != 1 || it.chunk != chunkAny {
			return it, 1, "bad blob index item"
		}
		return it, 1, ""
	case byte(String), byte(Blob), itemBlobData:
		size := int(binary.LittleEndian.Uint16(h[24:]))
		if size > (entryCount-i-1)*entrySize || span != 1+(size+entrySize-1)/entrySize {
			return it, 1, fmt.Sprintf("%q: data size %d and span %d don't fit", it.key, size, span)
		}
		if it.typ == itemBlobData && it.chunk == chunkAny {
			return it, 1, fmt.Sprintf("%q: blob chunk without index", it.key)
		}
		it.data = page[off+entrySize : off+entrySize+size]
		if crc(0xFFFFFFFF, it.data) != binary.LittleEndian.Uint32(h[28:]) {
			return it, span, fmt.Sprintf("%q: bad data CRC", it.key)
		}
		return it, span, ""
	}
	return it, 1, fmt.Sprintf("unknown item type 0x%02X", it.typ)
}

// cstr takes a NUL-terminated name
func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// collect turns the items into namespaces and values. A later item of
// the same key replaces an earlier one, as the newer page wins.
func (im *Image) collect(items []item, problem func(string, ...any)) {
	names := map[byte]string{}
	for _, it := range items {
		if it.ns != nsIndex {
			continue
		}
		if it.typ != byte(U8) || it.hdr[24] == nsIndex || it.hdr[24] == nsAny {
			problem("%s: bad namespace item %q", it.where, it.key)
			continue
		}
		idx := it.hdr[24]
		if old, ok := names[idx]; ok && old != it.key {
			problem("%s: namespace index %d is both %q and %q", it.where, idx, old, it.key)
		}
		names[idx] = it.key
		found := false
		for _, s := range im.spaces {
			found = found || s == it.key
		}
		if !found {
			im.spaces = append(im.spaces, it.key)
		}
	}

	type chunkID struct {
		ns    byte
		key   string
		chunk byte
	}
	chunks := map[chunkID]item{}
	usedChunks := map[chunkID]bool{}
	for _, it := range items {
		if it.ns != nsIndex && it.typ == itemBlobData {
			chunks[chunkID{it.ns, it.key, it.chunk}] = it
		}
	}

	pos := map[[2]string]int{}
	add := func(it item, e Entry) {
		id := [2]string{e.Namespace, e.Key}
		if i, ok := pos[id]; ok {
			problem("%s: %s/%s is written twice, the newer is taken", it.where, e.Namespace, e.Key)
			im.Entries[i] = e
			return
		}
		pos[id] = len(im.Entries)
		im.Entries = append(im.Entries, e)
	}
	for _, it := range items {
		if it.ns == nsIndex || it.typ == itemBlobData {
			continue
		}
		ns, ok := names[it.ns]
		if !ok {
			problem("%s: %q is in unknown namespace %d", it.where, it.key, it.ns)
			continue
		}
		e := Entry{Namespace: ns, Key: it.key, Type: Type(it.typ)}
		switch it.typ {
		case byte(String):
			s := it.data
			if n := len(s); n > 0 && s[n-1] == 0 {
				s = s[:n-1]
			}
			e.Value = string(s)
		case byte(Blob):
			e.Value = append([]byte{}, it.data...)
		case itemBlobIdx:
			e.Type = Blob
			size := int(binary.LittleEndian.Uint32(it.hdr[24:]))
			count, start := int(it.hdr[28]), int(it.hdr[29])
			var b []byte
			complete := true
			for c := start; c < start+count; c++ {
				id := chunkID{it.ns, it.key, byte(c)}
				ch, ok := chunks[id]
				if !ok || c > 0xFE {
					problem("%s: blob %s/%s misses chunk %d", it.where, ns, it.key, c)
					complete = false
					break
				}
				usedChunks[id] = true
				b = append(b, ch.data...)
			}
			if !complete {
				continue
			}
			if len(b) != size {
				problem("%s: blob %s/%s has %d bytes, its index says %d", it.where, ns, it.key, len(b), size)
				continue
			}
			if b == nil {
				b = []byte{}
			}
			e.Value = b
		default:
			v := uint64(0)
			n := e.Type.size()
			for i := 0; i < n; i++ {
				v |= uint64(it.hdr[24+i]) << (8 * i)
			}
			switch {
			case e.Type == Float:
				e.Value = float64(math.Float32frombits(uint32(v)))
			case e.Type == Double:
				e.Value = math.Float64frombits(v)
			case e.Type.signed():
				sh := uint(64 - 8*n)
				e.Value = int64(v<<sh) >> sh
			default:
				e.Value = v
			}
		}
		add(it, e)
	}
	for _, it := range items {
		if it.typ != itemBlobData || it.ns == nsIndex {
			continue
		}
		id := chunkID{it.ns, it.key, it.chunk}
		if !usedChunks[id] {
			usedChunks[id] = true // once per chunk, though it may repeat
			problem("%s: orphan blob chunk %d of %q (namespace %d)", it.where, it.chunk, it.key, it.ns)
		}
	}
}
