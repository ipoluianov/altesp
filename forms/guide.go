package forms

import (
	"image/color"
	"math"
	"time"

	"github.com/ipoluianov/nui/ui"
)

// The ESP32 guide: a window of topics about the chip, each with an animated
// picture and a short text, for those who start with the microcontroller.
// The pictures are drawn here, in a box of guideW x guideH units scaled to
// the widget.

const guideW, guideH = 640.0, 300.0

// guideTopic is a topic of the guide: its texts in the language, and the
// picture
type guideTopic struct {
	title, text func() string
	draw        func(s *scene)
}

// guideTopics are the topics in their order
func guideTopics() []guideTopic {
	return []guideTopic{
		{func() string { return G().ChipTitle }, func() string { return G().ChipText }, drawChip},
		{func() string { return G().MemoryTitle }, func() string { return G().MemoryText }, drawMemory},
		{func() string { return G().BootTitle }, func() string { return G().BootText }, drawBoot},
		{func() string { return G().MapTitle }, func() string { return G().MapText }, drawFlashMap},
		{func() string { return G().NVSTitle }, func() string { return G().NVSText }, drawNVS},
		{func() string { return G().OTATitle }, func() string { return G().OTAText }, drawOTA},
		{func() string { return G().FSTitle }, func() string { return G().FSText }, drawFS},
		{func() string { return G().EFuseTitle }, func() string { return G().EFuseText }, drawEFuse},
		{func() string { return G().SecurityTitle }, func() string { return G().SecurityText }, drawSecurity},
		{func() string { return G().USBTitle }, func() string { return G().USBText }, drawUSB},
		{func() string { return G().FamilyTitle }, func() string { return G().FamilyText }, drawFamily},
		{func() string { return G().PanicTitle }, func() string { return G().PanicText }, drawPanic},
	}
}

// guideView is the window of the guide (for the tests)
type guideView struct {
	topics  []guideTopic
	list    *ui.ListView
	picture *ui.Widget
	title   *ui.Label
	text    *ui.Widget
	current int
	start   time.Time
}

// showGuide opens the guide
func (m *MainForm) showGuide() {
	g := &guideView{topics: guideTopics(), start: time.Now()}
	m.guide = g
	c := &ui.DialogContent{}
	c.InitWidget()

	var names []string
	for _, t := range g.topics {
		names = append(names, t.title())
	}
	g.list = ui.NewListView()
	g.list.SetItems(names)
	g.list.SetMinWidth(260)
	g.list.SetMaxWidth(260)
	g.list.SetYExpandable(true)
	g.list.OnSelectionChanged.Connect(func(row int) {
		if row >= 0 {
			g.show(row)
		}
	})

	g.title = ui.NewLabel("")
	g.title.SetFontBold(true)
	g.title.SetFontSize(ui.ThemeFontSize() * 1.3)

	pic := &ui.Widget{}
	pic.InitWidget()
	pic.SetMinSize(int(guideW), int(guideH))
	pic.SetXExpandable(true)
	pic.SetYExpandable(true)
	pic.SetOnPaint(func(cnv *ui.Canvas) { g.paint(cnv, pic.Width(), pic.Height()) })
	g.picture = pic

	text := &ui.Widget{}
	text.InitWidget()
	text.SetMinSize(int(guideW), 150)
	text.SetXExpandable(true)
	text.SetOnPaint(func(cnv *ui.Canvas) { g.paintText(cnv, text.Width()) })
	g.text = text

	buttons := ui.NewDialogButtonBox()
	buttons.AddButton(G().Prev, ui.ButtonOther, func() { g.step(-1) })
	buttons.AddButton(G().Next, ui.ButtonOther, func() { g.step(1) })
	buttons.AddButton(T().CloseDiff, ui.ButtonReject, func() { c.Form().Close() })

	right := ui.NewPanel()
	right.SetPanelPadding(0)
	right.SetCellPadding(8)
	right.AddWidget(0, 0, g.title)
	right.AddWidget(1, 0, pic)
	right.AddWidget(2, 0, text)
	right.AddWidget(3, 0, buttons)

	c.AddWidget(0, 0, g.list)
	c.AddWidget(0, 1, right)
	// The pictures move: about 30 frames a second
	pic.AddTimer(33, pic.Update)
	c.OnDialogShow = func() {
		c.Form().SetTitle(G().Title)
		c.Form().SetSize(1000, 660)
	}
	g.list.SetCurrentRow(0)
	g.show(0)
	m.Form.Panel().ShowDialog(c)
}

