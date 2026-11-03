package forms

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// maxLines is the most lines the monitor keeps; the oldest go first
const maxLines = 200_000

// maxHeld is the most bytes kept while the view is paused
const maxHeld = 4 << 20

// monitorBauds are offered in the baud list; any other may be typed
var monitorBauds = []int{9600, 19200, 38400, 57600, 74880, 115200, 230400, 460800, 921600, 1500000, 2000000}

// lineEndings are the endings the send line adds, by their names
var lineEndings = []struct{ name, text string }{{"LF", "\n"}, {"CR", "\r"}, {"CRLF", "\r\n"}, {"", ""}}

// MonitorPanel is the content of the monitor window: the log of the
// running board, colored by the ESP-IDF log levels, and a line to send. It
// is made once: the log stays between the windows.
type MonitorPanel struct {
	ui.Panel
	m *MainForm

	log      *ui.ListView
	lines    *logModel
	state    *ui.Label
	baudBox  *ui.EditableComboBox
	ending   *ui.ComboBox
	send     *ui.TextBox
	pauseBtn *ui.Button
	paused   bool
	held     []byte

	// conn is the port open now; mu guards in, the bytes read by its
	// goroutine and not yet shown
	conn *monConn
	mu   sync.Mutex
	in   []byte
	// stateKind is what the monitor does now
	stateKind monState

	// The panic decoder of the .elf of the project
	decoder     *ops.PanicDecoder
	decELF      string // the .elf the decoder is for, or being opened
	elfLoading  bool   // decELF is being opened: the log waits for it
	elfLabel    *ui.Label
	warned      string // the .elf and board already warned about
	filterBar   *ui.HBox
	filterLabel *ui.Label

	// The search in the log
	searchBar   *ui.HBox
	searchBox   *ui.TextBox
	searchState *ui.Label
}

type monState int

const (
	monClosed monState = iota
	monOpening
	monOpen
	monWaiting
)

// monConn is an opened port with the goroutine reading it
type monConn struct {
	mon    *ops.Monitor
	ctx    context.Context
	cancel context.CancelFunc
}

