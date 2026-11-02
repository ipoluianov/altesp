package forms

import (
	"path/filepath"
	"strings"

	"github.com/ipoluianov/altesp/app"
	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/install"
	"github.com/ipoluianov/nui/ui"
)

// The tabs, in their order
const (
	tabFlash = iota
	tabMemory
	tabPartitions
	tabMonitor
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
	connectBtn *ui.Button
	toolsBtn   *ui.Button
	menuBtn    *ui.ToolButton

	tabs      *ui.TabWidget
	recentBox *ui.GroupBox

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

	p := m.Form.Panel()
	p.SetPanelPadding(0)
	p.SetCellPadding(0)
	p.AddWidget(0, 0, m.topBar())
	p.AddWidget(1, 0, m.center())
	p.AddWidget(2, 0, m.status)

	m.Form.AddShortcut("F5", m.connect)
	for i := tabFlash; i <= tabMonitor; i++ {
		i := i
		m.Form.AddShortcut("Mod+"+string(rune('1'+i)), func() { m.tabs.SetCurrentIndex(i) })
	}

	m.listPorts()
	m.showRecent()
	m.updateState()
	// The texts put together are made again in the new language
	m.Form.OnLanguageChanged.Connect(func(string) {
		m.listPorts()
		m.showRecent()
		m.updateState()
	})
	m.Form.OnClose = func() bool {
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

	m.portCombo = ui.NewComboBox()
	m.portCombo.SetMinWidth(260)
	m.portCombo.SetTooltipFunc(func() string { return T().PortTip })
	row.Add(m.portCombo)

	m.connectBtn = ui.NewButton("")
	m.connectBtn.SetTextFunc(func() string { return T().Connect })
	m.connectBtn.SetMinWidth(120)
	m.connectBtn.OnClick.Connect(func(ui.Widgeter) { m.connect() })
	m.connectBtn.SetTooltipFunc(func() string { return T().ConnectTip + " (F5)" })
	m.connectBtn.SetCanBeFocused(false)
	row.Add(m.connectBtn)
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

	m.tabs = ui.NewTabWidget()
	pages := []struct{ name, hint func() string }{
		tabFlash:      {func() string { return T().TabFlash }, func() string { return T().HintFlash }},
		tabMemory:     {func() string { return T().TabMemory }, func() string { return T().HintMemory }},
		tabPartitions: {func() string { return T().TabPartitions }, func() string { return T().HintPartitions }},
		tabMonitor:    {func() string { return T().TabMonitor }, func() string { return T().HintMonitor }},
	}
	for i, pg := range pages {
		// TODO: the pages of the tabs
		m.tabs.AddPage(pg.name(), placeholder(pg.hint))
		m.tabs.SetPageNameFunc(i, pg.name)
	}

	p := ui.NewPanel()
	p.SetPanelPadding(8)
	p.SetCellPadding(8)
	p.AddWidget(0, 0, left)
	p.AddWidget(0, 1, m.tabs)
	return p
}

// boardCard is the card of the connected board, shown on all the tabs
func (m *MainForm) boardCard() ui.Widgeter {
	card := ui.NewGroupBox("")
	card.SetTitleFunc(func() string { return T().Board })
	// TODO: the chip, MAC, flash, PSRAM, protection and what is flashed
	title := ui.NewLabel("")
	title.SetTextFunc(func() string { return T().NoBoard })
	title.SetFontBold(true)
	card.AddWidget(0, 0, title)
	hint := secondaryLabel(func() string { return T().NoBoardHint })
	card.AddWidget(1, 0, hint)
	return card
}

// placeholder is a page of a tab that only tells what it is for
func placeholder(hint func() string) ui.Widgeter {
	p := ui.NewPanel()
	p.AddVSpacer(0, 1)
	p.AddHSpacer(1, 0)
	p.AddWidget(1, 1, secondaryLabel(hint))
	p.AddHSpacer(1, 2)
	p.AddVSpacer(2, 1)
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

// listPorts fills the port list. TODO: the ports of the serial package,
// the one last used selected, a new one selected by itself when it is the
// only one
func (m *MainForm) listPorts() {
	m.portCombo.Clear()
	m.portCombo.AddItem(T().NoPorts, nil)
	m.portCombo.SetSelectedIndex(0)
	m.portCombo.SetEnabled(false)
}

// showRecent fills the Recent projects box: a folder is shown by its two
// last names, the whole path in the tooltip
func (m *MainForm) showRecent() {
	list := ui.NewVBox()
	folders := config.RecentProjects(maxRecent)
	if len(folders) == 0 {
		list.Add(secondaryLabel(func() string { return T().NoProjects }))
	}
	for _, f := range folders {
		l := ui.NewLabel(shortFolder(f))
		// A long name is cut by the width of the column
		l.SetXExpandable(true)
		l.SetTooltip(f)
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

// updateState shows the state of the connection in the status bar
func (m *MainForm) updateState() {
	// TODO: the port, its speed and the loader once connected
	m.status.SetState(T().NotConnected, colorLEDOff, "")
}

// connect resets the board into download mode and reads the chip
func (m *MainForm) connect() {
	if m.portCombo.SelectedIndex() < 0 || !m.portCombo.Enabled() {
		m.status.ShowMessage(T().NoPortSelected)
		return
	}
	// TODO: connect through the operations of the program
	m.status.ShowMessage(T().NotYet)
}

// Activate puts the keyboard into the window at the start
func (m *MainForm) Activate() {
	m.tabs.Focus()
}