// show shows the topic number i; its picture starts from the beginning
func (g *guideView) show(i int) {
	g.current = i
	g.start = time.Now()
	g.title.SetText(g.topics[i].title())
	g.picture.Update()
	g.text.Update()
}

// step goes to the topic before (-1) or after (1)
func (g *guideView) step(d int) {
	i := (g.current + d + len(g.topics)) % len(g.topics)
	g.list.SetCurrentRow(i)
	g.show(i)
}

func (g *guideView) paint(cnv *ui.Canvas, w, h int) {
	pal := ui.CurrentPalette()
	cnv.FillFrame(0, 0, w, h, 6, pal.Base, pal.Border)
	k := math.Min(float64(w)/guideW, float64(h)/guideH)
	s := &scene{
		cnv: cnv, pal: pal, k: k,
		x0: (float64(w) - guideW*k) / 2, y0: (float64(h) - guideH*k) / 2,
		t: time.Since(g.start).Seconds(),
	}
	g.topics[g.current].draw(s)
}

func (g *guideView) paintText(cnv *ui.Canvas, w int) {
	pal := ui.CurrentPalette()
	l := ui.NewTextLayout(g.topics[g.current].text(), ui.TextStyle{Size: ui.ThemeFontSize(), Color: pal.WindowText})
	l.SetWidth(float64(w - 8))
	l.SetLineSpacing(1.15)
	cnv.DrawTextLayout(l, 4, 4)
}

// scene draws a picture in the units of the guide box
type scene struct {
	cnv    *ui.Canvas
	pal    *ui.ThemePalette
	k      float64
	x0, y0 float64
	// t is the time since the topic was shown, in seconds
	t float64
	// small makes the text smaller: a dense picture
	small bool
}

// fontSize is the size of the text of the picture
func (s *scene) fontSize() float64 {
	f := ui.ThemeFontSize() * math.Min(s.k, 1.25)
	if s.small {
		f *= 0.88
	}
	return f
}

func (s *scene) X(v float64) int { return int(s.x0 + v*s.k) }
func (s *scene) Y(v float64) int { return int(s.y0 + v*s.k) }
func (s *scene) L(v float64) int { return max(1, int(v*s.k)) }

// phase is where the time is in a loop of the period, 0 to 1
func (s *scene) phase(period float64) float64 { return math.Mod(s.t, period) / period }

// guidePace slows the steps of the pictures down, to give time to read
// them; the movements inside a step keep their speed
const guidePace = 2.0

// step is the number of the step of a loop of n steps of dur seconds each,
// slowed by guidePace
func (s *scene) step(n int, dur float64) (i int, within float64) {
	dur *= guidePace
	p := math.Mod(s.t, float64(n)*dur) / dur
	return int(p), p - math.Floor(p)
}

// tint is a color lightened towards the background, for a fill
func (s *scene) tint(c color.RGBA, a float64) color.RGBA { return mix(c, s.pal.Base, a) }

// fg is the color of the text
func (s *scene) fg() color.RGBA { return s.pal.Text }

// dim is the color of what is not active
func (s *scene) dim() color.RGBA { return mix(s.pal.Text, s.pal.Base, 0.35) }

// box is a rounded box with a label in its middle; lines of the label are
// split at "\n"
func (s *scene) box(x, y, w, h float64, accent color.RGBA, label string) {
	s.cnv.FillFrame(s.X(x), s.Y(y), s.L(w), s.L(h), s.L(6), s.tint(accent, 0.18), mix(accent, s.pal.Base, 0.8))
	s.text(x, y, w, h, label, s.fg(), ui.HAlignCenter, false)
}

