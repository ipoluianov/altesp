package fatfs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The interop tests run the tools that build FAT images for the firmware,
// when they are on this machine: mkfatfs of PlatformIO and the Arduino IDE
// (it creates and unpacks plain FAT of 4 KB sectors, and creates no
// subdirectories), and the generators of an ESP-IDF checkout ($IDF_PATH:
// fatfsgen.py, wl_fatfsgen.py and fatfsparse.py, which need python3 with
// the construct package). They are skipped when not found.

func findMkfatfs(t *testing.T) string {
	home, _ := os.UserHomeDir()
	var paths []string
	for _, g := range []string{
		filepath.Join(home, ".platformio/packages/tool-mkfatfs*/mkfatfs*"),
		filepath.Join(home, ".arduino15/packages/*/tools/mkfatfs/*/mkfatfs*"),
	} {
		m, _ := filepath.Glob(g)
		paths = append(paths, m...)
	}
	if p, err := exec.LookPath("mkfatfs"); err == nil {
		paths = append(paths, p)
	}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	t.Skip("no mkfatfs on this machine")
	return ""
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	// mkfatfs 2.0.1 exits with 1 after every unpack, its own images too:
	// what it unpacked is compared instead
	unpack := strings.HasPrefix(filepath.Base(name), "mkfatfs") && len(args) > 0 && args[0] == "-u"
	if err != nil && !unpack {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(name), args, err, out)
	}
	return string(out)
}

// readTree reads the files and directories of a folder
func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		n := "/" + filepath.ToSlash(rel)
		if info.IsDir() {
			out[n+"/"] = nil
			return nil
		}
		d, err := os.ReadFile(p)
		out[n] = d
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, got, want map[string][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%d entries, want %d", len(got), len(want))
	}
	for n, w := range want {
		if g, ok := got[n]; !ok {
			t.Errorf("%s missing", n)
		} else if !bytes.Equal(g, w) {
			t.Errorf("%s: contents differ (%d bytes, want %d)", n, len(g), len(w))
		}
	}
}

// flatTree is what mkfatfs can create: files in the root
func flatTree() map[string][]byte {
	out := map[string][]byte{}
	for n, d := range testTree(20) {
		if strings.Count(n, "/") == 1 && !strings.HasSuffix(n, "/") {
			out[n] = d
		}
	}
	return out
}

// asciiTree leaves out the non-ASCII name: the tools' code pages differ
func asciiTree(seed int64) map[string][]byte {
	out := testTree(seed)
	for n := range out {
		if strings.ContainsFunc(n, func(r rune) bool { return r >= 0x80 }) {
			delete(out, n)
		}
	}
	return out
}

func TestMkfatfsImages(t *testing.T) {
	mk := findMkfatfs(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	tree := flatTree()
	writeTree(t, src, tree)
	img := filepath.Join(dir, "img.bin")
	run(t, dir, mk, "-c", src, "-s", strconv.Itoa(1<<20), img)
	data, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := Detect(data)
	if !ok || p.WL || p.SectorSize != 4096 {
		t.Fatalf("Detect %+v %v", p, ok)
	}
	f, err := Mount(data, p)
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, f, tree)

	// Changed here, unpacked by mkfatfs
	if err := f.Mkdir("/New Folder"); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/New Folder/long name inside.txt", []byte("inside")); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove("/big.bin"); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/readme.txt", []byte("replaced")); err != nil {
		t.Fatal(err)
	}
	tree["/New Folder/"] = nil
	tree["/New Folder/long name inside.txt"] = []byte("inside")
	delete(tree, "/big.bin")
	tree["/readme.txt"] = []byte("replaced")
	if err := os.WriteFile(img, f.Image(), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	run(t, dir, mk, "-u", out, "-s", strconv.Itoa(1<<20), img)
	sameTree(t, readTree(t, out), tree)
}

func TestMkfatfsUnpacks(t *testing.T) {
	mk := findMkfatfs(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	tree := asciiTree(21)
	writeTree(t, src, tree)
	for _, p := range []Params{Raw, {SectorSize: 4096, FATs: 1}} {
		data, err := FromDir(src, 1<<20, p)
		if err != nil {
			t.Fatal(err)
		}
		img := filepath.Join(dir, "img.bin")
		if err := os.WriteFile(img, data, 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "out")
		os.RemoveAll(out)
		run(t, dir, mk, "-u", out, "-s", strconv.Itoa(1<<20), img)
		sameTree(t, readTree(t, out), tree)
	}
}

// idfTools gives the python and the fatfs folder of an ESP-IDF checkout
func idfTools(t *testing.T) (py, fatfs string) {
	idf := os.Getenv("IDF_PATH")
	if idf == "" {
		t.Skip("IDF_PATH is not set")
	}
	fatfs = filepath.Join(idf, "components/fatfs")
	if _, err := os.Stat(filepath.Join(fatfs, "wl_fatfsgen.py")); err != nil {
		t.Skip("no wl_fatfsgen.py in IDF_PATH")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	if exec.Command(py, "-c", "import construct").Run() != nil {
		t.Skip("python3 has no construct package")
	}
	return py, fatfs
}

// Images of fatfsgen.py and wl_fatfsgen.py are detected and read
func TestFatfsgen(t *testing.T) {
	py, gen := idfTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	tree := asciiTree(22)
	writeTree(t, src, tree)
	for _, c := range []struct {
		script string
		args   []string
		p      Params
	}{
		{"fatfsgen.py", nil, Raw},
		{"wl_fatfsgen.py", nil, IDF},
		{"wl_fatfsgen.py", []string{"--sector_size", "512", "--wl_mode", "perf"}, Params{SectorSize: 512, WL: true}},
		{"fatfsgen.py", []string{"--sector_size", "512", "--fat_type", "16"}, Params{SectorSize: 512}},
	} {
		img := filepath.Join(dir, "img.bin")
		args := append([]string{filepath.Join(gen, c.script), src, "--output_file", img,
			"--partition_size", strconv.Itoa(2 << 20), "--long_name_support"}, c.args...)
		run(t, dir, py, args...)
		data, err := os.ReadFile(img)
		if err != nil {
			t.Fatal(err)
		}
		p, ok := Detect(data)
		if !ok || p.WL != c.p.WL || p.SectorSize != c.p.SectorSize {
			t.Fatalf("%s %v: Detect %+v %v", c.script, c.args, p, ok)
		}
		f, err := Mount(data, p)
		if err != nil {
			t.Fatal(err)
		}
		checkTree(t, f, tree)
	}
}

// Images built here are unpacked by fatfsparse.py
func TestFatfsparse(t *testing.T) {
	py, gen := idfTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	tree := asciiTree(23)
	writeTree(t, src, tree)
	for _, c := range []struct {
		p  Params
		wl string
	}{{Raw, "disabled"}, {IDF, "enabled"}} {
		data, err := FromDir(src, 2<<20, c.p)
		if err != nil {
			t.Fatal(err)
		}
		work := filepath.Join(dir, "work-"+c.wl)
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		img := filepath.Join(work, "img.bin")
		if err := os.WriteFile(img, data, 0o644); err != nil {
			t.Fatal(err)
		}
		run(t, work, py, filepath.Join(gen, "fatfsparse.py"), img, "--long-name-support", "--wl-layer", c.wl)
		// It unpacks into a folder named by the volume label
		list, _ := filepath.Glob(filepath.Join(work, "*"))
		var out string
		for _, p := range list {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				out = p
			}
		}
		if out == "" {
			t.Fatal("fatfsparse.py unpacked nothing")
		}
		sameTree(t, readTree(t, out), tree)
	}
}
