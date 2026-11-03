package fatfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	iofs "io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testTree is a set of files and directories: long and short names,
// lowercase 8.3 names, a non-ASCII name, an empty file, files of several
// clusters. A name ending in "/" is a directory.
func testTree(seed int64) map[string][]byte {
	r := rand.New(rand.NewSource(seed))
	rnd := func(n int) []byte {
		b := make([]byte, n)
		r.Read(b)
		return b
	}
	return map[string][]byte{
		"/readme.txt":                        []byte("hello\n"),
		"/README2.TXT":                       []byte("upper"),
		"/Mixed Case Name.html":              []byte("<html></html>"),
		"/empty":                             {},
		"/big.bin":                           rnd(300_000),
		"/www/":                              nil,
		"/www/index.html":                    bytes.Repeat([]byte("<p>fat</p>\n"), 500),
		"/www/js/":                           nil,
		"/www/js/a rather long file name.js": rnd(5000),
		"/www/js/ünïcödé.txt":                []byte("utf"),
		"/data/":                             nil,
		"/data/log.1":                        rnd(4096),
		"/data/log.2":                        rnd(4097),
	}
}

// checkTree compares the files and directories of a volume with want
func checkTree(t *testing.T, f *FS, want map[string][]byte) {
	t.Helper()
	got := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		list, err := f.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir %s: %v", dir, err)
		}
		for _, e := range list {
			p := strings.TrimSuffix(dir, "/") + "/" + e.Name
			if e.Dir {
				got[p+"/"] = true
				if _, ok := want[p+"/"]; !ok {
					t.Errorf("unexpected directory %s", p)
				}
				walk(p)
				continue
			}
			got[p] = true
			w, ok := want[p]
			if !ok {
				t.Errorf("unexpected file %s", p)
				continue
			}
			if e.Size != int64(len(w)) {
				t.Errorf("%s: size %d, want %d", p, e.Size, len(w))
			}
			d, err := f.ReadFile(p)
			if err != nil {
				t.Errorf("read %s: %v", p, err)
			} else if !bytes.Equal(d, w) {
				t.Errorf("%s: contents differ", p)
			}
		}
	}
	walk("/")
	for n := range want {
		if !got[n] {
			t.Errorf("%s missing", n)
		}
	}
}

func fill(t *testing.T, f *FS, tree map[string][]byte) {
	t.Helper()
	var names []string
	for n := range tree {
		names = append(names, n)
	}
	// Parents first
	sortByDepth(names)
	for _, n := range names {
		var err error
		if strings.HasSuffix(n, "/") {
			err = f.Mkdir(n)
		} else {
			err = f.WriteFile(n, tree[n])
		}
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
}

func sortByDepth(names []string) {
	depth := func(s string) int { return strings.Count(strings.TrimSuffix(s, "/"), "/") }
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if depth(names[j]) < depth(names[i]) || (depth(names[j]) == depth(names[i]) && names[j] < names[i]) {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
}

var layouts = []struct {
	name string
	size int
	p    Params
}{
	{"raw4096", 1 << 20, Raw},
	{"raw512", 1 << 20, Params{SectorSize: 512}},
	{"wl4096", 1 << 20, IDF},
	{"wl4096-2MB", 2 << 20, IDF}, // the state takes three sectors
	{"wl512perf", 1 << 20, Params{SectorSize: 512, WL: true}},
	{"wl512safe", 1 << 20, Params{SectorSize: 512, WL: true, Safe: true}},
	{"raw4096-1fat-2spc", 1536 << 10, Params{SectorSize: 4096, FATs: 1, ClusterSectors: 2, Label: "Espressif"}},
	{"fat16", 24 << 20, Params{SectorSize: 4096, WL: true}},
}

func TestRoundTrip(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			f, err := Format(make([]byte, l.size), l.p)
			if err != nil {
				t.Fatal(err)
			}
			tree := testTree(1)
			fill(t, f, tree)
			img := f.Image()
			if len(img) != l.size {
				t.Fatalf("image of %d bytes", len(img))
			}
			p, ok := Detect(img)
			if !ok || p.WL != l.p.WL || p.Safe != l.p.Safe || p.SectorSize != f.p.SectorSize {
				t.Fatalf("Detect %+v %v, want %+v", p, ok, l.p)
			}
			if l.p.Label != "" && p.Label != strings.ToUpper(l.p.Label) {
				t.Errorf("label %q", p.Label)
			}
			m, err := Mount(img, p)
			if err != nil {
				t.Fatal(err)
			}
			checkTree(t, m, tree)
			if l.name == "fat16" && m.Type() != "FAT16" {
				t.Errorf("type %s", m.Type())
			}
		})
	}
}