func newMonitorPanel(m *MainForm) *MonitorPanel {
	p := &MonitorPanel{m: m, lines: &logModel{}}
	p.InitWidget()
	p.SetPanelPadding(6)
	p.SetCellPadding(6)

	bar := ui.NewHBox()
	bar.SetCellPadding(6)
	p.baudBox = ui.NewEditableComboBox()
	var bauds []string
	for _, b := range monitorBauds {
		bauds = append(bauds, strconv.Itoa(b))
	}
	p.baudBox.SetItems(bauds)
	p.baudBox.SetText(strconv.Itoa(config.Get().MonitorBaud))
	p.baudBox.SetMinWidth(110)
	p.baudBox.SetMaxWidth(110)
	p.baudBox.SetTooltipFunc(func() string { return T().BaudTip })
	p.baudBox.SetOnItemSelected(func(int, string) { p.baudChosen() })
	p.baudBox.SetOnAccept(func(string) { p.baudChosen() })
	bar.Add(p.baudBox)
	bar.Add(p.button(func() string { return T().ResetBtn }, func() string { return T().ResetTip }, p.reset))
	bar.AddSpacing(8)
	p.pauseBtn = p.button(p.pauseText, nil, p.togglePause)
	bar.Add(p.pauseBtn)
	bar.Add(p.button(func() string { return T().Clear }, nil, p.clear))
	bar.Add(p.button(func() string { return T().SaveLog }, nil, p.saveLog))
	bar.AddSpacing(8)
	p.state = ui.NewLabel("")
	p.state.SetXExpandable(true)
	p.state.SetForegroundColor(colorSecondary.get())
	ui.OnThemeChanged.Connect(func(bool) { p.state.SetForegroundColor(colorSecondary.get()) })
	bar.Add(p.state)
	bar.Add(p.button(func() string { return T().CloseDiff }, func() string { return T().CloseMonitorTip }, m.closeMonitor))
	p.AddWidget(0, 0, bar)

	// The filter in effect, with the way to show all again
	p.filterBar = ui.NewHBox()
	p.filterBar.SetCellPadding(6)
	p.filterLabel = ui.NewLabel("")
	p.filterLabel.SetForegroundColor(colorWait.get())
	p.filterBar.Add(p.filterLabel)
	p.filterBar.Add(p.button(func() string { return T().ShowAll }, nil, func() { p.setFilter("", nil) }))
	p.filterBar.AddStretch()
	p.filterBar.SetVisible(false)

	p.log = ui.NewListView()
	p.log.SetFontFamily(ui.FontFamilyMono)
	p.log.SetModel(p.lines)
	p.log.SetName("monitor-log")
	p.log.SetContextMenu(p.lineMenu())
	p.log.OnMouseDown.Connect(func(e *ui.MouseEvent) {
		if e.Button == ui.MouseButtonRight {
			if row := p.log.RowAt(e.X, e.Y-p.log.ScrollY()); row >= 0 {
				p.log.SetCurrentRow(row)
			}
			// The menu has an item for each address of the line
			p.log.SetContextMenu(p.lineMenu())
		}
	})
	p.lines.decode = func(line string) []string {
		if p.decoder == nil {
			return nil
		}
		return p.decoder.Line(line)
	}
	logBox := ui.NewPanel()
	logBox.SetPanelPadding(0)
	logBox.SetCellPadding(4)
	logBox.AddWidget(0, 0, p.filterBar)
	logBox.AddWidget(1, 0, p.searchRow())
	logBox.AddWidget(2, 0, p.log)
	p.AddWidget(1, 0, logBox)

	sendRow := ui.NewHBox()
	sendRow.SetCellPadding(6)
	p.send = ui.NewTextBox()
	p.send.SetXExpandable(true)
	p.send.SetHintFunc(func() string { return T().SendHint })
	p.send.OnSubmit.Connect(func(string) { p.sendLine() })
	// The .elf that decodes the panics
	sendRow.Add(p.button(func() string { return T().SetELF }, func() string { return T().SetELFTip }, p.chooseELF))
	p.elfLabel = ui.NewLabel("")
	p.elfLabel.SetForegroundColor(colorSecondary.get())
	sendRow.Add(p.elfLabel)
	sendRow.Add(p.send)
	p.ending = ui.NewComboBox()
	cur := config.Get().LineEnding
	for i, e := range lineEndings {
		name := e.name
		if name == "" {
			name = T().NoEnding
		}
		p.ending.AddItem(name, i)
		if e.name == cur {
			p.ending.SetSelectedIndex(i)
		}
	}
	p.ending.SetTooltipFunc(func() string { return T().EndingTip })
	p.ending.OnActivated.Connect(func(i int) {
		config.Update(func(s *config.Settings) { s.LineEnding = lineEndings[i].name })
	})
	sendRow.Add(p.ending)
	sendRow.Add(p.button(func() string { return T().Send }, nil, p.sendLine))
	p.AddWidget(2, 0, sendRow)

	m.Form.OnLanguageChanged.Connect(func(string) {
		for i, e := range lineEndings {
			if e.name == "" {
				p.ending.SetItemText(i, T().NoEnding)
			}
		}
	})

	// The bytes come in by frames: a fast log doesn't repaint on every read
	p.log.AddTimer(50, p.pull)
	p.showState()
	return p
}

// button makes a button of the bar, out of the focus chain
func (p *MonitorPanel) button(text, tip func() string, onClick func()) *ui.Button {
	b := ui.NewButton("")
	b.SetTextFunc(text)
	if tip != nil {
		b.SetTooltipFunc(tip)
	}
	b.SetCanBeFocused(false)
	b.OnClick.Connect(func(ui.Widgeter) { onClick() })
	return b
}

// active tells whether the monitor has the port or waits for it
func (p *MonitorPanel) active() bool { return p.stateKind != monClosed }

// baud is the baud rate of the list
func (p *MonitorPanel) baud() int {
	b, err := strconv.Atoi(strings.TrimSpace(p.baudBox.Text()))
	if err != nil || b <= 0 {
		return config.DefaultMonitorBaud
	}
	return b
}

// baudChosen opens the port again at the new rate
func (p *MonitorPanel) baudChosen() {
	b, err := strconv.Atoi(strings.TrimSpace(p.baudBox.Text()))
	if err != nil || b <= 0 {
		p.m.status.ShowMessage(T().BadBaud)
		p.baudBox.SetText(strconv.Itoa(config.Get().MonitorBaud))
		return
	}
	if b == config.Get().MonitorBaud {
		return
	}
	config.Update(func(s *config.Settings) { s.MonitorBaud = b })
	if p.conn != nil {
		info := p.conn.mon.Info
		p.stop()
		p.open(info, false)
	}
}

