package forms

import (
	"image/color"

	"github.com/ipoluianov/nui/ui"
)

// The color themes offered in the settings
const (
	themeDark  = ""
	themeLight = "light"
)

// ApplyTheme switches the program to the theme of the settings
func ApplyTheme(theme string) {
	if theme == themeLight {
		ui.ApplyLightTheme()
	} else {
		ui.ApplyDarkTheme()
	}
}

// themeColor is a color of the program in the dark and the light theme
type themeColor struct {
	dark, light color.RGBA
}

func (c themeColor) get() color.RGBA {
	if ui.IsDarkTheme {
		return c.dark
	}
	return c.light
}

func hex(s string) color.RGBA { return ui.ColorFromHex(s) }

var (
	colorWait      = themeColor{hex("#d29922"), hex("#b07a0a")}
	colorLEDOff    = themeColor{hex("#3a3f47"), hex("#c9ced6")}
	colorLink      = themeColor{hex("#3d8bf2"), hex("#1d6fb5")}
	colorSecondary = themeColor{hex("#8b929c"), hex("#6e7781")}
)
