package spiffs

import (
	"bytes"
	"errors"
	iofs "io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// testFiles is a set of files with a few index pages' worth of data, a
// name of the longest length, an empty file and nested directories
func testFiles(seed int64) map[string][]byte {
	r := rand.New(rand.NewSource(seed))
	rnd := func(n int) []byte {
		b := make([]byte, n)
		r.Read(b)
		return b
	}
	return map[string][]byte{
		"/hello.txt":                            []byte("hello\n"),
		"/empty":                                nil,
		"/www/index.html":                       bytes.Repeat([]byte("<p>spiffs</p>\n"), 100),
		"/www/js/app.js":                        rnd(3000),
		"/big.bin":                              rnd(100_000), // spans several index pages
		"/a/b/c/thirty-one-bytes-name.txt"[:31]: rnd(251),     // one full data page
	}
}

func checkFiles(t *testing.T, f *FS, want map[string][]byte) {
	t.Helper()
	n := 0
	var walk func(dir string)
	walk = func(dir string) {
		list, err := f.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir %s: %v", dir, err)
		}
		for _, e := range list {
			p := dir + "/" + e.Name
			if dir == "/" {
				p = "/" + e.Name
			}
			if e.Dir {
				walk(p)
				continue
			}
			n++
			w, ok := want[p]
			if !ok {
				t.Errorf("unexpected file %s", p)
				continue
			}
			if e.Size != int64(len(w)) {
				t.Errorf("%s: size %d, want %d", p, e.Size, len(w))
			}
			got, err := f.ReadFile(p)
			if err != nil {
				t.Errorf("read %s: %v", p, err)
			} else if !bytes.Equal(got, w) {
				t.Errorf("%s: contents differ", p)
			}
		}
	}
	walk("/")
	if n != len(want) {
		t.Errorf("%d files, want %d", n, len(want))
	}
}

func TestRoundTrip(t *testing.T) {
	custom := Params{PageSize: 512, BlockSize: 8192, ObjNameLen: 32, MetaLen: 0, UseMagic: true, UseMagicLen: true}
	for _, p := range []Params{IDF, ESP8266, custom} {
		f, err := Format(make([]byte, 512*1024), p)
		if err != nil {
			t.Fatal(err)
		}
		files := testFiles(1)
		for n, d := range files {
			if err := f.WriteFile(n, d); err != nil {
				t.Fatalf("%+v: write %s: %v", p, n, err)
			}
		}
		got, ok := Detect(f.Image())
		if !ok || got != p {
			t.Errorf("Detect %+v %v, want %+v", got, ok, p)
		}
		m, err := Mount(f.Image(), got)
		if err != nil {
			t.Fatal(err)
		}
		checkFiles(t, m, files)
	}
}