func TestChanges(t *testing.T) {
	f, err := Format(make([]byte, 1<<20), IDF)
	if err != nil {
		t.Fatal(err)
	}
	tree := testTree(2)
	fill(t, f, tree)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.WriteFile("/www/index.html", []byte("replaced")))
	tree["/www/index.html"] = []byte("replaced")
	must(f.WriteFile("/README.TXT", []byte("case"))) // replaces /readme.txt
	tree["/readme.txt"] = []byte("case")
	must(f.Remove("/data"))
	delete(tree, "/data/")
	delete(tree, "/data/log.1")
	delete(tree, "/data/log.2")
	must(f.Rename("/www/js", "/js"))
	for n, d := range tree {
		if rest, ok := strings.CutPrefix(n, "/www/js/"); ok {
			delete(tree, n)
			tree["/js/"+rest] = d
		}
	}
	delete(tree, "/www/js/")
	tree["/js/"] = nil
	must(f.Rename("/big.bin", "/www/Big Binary File.bin"))
	tree["/www/Big Binary File.bin"] = tree["/big.bin"]
	delete(tree, "/big.bin")
	must(f.Rename("/empty", "/EMPTY"))
	tree["/EMPTY"] = tree["/empty"]
	delete(tree, "/empty")
	// The moved directory's ".." points to the root now
	_, e, _ := f.v.lookup("/js")
	if c := f.v.clus(e.clus); string(c[32:34]) != ".." || binary.LittleEndian.Uint16(c[32+26:]) != 0 {
		t.Error(`".." of a moved directory`)
	}

	if err := f.Rename("/www", "/www/inside"); err == nil {
		t.Error("a directory moved into itself")
	}
	if err := f.Rename("/README2.TXT", "/Mixed case name.HTML"); !errors.Is(err, iofs.ErrExist) {
		t.Errorf("rename onto a file: %v", err)
	}
	if err := f.Mkdir("/www"); !errors.Is(err, iofs.ErrExist) {
		t.Errorf("mkdir of a directory: %v", err)
	}
	if err := f.WriteFile("/nodir/x", nil); !errors.Is(err, iofs.ErrNotExist) {
		t.Errorf("write into nothing: %v", err)
	}
	if err := f.WriteFile("/a:b", nil); !errors.Is(err, ErrName) {
		t.Errorf("bad name: %v", err)
	}
	if _, err := f.ReadFile("/www"); err == nil {
		t.Error("read of a directory")
	}

	// A file too big fails and changes nothing
	before := append([]byte(nil), f.Image()...)
	if err := f.WriteFile("/huge", make([]byte, 1<<20)); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("huge file: %v", err)
	}
	if !bytes.Equal(before, f.Image()) {
		t.Fatal("a failed write changed the image")
	}

	m, err := Mount(f.Image(), IDF)
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, m, tree)
	used, total := m.Usage()
	if used <= 0 || used >= total {
		t.Errorf("usage %d of %d", used, total)
	}

	// Many entries: a directory of clusters grows, the fixed root fills
	for i := 0; i < 300; i++ {
		must(f.WriteFile("/www/many file name "+string(rune('a'+i%26))+string(rune('a'+i/26)), nil))
	}
	r, err := Format(make([]byte, 256<<10), Params{SectorSize: 512, RootEntries: 16})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for ; ; n++ {
		if err := r.WriteFile("/f"+string(rune('a'+n)), nil); err != nil {
			if !errors.Is(err, ErrNoSpace) {
				t.Fatal(err)
			}
			break
		}
	}
	if n != 16 {
		t.Errorf("%d files in a root of 16 entries", n)
	}
}

