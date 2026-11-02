package forms

import (
	"image/color"
	"time"

	"github.com/ipoluianov/nui/ui"
)

// StatusBar is the line at the bottom of the window: the state of the
// connection, the port and its speed, a message, the install link
type StatusBar struct {
	ui.Panel
	dot      *ui.Widget
	dotColor themeColor
	state    *ui.Label
	port     *ui.Label
	message  *clipLabel
	msgTimer *ui.Timer
	row      *ui.HBox
}

// AddLink adds a link at the right end: a text that does something when
// clicked, underlined under the mouse
func (s *StatusBar) AddLink(text func() string, onClick func()) *ui.Label {
	l := ui.NewLabel("")
	l.SetTextFunc(text)
	l.SetForegroundColor(colorLink.get())
	l.OnHoverChanged.Connect(l.SetUnderline)
	ui.OnThemeChanged.Connect(func(bool) { l.SetForegroundColor(colorLink.get()) })
	l.SetMouseCursor(ui.MouseCursorPointer)
	l.OnMouseDown.Connect(func(e *ui.MouseEvent) {
		if e.Button == ui.MouseButtonLeft {
			onClick()
			e.Handled = true
		}
	})
	s.row.Add(fixedSpace(6))
	s.row.Add(l)
	s.row.Add(fixedSpace(8))
	return l
}

// NewStatusBar makes the status bar
func NewStatusBar() *StatusBar {
	s := &StatusBar{}
	s.InitWidget()
	s.SetPanelPadding(0)
	s.SetCellPadding(0)

	s.dot = &ui.Widget{}
	s.dot.InitWidget()
	s.dot.SetMinSize(16, 16)
	s.dot.SetMaxSize(16, 16)
	s.dot.SetOnPaint(func(cnv *ui.Canvas) {
		cnv.FillCircle(8, 8, 4.5, s.dotColor.get())
	})
	s.dotColor = colorLEDOff

	s.state = ui.NewLabel("")
	s.port = ui.NewLabel("")
	s.port.SetForegroundColor(colorSecondary.get())
	s.message = newClipLabel(colorWait.get)

	row := ui.NewHBox()
	row.Add(fixedSpace(6))
	row.Add(s.dot)
	row.Add(s.state)
	row.Add(fixedSpace(12))
	row.Add(s.port)
	row.Add(fixedSpace(16))
	row.Add(s.message)
	s.row = row
	s.AddWidget(0, 0, row)

	s.msgTimer = s.NewTimer(4*time.Second, func() { s.message.SetText("") })
	s.msgTimer.SetSingleShot(true)

	ui.OnThemeChanged.Connect(func(bool) {
		s.port.SetForegroundColor(colorSecondary.get())
		s.dot.Update()
	})
	return s
}

func fixedSpace(w int) ui.Widgeter {
	sp := ui.NewSpace()
	sp.SetSize(w, 0)
	return sp
}

// SetState shows the state of the connection and the port with its speed
func (s *StatusBar) SetState(text string, col themeColor, port string) {
	s.state.SetText(text)
	s.port.SetText(port)
	if s.dotColor != col {
		s.dotColor = col
		s.dot.Update()
	}
}

// ShowMessage shows a message for a few seconds
func (s *StatusBar) ShowMessage(text string) {
	s.message.SetText(text)
	s.msgTimer.Start()
}

// clipLabel is a text that takes the room it is given and ends with "…"
// when it doesn't fit: a long message doesn't widen the window
type clipLabel struct {
	ui.Widget
	text  string
	color func() color.RGBA
}

func newClipLabel(col func() color.RGBA) *clipLabel {
	l := &clipLabel{color: col}
	l.InitWidget()
	_, h, _ := ui.MeasureText(ui.FontFamilySans, ui.ThemeFontSize(), "Ag")
	l.SetMinSize(20, h+4)
	l.SetXExpandable(true)
	l.SetOnPaint(func(cnv *ui.Canvas) {
		if l.text == "" {
			return
		}
		cnv.SetFontFamily(ui.FontFamilySans)
		cnv.SetFontSize(ui.ThemeFontSize())
		cnv.SetColor(l.color())
		cnv.SetVAlign(ui.VAlignCenter)
		text := l.text
		for len(text) > 1 {
			w, _, _ := ui.MeasureText(ui.FontFamilySans, ui.ThemeFontSize(), text)
			if w <= l.Width() {
				break
			}
			r := []rune(text)
			text = string(r[:len(r)-2]) + "…"
		}
		cnv.DrawText(0, 0, l.Width(), l.Height(), text)
	})
	return l
}

func (l *clipLabel) SetText(s string) {
	if l.text != s {
		l.text = s
		l.Update()
	}
}
