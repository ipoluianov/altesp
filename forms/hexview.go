package forms

import (
	"fmt"
	"image/color"

	"github.com/ipoluianov/nui/ui"
)

// The hex view is adapted from the one of AltHex: it draws only the rows on
// the screen and never waits for the board. Bytes not read yet are drawn as
// placeholders and asked for; they are drawn when they arrive.

// hexSource gives the bytes the view shows
type hexSource interface {
	Size() int
	// At returns the byte and whether it is loaded
	At(off int) (byte, bool)
	// Need asks for the bytes [from, to) to be loaded; called while painting
	Need(from, to int)
}

// hexMark is a range of bytes drawn on another background: a difference
type hexMark struct {
	Off, Len int
}

// hexPerRow is the bytes on a row
const hexPerRow = 16

// hexScrollW is the width of the scroll bar
const hexScrollW = 12

// HexView shows the bytes of a source: the address, the bytes in hex and
// as text; it keeps the bytes typed while editing until they are written
type HexView struct {
	ui.Widget

	src hexSource
	top int // the first row shown

	// The caret is a byte; the selection is from anchor to caret, both in
	editable bool
	caret    int
	anchor   int
	half     bool // the high digit of the byte at the caret was typed
	edits    map[int]byte

	marks []hexMark

	// Layout, from the font
	charW, rowH int

	mouse struct {
		selecting bool
		dragBar   bool
		dragY     int
		dragTop   int
	}

	// OnCaret is called when the caret or the selection moves
	OnCaret func()
	// OnEdit is called when a byte is typed
	OnEdit func()
}

func newHexView(src hexSource) *HexView {
	v := &HexView{src: src, edits: map[int]byte{}}
	v.InitWidget()
	v.SetTypeName("HexView")
	v.SetCanBeFocused(true)
	v.SetXExpandable(true)
	v.SetYExpandable(true)
	v.SetMinSize(200, 120)
	v.SetMouseCursor(ui.MouseCursorIBeam)
	v.SetOnPaint(v.paint)
	v.SetOnKeyDown(v.keyDown)
	v.SetOnChar(v.charTyped)
	v.SetOnMouseDown(v.mouseDown)
	v.SetOnMouseUp(func(ui.MouseButton, int, int, ui.KeyModifiers) bool {
		v.mouse.selecting, v.mouse.dragBar = false, false
		return true
	})
	v.SetOnMouseMove(v.mouseMove)
	v.SetOnMouseWheel(func(dx, dy int) bool {
		v.setTop(v.top - dy*3)
		return true
	})
	return v
}

// SetSource shows another source from its start; the edits go
func (v *HexView) SetSource(src hexSource) {
	v.src = src
	v.top, v.caret, v.anchor, v.half = 0, 0, 0, false
	v.edits = map[int]byte{}
	v.marks = nil
	v.changed()
}

// SetMarks sets the ranges drawn as differences
func (v *HexView) SetMarks(marks []hexMark) {
	v.marks = marks
	v.update()
}

// SetEditable allows typing over the bytes
func (v *HexView) SetEditable(on bool) {
	v.editable = on
	v.half = false
	v.update()
}

// Editable tells whether typing changes the bytes
func (v *HexView) Editable() bool { return v.editable }

// Edits returns the bytes typed and not written yet
func (v *HexView) Edits() map[int]byte { return v.edits }

// DropEdits forgets the bytes typed
func (v *HexView) DropEdits() {
	v.edits = map[int]byte{}
	v.half = false
	v.update()
}

// Caret is the byte of the caret
func (v *HexView) Caret() int { return v.caret }

// Selection returns the selected bytes: the byte of the caret when none
// are selected
func (v *HexView) Selection() (start, n int) {
	a, b := min(v.anchor, v.caret), max(v.anchor, v.caret)
	return a, b - a + 1
}

// Select selects n bytes from off and scrolls them into view
func (v *HexView) Select(off, n int) {
	size := v.src.Size()
	if size == 0 {
		return
	}
	off = max(0, min(off, size-1))
	end := max(off, min(off+max(n, 1)-1, size-1))
	v.anchor, v.caret = off, end
	v.half = false
	// The start at the top third of the view
	if row := off / hexPerRow; row < v.top || row >= v.top+v.rowsShown() {
		v.setTop(row - v.rowsShown()/3)
	}
	v.changed()
}