// open opens the port of the board without resetting it. after: the board
// was just reset, so a port of its own USB is waited for to come back.
func (p *MonitorPanel) open(info ops.PortInfo, after bool) {
	p.stop()
	ctx, cancel := context.WithCancel(context.Background())
	c := &monConn{ctx: ctx, cancel: cancel}
	p.conn = c
	p.setState(monOpening)
	baud := p.baud()
	go func() {
		var mon *ops.Monitor
		var err error
		if after {
			info, err = reappear(ctx, info)
		}
		if err == nil {
			mon, err = openMonitor(ctx, info, baud)
		}
		ui.Invoke(func() {
			if p.conn != c {
				if mon != nil {
					mon.Close()
				}
				return
			}
			if err != nil {
				p.conn = nil
				p.setState(monClosed)
				if !ctxErr(err) {
					p.m.status.ShowMessage(firstLine(err.Error()))
					p.state.SetText(firstLine(err.Error()))
				}
				return
			}
			c.mon = mon
			p.setState(monOpen)
			go p.read(c)
		})
	}()
}

// openMonitor opens the port, trying again for a moment: a port that has
// just come back may not open at once
func openMonitor(ctx context.Context, info ops.PortInfo, baud int) (*ops.Monitor, error) {
	var err error
	for i := 0; i < 10; i++ {
		var mon *ops.Monitor
		if mon, err = ops.OpenMonitor(info, baud); err == nil {
			return mon, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil, err
}

// read takes what the board sends until the port closes or goes away
func (p *MonitorPanel) read(c *monConn) {
	buf := make([]byte, 4096)
	for {
		n, err := c.mon.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.in = append(p.in, buf[:n]...)
			p.mu.Unlock()
		}
		if err == nil {
			continue
		}
		if c.ctx.Err() != nil || errors.Is(err, ops.ErrPortClosed) {
			return
		}
		ui.Invoke(func() { p.lost(c) })
		return
	}
}

// lost waits for the board whose port went away: unplugged, or reset with
// its own USB
func (p *MonitorPanel) lost(c *monConn) {
	if p.conn != c {
		return
	}
	c.mon.Close()
	info := c.mon.Info
	baud := p.baud()
	p.setState(monWaiting)
	go func() {
		q, err := ops.WaitPort(c.ctx, info, false)
		var mon *ops.Monitor
		if err == nil {
			mon, err = openMonitor(c.ctx, q, baud)
		}
		ui.Invoke(func() {
			if p.conn != c {
				if mon != nil {
					mon.Close()
				}
				return
			}
			if err != nil {
				p.conn = nil
				p.setState(monClosed)
				return
			}
			c.mon = mon
			p.setState(monOpen)
			go p.read(c)
		})
	}()
}

// stop closes the port
func (p *MonitorPanel) stop() {
	c := p.conn
	if c == nil {
		return
	}
	p.conn = nil
	c.cancel()
	if c.mon != nil {
		c.mon.Close()
	}
	p.setState(monClosed)
}

func (p *MonitorPanel) setState(s monState) {
	p.stateKind = s
	p.showState()
	p.m.updateState()
}

// showState tells in the bar what the monitor does
func (p *MonitorPanel) showState() {
	text := ""
	switch p.stateKind {
	case monOpening:
		text = T().MonOpening
	case monOpen:
		text = fmt.Sprintf(T().MonOpen, portText(p.conn.mon.Info, p.baud(), ""))
	case monWaiting:
		text = T().MonWaiting
	default:
		text = T().MonClosed
	}
	p.state.SetText(text)
}

// pull shows what came: a timer calls it. While the .elf is being opened
// the bytes wait, so that a panic printed at once is decoded too.
func (p *MonitorPanel) pull() {
	if !p.paused {
		p.syncELF()
		if p.elfLoading {
			return
		}
	}
	p.mu.Lock()
	data := p.in
	p.in = nil
	p.mu.Unlock()
	if p.paused {
		if len(p.held)+len(data) <= maxHeld {
			p.held = append(p.held, data...)
		}
		return
	}
	if len(data) == 0 {
		return
	}
	p.lines.add(data)
	p.lines.Changed()
	p.log.EnsureVisible(p.lines.RowCount() - 1)
}

func (p *MonitorPanel) pauseText() string {
	if p.paused {
		return T().Resume
	}
	return T().Pause
}

func (p *MonitorPanel) togglePause() {
	p.paused = !p.paused
	p.pauseBtn.SetText(p.pauseText())
	if !p.paused && len(p.held) > 0 {
		p.mu.Lock()
		p.in = append(p.held, p.in...)
		p.mu.Unlock()
		p.held = nil
		p.pull()
	}
}

func (p *MonitorPanel) clear() {
	p.lines.clear()
	p.lines.Changed()
	p.held = nil
}

// saveLog writes the lines to a text file
func (p *MonitorPanel) saveLog() {
	p.m.Form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:           T().SaveLog,
		DefaultFileName: "monitor_" + time.Now().Format("2006-01-02_15-04-05") + ".log",
		Filters:         []ui.FileDialogFilter{{DisplayName: T().TextFiles, Patterns: []string{"*.log", "*.txt"}}},
	}, func(path string, err error) {
		if err != nil || path == "" {
			return
		}
		if err := os.WriteFile(path, []byte(p.lines.text()), 0o644); err != nil {
			ui.ShowMessageBox(p.m.Form.Panel(), T().SaveLog, err.Error())
			return
		}
		p.m.status.ShowMessage(fmt.Sprintf(T().Saved, path))
	})
}

