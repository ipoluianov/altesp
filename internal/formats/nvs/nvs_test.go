package nvs

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sameEntries compares values regardless of their order
func sameEntries(t *testing.T, want, got []Entry) {
	t.Helper()
	m := map[[2]string]Entry{}
	for _, e := range got {
		m[[2]string{e.Namespace, e.Key}] = e
	}
	if len(got) != len(want) {
		t.Errorf("%d values, want %d", len(got), len(want))
	}
	for _, w := range want {
		w, err := w.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		g, ok := m[[2]string{w.Namespace, w.Key}]
		if !ok {
			t.Errorf("%s/%s is missing", w.Namespace, w.Key)
			continue
		}
		if g.Type != w.Type || !reflect.DeepEqual(g.Value, w.Value) {
			t.Errorf("%s/%s = %v %v, want %v %v", w.Namespace, w.Key, g.Type, short(g.Value), w.Type, short(w.Value))
		}
	}
}

func short(v any) any {
	if b, ok := v.([]byte); ok && len(b) > 16 {
		return hex.EncodeToString(b[:16]) + "..."
	}
	return v
}

func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) + seed
	}
	return b
}

func allTypes() []Entry {
	return []Entry{
		{"ints", "u8", U8, uint64(255)},
		{"ints", "i8", I8, int64(-128)},
		{"ints", "u16", U16, uint64(65535)},
		{"ints", "i16", I16, int64(-32768)},
		{"ints", "u32", U32, uint64(math.MaxUint32)},
		{"ints", "i32", I32, int64(math.MinInt32)},
		{"ints", "u64", U64, uint64(math.MaxUint64)},
		{"ints", "i64", I64, int64(math.MinInt64)},
		{"ints", "i64pos", I64, 1234567890123},
		{"ints", "float", Float, 1.5},
		{"ints", "double", Double, math.Pi},
		{"text", "empty", String, ""},
		{"text", "hello", String, "hello, world"},
		{"text", "exact32", String, strings.Repeat("x", 31)},
		{"text", "longest", String, strings.Repeat("L", strMax-1)},
		{"blobs", "empty", Blob, []byte{}},
		{"blobs", "small", Blob, []byte{1, 2, 3}},
		{"blobs", "chunk", Blob, pattern(chunkMax, 1)},
		{"blobs", "big", Blob, pattern(3*chunkMax+123, 2)},
	}
}

func TestRoundTripTypes(t *testing.T) {
	entries := allTypes()
	img, err := Build(entries, 0x6000*4)
	if err != nil {
		t.Fatal(err)
	}
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Problems) > 0 {
		t.Fatalf("problems: %v", im.Problems)
	}
	sameEntries(t, entries, im.Entries)
	if got := im.Namespaces(); !reflect.DeepEqual(got, []string{"ints", "text", "blobs"}) {
		t.Errorf("namespaces %v", got)
	}
	if im.UsedEntries+im.FreeEntries != len(img)/pageSize*entryCount {
		t.Errorf("used %d + free %d", im.UsedEntries, im.FreeEntries)
	}
	// Built again from the parsed values, the image is the same
	img2, err := im.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(img, img2) {
		t.Errorf("rebuilt image differs")
	}
}

// The big blob spans pages: its chunks are on different pages and the
// index follows the last one
func TestMultiPageBlob(t *testing.T) {
	data := pattern(10000, 9)
	img, err := Build([]Entry{{"ns", "blob", Blob, data}}, 0x5000)
	if err != nil {
		t.Fatal(err)
	}
	chunks := 0
	for p := 0; p < 5; p++ {
		page := img[p*pageSize:]
		for i := 0; i < entryCount; i++ {
			off := entryDataOff + i*entrySize
			if entryState(page, i) == entryWritten && page[off+1] == itemBlobData && page[off] == 1 {
				chunks++
			}
		}
	}
	if chunks != 3 {
		t.Errorf("%d chunks, want 3", chunks)
	}
	if binary.LittleEndian.Uint32(img[3*pageSize:]) != pageUninit || binary.LittleEndian.Uint32(img[4*pageSize:]) != pageUninit {
		t.Errorf("the last pages should stay empty")
	}
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Problems) > 0 {
		t.Fatal(im.Problems)
	}
	sameEntries(t, []Entry{{"ns", "blob", Blob, data}}, im.Entries)

	// A lost chunk is reported, the blob is dropped
	page := img[pageSize:]
	page[entryTableOff] &^= 3 // erase entry 0 of page 1, the second chunk
	im, err = Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Entries) != 0 || len(im.Problems) == 0 {
		t.Errorf("entries %d, problems %v", len(im.Entries), im.Problems)
	}
}

