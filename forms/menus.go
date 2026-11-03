package forms

import (
	"context"
	"fmt"

	"github.com/ipoluianov/altesp/app"
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

// mainMenu is the menu of the button at the left of the top bar: the
// settings of the program and what it is
func (m *MainForm) mainMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(nil)
	menu.AddItem(T().OpenImage, m.openImageDialog)
	menu.AddItem(T().OperationsLog, m.showOperations)
	menu.AddItemWithSubmenu(T().Tools, m.toolsMenu())
	menu.AddSeparator()

	menu.AddItem(T().Settings, m.settingsDialog)
	menu.AddItem(G().Menu, m.showGuide)
	menu.AddItem(T().Shortcuts, m.showShortcuts).SetShortcut("F1")
	menu.AddItem(T().CheckUpdates, m.checkUpdates)
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

// checkUpdates asks GitHub for the latest release: the only network access
// of the program, and only when the user asks
func (m *MainForm) checkUpdates() {
	m.status.ShowMessage(T().CheckingUpdates)
	go func() {
		r, err := app.LatestRelease(context.Background())
		ui.Invoke(func() {
			switch {
			case err != nil:
				ui.ShowMessageBox(m.Form.Panel(), T().CheckUpdatesTitle, fmt.Sprintf(T().UpdateError, err.Error()))
			case r.Newer:
				ask(m.Form, T().CheckUpdatesTitle, fmt.Sprintf(T().UpdateAvailable, app.DisplayName, r.Tag, app.Version),
					choice{T().OpenReleasePage, ui.ButtonAccept, func() { app.OpenURL(r.URL) }},
					choice{T().CloseDiff, ui.ButtonReject, nil})
			default:
				ui.ShowMessageBox(m.Form.Panel(), T().CheckUpdatesTitle, fmt.Sprintf(T().UpToDate, app.Version))
			}
		})
	}()
}
