package main

import (
	"os"

	"github.com/ipoluianov/altesp/app"
	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/forms"
	"github.com/ipoluianov/altesp/install"
)

// uninstall removes the installed program, leaving its settings and logs.
// It has no window: the questions are message boxes of the system. quiet
// removes it without asking or telling.
func uninstall(quiet bool) {
	// Only the language is needed; nothing is written
	config.Load()
	forms.SetLanguage(config.Get().Language)
	t := forms.T()

	if !quiet && !install.Confirm(app.DisplayName, forms.UninstallAsk()) {
		return
	}
	if err := install.Uninstall(); err != nil {
		if !quiet {
			install.Inform(app.DisplayName, err.Error())
		}
		os.Exit(1)
	}
	if !quiet {
		install.Inform(app.DisplayName, t.Uninstalled)
	}
}
