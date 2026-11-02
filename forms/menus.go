package forms

import (
	"github.com/ipoluianov/altesp/app"
	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/nui/ui"
)

// radio adds checkable items of which one is checked: the current value
// is asked each time the menu opens
func radio[T comparable](menu *ui.ContextMenu, items []T, name func(T) string, cur func() T, set func(T)) {
	var all []*ui.ContextMenuItem
	for _, v := range items {
		v := v
		it := menu.AddItem(name(v), func() { set(v) })
		it.SetCheckable(true)
		all = append(all, it)
	}
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		c := cur()
		for k, it := range all {
			it.SetChecked(items[k] == c)
		}
	})
}

// toolsMenu is the menu of the Tools button: what is not tied to an object
// on the screen
func (m *MainForm) toolsMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(nil)
	// TODO: the tools come with their stages; until then they are shown
	// greyed out, so the menu tells what is to come
	for _, text := range []string{T().ToolEfuse, T().ToolPartitionEditor, T().ToolImages, T().ToolCoredump} {
		menu.AddItem(text, nil).SetEnabled(false)
	}
	return menu
}

// mainMenu is the menu of the button at the left of the top bar: the
// settings of the program and what it is
func (m *MainForm) mainMenu() *ui.ContextMenu {
	owner := m.Form.Panel()
	menu := ui.NewContextMenu(nil)
	menu.AddItemWithSubmenu(T().Tools, m.toolsMenu())
	menu.AddSeparator()

	theme := ui.NewContextMenu(owner)
	radio(theme, []string{themeDark, themeLight}, func(t string) string {
		if t == themeLight {
			return T().ThemeLight
		}
		return T().ThemeDark
	}, func() string { return config.Get().Theme }, func(t string) {
		config.Update(func(s *config.Settings) { s.Theme = t })
		ApplyTheme(t)
	})
	menu.AddItemWithSubmenu(T().Theme, theme)

	lang := ui.NewContextMenu(owner)
	radio(lang, Languages, LanguageName, func() string { return ui.Language() }, func(l string) {
		config.Update(func(s *config.Settings) { s.Language = l })
		SetLanguage(l)
	})
	menu.AddItemWithSubmenu(T().Language, lang)
	menu.AddSeparator()
	menu.AddItem(T().About, m.about)
	return menu
}

func (m *MainForm) about() {
	ui.ShowMessageBox(m.Form.Panel(), T().About, aboutText())
}

// aboutText is the text of the About box: the program, its site and
// license, the trademark notice and the credits
func aboutText() string {
	return app.DisplayName + " " + app.Version + "\n\n" + T().AboutText + "\n\n" +
		app.Website + "\n" + app.Copyright() + "\n" + T().LicenseText + "\n\n" +
		T().StubCredit + "\n" + T().Trademark
}
