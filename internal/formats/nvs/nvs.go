// Package nvs reads, edits and builds ESP-IDF NVS (non-volatile storage)
// partition images, plain and encrypted, and converts them from and to
// the CSV of ESP-IDF's nvs_partition_gen.
//
// The layout is taken from the ESP-IDF Programming Guide "Non-Volatile
// Storage Library" (section "Internals") and from the Apache-2.0 sources
// of components/nvs_flash (ESP-IDF master, Nov 2026): nvs_constants.h,
// nvs_types.hpp/cpp, nvs_page.hpp/cpp, nvs_storage.cpp, nvs.h,
// nvs_flash.h, nvs_encrypted_partition.cpp. No GPL code is used.
//
// An image is a sequence of 4 KB pages. A page has a 32-byte header, a
// 32-byte entry state bitmap and 126 entries of 32 bytes. An item takes
// one entry (integers, a blob index, a namespace) or a header entry
// followed by data entries (string, blob chunk). Namespaces are U8 items
// in namespace 0 whose value is the namespace index.
//
// Parse reads every page; Set, Delete, AddNamespace and DeleteNamespace
// change the list of values; Bytes builds a fresh image of the same size
// from that list in the image's format version (version 2 writes blobs
// as index + chunks, version 1 as single BLOB items), so the
// result has no erased entries and leaves at least one free page, as the
// library needs.
package nvs

import (
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"strings"
)

// Layout constants: nvs_constants.h (ESP-IDF master, Nov 2026)
const (
	pageSize       = 4096                         // NVS_CONST_PAGE_SIZE
	entrySize      = 32                           // NVS_CONST_ENTRY_SIZE
	entryCount     = 126                          // NVS_CONST_ENTRY_COUNT
	entryTableOff  = 32                           // NVS_CONST_PAGE_ENTRY_TABLE_OFFSET
	entryDataOff   = 64                           // NVS_CONST_PAGE_ENTRY_DATA_OFFSET
	chunkMax       = entrySize * (entryCount - 1) // NVS_CONST_CHUNK_MAX_SIZE, 4000
	strMax         = chunkMax                     // NVS_CONST_STR_LEN_MAX_SIZE, with the NUL
	nsIndex        = 0                            // NVS_CONST_NS_INDEX: namespaces live here
	nsAny          = 255                          // NVS_CONST_NS_ANY
	chunkAny       = 0xFF                         // Item::CHUNK_ANY
	version2       = 0xFE                         // NVS_CONST_NVS_VERSION (decremented per format)
	version1       = 0xFF                         // the format before multi-page blobs
	keyMax         = 15                           // NVS_KEY_NAME_MAX_SIZE - 1 (nvs.h)
	minPages       = 3                            // nvs_partition_gen refuses less than 0x3000
	blobChunksMax  = 127                          // chunk indexes of one VerOffset range (nvs_types.hpp)
	smallTailroom  = chunkMax / 10                // writeMultiPageBlob: a first chunk smaller than this goes to a new page
	pageHeaderCRC0 = 4                            // header CRC covers mSeqNumber..mReserved
	pageHeaderCRC1 = 28
)

// Format versions. Version 1 (page header byte 0xFF) is what ESP-IDF
// up to v3.1 writes: a blob is one BLOB (0x41) item within a page, and a
// string or blob holds at most Page::BLOB_MAX_SIZE = 32 * (126/2 - 1) =
// 1984 bytes (nvs_page.hpp/cpp of ESP-IDF v3.0.3 and v3.1). Version 2
// (0xFE), with blobs as BLOB_IDX + BLOB_DATA chunks, came with ESP-IDF
// v3.2 (nvs_page.hpp of v3.2; v3.1.x later got it as an option). A v1
// library doesn't read version 2 pages.
const (
	Version1 = 1
	Version2 = 2
	v1VarMax = entrySize * (entryCount/2 - 1) // Page::BLOB_MAX_SIZE of ESP-IDF v3.0
)