// sendLine writes the line to the board with the line ending
func (p *MonitorPanel) sendLine() {
	if p.stateKind != monOpen {
		p.m.status.ShowMessage(T().MonNotOpen)
		return
	}
	text := p.send.Text()
	if i, ok := p.ending.ItemData(p.ending.SelectedIndex()).(int); ok {
		text += lineEndings[i].text
	}
	if _, err := p.conn.mon.Write([]byte(text)); err != nil {
		p.m.status.ShowMessage(err.Error())
		return
	}
	p.send.SetText("")
}

// reset restarts the application of the board; a port of the chip's own
// USB goes away and the monitor waits for it
func (p *MonitorPanel) reset() {
	p.onBoard(func(ctx context.Context, mon *ops.Monitor) error { return mon.Reset(ctx) })
}

// newSession marks in the log where the log of a new window starts: the
// lines of the windows before stay above it
func (p *MonitorPanel) newSession() {
	if p.lines.RowCount() == 0 {
		return
	}
	p.lines.mark(fmt.Sprintf(T().MonSession, time.Now().Format("15:04:05")))
	p.lines.Changed()
	p.log.EnsureVisible(p.lines.RowCount() - 1)
}

// onBoard does f with the port of the monitor, off the UI thread
func (p *MonitorPanel) onBoard(f func(ctx context.Context, mon *ops.Monitor) error) {
	if p.stateKind != monOpen {
		p.m.status.ShowMessage(T().MonNotOpen)
		return
	}
	c := p.conn
	go func() {
		if err := f(c.ctx, c.mon); err != nil && !ctxErr(err) {
			ui.Invoke(func() { p.m.status.ShowMessage(err.Error()) })
		}
	}()
}

// logModel is the lines of the monitor: the text without the ANSI codes and
// the level of each line. The last line may be open: still coming.
type logModel struct {
	ui.ModelSignal
	lines   []logLine
	partial []byte
	// esc is an escape sequence cut by the end of a read
	esc []byte

	// decode returns the lines to show under a line: a panic decoded
	decode func(line string) []string
	// filter shows only the lines it passes; shown are their indexes
	filter func(logLine) bool
	shown  []int
}

type logLine struct {
	text  string
	level byte // 'E', 'W', 'I', 'D', 'V', levelDecoded or 0
}

// levelDecoded marks the lines of a decoded panic, levelMark the line
// between the logs of two windows
const (
	levelDecoded = 'P'
	levelMark    = 'M'
)

func (l *logModel) RowCount() int {
	if l.filter != nil {
		return len(l.shown)
	}
	if len(l.partial) > 0 {
		return len(l.lines) + 1
	}
	return len(l.lines)
}

func (l *logModel) ColumnCount() int { return 1 }

func (l *logModel) line(row int) logLine {
	if l.filter != nil {
		if row < len(l.shown) {
			return l.lines[l.shown[row]]
		}
		return logLine{}
	}
	if row < len(l.lines) {
		return l.lines[row]
	}
	text := string(l.partial)
	return logLine{text: text, level: levelOf(text)}
}