// GoTo puts the caret at the byte
func (v *HexView) GoTo(off int) { v.Select(off, 1) }

// TopAddress is the first byte shown
func (v *HexView) TopAddress() int { return v.top * hexPerRow }

func (v *HexView) update() {
	if f := v.Form(); f != nil {
		f.Update()
	}
}

func (v *HexView) changed() {
	if v.OnCaret != nil {
		v.OnCaret()
	}
	v.update()
}

// Layout

func (v *HexView) measure() {
	w, h, _ := ui.MeasureText(ui.FontFamilyMono, ui.ThemeFontSize(), "0000000000")
	v.charW = max(1, w/10)
	v.rowH = h + 4
}

func (v *HexView) totalRows() int { return (v.src.Size() + hexPerRow - 1) / hexPerRow }

func (v *HexView) rowsShown() int {
	if v.rowH == 0 {
		v.measure()
	}
	return max(1, (v.Height()-4)/v.rowH)
}

func (v *HexView) maxTop() int { return max(0, v.totalRows()-v.rowsShown()) }

func (v *HexView) setTop(top int) {
	top = max(0, min(top, v.maxTop()))
	if top != v.top {
		v.top = top
		v.update()
	}
}

// The columns: the address (8 digits), the bytes in two groups of 8, the text
func (v *HexView) xHex(i int) int {
	x := v.charW * 10
	x += i * 3 * v.charW
	if i >= 8 {
		x += v.charW
	}
	return x
}

func (v *HexView) xText(i int) int { return v.xHex(hexPerRow) + 2*v.charW + i*v.charW }

// hit returns the byte under the point
func (v *HexView) hit(x, y int) (int, bool) {
	row := v.top + max(0, (y-2)/v.rowH)
	var col int
	switch {
	case x >= v.xText(0):
		col = (x - v.xText(0)) / v.charW
	case x >= v.xHex(8):
		col = 8 + (x-v.xHex(8))/(3*v.charW)
	default:
		col = (x - v.xHex(0)) / (3 * v.charW)
	}
	col = max(0, min(col, hexPerRow-1))
	off := row*hexPerRow + col
	size := v.src.Size()
	if size == 0 {
		return 0, false
	}
	return min(off, size-1), true
}

// Painting