// Page states: nvs_constants.h; each state clears one more bit
const (
	pageUninit  = 0xFFFFFFFF
	pageActive  = 0xFFFFFFFE
	pageFull    = 0xFFFFFFFC
	pageFreeing = 0xFFFFFFF8
	pageCorrupt = 0xFFFFFFF0
)

// Entry states, 2 bits per entry in the bitmap: nvs_constants.h
const (
	entryEmpty   = 3
	entryWritten = 2
	entryIllegal = 1
	entryErased  = 0
)

// Type is the type of a value. The numbers are the ItemType codes of
// nvs.h / nvs_types.hpp: the low nibble is the size, 0x10 marks signed.
// Blob stands for both blob forms: the single-page BLOB (0x41) of format
// version 1 and the BLOB_IDX + BLOB_DATA chunks of version 2.
type Type byte

// Value types: nvs_type_t of nvs.h (ESP-IDF master, Nov 2026)
const (
	U8     Type = 0x01
	I8     Type = 0x11
	U16    Type = 0x02
	I16    Type = 0x12
	U32    Type = 0x04
	I32    Type = 0x14
	U64    Type = 0x08
	I64    Type = 0x18
	Float  Type = 0x24 // NVS_TYPE_FLOAT, newer ESP-IDF only
	Double Type = 0x28 // NVS_TYPE_DOUBLE, newer ESP-IDF only
	String Type = 0x21
	Blob   Type = 0x41
)

// Item types that are not value types: nvs_types.hpp ItemType
const (
	itemBlobData = 0x42
	itemBlobIdx  = 0x48
)

// String gives the type as an encoding name of nvs_partition_gen CSV
func (t Type) String() string {
	switch t {
	case U8:
		return "u8"
	case I8:
		return "i8"
	case U16:
		return "u16"
	case I16:
		return "i16"
	case U32:
		return "u32"
	case I32:
		return "i32"
	case U64:
		return "u64"
	case I64:
		return "i64"
	case Float:
		return "float"
	case Double:
		return "double"
	case String:
		return "string"
	case Blob:
		return "blob"
	}
	return fmt.Sprintf("0x%02X", byte(t))
}

// size is the byte size of a fixed-size type, 0 for string and blob
func (t Type) size() int {
	switch t {
	case U8, I8, U16, I16, U32, I32, U64, I64, Float, Double:
		return int(t & 0x0F)
	}
	return 0
}

func (t Type) signed() bool { return t&0xF0 == 0x10 }

// ParseType takes a type name as given by String
func ParseType(s string) (Type, error) {
	for _, t := range []Type{U8, I8, U16, I16, U32, I32, U64, I64, Float, Double, String, Blob} {
		if strings.EqualFold(s, t.String()) {
			return t, nil
		}
	}
	return 0, fmt.Errorf("nvs: unknown type %q", s)
}

// Entry is one value. Value holds int64 for signed integers, uint64 for
// unsigned ones, float64 for Float and Double, string for String and
// []byte for Blob.
type Entry struct {
	Namespace, Key string
	Type           Type
	Value          any
}

// Page describes one page of a parsed image
type Page struct {
	State   string // "empty", "active", "full", "freeing", "corrupt" or "invalid"
	Seq     uint32
	Version int // 1 or 2, 0 when unknown
	Used    int // written entries
	Erased  int
	Free    int
}

// Image is a parsed NVS partition
type Image struct {
	Entries []Entry
	Size    int
	// UsedEntries and FreeEntries count as nvs_get_stats does: free
	// includes erased entries and the entries of empty pages
	UsedEntries, FreeEntries int
	Pages                    []Page
	// Problems are what was wrong with the image: bad CRCs, orphan blob
	// chunks, unknown namespaces. Parsing goes on past them.
	Problems []string
	// Encrypted is set for an NVS with encrypted entries. Without the
	// keys its Entries are empty and it can't be changed.
	Encrypted bool
	// Version is the format version, Version1 or Version2: that of the
	// pages read (2 for an erased partition), and the one Bytes writes
	Version int

	spaces []string // namespaces in order, empty ones too
	keys   *Keys    // set when the image was decrypted
}