// push adds a whole line, and what the decoder adds under it
func (l *logModel) push(line logLine) {
	l.lines = append(l.lines, line)
	if l.filter != nil && l.filter(line) {
		l.shown = append(l.shown, len(l.lines)-1)
	}
	if l.decode == nil || line.level == levelDecoded {
		return
	}
	for _, x := range l.decode(line.text) {
		l.push(logLine{text: "    " + x, level: levelDecoded})
	}
}

// setFilter shows only the lines f passes; nil shows all
func (l *logModel) setFilter(f func(logLine) bool) {
	l.filter = f
	l.shown = nil
	if f == nil {
		return
	}
	for i, line := range l.lines {
		if f(line) {
			l.shown = append(l.shown, i)
		}
	}
}

// tagOf is the tag of an ESP-IDF log line: "I (31) boot: ..." is "boot"
func tagOf(text string) string {
	if levelOf(text) == 0 {
		return ""
	}
	_, rest, ok := strings.Cut(text, ") ")
	if !ok {
		return ""
	}
	tag, _, ok := strings.Cut(rest, ":")
	if !ok {
		return ""
	}
	return tag
}

func (l *logModel) Text(row, col int) string { return l.line(row).text }

// Style colors the lines of the ESP-IDF log levels
func (l *logModel) Style(row, col int) ui.ItemStyle {
	switch l.line(row).level {
	case levelDecoded:
		return ui.ItemStyle{Color: colorDecoded.get(), Bold: true}
	case levelMark:
		return ui.ItemStyle{Color: colorSecondary.get()}
	}
	if c, ok := levelColor(l.line(row).level); ok {
		return ui.ItemStyle{Color: c}
	}
	return ui.ItemStyle{}
}

// add takes the bytes read: the lines end at LF, CR is dropped, the ANSI
// codes ESP-IDF colors its lines with are removed
func (l *logModel) add(data []byte) {
	data = append(l.esc, data...)
	l.esc = nil
	for i := 0; i < len(data); i++ {
		b := data[i]
		switch {
		case b == 0x1b:
			end := escapeEnd(data, i)
			if end < 0 {
				l.esc = append([]byte(nil), data[i:]...)
				return
			}
			i = end
		case b == '\n':
			text := string(l.partial)
			l.partial = l.partial[:0]
			l.push(logLine{text: text, level: levelOf(text)})
		case b == '\r':
		case b == '\t':
			l.partial = append(l.partial, "    "...)
		case b < 0x20:
		default:
			l.partial = append(l.partial, b)
		}
	}
	if len(l.lines) > maxLines {
		l.lines = append([]logLine(nil), l.lines[len(l.lines)-maxLines*9/10:]...)
		l.setFilter(l.filter)
	}
}

// escapeEnd finds the end of the escape sequence at i: the final byte of a
// CSI sequence (ESC [ ... letter), or the byte after ESC; -1 when the data
// ends first
func escapeEnd(data []byte, i int) int {
	if i+1 >= len(data) {
		return -1
	}
	if data[i+1] != '[' {
		return i + 1
	}
	for j := i + 2; j < len(data); j++ {
		if data[j] >= 0x40 && data[j] <= 0x7e {
			return j
		}
	}
	return -1
}

// levelOf tells the level of an ESP-IDF log line: "E (123) tag: text"
func levelOf(text string) byte {
	if len(text) >= 3 && text[1] == ' ' && text[2] == '(' {
		switch text[0] {
		case 'E', 'W', 'I', 'D', 'V':
			return text[0]
		}
	}
	return 0
}

// mark adds a line of its own: an open line coming is ended first
func (l *logModel) mark(text string) {
	if len(l.partial) > 0 {
		l.add([]byte("\n"))
	}
	l.push(logLine{text: text, level: levelMark})
}

func (l *logModel) clear() {
	l.shown = nil
	l.lines = nil
	l.partial = nil
	l.esc = nil
}

// text is the whole log
func (l *logModel) text() string {
	var b strings.Builder
	for _, line := range l.lines {
		b.WriteString(line.text)
		b.WriteByte('\n')
	}
	b.Write(l.partial)
	return b.String()
}

// Panic decoding

// elfPath is the .elf of the project: the one of the build folder, or the
// one set by hand
func (p *MonitorPanel) elfPath() string {
	if p.m.flash.elf != "" {
		return p.m.flash.elf
	}
	return config.Get().LastELF
}