func (v *HexView) paint(cnv *ui.Canvas) {
	v.measure()
	pal := ui.CurrentPalette()
	w, h := v.Width(), v.Height()
	cnv.FillFrame(0, 0, w, h, 3, pal.Base, pal.Border)
	size := v.src.Size()
	rows := v.rowsShown()
	v.top = min(v.top, v.maxTop())
	from := v.top * hexPerRow
	to := min(size, (v.top+rows+1)*hexPerRow)
	if to > from {
		v.src.Need(from, to)
	}
	cnv.SetFontFamily(ui.FontFamilyMono)
	cnv.SetFontSize(ui.ThemeFontSize())
	cnv.SetVAlign(ui.VAlignCenter)
	cnv.SetHAlign(ui.HAlignLeft)
	selA, selN := v.Selection()
	secondary := colorSecondary.get()
	dim := mix(pal.Text, pal.Base, 0.45)
	markBack := mix(colorError.get(), pal.Base, 0.25)
	selBack := pal.Selection
	if v.IsFocused() {
		selBack = mix(pal.Highlight, pal.Base, 0.45)
	}
	editColor := colorWarning.get()
	pad := 6
	for r := 0; r < rows; r++ {
		row := v.top + r
		start := row * hexPerRow
		if start >= size {
			break
		}
		y := 2 + r*v.rowH
		cnv.SetColor(secondary)
		cnv.DrawText(pad, y, v.charW*9, v.rowH, fmt.Sprintf("%08X", start))
		for i := 0; i < hexPerRow && start+i < size; i++ {
			off := start + i
			b, ok := v.src.At(off)
			eb, edited := v.edits[off]
			if edited {
				b, ok = eb, true
			}
			var back color.RGBA
			hasBack := false
			if v.marked(off) {
				back, hasBack = markBack, true
			}
			if off >= selA && off < selA+selN {
				if hasBack {
					back = mix(selBack, markBack, 0.5)
				} else {
					back, hasBack = selBack, true
				}
			}
			if hasBack {
				cnv.FillRect(pad+v.xHex(i)-v.charW/2, y, 3*v.charW, v.rowH, back)
				cnv.FillRect(pad+v.xText(i), y, v.charW, v.rowH, back)
			}
			col := pal.Text
			switch {
			case !ok:
				col = secondary
			case edited:
				col = editColor
			case b == 0xFF || b == 0x00:
				col = dim
			}
			cnv.SetColor(col)
			if !ok {
				cnv.DrawText(pad+v.xHex(i), y, 2*v.charW+2, v.rowH, "··")
				cnv.DrawText(pad+v.xText(i), y, v.charW+2, v.rowH, "·")
				continue
			}
			cnv.DrawText(pad+v.xHex(i), y, 2*v.charW+2, v.rowH, fmt.Sprintf("%02X", b))
			ch := "."
			if b >= 0x20 && b < 0x7f {
				ch = string(rune(b))
			}
			cnv.DrawText(pad+v.xText(i), y, v.charW+2, v.rowH, ch)
		}
	}
	// The caret: a frame around its byte when focused
	if crow := v.caret/hexPerRow - v.top; crow >= 0 && crow < rows && size > 0 {
		i := v.caret % hexPerRow
		y := 2 + crow*v.rowH
		col := pal.Highlight
		cnv.DrawLine(pad+v.xHex(i)-1, y+v.rowH-2, pad+v.xHex(i)+2*v.charW+1, y+v.rowH-2, 2, col)
		cnv.DrawLine(pad+v.xText(i), y+v.rowH-2, pad+v.xText(i)+v.charW, y+v.rowH-2, 2, col)
	}
	v.paintScrollBar(cnv)
}

func (v *HexView) marked(off int) bool {
	for _, m := range v.marks {
		if off >= m.Off && off < m.Off+m.Len {
			return true
		}
	}
	return false
}

// scrollGeometry returns the thumb of the scroll bar: its y and height
func (v *HexView) scrollGeometry() (thumbY, thumbH, track int) {
	track = v.Height() - 4
	maxTop := v.maxTop()
	if maxTop == 0 {
		return 0, track, track
	}
	thumbH = max(20, int(float64(track)*float64(v.rowsShown())/float64(v.totalRows())))
	thumbY = int(float64(track-thumbH) * float64(v.top) / float64(maxTop))
	return thumbY, thumbH, track
}

func (v *HexView) paintScrollBar(cnv *ui.Canvas) {
	if v.maxTop() == 0 {
		return
	}
	pal := ui.CurrentPalette()
	x := v.Width() - hexScrollW - 2
	ty, th, _ := v.scrollGeometry()
	cnv.FillRect(x+3, 2+ty, hexScrollW-6, th, mix(pal.Text, pal.Base, 0.3))
	// The differences on the bar
	total := max(1, v.src.Size())
	for _, m := range v.marks {
		y := 2 + int(float64(v.Height()-4)*float64(m.Off)/float64(total))
		cnv.FillRect(x, y, hexScrollW, 2, colorError.get())
	}
}

// Input

