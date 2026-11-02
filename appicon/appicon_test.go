package appicon

import "testing"

func TestImageSizes(t *testing.T) {
	for _, size := range []int{16, 32, 48, 256, 1024} {
		img := Image(size)
		b := img.Bounds()
		if b.Dx() != size || b.Dy() != size {
			t.Fatalf("size %d: got %dx%d", size, b.Dx(), b.Dy())
		}
		// The corner is transparent, the left part of the chip is white
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("size %d: the corner is not transparent", size)
		}
		r, g, bl, _ := img.At(size*85/256, size*100/256).RGBA()
		if r>>8 < 0xf0 || g>>8 < 0xf0 || bl>>8 < 0xf0 {
			t.Errorf("size %d: the chip is not white: %x %x %x", size, r>>8, g>>8, bl>>8)
		}
	}
	// The bolt in the middle of the chip is in the color of the tile
	r, g, b, _ := Image(256).At(128, 128).RGBA()
	if r>>8 != 0xe0 || g>>8 != 0x53 || b>>8 != 0x2f {
		t.Errorf("the bolt is not orange: %x %x %x", r>>8, g>>8, b>>8)
	}
}