// syncELF opens the decoder of the .elf of the project when it changed;
// the .elf is read in the background
func (p *MonitorPanel) syncELF() {
	path := p.elfPath()
	if path == p.decELF {
		p.checkMatch()
		return
	}
	p.decELF = path
	p.decoder = nil
	p.showELF()
	if path == "" {
		p.elfLoading = false
		return
	}
	p.elfLoading = true
	go func() {
		d, err := ops.NewPanicDecoder(path)
		ui.Invoke(func() {
			if p.decELF != path {
				return
			}
			p.elfLoading = false
			if err != nil {
				p.m.status.ShowMessage(fmt.Sprintf(T().ELFError, filepath.Base(path), firstLine(err.Error())))
				p.elfLabel.SetText("")
				return
			}
			p.decoder = d
			p.showELF()
			p.checkMatch()
		})
	}()
}

// showELF tells which .elf decodes the panics
func (p *MonitorPanel) showELF() {
	if p.decELF == "" {
		p.elfLabel.SetText(T().NoELF)
		p.elfLabel.SetTooltip("")
		return
	}
	p.elfLabel.SetText(fmt.Sprintf(T().ELFShown, filepath.Base(p.decELF)))
	p.elfLabel.SetTooltip(p.decELF)
}

// checkMatch warns once when the .elf is not the one the application that
// boots was built from
func (p *MonitorPanel) checkMatch() {
	m := p.m
	if p.decoder == nil || !m.hasBoard {
		return
	}
	act, ok := ops.ActivePartition(m.board.Partitions, m.board.OTA)
	if !ok {
		return
	}
	key := p.decELF + "|" + m.board.Port.Key() + "|" + act.Name
	if p.warned == key {
		return
	}
	p.warned = key
	if match, known := p.decoder.MatchesApp(m.board.Apps[act.Name]); known && !match {
		text := fmt.Sprintf(T().ELFMismatch, filepath.Base(p.decELF), act.Name)
		m.status.ShowMessage(text)
		p.lines.push(logLine{text: "⚠ " + text, level: 'W'})
		p.lines.Changed()
	}
}

// chooseELF sets the .elf of the project by hand
func (p *MonitorPanel) chooseELF() {
	p.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{
		Title:   T().SetELF,
		Filters: []ui.FileDialogFilter{{DisplayName: "ELF", Patterns: []string{"*.elf"}}, {DisplayName: T().AllFiles, Patterns: []string{"*"}}},
	}, func(paths []string, err error) {
		if err == nil && len(paths) > 0 {
			p.setELF(paths[0])
		}
	})
}

// setELF remembers the .elf: for the project of the Flash tab, and as the
// last one
func (p *MonitorPanel) setELF(path string) {
	config.Update(func(s *config.Settings) { s.LastELF = path })
	if p.m.flash.project != "" {
		p.m.flash.elf = path
		p.m.flash.saveProject()
	}
	p.syncELF()
}

// lineMenu is the context menu of a line of the log
func (p *MonitorPanel) lineMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(p.log)
	item := func(text func() string, f func()) *ui.ContextMenuItem {
		it := menu.AddItem("", f)
		it.SetTextFunc(text)
		return it
	}
	cp := item(func() string { return T().CopyLine }, func() {
		if line, ok := p.currentLine(); ok {
			ui.ClipboardSetText(line.text)
			p.m.status.ShowMessage(T().Copied)
		}
	})
	dec := item(func() string { return T().DecodeAddress }, p.decodeAddress)
	menu.AddSeparator()
	byTag := item(func() string { return T().FilterTag }, func() {
		if line, ok := p.currentLine(); ok {
			tag := tagOf(line.text)
			p.setFilter(fmt.Sprintf(T().FilteredTag, tag), func(l logLine) bool { return tagOf(l.text) == tag })
		}
	})
	byLevel := item(func() string { return T().FilterLevel }, func() {
		if line, ok := p.currentLine(); ok {
			lv := line.level
			p.setFilter(fmt.Sprintf(T().FilteredLevel, string(lv)), func(l logLine) bool { return l.level == lv })
		}
	})
	all := item(func() string { return T().ShowAll }, func() { p.setFilter("", nil) })
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		line, ok := p.currentLine()
		cp.SetEnabled(ok)
		dec.SetEnabled(ok && len(addressesIn(line.text)) > 0)
		byTag.SetEnabled(ok && tagOf(line.text) != "")
		byLevel.SetEnabled(ok && levelOf(line.text) != 0)
		all.SetEnabled(p.lines.filter != nil)
	})
	return menu
}

