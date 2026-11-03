package forms

import (
	"image"
	"image/color"

	"github.com/ipoluianov/nui/ui"
)

// The icons are paths on a grid of 24 x 24, filled by the even-odd rule:
// a shape inside another one is a hole
var iconPaths = map[string]string{
	"menu": "M4 6h16v2H4zM4 11h16v2H4zM4 16h16v2H4z",
	// A ring open at the top right, its arrow pointing into the gap
	"refresh": "M16 5.07A8 8 0 1 0 19.73 9.93L17.8 10.45A6 6 0 1 1 15 6.8ZM17.1 3.17L18.96 7.94L13.9 8.71Z",
}

// IconSize is the side of the toolbar icons
const IconSize = 18

// iconColor is the color of the icons in the theme
var iconColor = themeColor{hex("#c9d1d9"), hex("#455a64")}

// drawIcon renders the icon of the name in the color, side pixels wide
func drawIcon(name string, side int, col color.RGBA) image.Image {
	d, ok := iconPaths[name]
	if !ok {
		return nil
	}
	p, err := ui.ParseSVGPath(d)
	if err != nil {
		return nil
	}
	p.SetFillRule(ui.FillRuleEvenOdd)
	// An image is drawn at its size in logical pixels
	cnv, img := ui.NewImageCanvas(side, side, 1)
	cnv.ScaleBy(float64(side)/24, float64(side)/24)
	cnv.FillPath(p, col)
	return img
}

// icon renders the icon in the color of the theme
func icon(name string) image.Image {
	return drawIcon(name, IconSize, iconColor.get())
}

// themedIcon sets the icon now and again when the theme changes
func themedIcon(name string, set func(img image.Image)) {
	set(icon(name))
	ui.OnThemeChanged.Connect(func(bool) { set(icon(name)) })
}

// iconButton is a flat button of the toolbar with an icon
func iconButton(name string, tooltip func() string, onClick func()) *ui.ToolButton {
	b := ui.NewToolButton(nil, "", onClick)
	themedIcon(name, b.SetImage)
	b.SetFlat(true)
	side := ui.ThemeControlHeight()
	b.SetButtonSize(side, side)
	b.SetTooltipFunc(tooltip)
	b.SetCanBeFocused(false)
	return b
}