func TestManyEntries(t *testing.T) {
	var entries []Entry
	for i := 0; i < 600; i++ {
		ns := []string{"a", "b", "c"}[i%3]
		entries = append(entries, Entry{ns, "k" + string(rune('A'+i/26%26)) + string(rune('a'+i%26)) + string(rune('0'+i/676)), U32, uint64(i * 1000)})
	}
	img, err := Build(entries, 0x8000)
	if err != nil {
		t.Fatal(err)
	}
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Problems) > 0 {
		t.Fatal(im.Problems)
	}
	sameEntries(t, entries, im.Entries)
	if im.UsedEntries != 603 {
		t.Errorf("used %d, want 603", im.UsedEntries)
	}
	full, active := 0, 0
	for _, p := range im.Pages {
		switch p.State {
		case "full":
			full++
		case "active":
			active++
		}
	}
	if full != 4 || active != 1 {
		t.Errorf("full %d, active %d", full, active)
	}
	// Too much for the partition: one page has to stay free
	if _, err := Build(entries, 0x5000); !errors.Is(err, ErrNoSpace) {
		t.Errorf("err %v, want ErrNoSpace", err)
	}
}

func TestEdit(t *testing.T) {
	img, err := Build(allTypes(), 0x10000)
	if err != nil {
		t.Fatal(err)
	}
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if !im.Delete("ints", "u8") || im.Delete("ints", "nothing") {
		t.Fatal("delete")
	}
	if err := im.Set(Entry{"text", "hello", U16, 7}); err != nil { // retyped
		t.Fatal(err)
	}
	if err := im.Set(Entry{"fresh", "key", String, "v"}); err != nil {
		t.Fatal(err)
	}
	if err := im.Set(Entry{"fresh", "bad", U8, 256}); err == nil {
		t.Error("256 accepted as u8")
	}
	if err := im.Set(Entry{"fresh", "a_key_too_long_x", U8, 1}); err == nil {
		t.Error("16-byte key accepted")
	}
	if err := im.AddNamespace("empty"); err != nil {
		t.Fatal(err)
	}
	if !im.DeleteNamespace("blobs") {
		t.Fatal("delete namespace")
	}
	img2, err := im.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(img2) != len(img) {
		t.Fatalf("size %d", len(img2))
	}
	im2, err := Parse(img2)
	if err != nil {
		t.Fatal(err)
	}
	if len(im2.Problems) > 0 {
		t.Fatal(im2.Problems)
	}
	sameEntries(t, im.Entries, im2.Entries)
	if _, ok := im2.Get("ints", "u8"); ok {
		t.Error("deleted key is back")
	}
	if e, _ := im2.Get("text", "hello"); e.Type != U16 || e.Value != uint64(7) {
		t.Errorf("hello = %v", e)
	}
	if got := im2.Namespaces(); !reflect.DeepEqual(got, []string{"ints", "text", "fresh", "empty"}) {
		t.Errorf("namespaces %v", got)
	}
}

// A page as the ESP-IDF v3.0 library wrote it on an ESP32 (entries of a
// real board running ESP-AT): format version 1, a namespace, an i32 and
// a single-page BLOB whose padding is not 0xFF. The CRCs were computed
// by the firmware.
func knownPage(t *testing.T) []byte {
	page := bytes.Repeat([]byte{0xFF}, pageSize)
	put := func(off int, h string) {
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		copy(page[off:], b)
	}
	put(0, "feffffff00000000ffffffffffffffffffffffffffffffffffffffffc216dddc")       // ACTIVE, seq 0, version 0xFF
	put(32, "aafa")                                                                  // entries 0..5 written
	put(64+0*32, "000101ff779eb40b5541525400000000000000000000000001ffffffffffffff") // namespace UART = 1
	put(64+1*32, "011401ffae7ae9cf7261746500000000000000000000000000c20100ffffffff") // UART/rate i32 115200
	put(64+2*32, "000101ffbc2cabfe6d69736300000000000000000000000002ffffffffffffff") // namespace misc = 2
	put(64+3*32, "024102ff57bedc4a6c6f67000000000000000000000000001000ffff9cda5f78") // misc/log BLOB, 16 bytes
	put(64+4*32, "0200000000000000000000000000000000000000000000001000ffff9cda5f78") // its data
	return page
}

