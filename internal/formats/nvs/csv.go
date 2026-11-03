package nvs

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The CSV of nvs_partition_gen: ESP-IDF Programming Guide "NVS Partition
// Generator Utility" (ESP-IDF master, Nov 2026). Columns key, type,
// encoding, value; the first line is that header. A row of type
// "namespace" starts a namespace; rows of type "data" hold the value
// itself, rows of type "file" the path of a file with the value.
// Encodings: u8, i8, u16, i16, u32, i32, u64, i64, string, hex2bin,
// base64, binary (file only); hex2bin, base64 and binary give a blob.
//
// Differences: the utility resolves a relative file path against its
// working directory, ReadCSV against dir (the CSV's folder); "float" and
// "double" are accepted and written for the newer NVS types, which the
// utility may not know.

// ReadCSV reads values from an nvs_partition_gen CSV. Relative file
// paths are taken from dir. Namespaces without values are dropped;
// ReadCSVImage keeps them.
func ReadCSV(r io.Reader, dir string) ([]Entry, error) {
	entries, _, err := readCSV(r, dir)
	return entries, err
}

// ReadCSVImage reads a CSV into an empty image of the given size, so
// that namespaces without values are kept
func ReadCSVImage(r io.Reader, dir string, size int) (*Image, error) {
	entries, spaces, err := readCSV(r, dir)
	if err != nil {
		return nil, err
	}
	im := &Image{Size: size, Version: Version2, spaces: spaces}
	for _, e := range entries {
		if err := im.Set(e); err != nil {
			return nil, err
		}
	}
	return im, nil
}

func readCSV(r io.Reader, dir string) ([]Entry, []string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.Comment = '#'
	var entries []Entry
	var spaces []string
	ns := ""
	first := true
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("nvs csv: %w", err)
		}
		line, _ := cr.FieldPos(0)
		fail := func(format string, a ...any) ([]Entry, []string, error) {
			return nil, nil, fmt.Errorf("nvs csv line %d: %s", line, fmt.Sprintf(format, a...))
		}
		for len(rec) < 4 {
			rec = append(rec, "")
		}
		key := strings.TrimSpace(rec[0])
		typ := strings.ToLower(strings.TrimSpace(rec[1]))
		enc := strings.ToLower(strings.TrimSpace(rec[2]))
		val := rec[3]
		if first {
			first = false
			if strings.EqualFold(key, "key") {
				continue
			}
		}
		if key == "" && typ == "" {
			continue
		}
		switch typ {
		case "namespace":
			if err := checkName("namespace", key); err != nil {
				return fail("%v", err)
			}
			ns = key
			found := false
			for _, s := range spaces {
				found = found || s == key
			}
			if !found {
				spaces = append(spaces, key)
			}
			continue
		case "data", "file":
		default:
			return fail("unknown type %q", rec[1])
		}
		if ns == "" {
			return fail("key %q comes before any namespace", key)
		}
		var raw []byte
		if typ == "file" {
			p := strings.TrimSpace(val)
			if !filepath.IsAbs(p) {
				p = filepath.Join(dir, p)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return fail("%v", err)
			}
			raw = b
			val = string(b)
		} else if enc == "binary" {
			return fail("encoding binary needs type file")
		}
		e := Entry{Namespace: ns, Key: key}
		switch enc {
		case "string":
			e.Type, e.Value = String, val
		case "hex2bin":
			b, err := hex.DecodeString(strings.Join(strings.Fields(val), ""))
			if err != nil {
				return fail("hex2bin: %v", err)
			}
			e.Type, e.Value = Blob, b
		case "base64":
			b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(val), ""))
			if err != nil {
				return fail("base64: %v", err)
			}
			e.Type, e.Value = Blob, b
		case "binary":
			e.Type, e.Value = Blob, raw
		case "float", "double":
			f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
			if err != nil {
				return fail("%v", err)
			}
			e.Type, e.Value = Float, f
			if enc == "double" {
				e.Type = Double
			}
		default:
			t, err := ParseType(enc)
			if err != nil || t.size() == 0 {
				return fail("unknown encoding %q", rec[2])
			}
			e.Type = t
			s := strings.TrimSpace(val)
			if t.signed() {
				v, err := strconv.ParseInt(s, 0, 64)
				if err != nil {
					return fail("%v", err)
				}
				e.Value = v
			} else {
				v, err := strconv.ParseUint(s, 0, 64)
				if err != nil {
					return fail("%v", err)
				}
				e.Value = v
			}
		}
		e, err = e.Normalize()
		if err != nil {
			return fail("%v", err)
		}
		entries = append(entries, e)
	}
	return entries, spaces, nil
}

// WriteCSV writes values as an nvs_partition_gen CSV, grouped by
// namespace in the order they first appear. Blobs are written as
// hex2bin.
func WriteCSV(w io.Writer, entries []Entry) error {
	return writeCSV(w, nil, entries)
}

// WriteCSV writes the image's values, with its empty namespaces too; a
// namespace list limits it to those namespaces
func (im *Image) WriteCSV(w io.Writer, namespaces ...string) error {
	if im.Encrypted && im.keys == nil {
		return ErrNoKeys
	}
	spaces := im.spaces
	entries := im.Entries
	if len(namespaces) > 0 {
		want := map[string]bool{}
		for _, s := range namespaces {
			want[s] = true
		}
		spaces = nil
		for _, s := range im.spaces {
			if want[s] {
				spaces = append(spaces, s)
			}
		}
		entries = nil
		for _, e := range im.Entries {
			if want[e.Namespace] {
				entries = append(entries, e)
			}
		}
	}
	return writeCSV(w, spaces, entries)
}

func writeCSV(w io.Writer, spaces []string, entries []Entry) error {
	order := append([]string{}, spaces...)
	byNS := map[string][]Entry{}
	for _, e := range entries {
		e, err := e.Normalize()
		if err != nil {
			return err
		}
		if _, ok := byNS[e.Namespace]; !ok {
			found := false
			for _, s := range order {
				found = found || s == e.Namespace
			}
			if !found {
				order = append(order, e.Namespace)
			}
		}
		byNS[e.Namespace] = append(byNS[e.Namespace], e)
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"key", "type", "encoding", "value"}); err != nil {
		return err
	}
	for _, ns := range order {
		if err := cw.Write([]string{ns, "namespace", "", ""}); err != nil {
			return err
		}
		for _, e := range byNS[ns] {
			var enc, val string
			switch v := e.Value.(type) {
			case string:
				enc, val = "string", v
			case []byte:
				enc, val = "hex2bin", hex.EncodeToString(v)
			case int64:
				enc, val = e.Type.String(), strconv.FormatInt(v, 10)
			case uint64:
				enc, val = e.Type.String(), strconv.FormatUint(v, 10)
			case float64:
				bits := 64
				if e.Type == Float {
					bits = 32
				}
				enc, val = e.Type.String(), strconv.FormatFloat(v, 'g', -1, bits)
			default:
				return errors.New("nvs csv: unexpected value")
			}
			if err := cw.Write([]string{e.Key, "data", enc, val}); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}
