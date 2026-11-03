package spiffs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The interop tests run the tools that build SPIFFS images for the
// firmware, when they are on this machine: the mkspiffs builds of
// PlatformIO and the Arduino IDE (they create and unpack images), and
// spiffsgen.py of an ESP-IDF checkout ($IDF_PATH; it only creates). They
// are skipped when none is found.

type mkspiffs struct {
	path  string
	build string // "esp-idf", "arduino-esp32", "arduino-esp8266"
	p     Params
}

func findMkspiffs(t *testing.T) []mkspiffs {
	home, _ := os.UserHomeDir()
	var paths []string
	for _, g := range []string{
		filepath.Join(home, ".platformio/packages/tool-mkspiffs*/mkspiffs*"),
		filepath.Join(home, ".arduino15/packages/*/tools/mkspiffs/*/mkspiffs*"),
	} {
		m, _ := filepath.Glob(g)
		paths = append(paths, m...)
	}
	if p, err := exec.LookPath("mkspiffs"); err == nil {
		paths = append(paths, p)
	}
	re := regexp.MustCompile(`Build configuration name: (\S+)`)
	var out []mkspiffs
	for _, p := range paths {
		v, err := exec.Command(p, "--version").CombinedOutput()
		if err != nil {
			continue
		}
		m := re.FindSubmatch(v)
		if m == nil {
			continue
		}
		x := mkspiffs{path: p, build: string(m[1]), p: IDF}
		switch x.build {
		case "esp-idf", "arduino-esp32":
		case "arduino-esp8266":
			x.p = ESP8266
		default:
			continue
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		t.Skip("no mkspiffs on this machine")
	}
	return out
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(name), args, err, out)
	}
}

// readTree reads the files of a folder as "/a/b" names
func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		d, err := os.ReadFile(p)
		out["/"+filepath.ToSlash(rel)] = d
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
		t.Errorf("%d files, want %d", len(got), len(want))
	}
	for n, w := range want {
		if g, ok := got[n]; !ok {
			t.Errorf("%s missing", n)
		} else if !bytes.Equal(g, w) {
			t.Errorf("%s: contents differ (%d bytes, want %d)", n, len(g), len(w))
		}
	}
}

const interopSize = 1 << 20

// Images of mkspiffs are detected and read
func TestMkspiffsImages(t *testing.T) {
	for _, m := range findMkspiffs(t) {
		t.Run(m.build, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			files := testFiles(10)
			writeTree(t, src, files)
			img := filepath.Join(dir, "img.bin")
			run(t, m.path, "-c", src, "-p", strconv.Itoa(m.p.PageSize), "-b", strconv.Itoa(m.p.BlockSize),
				"-s", strconv.Itoa(interopSize), img)
			data, err := os.ReadFile(img)
			if err != nil {
				t.Fatal(err)
			}
			p, ok := Detect(data)
			if !ok || p != m.p {
				t.Fatalf("Detect %+v %v, want %+v", p, ok, m.p)
			}
			f, err := Mount(data, p)
			if err != nil {
				t.Fatal(err)
			}
			checkFiles(t, f, files)
		})
	}
}

// Images built and changed here are unpacked by mkspiffs
func TestMkspiffsUnpacks(t *testing.T) {
	for _, m := range findMkspiffs(t) {
		t.Run(m.build, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			files := testFiles(11)
			writeTree(t, src, files)
			data, err := FromDir(src, interopSize, m.p)
			if err != nil {
				t.Fatal(err)
			}
			unpack := func(data []byte, want map[string][]byte) {
				img := filepath.Join(dir, "img.bin")
				out := filepath.Join(dir, "out")
				os.RemoveAll(out)
				if err := os.WriteFile(img, data, 0o644); err != nil {
					t.Fatal(err)
				}
				run(t, m.path, "-u", out, "-p", strconv.Itoa(m.p.PageSize), "-b", strconv.Itoa(m.p.BlockSize),
					"-s", strconv.Itoa(interopSize), img)
				sameTree(t, readTree(t, out), want)
			}
			unpack(data, files)

			// Changes to an image of mkspiffs: replace, delete, rename,
			// and enough rewrites to compact it
			img := filepath.Join(dir, "mk.bin")
			run(t, m.path, "-c", src, "-p", strconv.Itoa(m.p.PageSize), "-b", strconv.Itoa(m.p.BlockSize),
				"-s", strconv.Itoa(interopSize), img)
			data, err = os.ReadFile(img)
			if err != nil {
				t.Fatal(err)
			}
			f, err := Mount(data, m.p)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 30; i++ {
				d := bytes.Repeat([]byte{byte(i)}, 60_000)
				if err := f.WriteFile("/big.bin", d); err != nil {
					t.Fatal(err)
				}
				files["/big.bin"] = d
			}
			if err := f.Remove("/hello.txt"); err != nil {
				t.Fatal(err)
			}
			delete(files, "/hello.txt")
			if err := f.Rename("/www/index.html", "/index.htm"); err != nil {
				t.Fatal(err)
			}
			files["/index.htm"] = files["/www/index.html"]
			delete(files, "/www/index.html")
			if err := f.WriteFile("/new/file.txt", []byte("new")); err != nil {
				t.Fatal(err)
			}
			files["/new/file.txt"] = []byte("new")
			unpack(f.Image(), files)
		})
	}
}

// Images of spiffsgen.py (ESP-IDF) are detected and read
func TestSpiffsgen(t *testing.T) {
	gen := filepath.Join(os.Getenv("IDF_PATH"), "components/spiffs/spiffsgen.py")
	if os.Getenv("IDF_PATH") == "" {
		t.Skip("IDF_PATH is not set")
	}
	if _, err := os.Stat(gen); err != nil {
		t.Skip("no spiffsgen.py in IDF_PATH")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	files := testFiles(12)
	writeTree(t, src, files)
	for _, c := range []struct {
		args []string
		p    Params
	}{
		{nil, IDF},
		{[]string{"--meta-len", "0", "--no-magic-len", "--aligned-obj-ix-tables", "--block-size", "8192"}, ESP8266},
	} {
		img := filepath.Join(dir, "img.bin")
		run(t, py, append([]string{"-I", gen, strconv.Itoa(interopSize), src, img}, c.args...)...)
		data, err := os.ReadFile(img)
		if err != nil {
			t.Fatal(err)
		}
		p, ok := Detect(data)
		if !ok || p != c.p {
			t.Fatalf("Detect %+v %v, want %+v", p, ok, c.p)
		}
		f, err := Mount(data, p)
		if err != nil {
			t.Fatal(err)
		}
		checkFiles(t, f, files)
	}
}