// Errors
var (
	ErrNoKeys    = errors.New("nvs: encrypted, keys needed")
	ErrWrongKeys = errors.New("nvs: the keys don't decrypt this NVS")
	ErrNoSpace   = errors.New("nvs: the values don't fit into the partition")
)

// crc is esp_rom_crc32_le(0xffffffff, ...) as NVS calls it. The ROM
// function inverts the value on entry and exit like zlib's crc32 does,
// so starting from 0xffffffff the register starts at 0; Go's Update
// works the same way.
func crc(init uint32, b ...[]byte) uint32 {
	c := init
	for _, p := range b {
		c = crc32.Update(c, crc32.IEEETable, p)
	}
	return c
}

// checkName checks a key or namespace name: 1..15 bytes, no NUL
func checkName(what, s string) error {
	if s == "" {
		return fmt.Errorf("nvs: empty %s", what)
	}
	if len(s) > keyMax {
		return fmt.Errorf("nvs: %s %q is longer than %d bytes", what, s, keyMax)
	}
	if strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("nvs: %s %q has a NUL byte", what, s)
	}
	return nil
}

// Normalize checks the entry and brings its value to the form Entry
// describes: any Go integer, float, string or []byte is accepted when it
// fits the type.
func (e Entry) Normalize() (Entry, error) {
	if err := checkName("namespace", e.Namespace); err != nil {
		return e, err
	}
	if err := checkName("key", e.Key); err != nil {
		return e, err
	}
	bad := func() (Entry, error) {
		return e, fmt.Errorf("nvs: %s/%s: value %v (%T) doesn't fit %v", e.Namespace, e.Key, e.Value, e.Value, e.Type)
	}
	switch e.Type {
	case String:
		var s string
		switch v := e.Value.(type) {
		case string:
			s = v
		case []byte:
			s = string(v)
		default:
			return bad()
		}
		if strings.IndexByte(s, 0) >= 0 || len(s)+1 > strMax {
			return e, fmt.Errorf("nvs: %s/%s: a string has up to %d bytes and no NUL", e.Namespace, e.Key, strMax-1)
		}
		e.Value = s
		return e, nil
	case Blob:
		switch v := e.Value.(type) {
		case []byte:
			e.Value = append([]byte{}, v...)
		case string:
			e.Value = []byte(v)
		default:
			return bad()
		}
		return e, nil
	case Float, Double:
		var f float64
		switch v := e.Value.(type) {
		case float64:
			f = v
		case float32:
			f = float64(v)
		default:
			i, ok := toInt(e.Value)
			if !ok {
				return bad()
			}
			f = float64(i)
		}
		if e.Type == Float {
			f = float64(float32(f))
		}
		e.Value = f
		return e, nil
	}
	n := e.Type.size()
	if n == 0 {
		return e, fmt.Errorf("nvs: %s/%s: unknown type %v", e.Namespace, e.Key, e.Type)
	}
	bits := uint(n * 8)
	if e.Type.signed() {
		var v int64
		switch x := e.Value.(type) {
		case uint64:
			if x > math.MaxInt64 {
				return bad()
			}
			v = int64(x)
		case uint:
			if uint64(x) > math.MaxInt64 {
				return bad()
			}
			v = int64(x)
		default:
			i, ok := toInt(e.Value)
			if !ok {
				return bad()
			}
			v = i
		}
		if bits < 64 && (v < -(1<<(bits-1)) || v >= 1<<(bits-1)) {
			return bad()
		}
		e.Value = v
		return e, nil
	}
	var v uint64
	switch x := e.Value.(type) {
	case uint64:
		v = x
	case uint:
		v = uint64(x)
	default:
		i, ok := toInt(e.Value)
		if !ok || i < 0 {
			return bad()
		}
		v = uint64(i)
	}
	if bits < 64 && v >= 1<<bits {
		return bad()
	}
	e.Value = v
	return e, nil
}

// toInt takes any Go integer that fits int64
func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		if x > math.MaxInt64 {
			return 0, false
		}
		return int64(x), true
	case uint:
		if uint64(x) > math.MaxInt64 {
			return 0, false
		}
		return int64(x), true
	}
	return 0, false
}