func TestShortNames(t *testing.T) {
	none := func([]byte) bool { return false }
	for _, c := range []struct {
		name, short string
		lfn         bool
		nt          byte
	}{
		{"readme.txt", "README  TXT", false, 0x18},
		{"README.TXT", "README  TXT", false, 0},
		{"Readme.txt", "README  TXT", true, 0},
		{"readme.TXT", "README  TXT", false, 0x08},
		{"a long file name.text", "ALONGF~1TEX", true, 0},
		{"archive.tar.gz", "ARCHIV~1GZ ", true, 0},
		{".hidden", "HIDDEN~1   ", true, 0},
		{"ünï.txt", "_N_~1   TXT", true, 0},
		{"abcdefghi", "ABCDEF~1   ", true, 0},
	} {
		s, lfn, nt, err := makeShortName(c.name, none)
		if err != nil || string(s) != c.short || lfn != c.lfn || nt != c.nt {
			t.Errorf("%q: %q %v %#x %v, want %q %v %#x", c.name, s, lfn, nt, err, c.short, c.lfn, c.nt)
		}
	}
	taken := map[string]bool{"ALONGF~1TEX": true, "ALONGF~2TEX": true}
	s, _, _, _ := makeShortName("a long file name.text", func(b []byte) bool { return taken[string(b)] })
	if string(s) != "ALONGF~3TEX" {
		t.Errorf("alias %q", s)
	}
	for _, n := range []string{"", "...", "a/b", "a*b", "a\x01"} {
		if _, err := cleanName(n); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
	if n, _ := cleanName("trailing. . "); n != "trailing" {
		t.Errorf("trailing dots and spaces: %q", n)
	}
}

// moveDummy does on a WL partition what WL_Flash::updateWL does when the
// erase count reaches the limit: the sector after the dummy is copied
// into it, the move is recorded, and after a round the state is
// rewritten with the data turned by a sector
func moveDummy(img []byte) {
	w, _ := openWL(img)
	st := img[w.state1 : w.state1+wlStateLen]
	id := binary.LittleEndian.Uint32(st[28:])
	pos := w.pos
	next := pos + 1
	if next >= w.maxPos {
		next = 0
	}
	copy(img[pos*wlSector:(pos+1)*wlSector], img[next*wlSector:(next+1)*wlSector])
	rec := make([]byte, wlRecord)
	for k := 0; k < 4; k++ {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], id+uint32(pos*4+k))
		binary.LittleEndian.PutUint32(rec[k*4:], wlCRC(b[:]))
	}
	copy(img[w.state1+wlStateLen+pos*wlRecord:], rec)
	copy(img[w.state2+wlStateLen+pos*wlRecord:], rec)
	pos++
	if pos >= w.maxPos {
		moves := w.moves + 1
		if moves >= w.maxPos-1 {
			moves = 0
		}
		s := append([]byte(nil), st...)
		binary.LittleEndian.PutUint32(s[0:], 0)
		binary.LittleEndian.PutUint32(s[8:], uint32(moves))
		binary.LittleEndian.PutUint32(s[60:], wlCRC(s[:wlCRCLenV2]))
		for _, o := range []int{w.state1, w.state2} {
			for i := 0; i < w.stateSize; i++ {
				img[o+i] = 0xFF
			}
			copy(img[o:], s)
		}
	}
}

// The data stays readable wherever the dummy is and however far the data
// turned, and what is written then is where the chip looks
func TestWLMoves(t *testing.T) {
	for _, size := range []int{128 << 10, 1 << 20} {
		f, err := Format(make([]byte, size), IDF)
		if err != nil {
			t.Fatal(err)
		}
		tree := map[string][]byte{"/a.txt": []byte("a"), "/d/": nil, "/d/b.bin": bytes.Repeat([]byte{7}, 20000)}
		fill(t, f, tree)
		img := append([]byte(nil), f.Image()...)
		w, _ := openWL(img)
		total := 0
		for _, n := range []int{1, 3, w.maxPos - 4, 1, w.maxPos, 2*w.maxPos + 5} {
			for i := 0; i < n; i++ {
				moveDummy(img)
			}
			total += n
			m, err := Mount(img, IDF)
			if err != nil {
				t.Fatalf("after %d moves: %v", total, err)
			}
			checkTree(t, m, tree)
			name := "/after" + string(rune('a'+total%26))
			if err := m.WriteFile(name, []byte(name)); err != nil {
				t.Fatal(err)
			}
			tree[name] = []byte(name)
			img = append([]byte(nil), m.Image()...)
		}
		if w, _ := openWL(img); w.moves == 0 && w.pos == 0 {
			t.Error("the dummy did not move")
		}
	}
}

