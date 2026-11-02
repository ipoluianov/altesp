// mkicon writes the icon of the program as PNG files of the given sizes
// and as a Windows .ico, for the packaging scripts:
//
//	go run ./cmd/mkicon -out dist/icons -sizes 16,32,48,64,128,256,512,1024
//
// The files are named altesp-<size>.png and altesp.ico.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ipoluianov/altesp/appicon"
)

// icoSizes are the sizes put into the .ico: Windows takes nothing larger
// than 256
var icoSizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	out := flag.String("out", "icons", "the directory to write the files into")
	sizes := flag.String("sizes", "16,32,48,64,128,256,512,1024", "the sizes of the PNG files, comma separated")
	ico := flag.Bool("ico", true, "write altesp.ico too")
	flag.Parse()

	list, err := parseSizes(*sizes)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for _, size := range list {
		data, err := encodePNG(size)
		if err != nil {
			fail(err)
		}
		name := filepath.Join(*out, fmt.Sprintf("altesp-%d.png", size))
		if err := os.WriteFile(name, data, 0o644); err != nil {
			fail(err)
		}
	}
	if *ico {
		data, err := makeICO(icoSizes)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(filepath.Join(*out, "altesp.ico"), data, 0o644); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mkicon:", err)
	os.Exit(1)
}

// parseSizes reads "16,32,48" into sorted sizes without repeats
func parseSizes(s string) ([]int, error) {
	seen := map[int]bool{}
	var list []int
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > 4096 {
			return nil, fmt.Errorf("bad size %q", f)
		}
		if !seen[n] {
			seen[n] = true
			list = append(list, n)
		}
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no sizes")
	}
	sort.Ints(list)
	return list, nil
}

func encodePNG(size int) ([]byte, error) { return appicon.PNG(size) }

func makeICO(sizes []int) ([]byte, error) { return appicon.ICO(sizes) }
