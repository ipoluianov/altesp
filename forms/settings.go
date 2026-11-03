package forms

import (
	"context"
	"strconv"
	"strings"

	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// settingsDialog shows the few settings the program has: the language,
// the theme and the default baud rate of the port. Everything else is
// changed where it is used.
func (m *MainForm) settingsDialog() {
	c := &ui.DialogContent{}
	c.InitWidget()
	grid := ui.NewPanel()
	grid.SetPanelPadding(0)
	st := config.Get()

	grid.AddLabel(0, 0, T().Language)
	lang := ui.NewComboBox()
	lang.AddItem(T().SystemLanguage, "")
	for _, l := range Languages {
		lang.AddItem(LanguageName(l), l)
		if l == st.Language {
			lang.SetSelectedIndex(lang.ItemCount() - 1)
		}
	}
	if st.Language == "" {
		lang.SetSelectedIndex(0)
	}
	lang.SetMinWidth(240)
	grid.AddWidget(0, 1, lang)

	grid.AddLabel(1, 0, T().Theme)
	theme := ui.NewComboBox()
	theme.AddItem(T().ThemeDark, themeDark)
	theme.AddItem(T().ThemeLight, themeLight)
	if st.Theme == themeLight {
		theme.SetSelectedIndex(1)
	} else {
		theme.SetSelectedIndex(0)
	}
	grid.AddWidget(1, 1, theme)

	grid.AddLabel(2, 0, T().DefaultBaud)
	baud := ui.NewEditableComboBox()
	var bauds []string
	for _, b := range monitorBauds {
		bauds = append(bauds, strconv.Itoa(b))
	}
	baud.SetItems(bauds)
	baud.SetText(strconv.Itoa(st.MonitorBaud))
	grid.AddWidget(2, 1, baud)
	c.AddWidget(0, 0, grid)
	errLabel := ui.NewLabel("")
	errLabel.SetForegroundColor(colorError.get())
	c.AddWidget(1, 0, errLabel)

	buttons := ui.NewDialogButtonBox()
	buttons.AddOK(func() {
		b, err := strconv.Atoi(strings.TrimSpace(baud.Text()))
		if err != nil || b <= 0 {
			errLabel.SetText(T().BadBaud)
			return
		}
		l, _ := lang.ItemData(lang.SelectedIndex()).(string)
		th, _ := theme.ItemData(theme.SelectedIndex()).(string)
		c.RunInParent(func() { m.applySettings(l, th, b) })
		c.Form().Close()
	})
	buttons.AddCancel(func() { c.Form().Close() })
	c.AddWidget(2, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(T().SettingsTitle) }
	m.Form.Panel().ShowDialog(c)
}

// applySettings takes the settings of the dialog
func (m *MainForm) applySettings(lang, theme string, baud int) {
	old := config.Get()
	config.Update(func(s *config.Settings) {
		s.Language, s.Theme, s.MonitorBaud = lang, theme, baud
	})
	if theme != old.Theme {
		ApplyTheme(theme)
	}
	if lang != old.Language {
		if lang == "" {
			SetLanguage(ui.SystemLanguage())
		} else {
			SetLanguage(lang)
		}
	}
	if baud != old.MonitorBaud {
		m.monitor.baudBox.SetText(strconv.Itoa(baud))
		if m.monitor.conn != nil && m.monitor.conn.mon != nil {
			info := m.monitor.conn.mon.Info
			m.monitor.stop()
			m.monitor.open(info, false)
		}
	}
}

// Shortcuts

// shortcut is a key of the window and what it does
type shortcut struct {
	keys, show string // as AddShortcut takes them, as the list shows them
	what       func() string
	do         func()
}

// shortcuts are the keys of the main actions (TZ: flash, read flash,
// reset, the tabs, go to address)
func (m *MainForm) shortcutList() []shortcut {
	tab := func(i int) func() { return func() { m.tabs.SetCurrentIndex(i) } }
	return []shortcut{
		{"F5", "F5", func() string { return T().KeyConnect }, m.connect},
		{"Mod+Enter", "Ctrl+Enter", func() string { return T().FlashBtn }, func() { m.tabs.SetCurrentIndex(tabFlash); m.flash.flash() }},
		{"Mod+R", "Ctrl+R", func() string { return T().ReadFlash }, func() { m.flash.readFlash(nil) }},
		{"Mod+Shift+R", "Ctrl+Shift+R", func() string { return T().ResetBoard }, m.resetBoard},
		{"Mod+1", "Ctrl+1", func() string { return T().TabFlash }, tab(tabFlash)},
		{"Mod+2", "Ctrl+2", func() string { return T().TabMemory }, tab(tabMemory)},
		{"Mod+3", "Ctrl+3", func() string { return T().TabPartitions }, tab(tabPartitions)},
		{"Mod+4", "Ctrl+4", func() string { return T().TabMonitor }, tab(tabMonitor)},
		{"Mod+G", "Ctrl+G", func() string { return T().GoToAddr }, func() {
			m.tabs.SetCurrentIndex(tabMemory)
			m.memory.goToDialog()
		}},
		{"Mod+F", "Ctrl+F", func() string { return T().KeyFind }, m.find},
		{"F3", "F3", func() string { return T().FindNext }, func() {
			if m.tabs.CurrentIndex() == tabMonitor {
				m.monitor.searchNext(1)
			}
		}},
		{"F1", "F1", func() string { return T().Shortcuts }, m.showShortcuts},
	}
}

func (m *MainForm) shortcuts() {
	for _, s := range m.shortcutList() {
		m.Form.AddShortcut(s.keys, s.do)
	}
}

// find searches where the tab shows something to search: the bytes of the
// Memory tab, the log of the monitor
func (m *MainForm) find() {
	switch m.tabs.CurrentIndex() {
	case tabMemory:
		m.memory.findDialog()
	case tabMonitor:
		m.monitor.openSearch()
	}
}

// showShortcuts lists the keys
func (m *MainForm) showShortcuts() {
	var b strings.Builder
	for _, s := range m.shortcutList() {
		b.WriteString(s.show + "\t" + s.what() + "\n")
	}
	ui.ShowMessageBox(m.Form.Panel(), T().Shortcuts, b.String())
}

// The port menu of the status bar

// resetNames are the reset sequences by the names kept in config
var resetNames = []struct {
	name  string
	reset ops.Reset
}{{"", ops.ResetAuto}, {"bridge", ops.ResetBridge}, {"usbjtag", ops.ResetUSBJTAG}, {"manual", ops.ResetManual}}

// resetFor is the reset sequence chosen for the device
func (m *MainForm) resetFor(p ops.PortInfo) ops.Reset {
	name := config.Get().Resets[p.Key()]
	for _, r := range resetNames {
		if r.name == name {
			return r.reset
		}
	}
	return ops.ResetAuto
}

// resetText is the name of a sequence for the menu
func resetText(name string) string {
	switch name {
	case "bridge":
		return T().ResetBridge
	case "usbjtag":
		return T().ResetUSBJTAG
	case "manual":
		return T().ResetManual
	}
	return T().ResetAuto
}

// portMenu is the menu of the port in the status bar
func (m *MainForm) portMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(nil)
	seq := ui.NewContextMenu(m.Form.Panel())
	var names []string
	for _, r := range resetNames {
		names = append(names, r.name)
	}
	radio(seq, names, resetText, func() string { return config.Get().Resets[m.sel.Key()] }, func(name string) {
		key := m.sel.Key()
		config.Update(func(s *config.Settings) {
			if s.Resets == nil {
				s.Resets = map[string]string{}
			}
			if name == "" {
				delete(s.Resets, key)
			} else {
				s.Resets[key] = name
			}
		})
		m.status.ShowMessage(T().ResetNext)
	})
	it := menu.AddItemWithSubmenu(T().ResetSequence, seq)
	it.SetEnabled(m.hasSel && !isJTAG(m.sel))
	menu.AddItem(T().Reconnect, func() { m.reconnect(false) }).SetEnabled(m.hasSel && !m.busy)
	menu.AddItem(T().AdapterProps, m.adapterProps).SetEnabled(m.hasSel)
	return menu
}

// showPortMenu opens the port menu at the status bar
func (m *MainForm) showPortMenu() {
	x, y := m.status.port.RectClientAreaOnWindow()
	m.Form.ShowContextMenu(m.portMenu(), x, y-4*ui.ThemeControlHeight())
}

// adapterProps shows what the system tells about the port's adapter
func (m *MainForm) adapterProps() {
	text := strings.Join(m.sel.Details(), "\n")
	if s := resetText(config.Get().Resets[m.sel.Key()]); s != "" {
		text += "\n\n" + T().ResetSequence + ": " + s
	}
	ui.ShowMessageBox(m.Form.Panel(), T().AdapterProps, text)
}

// reconnect ends the session and connects again; noStub connects without
// the stub loader (the eFuse burn of the ESP32 and ESP32-S2)
func (m *MainForm) reconnect(noStub bool) {
	if noStub {
		m.flash.noStub = true
	}
	again := func() { m.run(true, nil, func(err error) { m.showError(T().Connect, err) }) }
	if m.sess == nil {
		again()
		return
	}
	m.run(false, func(ctx context.Context, s *ops.Session) error {
		return s.Close(ctx, false)
	}, func(error) {
		m.setSession(nil)
		again()
	})
}
