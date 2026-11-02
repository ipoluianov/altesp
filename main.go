// AltEsp flashes and explores ESP32 boards: simple outside, powerful inside
package main

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/ipoluianov/altesp/app"
	"github.com/ipoluianov/altesp/cli"
	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/forms"
	"github.com/ipoluianov/altesp/install"
	"github.com/ipoluianov/nui/ui"
)

// icon.png is rendered from the drawing of the appicon package
// (scripts/render-icon.sh); icon.svg is the same for Linux
//
//go:embed icon.png
var iconPNG []byte

func appIcon() image.Image {
	img, err := png.Decode(bytes.NewReader(iconPNG))
	if err != nil {
		return nil
	}
	return img
}

func main() {
	install.SetIcon(iconPNG)
	// Started from "Installed apps" to remove it
	if install.HasArg(install.UninstallArg) {
		uninstall(install.HasArg(install.QuietArg))
		return
	}
	// ports, info, flash, read, erase, monitor: the program works in the console
	args := os.Args[1:]
	if cli.IsCommand(args) {
		cli.AttachConsole()
		os.Exit(cli.Run(args))
	}

	config.Load()
	st := config.Get()
	forms.SetLanguage(st.Language)
	forms.ApplyTheme(st.Theme)
	ui.SetAppIcon(appIcon())

	m := forms.NewMainForm()
	// Each copy of the program is a window of its own: they all remember
	// the place of the last one closed
	if ws, err := ui.OpenSettingsFile(filepath.Join(config.Dir(), "window.json")); err == nil {
		m.Form.RememberState(ws, "main")
	}
	m.Form.Show()
	m.Form.Invoke(func() {
		m.Activate()
		if install.HasArg(install.InstalledArg) {
			m.ShowInstalled()
		}
	})
	m.Form.Exec()

	// Installed from this copy: the installed one takes over
	if install.RelaunchPending() {
		if err := install.StartInstalled(); err != nil {
			install.Inform(app.DisplayName, err.Error())
		}
	}
}