// frame is the outline of a rounded box, with its title at the top left
func (s *scene) frame(x, y, w, h float64, col color.RGBA, title string) {
	s.cnv.FillFrame(s.X(x), s.Y(y), s.L(w), s.L(h), s.L(10), s.tint(col, 0.06), mix(col, s.pal.Base, 0.7))
	if title != "" {
		s.text(x+10, y+4, w-20, 20, title, mix(col, s.pal.Text, 0.6), ui.HAlignLeft, true)
	}
}

// text writes a text in a box; several lines at "\n"
func (s *scene) text(x, y, w, h float64, str string, col color.RGBA, align ui.HAlign, bold bool) {
	l := ui.NewTextLayout(str, ui.TextStyle{Size: s.fontSize(), Color: col, Bold: bold})
	l.SetWidth(w * s.k)
	l.SetAlign(align)
	_, th := l.Size()
	s.cnv.DrawTextLayout(l, float64(s.X(x)), float64(s.Y(y))+(h*s.k-th)/2)
}

// mono writes a line in the fixed font, at the left of a box
func (s *scene) mono(x, y, w, h float64, str string, col color.RGBA) {
	l := ui.NewTextLayout(str, ui.TextStyle{Font: ui.FontFamilyMono, Size: s.fontSize(), Color: col})
	l.SetWidth(w * s.k)
	_, th := l.Size()
	s.cnv.DrawTextLayout(l, float64(s.X(x)), float64(s.Y(y))+(h*s.k-th)/2)
}

func (s *scene) line(x1, y1, x2, y2 float64, col color.RGBA, width float64) {
	s.cnv.DrawLine(s.X(x1), s.Y(y1), s.X(x2), s.Y(y2), s.L(width), col)
}

// arrow is a line with its head at the end
func (s *scene) arrow(x1, y1, x2, y2 float64, col color.RGBA) {
	s.line(x1, y1, x2, y2, col, 2)
	a := math.Atan2(y2-y1, x2-x1)
	const head, spread = 9.0, 0.45
	s.cnv.FillTriangle(s.X(x2), s.Y(y2),
		s.X(x2-head*math.Cos(a-spread)), s.Y(y2-head*math.Sin(a-spread)),
		s.X(x2-head*math.Cos(a+spread)), s.Y(y2-head*math.Sin(a+spread)), col)
}

func (s *scene) dot(x, y, r float64, col color.RGBA) {
	s.cnv.FillCircle(float64(s.X(x)), float64(s.Y(y)), r*s.k, col)
}

// along is the point a share p (0 to 1) of the way along the polyline
func along(pts [][2]float64, p float64) (x, y float64) {
	var total float64
	for i := 1; i < len(pts); i++ {
		total += math.Hypot(pts[i][0]-pts[i-1][0], pts[i][1]-pts[i-1][1])
	}
	d := p * total
	for i := 1; i < len(pts); i++ {
		seg := math.Hypot(pts[i][0]-pts[i-1][0], pts[i][1]-pts[i-1][1])
		if d <= seg || i == len(pts)-1 {
			f := math.Min(1, d/math.Max(seg, 1e-9))
			return pts[i-1][0] + (pts[i][0]-pts[i-1][0])*f, pts[i-1][1] + (pts[i][1]-pts[i-1][1])*f
		}
		d -= seg
	}
	return pts[0][0], pts[0][1]
}

// polyline draws the lines between the points
func (s *scene) polyline(pts [][2]float64, col color.RGBA, width float64) {
	for i := 1; i < len(pts); i++ {
		s.line(pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1], col, width)
	}
}

// pulse is 0 to 1 and back, smoothly, in the period
func (s *scene) pulse(period float64) float64 {
	return 0.5 - 0.5*math.Cos(2*math.Pi*s.phase(period))
}
