package nvs

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestRealImage reads an NVS partition from a flash backup given as
// ALTESP_NVS_FILE=path:offset:size (e.g. backup.bin:0x12000:0xE000). The
// backups are of real boards and are not kept in the repository.
func TestRealImage(t *testing.T) {
	spec := os.Getenv("ALTESP_NVS_FILE")
	if spec == "" {
		t.Skip("ALTESP_NVS_FILE is not set")
	}
	parts := strings.Split(spec, ":")
	if len(parts) != 3 {
		t.Fatalf("ALTESP_NVS_FILE=%q, want path:offset:size", spec)
	}
	off, err1 := strconv.ParseInt(parts[1], 0, 64)
	size, err2 := strconv.ParseInt(parts[2], 0, 64)
	if err1 != nil || err2 != nil {
		t.Fatalf("bad offset or size in %q", spec)
	}
	b, err := os.ReadFile(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(b)) < off+size {
		t.Fatalf("file has %d bytes", len(b))
	}
	data := b[off : off+size]
	im, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range im.Problems {
		t.Errorf("problem: %s", p)
	}
	t.Logf("used %d, free %d, namespaces %v", im.UsedEntries, im.FreeEntries, im.Namespaces())
	for _, p := range im.Pages {
		t.Logf("page %+v", p)
	}
	for _, e := range im.Entries {
		v := e.Value
		if b, ok := v.([]byte); ok {
			v = fmt.Sprintf("%d bytes", len(b))
		}
		t.Logf("%s/%s %v = %v", e.Namespace, e.Key, e.Type, v)
	}
	// Rebuilt with a changed value, it keeps its format version and
	// reads back the same plus the change
	ver := im.Version
	if err := im.Set(Entry{"UART", "rate", I32, 921600}); err != nil {
		t.Fatal(err)
	}
	out, err := im.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	im2, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(im2.Problems) > 0 {
		t.Errorf("rebuilt: %v", im2.Problems)
	}
	sameEntries(t, im.Entries, im2.Entries)
	if im2.Version != ver {
		t.Errorf("version %d, was %d", im2.Version, ver)
	}
	for _, p := range im2.Pages {
		if p.Version != 0 && p.Version != ver {
			t.Errorf("page %+v", p)
		}
	}
	if e, _ := im2.Get("UART", "rate"); e.Value != int64(921600) {
		t.Errorf("rate = %v", e.Value)
	}
}
