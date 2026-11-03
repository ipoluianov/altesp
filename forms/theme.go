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
	colorOK        = themeColor{hex("#3fb950"), hex("#1a7f37")}
	colorError     = themeColor{hex("#f85149"), hex("#cf222e")}
	colorWarning   = themeColor{hex("#e3b341"), hex("#9a6700")}
	colorDecoded   = themeColor{hex("#d2a8ff"), hex("#8250df")}
	colorWait      = themeColor{hex("#d29922"), hex("#b07a0a")}
	colorLEDOff    = themeColor{hex("#3a3f47"), hex("#c9ced6")}
	colorLink      = themeColor{hex("#3d8bf2"), hex("#1d6fb5")}
	colorSecondary = themeColor{hex("#8b929c"), hex("#6e7781")}
)

// levelColor is the color of a line of the ESP-IDF log level, as the
// ESP-IDF monitor colors them: errors red, warnings yellow, info green;
// debug and verbose stay plain
func levelColor(level byte) (color.RGBA, bool) {
	switch level {
	case 'E':
		return colorError.get(), true
	case 'W':
		return colorWarning.get(), true
	case 'I':
		return colorOK.get(), true
	case 'V':
		return colorSecondary.get(), true
	}
	return color.RGBA{}, false
}