// NewImage makes an empty image of the given size
func NewImage(size int) *Image {
	return &Image{Size: size, Version: Version2}
}

// NewEncryptedImage makes an empty image that is encrypted with the keys
func NewEncryptedImage(size int, k Keys) *Image {
	return &Image{Size: size, Encrypted: true, Version: Version2, keys: &k}
}

// Namespaces gives the namespace names in image order, empty ones too
func (im *Image) Namespaces() []string {
	return append([]string{}, im.spaces...)
}

// Get finds a value
func (im *Image) Get(ns, key string) (Entry, bool) {
	for _, e := range im.Entries {
		if e.Namespace == ns && e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

func (im *Image) writable() error {
	if im.Encrypted && im.keys == nil {
		return ErrNoKeys
	}
	return nil
}

// Set adds a value or replaces the one with the same namespace and key
// (of any type, as a key is unique in a namespace). A missing namespace
// is added.
func (im *Image) Set(e Entry) error {
	if err := im.writable(); err != nil {
		return err
	}
	e, err := e.Normalize()
	if err != nil {
		return err
	}
	if im.Version == Version1 {
		if err := fitsV1(e); err != nil {
			return err
		}
	}
	if err := im.AddNamespace(e.Namespace); err != nil {
		return err
	}
	for i := range im.Entries {
		if im.Entries[i].Namespace == e.Namespace && im.Entries[i].Key == e.Key {
			im.Entries[i] = e
			return nil
		}
	}
	im.Entries = append(im.Entries, e)
	return nil
}

// Delete removes a value and tells if it was there
func (im *Image) Delete(ns, key string) bool {
	if im.writable() != nil {
		return false
	}
	for i, e := range im.Entries {
		if e.Namespace == ns && e.Key == key {
			im.Entries = append(im.Entries[:i], im.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// AddNamespace adds an empty namespace; an existing one is left as is
func (im *Image) AddNamespace(name string) error {
	if err := im.writable(); err != nil {
		return err
	}
	if err := checkName("namespace", name); err != nil {
		return err
	}
	for _, s := range im.spaces {
		if s == name {
			return nil
		}
	}
	if len(im.spaces) >= nsAny-1 {
		return fmt.Errorf("nvs: no more than %d namespaces", nsAny-1)
	}
	im.spaces = append(im.spaces, name)
	return nil
}

// DeleteNamespace removes a namespace with all its values and tells if
// it was there
func (im *Image) DeleteNamespace(name string) bool {
	if im.writable() != nil {
		return false
	}
	found := false
	for i, s := range im.spaces {
		if s == name {
			im.spaces = append(im.spaces[:i], im.spaces[i+1:]...)
			found = true
			break
		}
	}
	out := im.Entries[:0]
	for _, e := range im.Entries {
		if e.Namespace != name {
			out = append(out, e)
		}
	}
	im.Entries = out
	return found
}

// Bytes builds the image anew at the same size and format version,
// encrypted with the keys it was read with
func (im *Image) Bytes() ([]byte, error) {
	if err := im.writable(); err != nil {
		return nil, err
	}
	return build(im.spaces, im.Entries, im.Size, Options{Version: im.Version, Keys: im.keys})
}

// Options of BuildWith
type Options struct {
	Version int   // Version1 or Version2; 0 is Version2
	Keys    *Keys // encrypts the image when set
}

// Build makes a version 2 image of the given size from the values;
// namespaces go in the order they first appear
func Build(entries []Entry, size int) ([]byte, error) {
	return build(nil, entries, size, Options{})
}

// BuildEncrypted makes an encrypted version 2 image
func BuildEncrypted(entries []Entry, size int, k Keys) ([]byte, error) {
	return build(nil, entries, size, Options{Keys: &k})
}

// BuildWith makes an image with the given format version and keys
func BuildWith(entries []Entry, size int, o Options) ([]byte, error) {
	return build(nil, entries, size, o)
}
