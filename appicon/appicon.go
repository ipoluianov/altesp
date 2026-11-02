// Package appicon draws the icon of the program at any size: for the
// window, and for the packages of Linux, Windows and macOS
package appicon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"

	"github.com/ipoluianov/nui/ui"
)

// side is the size the icon is drawn in; other sizes are the same drawing
// at another scale of the canvas
const side = 256

var (
	orange = color.RGBA{0xe0, 0x53, 0x2f, 0xff}
	white  = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// chip is the body of the chip with four pins on each side; bolt is the
// flash mark cut out of it
const (
	chip = "M84 72h88a12 12 0 0 1 12 12v88a12 12 0 0 1-12 12H84a12 12 0 0 1-12-12V84a12 12 0 0 1 12-12z" +
		"M83 46h12v26H83zM109 46h12v26h-12zM135 46h12v26h-12zM161 46h12v26h-12z" +
		"M83 184h12v26H83zM109 184h12v26h-12zM135 184h12v26h-12zM161 184h12v26h-12z" +
		"M46 83h26v12H46zM46 109h26v12H46zM46 135h26v12H46zM46 161h26v12H46z" +
		"M184 83h26v12h-26zM184 109h26v12h-26zM184 135h26v12h-26zM184 161h26v12h-26z"
	bolt = "M138 84L100 136h24l-10 36 42-56h-24z"
)

// Image draws the icon of the program, size x size pixels: a chip with a
// flash bolt on it. A size of 0 or less gives 256.
func Image(size int) image.Image {
	if size <= 0 {
		size = side
	}
	cnv, img := ui.NewImageCanvas(side, side, float64(size)/side)
	cnv.SetColor(orange)
	cnv.FillRoundedRect(8, 8, side-16, side-16, 48)
	body, _ := ui.ParseSVGPath(chip)
	cnv.FillPath(body, white)
	mark, _ := ui.ParseSVGPath(bolt)
	cnv.FillPath(mark, orange)
	return img
}

// PNG encodes the icon of the size as a PNG file
func PNG(size int) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, Image(size)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ICO writes an .ico file with a PNG image of each size, as Windows
// Vista and later read them: a header of 6 bytes, a directory entry of 16
// bytes for each image, then the PNG files one after another
func ICO(sizes []int) ([]byte, error) {
	images := make([][]byte, len(sizes))
	for i, size := range sizes {
		if size < 1 || size > 256 {
			return nil, fmt.Errorf("an .ico image cannot be %d pixels", size)
		}
		data, err := PNG(size)
		if err != nil {
			return nil, err
		}
		images[i] = data
	}
	var buf bytes.Buffer
	le := binary.LittleEndian
	w16 := func(v uint16) { buf.Write(le.AppendUint16(nil, v)) }
	w32 := func(v uint32) { buf.Write(le.AppendUint32(nil, v)) }
	w16(0) // reserved
	w16(1) // the type: an icon
	w16(uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for i, size := range sizes {
		// 256 is written as 0: the field is one byte
		dim := byte(size)
		if size == 256 {
			dim = 0
		}
		buf.WriteByte(dim) // width
		buf.WriteByte(dim) // height
		buf.WriteByte(0)   // colors in the palette: none
		buf.WriteByte(0)   // reserved
		w16(1)             // color planes
		w16(32)            // bits per pixel
		w32(uint32(len(images[i])))
		w32(uint32(offset))
		offset += len(images[i])
	}
	for _, data := range images {
		buf.Write(data)
	}
	return buf.Bytes(), nil
}
