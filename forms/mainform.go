package forms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ipoluianov/altesp/app"
	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/install"
	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// The tabs, in their order
const (
	tabFlash = iota
	tabMemory
	tabPartitions
)

// maxRecent is the most projects in the Recent projects box
const maxRecent = 8

// cardWidth is the width of the left column: the board card and the
// recent projects
const cardWidth = 250

// MainForm is the window of the program: one board
type MainForm struct {
	Form   *ui.Form
	status *StatusBar

	portCombo  *ui.ComboBox
	refreshBtn *ui.ToolButton
	connectBtn *ui.Button
	monitorBtn *ui.Button
	toolsBtn   *ui.Button
	menuBtn    *ui.ToolButton
	manual     *ui.HBox

	tabs      *ui.TabWidget
	card      *ui.GroupBox
	recentBox *ui.GroupBox
	flash     *FlashTab
	memory    *MemoryTab
	parts     *PartitionsTab
	monitor   *MonitorPanel
	// monWin is the monitor window while it is open
	monWin *ui.DialogContent

	// The ports in the list, in its order, and the one selected; the
	// USB-JTAG devices of the entries named "jtag:<serial>"
	jtagDevs map[string]ops.JTAGDevice
	ports    []ops.PortInfo
	sel      ops.PortInfo
	hasSel   bool
	listing  atomic.Bool
	lastList atomic.Value // the list seen last, as text

	// sess is the board in download mode; board is the card of the board
	// connected last, kept after the session ends
	sess     *ops.Session
	board    ops.Board
	hasBoard bool
	// resetAt is when the board was last reset into its application: a
	// port of its own USB is then away for a moment
	resetAt time.Time
	// goneSince is when the port of the board connected went away; zero
	// while it is there (see checkUnplugged)
	goneSince time.Time

	// busy: a job runs (see run); cancel stops it
	busy   bool
	cancel context.CancelFunc

	// efuse is the eFuse window shown last (for the tests)
	efuse *efuseView
	// guide is the ESP32 guide shown last (for the tests)
	guide *guideView
	// loggedErr is the error logged last: showError doesn't log it again
	loggedErr error
	// stateKey is the state of the board the Connect button stands for,
	// stateAt when it last changed (see settling); connecting: a job is
	// connecting to the board
	stateKey   string
	stateAt    time.Time
	connecting bool
	// sessMu is held while the session is used: by a job, or by the
	// reading of the Memory tab
	sessMu sync.Mutex

	// image is the file the Memory and Partitions tabs show instead of the
	// board
	image *ops.Image

	installStatus install.Status
}

// NewMainForm builds the window
func NewMainForm() *MainForm {
	m := &MainForm{}
	m.Form = ui.NewForm()
	m.Form.SetTitle(app.DisplayName)
	m.Form.SetSize(1100, 700)

	m.status = NewStatusBar()
	// A downloaded copy offers to install or update itself, the installed one to be removed
	m.installStatus = install.CurrentStatus()
	if m.installStatus != install.StatusNone {
		m.status.AddLink(func() string { return installText(m.installStatus) }, m.onInstallLink)
	}

	// The port in the status bar opens its menu: the reset sequence, the
	// adapter
	m.status.port.SetMouseCursor(ui.MouseCursorPointer)
	m.status.port.OnMouseDown.Connect(func(e *ui.MouseEvent) {
		m.showPortMenu()
		e.Handled = true
	})

	p := m.Form.Panel()
	p.SetPanelPadding(0)
	p.SetCellPadding(0)
	p.AddWidget(0, 0, m.topBar())
	p.AddWidget(1, 0, m.manualBar())
	p.AddWidget(2, 0, m.center())
	p.AddWidget(3, 0, m.status)

	m.shortcuts()
	m.Form.SetOnFilesDropped(func(files []string, x, y int) {
		// Files dropped on a file system go into it
		if m.tabs.CurrentIndex() == tabPartitions && m.parts.viewer != nil && m.parts.viewer.dropped(files) {
			return
		}
		// A file dropped on Memory or Partitions is an image to look at
		if i := m.tabs.CurrentIndex(); (i == tabMemory || i == tabPartitions) && len(files) == 1 && !isDir(files[0]) {
			m.openImage(files[0])
			return
		}
		m.tabs.SetCurrentIndex(tabFlash)
		m.flash.addPaths(files)
	})

	m.listPortsNow()
	// The Connect button comes back when the state has settled
	m.status.AddTimer(100, func() {
		if time.Since(m.stateAt) < clickGuard+300*time.Millisecond {
			m.showConnectButton()
		}
	})
	m.status.AddTimer(portPoll, func() {
		m.listPorts()
		m.checkUnplugged()
	})
	m.showRecent()
	m.updateState()
	// The texts put together are made again in the new language
	m.Form.OnLanguageChanged.Connect(func(string) {
		m.fillPortCombo()
		m.flash.showNoPorts(len(m.ports) == 0)
		m.showBoard()
		m.showRecent()
		m.updateState()
	})
	m.Form.OnClose = func() bool {
		m.stopJob()
		m.monitor.stop()
		if s := m.sess; s != nil {
			// The board goes back to its application
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			s.Close(ctx, true)
			cancel()
		}
		m.flash.saveProject()
		config.Save()
		return true
	}
	return m
}