func TestKnownImage(t *testing.T) {
	img := append(knownPage(t), bytes.Repeat([]byte{0xFF}, 2*pageSize)...)
	// entry 5 is marked written but empty: a write cut by power loss
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"UART", "rate", I32, int64(115200)},
		{"misc", "log", Blob, append([]byte{2}, make([]byte, 15)...)},
	}
	sameEntries(t, want, im.Entries)
	if im.Pages[0].Version != 1 || im.Pages[0].State != "active" || im.Version != Version1 {
		t.Errorf("page %+v", im.Pages[0])
	}
	if len(im.Problems) != 1 || !strings.Contains(im.Problems[0], "entry 5") {
		t.Errorf("problems %v", im.Problems)
	}
	if im.UsedEntries != 6 || im.FreeEntries != 3*entryCount-6 {
		t.Errorf("used %d free %d", im.UsedEntries, im.FreeEntries)
	}
	// The builder writes the same bytes the library did for the items of
	// one entry, and the same header CRC for a version 1 page
	out, err := Build(want[:1], 3*pageSize)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[64:128], img[64:128]) {
		t.Errorf("built\n%x\nwant\n%x", out[64:128], img[64:128])
	}
	hdr := append([]byte{}, out[:32]...)
	hdr[8] = version1
	if crc(0xFFFFFFFF, hdr[4:28]) != binary.LittleEndian.Uint32(img[28:]) {
		t.Errorf("header CRC differs")
	}
}

func TestBadCRC(t *testing.T) {
	img, err := Build([]Entry{{"n", "a", U8, 1}, {"n", "b", String, "text"}}, 0x3000)
	if err != nil {
		t.Fatal(err)
	}
	img[entryDataOff+1*entrySize+24] ^= 1 // value of a
	img[entryDataOff+3*entrySize] ^= 1    // data of b
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Entries) != 0 || len(im.Problems) != 2 || im.Encrypted {
		t.Errorf("entries %v problems %v", im.Entries, im.Problems)
	}
	if _, err := Parse(img[:100]); err == nil {
		t.Error("short image parsed")
	}
	if _, err := Parse(bytes.Repeat([]byte{0x55}, 0x3000)); err == nil {
		t.Error("garbage parsed")
	}
}

func TestOrphanChunk(t *testing.T) {
	img, err := Build([]Entry{{"n", "b", Blob, []byte{1, 2, 3}}}, 0x3000)
	if err != nil {
		t.Fatal(err)
	}
	img[entryTableOff] &^= 3 << 6 // erase entry 3, the blob index
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Entries) != 0 || len(im.Problems) != 1 || !strings.Contains(im.Problems[0], "orphan") {
		t.Errorf("entries %v problems %v", im.Entries, im.Problems)
	}
}

const testCSV = `key,type,encoding,value
# a comment
storage,namespace,,
u8key,data,u8,255
i8key,data,i8,-128
u16key,data,u16,0xFFFF
i32key,data,i32,-2147483648
u64key,data,u64,18446744073709551615
strkey,data,string,"hello, ""world"""
hexkey,data,hex2bin,0102AAff
b64key,data,base64,AQID
other,namespace,,
binfile,file,binary,blob.bin
hexfile,file,hex2bin,blob.hex
strfile,file,string,text.txt
empty,namespace,,
`