func TestNoMagic(t *testing.T) {
	p := IDF
	p.UseMagic, p.UseMagicLen = false, false
	f, err := Format(make([]byte, 256*1024), p)
	if err != nil {
		t.Fatal(err)
	}
	files := testFiles(2)
	for n, d := range files {
		if err := f.WriteFile(n, d); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := Detect(f.Image())
	if !ok || got != p {
		t.Fatalf("Detect %+v %v, want %+v", got, ok, p)
	}
	checkFiles(t, f, files)
}

func TestDetectErased(t *testing.T) {
	img := bytes.Repeat([]byte{0xFF}, 64*1024)
	if p, ok := Detect(img); ok {
		t.Fatalf("erased flash detected as %+v", p)
	}
	if _, err := Mount(img, IDF); err == nil {
		t.Fatal("erased flash mounted")
	}
}

func TestChanges(t *testing.T) {
	f, err := Format(make([]byte, 128*1024), IDF)
	if err != nil {
		t.Fatal(err)
	}
	files := testFiles(3)
	delete(files, "/big.bin")
	for n, d := range files {
		if err := f.WriteFile(n, d); err != nil {
			t.Fatal(err)
		}
	}
	// Replacing a file again and again uses up the free pages: the image
	// is compacted on the way
	r := rand.New(rand.NewSource(4))
	for i := 0; i < 40; i++ {
		d := make([]byte, 10_000+r.Intn(20_000))
		r.Read(d)
		if err := f.WriteFile("/log.bin", d); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		files["/log.bin"] = d
	}
	if f.u16(f.g.eraseOff(0)) == 0 {
		t.Error("no compaction happened")
	}
	if err := f.Rename("/www", "/web"); err != nil {
		t.Fatal(err)
	}
	files["/web/index.html"], files["/web/js/app.js"] = files["/www/index.html"], files["/www/js/app.js"]
	delete(files, "/www/index.html")
	delete(files, "/www/js/app.js")
	if err := f.Rename("hello.txt", "/hi.txt"); err != nil {
		t.Fatal(err)
	}
	files["/hi.txt"] = files["/hello.txt"]
	delete(files, "/hello.txt")
	if err := f.Remove("/empty"); err != nil {
		t.Fatal(err)
	}
	delete(files, "/empty")
	if err := f.Rename("/hi.txt", "/web/index.html"); !errors.Is(err, iofs.ErrExist) {
		t.Errorf("rename onto a file: %v", err)
	}
	if err := f.Remove("/nothing"); !errors.Is(err, iofs.ErrNotExist) {
		t.Errorf("remove of nothing: %v", err)
	}
	m, err := Mount(f.Image(), IDF)
	if err != nil {
		t.Fatal(err)
	}
	checkFiles(t, m, files)

	// A file too big fails and changes nothing
	before := append([]byte(nil), f.Image()...)
	if err := f.WriteFile("/huge", make([]byte, 120*1024)); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("huge file: %v", err)
	}
	if !bytes.Equal(before, f.Image()) {
		t.Fatal("a failed write changed the image")
	}
	if err := f.WriteFile("/"+string(bytes.Repeat([]byte("n"), 31)), nil); !errors.Is(err, ErrName) {
		t.Errorf("long name: %v", err)
	}
	used, total := f.Usage()
	if used <= 0 || used > total {
		t.Errorf("usage %d of %d", used, total)
	}
}

// Filling an image: SPIFFS keeps two blocks free, so the image can still
// be written to on the chip
func TestFull(t *testing.T) {
	f, err := Format(make([]byte, 64*1024), IDF)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for ; ; n++ {
		if err := f.WriteFile("/f"+string(rune('a'+n%26))+string(rune('a'+n/26)), make([]byte, 1000)); err != nil {
			if !errors.Is(err, ErrNoSpace) {
				t.Fatal(err)
			}
			break
		}
	}
	free := 0
	for b := 0; b < f.g.blocks; b++ {
		all := true
		for i := f.g.lu; i < f.g.ppb; i++ {
			if f.lookup(b*f.g.ppb+i) != idFree {
				all = false
			}
		}
		if all {
			free++
		}
	}
	if free < spareBlocks || n == 0 {
		t.Fatalf("%d files, %d free blocks", n, free)
	}
}

func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	files := testFiles(5)
	writeTree(t, dir, files)
	img, err := FromDir(dir, 512*1024, IDF)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := Detect(img)
	if !ok || p != IDF {
		t.Fatalf("Detect %+v %v", p, ok)
	}
	f, err := Mount(img, p)
	if err != nil {
		t.Fatal(err)
	}
	checkFiles(t, f, files)
}

func writeTree(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for n, d := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, d, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Damaged images give errors, not panics
func TestDamaged(t *testing.T) {
	f, err := Format(make([]byte, 64*1024), IDF)
	if err != nil {
		t.Fatal(err)
	}
	for n, d := range testFiles(8) {
		if n != "/big.bin" {
			if err := f.WriteFile(n, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	good := f.Image()
	r := rand.New(rand.NewSource(9))
	for i := 0; i < 300; i++ {
		img := append([]byte(nil), good...)
		for k := 0; k < 1+r.Intn(200); k++ {
			img[r.Intn(len(img))] = byte(r.Intn(256))
		}
		p, ok := Detect(img)
		if !ok {
			p = IDF
		}
		m, err := Mount(img, p)
		if err != nil {
			continue
		}
		list, _ := m.ReadDir("/")
		for _, e := range list {
			m.ReadFile("/" + e.Name)
		}
		m.WriteFile("/new.txt", []byte("new"))
		m.Remove("/www")
		m.Rename("/hello.txt", "/x")
	}
}