func (p *MonitorPanel) currentLine() (logLine, bool) {
	row := p.log.CurrentRow()
	if row < 0 || row >= p.lines.RowCount() {
		return logLine{}, false
	}
	return p.lines.line(row), true
}

// setFilter shows only the lines the filter passes; text tells it
func (p *MonitorPanel) setFilter(text string, f func(logLine) bool) {
	p.lines.setFilter(f)
	p.lines.Changed()
	p.filterLabel.SetText(text)
	p.filterBar.SetVisible(f != nil)
	if n := p.lines.RowCount(); n > 0 {
		p.log.EnsureVisible(n - 1)
	}
}

// addressRe finds the addresses in a line: 0x and 8 hex digits
var addressRe = regexp.MustCompile(`0x[0-9a-fA-F]{8}`)

func addressesIn(text string) []uint32 {
	var out []uint32
	for _, s := range addressRe.FindAllString(text, -1) {
		if v, err := strconv.ParseUint(s[2:], 16, 32); err == nil {
			out = append(out, uint32(v))
		}
	}
	return out
}

// decodeAddress tells where the addresses of the line are in the .elf
func (p *MonitorPanel) decodeAddress() {
	line, ok := p.currentLine()
	if !ok {
		return
	}
	if p.decoder == nil {
		ui.ShowMessageBox(p.m.Form.Panel(), T().DecodeAddress, T().NeedELF)
		return
	}
	var b strings.Builder
	for _, a := range addressesIn(line.text) {
		fmt.Fprintf(&b, "0x%08X\n", a)
		frames := p.decoder.Lookup(a)
		if len(frames) == 0 {
			b.WriteString("    " + T().NotInELF + "\n")
		}
		for _, f := range frames {
			b.WriteString("    " + f + "\n")
		}
	}
	ui.ShowMessageBox(p.m.Form.Panel(), T().DecodeAddress, b.String())
}

// Search

// searchRow is the bar of the search in the log, shown by Ctrl+F
func (p *MonitorPanel) searchRow() ui.Widgeter {
	p.searchBar = ui.NewHBox()
	p.searchBar.SetCellPadding(6)
	p.searchBox = ui.NewTextBox()
	p.searchBox.SetName("monitor-search")
	p.searchBox.SetHintFunc(func() string { return T().SearchLog })
	p.searchBox.SetMinWidth(280)
	p.searchBox.OnSubmit.Connect(func(string) { p.searchNext(1) })
	p.searchBar.Add(p.searchBox)
	p.searchBar.Add(p.button(func() string { return "↑" }, func() string { return T().FindPrev }, func() { p.searchNext(-1) }))
	p.searchBar.Add(p.button(func() string { return "↓" }, func() string { return T().FindNext }, func() { p.searchNext(1) }))
	p.searchState = ui.NewLabel("")
	p.searchState.SetForegroundColor(colorSecondary.get())
	p.searchBar.Add(p.searchState)
	p.searchBar.AddStretch()
	p.searchBar.Add(p.button(func() string { return "✕" }, nil, func() { p.searchBar.SetVisible(false) }))
	p.searchBar.SetVisible(false)
	return p.searchBar
}

// openSearch shows the search bar with the keyboard in it
func (p *MonitorPanel) openSearch() {
	p.searchBar.SetVisible(true)
	p.searchBox.Focus()
	p.searchBox.SelectAllText()
}

// searchNext selects the next line (dir 1) or the one before (-1) that has
// the text, from the line selected, around the ends
func (p *MonitorPanel) searchNext(dir int) {
	q := strings.ToLower(p.searchBox.Text())
	n := p.lines.RowCount()
	if q == "" || n == 0 {
		return
	}
	from := p.log.CurrentRow()
	if from < 0 && dir < 0 {
		from = 0
	}
	for k := 1; k <= n; k++ {
		row := ((from+dir*k)%n + n) % n
		if strings.Contains(strings.ToLower(p.lines.line(row).text), q) {
			p.log.SetCurrentRow(row)
			p.log.EnsureVisible(row)
			p.searchState.SetText("")
			return
		}
	}
	p.searchState.SetText(T().NotFound)
}