func TestCSV(t *testing.T) {
	dir := t.TempDir()
	big := pattern(5000, 3)
	os.WriteFile(filepath.Join(dir, "blob.bin"), big, 0o644)
	os.WriteFile(filepath.Join(dir, "blob.hex"), []byte("de ad\nbe ef\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "text.txt"), []byte("line 1\nline 2"), 0o644)
	want := []Entry{
		{"storage", "u8key", U8, 255},
		{"storage", "i8key", I8, -128},
		{"storage", "u16key", U16, 65535},
		{"storage", "i32key", I32, math.MinInt32},
		{"storage", "u64key", U64, uint64(math.MaxUint64)},
		{"storage", "strkey", String, `hello, "world"`},
		{"storage", "hexkey", Blob, []byte{1, 2, 0xAA, 0xFF}},
		{"storage", "b64key", Blob, []byte{1, 2, 3}},
		{"other", "binfile", Blob, big},
		{"other", "hexfile", Blob, []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"other", "strfile", String, "line 1\nline 2"},
	}
	got, err := ReadCSV(strings.NewReader(testCSV), dir)
	if err != nil {
		t.Fatal(err)
	}
	sameEntries(t, want, got)

	im, err := ReadCSVImage(strings.NewReader(testCSV), dir, 0x6000)
	if err != nil {
		t.Fatal(err)
	}
	img, err := im.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	im2, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	sameEntries(t, want, im2.Entries)
	if got := im2.Namespaces(); !reflect.DeepEqual(got, []string{"storage", "other", "empty"}) {
		t.Errorf("namespaces %v", got)
	}

	// Written and read back, the CSV gives the same values
	var buf bytes.Buffer
	if err := im2.WriteCSV(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "key,type,encoding,value\nstorage,namespace,,\nu8key,data,u8,255\n") {
		t.Errorf("csv:\n%s", buf.String()[:100])
	}
	back, err := ReadCSV(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	sameEntries(t, want, back)
	// all types, floats too
	buf.Reset()
	if err := WriteCSV(&buf, allTypes()); err != nil {
		t.Fatal(err)
	}
	back, err = ReadCSV(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	sameEntries(t, allTypes(), back)

	for _, bad := range []string{
		"key,type,encoding,value\nk,data,u8,1\n", // no namespace
		"key,type,encoding,value\nn,namespace,,\nk,data,u8,256\n",
		"key,type,encoding,value\nn,namespace,,\nk,data,hex2bin,123\n",
		"key,type,encoding,value\nn,namespace,,\nk,data,binary,x\n",
		"key,type,encoding,value\nn,namespace,,\nk,data,u9,1\n",
	} {
		if _, err := ReadCSV(strings.NewReader(bad), dir); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func testKeys() Keys {
	var k Keys
	for i := range k.Data {
		k.Data[i] = byte(i)
		k.Tweak[i] = byte(32 + i)
	}
	return k
}

// The XTS vector is from OpenSSL (Python cryptography, AES-256-XTS with
// the 64-byte key 00..3F and the tweak 0x1060 little-endian)
func TestXTS(t *testing.T) {
	x, err := newXTS(testKeys())
	if err != nil {
		t.Fatal(err)
	}
	pt, _ := hex.DecodeString("011401ffae7ae9cf7261746500000000000000000000000000c20100ffffffff")
	ct, _ := hex.DecodeString("9d8c652efd13386d1ea5ce5db24a634626af5a3a7f2cc2b3e53c6d5871522ef7")
	b := append([]byte{}, pt...)
	x.encrypt(b, 0x1060)
	if !bytes.Equal(b, ct) {
		t.Fatalf("encrypted %x", b)
	}
	x.decrypt(b, 0x1060)
	if !bytes.Equal(b, pt) {
		t.Fatalf("decrypted %x", b)
	}
}

func TestEncrypted(t *testing.T) {
	k := testKeys()
	part := k.Partition(pageSize)
	k2, err := ParseKeys(part)
	if err != nil || k2 != k {
		t.Fatalf("keys %v", err)
	}
	part[5] ^= 1
	if _, err := ParseKeys(part); err == nil {
		t.Error("bad CRC accepted")
	}
	if _, err := ParseKeys(bytes.Repeat([]byte{0xFF}, pageSize)); err == nil {
		t.Error("empty keys accepted")
	}

	entries := allTypes()
	img, err := BuildEncrypted(entries, 0x10000, k)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := Build(entries, 0x10000)
	if bytes.Equal(img, plain) || !bytes.Equal(img[:64], plain[:64]) {
		t.Error("only the entries should differ")
	}
	// Without keys: encrypted, read-only
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if !im.Encrypted || len(im.Entries) != 0 || len(im.Problems) != 1 {
		t.Fatalf("encrypted %v, %d entries, problems %v", im.Encrypted, len(im.Entries), im.Problems)
	}
	if err := im.Set(Entry{"a", "b", U8, 1}); !errors.Is(err, ErrNoKeys) {
		t.Errorf("set: %v", err)
	}
	if _, err := im.Bytes(); !errors.Is(err, ErrNoKeys) {
		t.Errorf("bytes: %v", err)
	}
	// Wrong keys
	wrong := k
	wrong.Data[0] ^= 1
	if _, err := ParseEncrypted(img, wrong); !errors.Is(err, ErrWrongKeys) {
		t.Errorf("wrong keys: %v", err)
	}
	// With keys: read, edit, encrypted back
	im, err = ParseEncrypted(img, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Problems) > 0 {
		t.Fatal(im.Problems)
	}
	sameEntries(t, entries, im.Entries)
	im.Set(Entry{"ints", "u8", U8, 7})
	img2, err := im.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	im2, err := ParseEncrypted(img2, k)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := im2.Get("ints", "u8"); e.Value != uint64(7) {
		t.Errorf("u8 = %v", e.Value)
	}
	if p, _ := Parse(img2); !p.Encrypted {
		t.Error("written back in plain")
	}
}

// Format version 1 as ESP-IDF before v3.2 reads it: 0xFF in the page
// header, blobs as one BLOB item, up to 1984 bytes
func TestVersion1(t *testing.T) {
	entries := []Entry{
		{"v1", "n", U32, 7},
		{"v1", "s", String, strings.Repeat("s", v1VarMax-1)},
		{"v1", "b", Blob, pattern(v1VarMax, 5)},
		{"v1", "e", Blob, []byte{}},
	}
	img, err := BuildWith(entries, 0x4000, Options{Version: Version1})
	if err != nil {
		t.Fatal(err)
	}
	for p := 0; p < 4; p++ {
		page := img[p*pageSize:]
		if binary.LittleEndian.Uint32(page) == pageUninit {
			continue
		}
		if page[8] != version1 {
			t.Errorf("page %d version byte 0x%02X", p, page[8])
		}
		for i := 0; i < entryCount; i++ {
			off := entryDataOff + i*entrySize
			if entryState(page, i) == entryWritten && (page[off+1] == itemBlobData || page[off+1] == itemBlobIdx) {
				t.Errorf("page %d entry %d: a version 2 blob item", p, i)
			}
		}
	}
	im, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Problems) > 0 {
		t.Fatal(im.Problems)
	}
	if im.Version != Version1 {
		t.Errorf("version %d", im.Version)
	}
	sameEntries(t, entries, im.Entries)
	if err := im.Set(Entry{"v1", "n", U32, 8}); err != nil {
		t.Fatal(err)
	}
	img2, err := im.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	im2, err := Parse(img2)
	if err != nil {
		t.Fatal(err)
	}
	if im2.Version != Version1 || img2[8] != version1 {
		t.Errorf("rebuilt as version %d", im2.Version)
	}
	sameEntries(t, im.Entries, im2.Entries)

	// Too long for version 1: refused by Set and by the builder
	big := Entry{"v1", "big", Blob, pattern(v1VarMax+1, 1)}
	if err := im.Set(big); err == nil {
		t.Error("Set took an oversized v1 blob")
	}
	if err := im.Set(Entry{"v1", "str", String, strings.Repeat("s", v1VarMax)}); err == nil {
		t.Error("Set took an oversized v1 string")
	}
	if err := im.Set(Entry{"v1", "f", Float, 1}); err == nil {
		t.Error("Set took a float in v1")
	}
	if _, err := BuildWith([]Entry{big}, 0x4000, Options{Version: Version1}); err == nil || !strings.Contains(err.Error(), "1984") {
		t.Errorf("err %v", err)
	}
	// The same blob is fine in version 2, and the default is version 2
	im.Version = Version2
	if err := im.Set(big); err != nil {
		t.Fatal(err)
	}
	if img3, err := im.Bytes(); err != nil || img3[8] != version2 {
		t.Errorf("v2: %v", err)
	}
	if NewImage(0x3000).Version != Version2 {
		t.Error("NewImage version")
	}
}
