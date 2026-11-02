package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestParseSizes(t *testing.T) {
	got, err := parseSizes(" 32,16,32 ,256")
	if err != nil || len(got) != 3 || got[0] != 16 || got[1] != 32 || got[2] != 256 {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"", "0", "x", "16,-1"} {
		if _, err := parseSizes(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestMakeICO(t *testing.T) {
	sizes := []int{16, 48, 256}
	data, err := makeICO(sizes)
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	if le.Uint16(data[0:]) != 0 || le.Uint16(data[2:]) != 1 || int(le.Uint16(data[4:])) != len(sizes) {
		t.Fatalf("bad header % x", data[:6])
	}
	for i, size := range sizes {
		e := data[6+16*i:]
		w := int(e[0])
		if w == 0 {
			w = 256
		}
		if w != size || int(e[1]) != int(e[0]) {
			t.Errorf("entry %d: %d, want %d", i, w, size)
		}
		n, off := le.Uint32(e[8:]), le.Uint32(e[12:])
		img, err := png.Decode(bytes.NewReader(data[off : off+n]))
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if img.Bounds().Dx() != size {
			t.Errorf("entry %d: the PNG is %d wide", i, img.Bounds().Dx())
		}
	}
	if _, err := makeICO([]int{512}); err == nil {
		t.Error("512 is accepted")
	}
}
