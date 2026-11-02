package forms

import (
	"fmt"

	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/install"
	"github.com/ipoluianov/nui/ui"
)

// installText is the text of the install link for what it does now
func installText(s install.Status) string {
	switch s {
	case install.StatusUpdate:
		return T().UpdateLink
	case install.StatusUninstall:
		return T().UninstallLink
	}
	return T().InstallLink
}

// UninstallAsk is the question before the program is removed
func UninstallAsk() string { return fmt.Sprintf(T().UninstallAsk, config.Dir()) }

// onInstallLink installs, updates or removes the program, as the link says
func (m *MainForm) onInstallLink() {
	if m.installStatus == install.StatusUninstall {
		m.uninstall()
		return
	}
	m.install()
}

// install copies the program to ~/.altbins and registers it, then quits for
// the installed copy to start (see main). Over an older installed version
// it is the same: that one is replaced.
func (m *MainForm) install() {
	ui.ShowQuestionMessageBoxOKCancel(m.Form.Panel(), installText(m.installStatus),
		fmt.Sprintf(T().InstallAsk, install.Dir()), func() {
			if err := install.Install(); err != nil {
				ui.ShowMessageBox(m.Form.Panel(), T().InstallLink, fmt.Sprintf(T().InstallFailed, err.Error()))
				return
			}
			install.RelaunchAfterExit()
			m.Form.RequestClose()
		}, nil)
}

// uninstall removes the installed copy; the settings and the projects stay.
// When that is this copy, it quits and its binary is deleted once it has.
func (m *MainForm) uninstall() {
	ui.ShowQuestionMessageBoxOKCancel(m.Form.Panel(), T().UninstallLink, UninstallAsk(), func() {
		installed := install.IsInstalledCopy()
		if err := install.Uninstall(); err != nil {
			ui.ShowMessageBox(m.Form.Panel(), T().UninstallLink, err.Error())
			return
		}
		if installed {
			m.Form.RequestClose()
			return
		}
		m.installStatus = install.CurrentStatus()
		m.Form.ShowToast(T().Uninstalled, ui.ToastSuccess)
	}, nil)
}

// ShowInstalled tells in the installed copy that it has just been installed
func (m *MainForm) ShowInstalled() {
	m.Form.ShowToast(T().Installed, ui.ToastSuccess)
}