// topBar is the row of the port, the Connect button and the Tools menu
func (m *MainForm) topBar() ui.Widgeter {
	row := ui.NewHBox()
	row.SetPanelPadding(4)
	row.Add(m.menuButton())
	m.refreshBtn = iconButton("refresh", func() string { return T().RefreshPortsTip }, m.refreshPorts)
	row.Add(m.refreshBtn)

	m.portCombo = ui.NewComboBox()
	m.portCombo.SetMinWidth(300)
	m.portCombo.SetTooltipFunc(func() string { return T().PortTip })
	m.portCombo.OnActivated.Connect(func(int) { m.portChosen() })
	row.Add(m.portCombo)

	m.connectBtn = ui.NewButton("")
	m.connectBtn.SetTextFunc(m.connectText)
	m.connectBtn.SetMinWidth(120)
	m.connectBtn.OnClick.Connect(func(ui.Widgeter) { m.connect() })
	m.connectBtn.SetTooltipFunc(func() string { return T().ConnectTip + " (F5)" })
	m.connectBtn.SetCanBeFocused(false)
	row.Add(m.connectBtn)

	m.monitorBtn = ui.NewButton("")
	m.monitorBtn.SetTextFunc(func() string { return T().MonitorBtn })
	m.monitorBtn.SetMinWidth(120)
	m.monitorBtn.OnClick.Connect(func(ui.Widgeter) { m.openMonitor() })
	m.monitorBtn.SetTooltipFunc(func() string { return T().MonitorTip + " (F6)" })
	m.monitorBtn.SetCanBeFocused(false)
	row.Add(m.monitorBtn)
	row.AddStretch()

	m.toolsBtn = ui.NewButton("")
	m.toolsBtn.SetTextFunc(func() string { return T().Tools + " ▾" })
	m.toolsBtn.SetCanBeFocused(false)
	m.toolsBtn.OnClick.Connect(func(ui.Widgeter) {
		x, y := m.toolsBtn.RectClientAreaOnWindow()
		m.Form.ShowContextMenu(m.toolsMenu(), x, y+m.toolsBtn.Height())
	})
	row.Add(m.toolsBtn)
	return row
}

// connectText is the text of the Connect button for the state
func (m *MainForm) connectText() string {
	switch {
	case m.busy && m.connecting && m.settling():
		return T().Connecting
	case m.busy:
		return T().Cancel
	case m.sess != nil:
		return T().Disconnect
	}
	return T().Connect
}

// menuButton is the first button of the top bar: the settings of the
// program and About are in its menu
func (m *MainForm) menuButton() *ui.ToolButton {
	b := iconButton("menu", func() string { return T().MenuTip }, nil)
	b.SetOnClick(func() {
		x, y := b.RectClientAreaOnWindow()
		m.Form.ShowContextMenu(m.mainMenu(), x, y+b.Height())
	})
	m.menuBtn = b
	return b
}