// The chip's first mount of a partition without a state puts the data
// after a dummy at sector 0; so does Mount
func TestWLFresh(t *testing.T) {
	f, err := Format(make([]byte, 256<<10), IDF)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, f, map[string][]byte{"/x": []byte("x")})
	img := f.Image()
	l, _ := newWLLayout(len(img))
	for i := l.state1; i < len(img); i++ {
		img[i] = 0xFF
	}
	if _, ok := Detect(img); ok {
		t.Error("detected without the config sector")
	}
	m, err := Mount(img, IDF)
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, m, map[string][]byte{"/x": []byte("x")})
	if _, ok := Detect(m.Image()); !ok {
		t.Error("no state written for a fresh partition")
	}
}

// A sector copy of safe mode cut short is finished on mount
func TestSafeRecover(t *testing.T) {
	logical := make([]byte, 8*wlSector)
	for i := range logical {
		logical[i] = byte(i / 512)
	}
	dump := logical[7*wlSector:]
	for i := range dump {
		dump[i] = 0xAA
	}
	st := logical[6*wlSector:]
	for i, v := range []uint32{safeOK, 2, 3, 2} {
		binary.LittleEndian.PutUint32(st[i*4:], v)
	}
	if err := recoverSafe(logical); err != nil {
		t.Fatal(err)
	}
	sec := logical[2*wlSector:]
	for i := 0; i < 8; i++ {
		want := byte(0xAA)
		if i == 3 || i == 4 {
			want = 0xFF
		}
		if sec[i*512] != want {
			t.Errorf("sector %d: %#x, want %#x", i, sec[i*512], want)
		}
	}
	if binary.LittleEndian.Uint32(st) != 0xFFFFFFFF {
		t.Error("the state is not cleared")
	}
}

func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	tree := testTree(3)
	writeTree(t, dir, tree)
	for _, l := range layouts[:6] {
		img, err := FromDir(dir, l.size, l.p)
		if err != nil {
			t.Fatal(err)
		}
		p, ok := Detect(img)
		if !ok {
			t.Fatalf("%s: not detected", l.name)
		}
		f, err := Mount(img, p)
		if err != nil {
			t.Fatal(err)
		}
		checkTree(t, f, tree)
	}
}

func TestDetectOther(t *testing.T) {
	for _, img := range [][]byte{bytes.Repeat([]byte{0xFF}, 1<<20), make([]byte, 1<<20), {}} {
		if p, ok := Detect(img); ok {
			t.Errorf("detected %+v", p)
		}
	}
}

func writeTree(t *testing.T, dir string, tree map[string][]byte) {
	t.Helper()
	for n, d := range tree {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if strings.HasSuffix(n, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
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
	f, err := Format(make([]byte, 256<<10), IDF)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, f, map[string][]byte{"/a/": nil, "/a/b.txt": bytes.Repeat([]byte("b"), 9000), "/long file name.txt": []byte("x")})
	good := f.Image()
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 300; i++ {
		img := append([]byte(nil), good...)
		for k := 0; k < 1+r.Intn(200); k++ {
			// Most damage where the metadata is: the start and the end
			o := r.Intn(48 << 10)
			if r.Intn(2) == 0 {
				o = len(img) - 1 - r.Intn(16<<10)
			}
			img[o] = byte(r.Intn(256))
		}
		p, ok := Detect(img)
		if !ok {
			p = IDF
		}
		m, err := Mount(img, p)
		if err != nil {
			continue
		}
		var walk func(string)
		walk = func(dir string) {
			list, _ := m.ReadDir(dir)
			for _, e := range list {
				p := strings.TrimSuffix(dir, "/") + "/" + e.Name
				if e.Dir && strings.Count(p, "/") < 8 {
					walk(p)
				} else {
					m.ReadFile(p)
				}
			}
		}
		walk("/")
		m.WriteFile("/new.txt", []byte("new"))
		m.Remove("/a")
		m.Image()
	}
}