func (v *HexView) keyDown(key ui.Key, mods ui.KeyModifiers) bool {
	shift := mods.Shift
	ctrl := mods.Ctrl || mods.Cmd
	page := max(1, v.rowsShown()-1) * hexPerRow
	switch key {
	case ui.KeyArrowLeft:
		v.moveCaret(v.caret-1, shift)
	case ui.KeyArrowRight:
		v.moveCaret(v.caret+1, shift)
	case ui.KeyArrowUp:
		v.moveCaret(v.caret-hexPerRow, shift)
	case ui.KeyArrowDown:
		v.moveCaret(v.caret+hexPerRow, shift)
	case ui.KeyPageUp:
		v.setTop(v.top - page/hexPerRow)
		v.moveCaret(v.caret-page, shift)
	case ui.KeyPageDown:
		v.setTop(v.top + page/hexPerRow)
		v.moveCaret(v.caret+page, shift)
	case ui.KeyHome:
		if ctrl {
			v.moveCaret(0, shift)
		} else {
			v.moveCaret(v.caret-v.caret%hexPerRow, shift)
		}
	case ui.KeyEnd:
		if ctrl {
			v.moveCaret(v.src.Size()-1, shift)
		} else {
			v.moveCaret(v.caret-v.caret%hexPerRow+hexPerRow-1, shift)
		}
	default:
		return false
	}
	return true
}

// moveCaret puts the caret at the byte; extend keeps the anchor
func (v *HexView) moveCaret(off int, extend bool) {
	off = max(0, min(off, v.src.Size()-1))
	v.caret = off
	v.half = false
	if !extend {
		v.anchor = off
	}
	if row := off / hexPerRow; row < v.top {
		v.setTop(row)
	} else if row >= v.top+v.rowsShown() {
		v.setTop(row - v.rowsShown() + 1)
	}
	v.changed()
}

// charTyped types a hex digit over the byte at the caret, when editing:
// the high digit, then the low one, then the caret goes on
func (v *HexView) charTyped(r rune, mods ui.KeyModifiers) bool {
	if !v.editable || mods.Ctrl || mods.Cmd {
		return false
	}
	d := hexDigit(r)
	if d < 0 {
		return false
	}
	b, ok := v.edits[v.caret]
	if !ok {
		if b, ok = v.src.At(v.caret); !ok {
			return true // not read yet
		}
	}
	if !v.half {
		b = byte(d)<<4 | b&0x0F
		v.half = true
	} else {
		b = b&0xF0 | byte(d)
		v.half = false
	}
	v.edits[v.caret] = b
	if !v.half && v.caret < v.src.Size()-1 {
		v.caret++
		v.anchor = v.caret
	}
	if v.OnEdit != nil {
		v.OnEdit()
	}
	v.changed()
	return true
}

func hexDigit(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return -1
}

func (v *HexView) mouseDown(button ui.MouseButton, x, y int, mods ui.KeyModifiers) bool {
	v.Focus()
	if x >= v.Width()-hexScrollW-2 && v.maxTop() > 0 && button == ui.MouseButtonLeft {
		ty, th, _ := v.scrollGeometry()
		switch {
		case y < ty:
			v.setTop(v.top - v.rowsShown() + 1)
		case y >= ty+th:
			v.setTop(v.top + v.rowsShown() - 1)
		default:
			v.mouse.dragBar, v.mouse.dragY, v.mouse.dragTop = true, y, v.top
		}
		return true
	}
	off, ok := v.hit(x-6, y)
	if !ok {
		return true
	}
	// A right click inside the selection keeps it for the context menu
	if button == ui.MouseButtonRight {
		if a, n := v.Selection(); off >= a && off < a+n {
			return false
		}
		v.caret, v.anchor = off, off
		v.changed()
		return false
	}
	if button != ui.MouseButtonLeft {
		return false
	}
	v.caret = off
	v.half = false
	if !mods.Shift {
		v.anchor = off
	}
	v.mouse.selecting = true
	v.changed()
	return true
}

func (v *HexView) mouseMove(x, y int, mods ui.KeyModifiers) bool {
	if v.mouse.dragBar {
		_, th, track := v.scrollGeometry()
		if track > th {
			v.setTop(v.mouse.dragTop + int(float64(y-v.mouse.dragY)*float64(v.maxTop())/float64(track-th)))
		}
		return true
	}
	if v.mouse.selecting {
		if off, ok := v.hit(x-6, y); ok && off != v.caret {
			v.caret = off
			v.changed()
		}
		return true
	}
	return false
}

// mix returns a between b (t=0) and a (t=1)
func mix(a, b color.RGBA, t float64) color.RGBA {
	f := func(x, y uint8) uint8 { return uint8(float64(y) + (float64(x)-float64(y))*t) }
	return color.RGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}