// center lays out the board card and the recent projects at the left, the
// tabs at the right
func (m *MainForm) center() ui.Widgeter {
	left := ui.NewVBox()
	left.SetCellPadding(8)
	left.SetMinWidth(cardWidth)
	left.SetMaxWidth(cardWidth)
	left.Add(m.boardCard())

	m.recentBox = ui.NewGroupBox("")
	m.recentBox.SetTitleFunc(func() string { return T().RecentProjects })
	left.Add(m.recentBox)
	left.AddStretch()

	m.flash = newFlashTab(m)
	m.monitor = newMonitorPanel(m)
	m.memory = newMemoryTab(m)
	m.parts = newPartitionsTab(m)
	m.tabs = ui.NewTabWidget()
	pages := []struct {
		name func() string
		page ui.Widgeter
	}{
		tabFlash:      {func() string { return T().TabFlash }, m.flash},
		tabMemory:     {func() string { return T().TabMemory }, m.memory},
		tabPartitions: {func() string { return T().TabPartitions }, m.parts},
	}
	for i, pg := range pages {
		m.tabs.AddPage(pg.name(), pg.page)
		m.tabs.SetPageNameFunc(i, pg.name)
	}

	p := ui.NewPanel()
	p.SetPanelPadding(8)
	p.SetCellPadding(8)
	p.AddWidget(0, 0, left)
	p.AddWidget(0, 1, m.tabs)
	return p
}

// secondaryLabel is a label in the dimmer color of the hints
func secondaryLabel(text func() string) *ui.Label {
	l := ui.NewLabel("")
	l.SetTextFunc(text)
	l.SetForegroundColor(colorSecondary.get())
	ui.OnThemeChanged.Connect(func(bool) { l.SetForegroundColor(colorSecondary.get()) })
	return l
}

// showRecent fills the Recent projects box: a folder is shown by its two
// last names, the whole path in the tooltip; a click shows its files
func (m *MainForm) showRecent() {
	list := ui.NewVBox()
	folders := config.RecentProjects(maxRecent)
	if len(folders) == 0 {
		list.Add(secondaryLabel(func() string { return T().NoProjects }))
	}
	for _, f := range folders {
		f := f
		l := ui.NewLabel(shortFolder(f))
		// A long name is cut by the width of the column
		l.SetXExpandable(true)
		l.SetTooltip(f)
		l.SetMouseCursor(ui.MouseCursorPointer)
		l.OnHoverChanged.Connect(l.SetUnderline)
		l.OnMouseDown.Connect(func(e *ui.MouseEvent) {
			if e.Button == ui.MouseButtonLeft {
				m.tabs.SetCurrentIndex(tabFlash)
				m.flash.loadRemembered(f)
				e.Handled = true
			}
		})
		list.Add(l)
	}
	m.recentBox.RemoveAllWidgets()
	m.recentBox.AddWidget(0, 0, list)
}

// shortFolder is the folder by its two last names: sensor-node/build
func shortFolder(path string) string {
	path = filepath.Clean(path)
	base := filepath.Base(path)
	parent := filepath.Base(filepath.Dir(path))
	if parent == "." || parent == string(filepath.Separator) || strings.HasSuffix(parent, ":\\") {
		return base
	}
	return parent + "/" + base
}

// knownBoard is the board connected last, or the zero Board
func (m *MainForm) knownBoard() ops.Board {
	if m.hasBoard {
		return m.board
	}
	return ops.Board{}
}

// openMonitor shows the monitor window over the main one. The board runs
// its application while it is open: one in download mode is first reset
// into it, and the main window, which works with the loader, waits.
func (m *MainForm) openMonitor() {
	if m.busy || m.monWin != nil {
		return
	}
	p, ok := m.selectedPort()
	switch {
	case !ok:
		m.status.ShowMessage(T().NoPortSelected)
		return
	case isJTAG(p):
		m.status.ShowMessage(T().MonJTAG)
		return
	}
	if m.sess != nil {
		m.disconnect(m.openMonitor)
		return
	}
	c := &ui.DialogContent{}
	c.InitWidget()
	c.AddWidget(0, 0, m.monitor)
	c.OnDialogShow = func() {
		f := c.Form()
		f.SetTitle(fmt.Sprintf(T().MonitorTitle, p.Short))
		w, h := config.Get().MonitorWidth, config.Get().MonitorHeight
		if w <= 0 || h <= 0 {
			w, h = 1000, 640
		}
		f.SetSize(w, h)
		f.AddShortcut("Mod+F", m.monitor.openSearch)
		f.AddShortcut("F3", func() { m.monitor.searchNext(1) })
	}
	c.OnDialogReject = func() bool {
		m.monitorClosed(c)
		return true
	}
	m.monWin = c
	m.monitor.newSession()
	m.Form.Panel().ShowDialog(c)
	m.monitor.open(p, time.Since(m.resetAt) < 5*time.Second)
	m.updateState()
}

