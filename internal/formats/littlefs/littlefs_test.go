package littlefs

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCRC(t *testing.T) {
	// The CRC-32 check value 0xCBF43926 without the final inversion
	if c := crc(0xFFFFFFFF, []byte("123456789")); c != ^uint32(0xCBF43926) {
		t.Fatalf("crc %08X", c)
	}
}

func TestCTZCount(t *testing.T) {
	// Block 0 holds 4096 bytes, block 1 4092, block 2 4088, block 3 4092
	for _, c := range []struct {
		size uint64
		n    int
	}{{1, 1}, {4096, 1}, {4097, 2}, {4096 + 4092, 2}, {4096 + 4092 + 1, 3}, {4096 + 4092 + 4088 + 4092, 4}} {
		if n := ctzCount(c.size, 4096, 1<<20); n != c.n {
			t.Fatalf("%d bytes: %d blocks, want %d", c.size, n, c.n)
		}
	}
}

func pattern(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func mustFormat(t *testing.T, size int, cfg Config) *FS {
	t.Helper()
	fsys, err := Format(make([]byte, size), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

func remount(t *testing.T, fsys *FS) *FS {
	t.Helper()
	m, err := Mount(fsys.Image())
	if err != nil {
		t.Fatal(err)
	}
	if m.repair {
		t.Fatal("global state not clear")
	}
	return m
}

// tree lists every file of an image with its content, directories end
// with "/"
func tree(t *testing.T, fsys *FS) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(dir string)
	walk = func(dir string) {
		list, err := fsys.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range list {
			p := path.Join(dir, in.Name)
			if in.Dir {
				out[p+"/"] = ""
				walk(p)
				continue
			}
			data, err := fsys.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(data)) != in.Size {
				t.Fatalf("%s: %d bytes, size %d", p, len(data), in.Size)
			}
			out[p] = string(data)
		}
	}
	walk("/")
	return out
}

func sameTree(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		g, ok := got[k]
		if !ok {
			t.Fatalf("%s missing", k)
		}
		if g != v {
			t.Fatalf("%s: %d bytes differ from %d", k, len(g), len(v))
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Fatalf("%s is extra", k)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, v := range []uint32{Version20, Version21} {
		t.Run(fmt.Sprintf("%x", v), func(t *testing.T) {
			fsys := mustFormat(t, 1<<20, Config{BlockSize: 4096, DiskVersion: v})
			want := map[string]string{}
			if err := fsys.Mkdir("/d"); err != nil {
				t.Fatal(err)
			}
			if err := fsys.Mkdir("d/e"); err != nil {
				t.Fatal(err)
			}
			want["/d/"], want["/d/e/"] = "", ""
			for i, n := range []int{0, 1, 127, 128, 129, 4095, 4096, 4097, 4096 + 4092, 4096 + 4092 + 1, 3 * 4096, 100000, 300000} {
				p := fmt.Sprintf("/d/e/f%d", n)
				if i%2 == 0 {
					p = fmt.Sprintf("/f%d", n)
				}
				data := pattern(n, int64(n))
				if err := fsys.WriteFile(p, data); err != nil {
					t.Fatalf("%s: %v", p, err)
				}
				want[p] = string(data)
			}
			sameTree(t, tree(t, fsys), want)
			m := remount(t, fsys)
			if m.Config().DiskVersion != v || m.Config().BlockCount != 256 || m.Config().NameMax != DefaultNameMax {
				t.Fatalf("config %+v", m.Config())
			}
			sameTree(t, tree(t, m), want)
			st, err := m.Stat("/d/e/f100000")
			if err != nil || st.Size != 100000 || st.Dir {
				t.Fatalf("stat %+v %v", st, err)
			}
			if st, err := m.Stat("/d"); err != nil || !st.Dir {
				t.Fatalf("stat %+v %v", st, err)
			}
			if _, err := m.Stat("/nope"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("stat of a missing file: %v", err)
			}
			// Replacing a large file with a small one frees its blocks
			before := m.usedCount()
			if err := m.WriteFile("/f300000", []byte("small")); err != nil {
				t.Fatal(err)
			}
			if after := m.usedCount(); after >= before-70 {
				t.Fatalf("blocks in use %d, then %d", before, after)
			}
			want["/f300000"] = "small"
			sameTree(t, tree(t, remount(t, m)), want)
		})
	}
}

func (fsys *FS) usedCount() int {
	n := 0
	for _, u := range fsys.used {
		if u {
			n++
		}
	}
	return n
}

func TestManyFiles(t *testing.T) {
	fsys := mustFormat(t, 2<<20, Config{BlockSize: 4096})
	want := map[string]string{}
	if err := fsys.Mkdir("sub"); err != nil {
		t.Fatal(err)
	}
	want["/sub/"] = ""
	for i := range 400 {
		dir := "/"
		if i%2 == 1 {
			dir = "/sub"
		}
		p := path.Join(dir, fmt.Sprintf("file-with-a-longer-name-%03d.txt", (i*37)%400))
		data := pattern(i%150, int64(i))
		if err := fsys.WriteFile(p, data); err != nil {
			t.Fatal(err)
		}
		want[p] = string(data)
	}
	if len(fsys.dirs) < 10 {
		t.Fatalf("only %d metadata pairs: the directories did not split", len(fsys.dirs))
	}
	// Each pair of a directory holds names greater than the one before
	for _, dir := range []pair{fsys.root} {
		ch, _ := fsys.chain(dir)
		last := ""
		for _, d := range ch {
			for _, e := range d.entries {
				if e.typ != typeSuper {
					if e.name <= last {
						t.Fatalf("%q after %q", e.name, last)
					}
					last = e.name
				}
			}
		}
	}
	m := remount(t, fsys)
	sameTree(t, tree(t, m), want)
	pairs := len(m.dirs)
	// Remove most: emptied pairs leave the lists
	for k := range want {
		if strings.HasSuffix(k, "/") || strings.HasSuffix(k, "7.txt") {
			continue
		}
		if err := m.Remove(k); err != nil {
			t.Fatal(err)
		}
		delete(want, k)
	}
	m = remount(t, m)
	sameTree(t, tree(t, m), want)
	if len(m.dirs) >= pairs {
		t.Fatalf("%d metadata pairs left of %d", len(m.dirs), pairs)
	}
	// Remove the rest: one pair per directory stays
	for k := range want {
		if !strings.HasSuffix(k, "/") {
			if err := m.Remove(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	m = remount(t, m)
	if len(m.dirs) != 2 || m.usedCount() != 4 {
		for k, d := range m.dirs {
			t.Logf("%v: %d entries, tail %v %x", k, len(d.entries), d.tail, d.tailType)
		}
		t.Fatalf("%d metadata pairs, %d blocks left", len(m.dirs), m.usedCount())
	}
}

func TestDirs(t *testing.T) {
	fsys := mustFormat(t, 256<<10, Config{BlockSize: 4096})
	for _, d := range []string{"a", "a/b", "a/b/c", "x"} {
		if err := fsys.Mkdir(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := fsys.Mkdir("a"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("mkdir twice: %v", err)
	}
	if err := fsys.Mkdir("q/r"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("mkdir without parent: %v", err)
	}
	big := pattern(10000, 1)
	if err := fsys.WriteFile("a/b/c/big", big); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile("a/b/small", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Remove("a/b"); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("remove of a full directory: %v", err)
	}
	if err := fsys.WriteFile("a/b", nil); !errors.Is(err, ErrIsDir) {
		t.Fatalf("write over a directory: %v", err)
	}
	// Rename a directory into another
	if err := fsys.Rename("a/b", "x/b2"); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Rename("x", "x/b2/c/x"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("rename into itself: %v", err)
	}
	// Rename a file over another
	if err := fsys.WriteFile("x/b2/other", []byte("other")); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Rename("x/b2/small", "x/b2/other"); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Rename("x/b2/c/big", "/big"); err != nil {
		t.Fatal(err)
	}
	m := remount(t, fsys)
	sameTree(t, tree(t, m), map[string]string{
		"/a/": "", "/x/": "", "/x/b2/": "", "/x/b2/c/": "",
		"/x/b2/other": "hi", "/big": string(big),
	})
	// Remove the empty directories; their pairs leave the list
	for _, d := range []string{"x/b2/c", "x/b2/other", "x/b2", "x", "a"} {
		if err := m.Remove(d); err != nil {
			t.Fatal(err)
		}
	}
	m = remount(t, m)
	sameTree(t, tree(t, m), map[string]string{"/big": string(big)})
	if len(m.dirs) != 1 || m.usedCount() != 2+3 {
		t.Fatalf("%d pairs, %d blocks in use", len(m.dirs), m.usedCount())
	}
}

func TestNoSpace(t *testing.T) {
	fsys := mustFormat(t, 16*4096, Config{BlockSize: 4096})
	if err := fsys.WriteFile("a", pattern(8*4096, 1)); err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(fsys.Image())
	if err := fsys.WriteFile("b", pattern(8*4096, 2)); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("write past the end: %v", err)
	}
	if !bytes.Equal(before, fsys.Image()) {
		t.Fatal("a failed write changed the image")
	}
	if err := fsys.WriteFile("b", pattern(4*4096, 2)); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile(strings.Repeat("n", 65), nil); !errors.Is(err, ErrNameLen) {
		t.Fatalf("long name: %v", err)
	}
}

// TestRandom runs random operations against a map
func TestRandom(t *testing.T) {
	for _, v := range []uint32{Version20, Version21} {
		fsys := mustFormat(t, 512<<10, Config{BlockSize: 4096, DiskVersion: v})
		want := churn(t, fsys, int64(v), 3000)
		sameTree(t, tree(t, remount(t, fsys)), want)
	}
}

// churn runs random operations on an image, checking it against a map
// on the way, and returns what the image holds
func churn(t *testing.T, fsys *FS, seed int64, ops int) map[string]string {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	want := map[string]string{}
	dirs := []string{"/"}
	for i := range ops {
		dir := dirs[r.Intn(len(dirs))]
		name := path.Join(dir, fmt.Sprintf("n%d", r.Intn(40)))
		_, isDir := want[name+"/"]
		_, isFile := want[name]
		switch op := r.Intn(10); {
		case op < 5:
			data := string(pattern([]int{0, 10, 200, 5000, 20000}[r.Intn(5)], int64(i)))
			err := fsys.WriteFile(name, []byte(data))
			switch {
			case isDir:
				if !errors.Is(err, ErrIsDir) {
					t.Fatalf("%d: write over dir %s: %v", i, name, err)
				}
			case errors.Is(err, ErrNoSpace):
				// The old content stays
			case err != nil:
				t.Fatalf("%d: write %s: %v", i, name, err)
			default:
				want[name] = data
			}
		case op < 6 && len(dirs) < 12:
			err := fsys.Mkdir(name)
			if isDir || isFile {
				if !errors.Is(err, fs.ErrExist) {
					t.Fatalf("%d: mkdir %s: %v", i, name, err)
				}
			} else if err != nil {
				t.Fatalf("%d: mkdir %s: %v", i, name, err)
			} else {
				want[name+"/"] = ""
				dirs = append(dirs, name)
			}
		case op < 8:
			if !isFile {
				continue
			}
			to := path.Join(dirs[r.Intn(len(dirs))], fmt.Sprintf("n%d", r.Intn(40)))
			if _, ok := want[to+"/"]; ok {
				continue
			}
			if err := fsys.Rename(name, to); err != nil {
				t.Fatalf("%d: rename %s %s: %v", i, name, to, err)
			}
			data := want[name]
			delete(want, name)
			want[to] = data
		default:
			err := fsys.Remove(name)
			switch {
			case isFile:
				if err != nil {
					t.Fatalf("%d: remove %s: %v", i, name, err)
				}
				delete(want, name)
			case !isDir:
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("%d: remove missing %s: %v", i, name, err)
				}
			}
			if isDir {
				empty := true
				for k := range want {
					if strings.HasPrefix(k, name+"/") {
						empty = false
					}
				}
				if empty != (err == nil) {
					t.Fatalf("%d: remove dir %s (empty %v): %v", i, name, empty, err)
				}
				if empty {
					delete(want, name+"/")
					for j, d := range dirs {
						if d == name {
							dirs = append(dirs[:j], dirs[j+1:]...)
							break
						}
					}
				}
			}
		}
		if i%250 == 0 {
			// Nothing leaks: the blocks in use are the ones a fresh
			// mount finds
			m := remount(t, fsys)
			sameTree(t, tree(t, m), want)
			if m.usedCount() != fsys.usedCount() {
				t.Fatalf("%d: %d blocks in use, a mount finds %d", i, fsys.usedCount(), m.usedCount())
			}
		}
	}
	return want
}

// TestMoveRepair writes the first half of a move between directories,
// as a power loss would leave it: the file is in both, and the global
// state names the source
func TestMoveRepair(t *testing.T) {
	fsys := mustFormat(t, 128<<10, Config{BlockSize: 4096})
	if err := fsys.Mkdir("d"); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile("f", []byte("data")); err != nil {
		t.Fatal(err)
	}
	src, _ := fsys.resolve("f")
	dst, _ := fsys.walkDir([]string{"d"})
	dd := fsys.dirs[dst.key()]
	e := *src.entry()
	dd.entries = append(dd.entries, &e)
	g := mkTag(tagDelete, uint16(src.id), 0)
	dd.gdelta[0], dd.gdelta[1], dd.gdelta[2], dd.gdelta[3] = byte(g), byte(g>>8), byte(g>>16), byte(g>>24)
	copy(dd.gdelta[4:], src.d.pair.bytes())
	dd.dirty = true
	if err := fsys.sync(); err != nil {
		t.Fatal(err)
	}
	m, err := Mount(fsys.Image())
	if err != nil {
		t.Fatal(err)
	}
	if !m.repair {
		t.Fatal("the move is not seen")
	}
	want := map[string]string{"/d/": "", "/d/f": "data"}
	sameTree(t, tree(t, m), want)
	if err := m.WriteFile("g", []byte("x")); err != nil {
		t.Fatal(err)
	}
	want["/g"] = "x"
	sameTree(t, tree(t, remount(t, m)), want)
}

func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	want := writeHostTree(t, dir, 1)
	img, err := FromDir(dir, 2<<20, 4096)
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := Mount(img)
	if err != nil {
		t.Fatal(err)
	}
	sameTree(t, tree(t, fsys), want)
}

// writeHostTree makes a folder of files of many sizes and returns what
// tree() should give for it
func writeHostTree(t *testing.T, dir string, seed int64) map[string]string {
	t.Helper()
	want := map[string]string{}
	r := rand.New(rand.NewSource(seed))
	for _, d := range []string{"cfg", "www", "www/img", "empty"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
		want["/"+d+"/"] = ""
	}
	sizes := []int{0, 1, 60, 100, 128, 129, 255, 300, 1000, 4095, 4096, 4097, 9000, 40000, 70000}
	for i := range 60 {
		d := []string{"", "cfg", "www", "www/img"}[i%4]
		name := fmt.Sprintf("f%02d_%d.bin", i, r.Intn(1000))
		data := pattern(sizes[i%len(sizes)], seed*1000+int64(i))
		if err := os.WriteFile(filepath.Join(dir, d, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		want[path.Join("/", d, name)] = string(data)
	}
	return want
}

// The reference tool: mklittlefs of PlatformIO or Arduino, if present
func findMklittlefs(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("mklittlefs"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, pat := range []string{
		".platformio/packages/tool-mklittlefs*/mklittlefs",
		".arduino15/packages/*/tools/mklittlefs/*/mklittlefs",
	} {
		m, _ := filepath.Glob(filepath.Join(home, pat))
		sort.Strings(m)
		if len(m) > 0 {
			return m[len(m)-1]
		}
	}
	t.Skip("mklittlefs not found")
	return ""
}

func runTool(t *testing.T, tool string, args ...string) {
	t.Helper()
	out, err := exec.Command(tool, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("mklittlefs %v: %v\n%s", args, err, out)
	}
}

// hostTree reads a folder the way tree() reads an image
func hostTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel := "/" + filepath.ToSlash(strings.TrimPrefix(p, dir+string(filepath.Separator)))
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(p)
		out[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The tool's images are read by us
func TestMklittlefsImages(t *testing.T) {
	tool := findMklittlefs(t)
	for _, c := range []struct{ block, page, size int }{
		{4096, 256, 1 << 20}, {4096, 16, 1 << 20}, {8192, 256, 2 << 20},
	} {
		t.Run(fmt.Sprintf("%d_%d", c.block, c.page), func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			want := writeHostTree(t, src, int64(c.page))
			// The tool leaves out empty folders
			delete(want, "/empty/")
			img := filepath.Join(dir, "img.bin")
			runTool(t, tool, "-c", src, "-b", fmt.Sprint(c.block), "-p", fmt.Sprint(c.page), "-s", fmt.Sprint(c.size), img)
			b, err := os.ReadFile(img)
			if err != nil {
				t.Fatal(err)
			}
			fsys, err := Mount(b)
			if err != nil {
				t.Fatal(err)
			}
			if cfg := fsys.Config(); cfg.BlockSize != uint32(c.block) || cfg.BlockCount != uint32(c.size/c.block) {
				t.Fatalf("config %+v", cfg)
			}
			sameTree(t, tree(t, fsys), want)

			// Changed by us, read back by the tool
			if err := fsys.WriteFile("/cfg/new.txt", []byte("new file")); err != nil {
				t.Fatal(err)
			}
			if err := fsys.WriteFile("/www/big.bin", pattern(50000, 9)); err != nil {
				t.Fatal(err)
			}
			if err := fsys.Mkdir("/www/img/deeper"); err != nil {
				t.Fatal(err)
			}
			for k := range want {
				if strings.HasPrefix(k, "/www/img/f") {
					if err := fsys.Rename(k, "/www/img/deeper/"+path.Base(k)); err != nil {
						t.Fatal(err)
					}
				}
				if strings.HasPrefix(k, "/f") {
					if err := fsys.Remove(k); err != nil {
						t.Fatal(err)
					}
				}
			}
			mine := tree(t, fsys)
			if err := os.WriteFile(img, fsys.Image(), 0o644); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "out")
			runTool(t, tool, "-u", out, "-b", fmt.Sprint(c.block), "-p", fmt.Sprint(c.page), "-s", fmt.Sprint(c.size), img)
			sameTree(t, withEmptyDirs(hostTree(t, out), mine), mine)
		})
	}
}

// Our images are read by the tool
func TestMklittlefsUnpack(t *testing.T) {
	tool := findMklittlefs(t)
	for _, v := range []uint32{Version20} {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		want := writeHostTree(t, src, 3)
		// Many files in one folder: split directories
		for i := range 150 {
			name := fmt.Sprintf("many_%03d.txt", i)
			data := pattern(i, int64(i))
			if err := os.WriteFile(filepath.Join(src, "cfg", name), data, 0o644); err != nil {
				t.Fatal(err)
			}
			want["/cfg/"+name] = string(data)
		}
		// The tool's littlefs takes names up to 32 bytes
		img, err := Build(src, 1<<20, Config{BlockSize: 4096, NameMax: 32, DiskVersion: v})
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, "img.bin")
		if err := os.WriteFile(f, img, 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "out")
		runTool(t, tool, "-u", out, "-b", "4096", "-p", "256", "-s", fmt.Sprint(1<<20), f)
		sameTree(t, withEmptyDirs(hostTree(t, out), want), want)

		// An image after many changes
		fsys := mustFormat(t, 512<<10, Config{BlockSize: 4096, NameMax: 32, DiskVersion: v})
		want = churn(t, fsys, 11, 2000)
		if err := os.WriteFile(f, fsys.Image(), 0o644); err != nil {
			t.Fatal(err)
		}
		out = filepath.Join(dir, "out2")
		runTool(t, tool, "-u", out, "-b", "4096", "-p", "256", "-s", fmt.Sprint(512<<10), f)
		sameTree(t, withEmptyDirs(hostTree(t, out), want), want)
	}
}

// withEmptyDirs adds the empty directories of want the tool may not
// create when unpacking
func withEmptyDirs(got, want map[string]string) map[string]string {
	for k := range want {
		if strings.HasSuffix(k, "/") {
			if _, ok := got[k]; !ok {
				empty := true
				for g := range want {
					if g != k && strings.HasPrefix(g, k) {
						empty = false
					}
				}
				if empty {
					got[k] = ""
				}
			}
		}
	}
	return got
}
