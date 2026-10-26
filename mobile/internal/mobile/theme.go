package mobile

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// vehicleTheme keeps text on opaque navy/graphite surfaces. Photography is
// confined to the hero; statuses never depend on a color alone.
type vehicleTheme struct{}

func (vehicleTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 10, G: 18, B: 30, A: 255}
	case theme.ColorNameInputBackground, theme.ColorNameOverlayBackground, theme.ColorNameMenuBackground:
		return color.NRGBA{R: 25, G: 37, B: 52, A: 255}
	case theme.ColorNameButton, theme.ColorNameHeaderBackground:
		return color.NRGBA{R: 40, G: 56, B: 74, A: 255}
	case theme.ColorNameDisabledButton:
		return color.NRGBA{R: 28, G: 39, B: 53, A: 255}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 244, G: 247, B: 250, A: 255}
	case theme.ColorNameDisabled, theme.ColorNamePlaceHolder:
		return color.NRGBA{R: 168, G: 181, B: 197, A: 255}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 183, G: 213, B: 242, A: 255}
	case theme.ColorNameForegroundOnPrimary:
		return color.NRGBA{R: 10, G: 18, B: 30, A: 255}
	case theme.ColorNameSeparator, theme.ColorNameInputBorder:
		return color.NRGBA{R: 73, G: 91, B: 112, A: 255}
	}
	return theme.DefaultTheme().Color(name, theme.VariantDark)
}
func (vehicleTheme) Font(style fyne.TextStyle) fyne.Resource { return theme.DefaultTheme().Font(style) }
func (vehicleTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}
func (vehicleTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 8
	case theme.SizeNameText:
		return 16
	case theme.SizeNameHeadingText:
		return 24
	case theme.SizeNameSubHeadingText:
		return 20
	}
	return theme.DefaultTheme().Size(name)
}