// closeMonitor closes the monitor window: the Close button
func (m *MainForm) closeMonitor() {
	c := m.monWin
	if c == nil {
		return
	}
	m.monitorClosed(c)
	c.Form().Close()
}

// monitorClosed lets the port go and keeps the size of the window; the
// panel is taken out of it, for the next window
func (m *MainForm) monitorClosed(c *ui.DialogContent) {
	if m.monWin != c {
		return
	}
	m.monWin = nil
	m.monitor.stop()
	if f := c.Form(); f != nil {
		w, h := f.Size()
		config.Update(func(s *config.Settings) { s.MonitorWidth, s.MonitorHeight = w, h })
	}
	c.RemoveAllWidgets()
	m.updateState()
}

// updateState shows the state of the board: the buttons, the status bar
func (m *MainForm) updateState() {
	if key := fmt.Sprint(m.busy, m.sess != nil); key != m.stateKey {
		// The state the window opens in is no change
		if m.stateKey != "" {
			m.stateAt = time.Now()
		}
		m.stateKey = key
	}
	m.showConnectButton()
	m.monitorBtn.SetEnabled(!m.busy && m.hasSel && !isJTAG(m.sel) && m.monWin == nil)
	m.portCombo.SetEnabled(!m.busy && m.sess == nil && len(m.ports) > 0)
	m.flash.setBusy(m.busy)
	m.monitor.showState()
	m.syncViews()
	if m.busy {
		// The steps of the job are shown as they come
		return
	}
	m.showStatus()
}

// clickGuard is how long the Connect button takes no clicks after the
// state changed: a double click, or a click again out of impatience,
// doesn't cancel the connection it has just started, nor disconnect the
// board it has just connected. The tests of the window click faster.
var clickGuard = time.Second

// settling tells the state changed less than clickGuard ago
func (m *MainForm) settling() bool { return time.Since(m.stateAt) < clickGuard }

// showConnectButton shows the Connect button for the state: dim while the
// state settles
func (m *MainForm) showConnectButton() {
	m.connectBtn.SetText(m.connectText())
	m.connectBtn.SetEnabled((m.busy || m.sess != nil || m.hasSel) && !m.settling())
}

// showStatus shows in the status bar what the board is connected for
func (m *MainForm) showStatus() {
	switch {
	case m.sess != nil:
		b := m.board
		loader := T().ROMLoader
		switch {
		case b.JTAG:
			loader = "JTAG"
		case b.Stub:
			loader = T().StubLoader
		}
		m.status.SetState(T().Connected, colorOK, portText(b.Port, b.Baud, loader))
	case m.monitor.stateKind == monOpen:
		m.status.SetState(T().Monitoring, colorOK, portText(m.monitor.conn.mon.Info, m.monitor.baud(), ""))
	case m.monitor.stateKind == monWaiting || m.monitor.stateKind == monOpening:
		m.status.SetState(T().MonWaiting, colorWait, m.sel.Short)
	case m.hasSel:
		m.status.SetState(T().NotConnected, colorLEDOff, m.sel.Short)
	default:
		m.status.SetState(T().NotConnected, colorLEDOff, "")
	}
}

// disconnect resets the board into its application and ends the session;
// then runs after
func (m *MainForm) disconnect(then func()) {
	m.run(false, func(ctx context.Context, s *ops.Session) error {
		if s == nil {
			return nil
		}
		return s.Close(ctx, true)
	}, func(err error) {
		m.setSession(nil)
		m.resetAt = time.Now()
		if err != nil && !ctxErr(err) {
			m.status.ShowMessage(err.Error())
		}
		if then != nil {
			then()
		}
	})
}

// Activate puts the keyboard into the window at the start
func (m *MainForm) Activate() {
	m.tabs.Focus()
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
