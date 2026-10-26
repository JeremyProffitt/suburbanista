// Package appearance adapts the shared calendar and artwork to the Fyne app.
package appearance

import (
	"fmt"
	"fyne.io/fyne/v2"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
)

type Settings = appearancecore.Settings
type Style = appearancecore.Style
type Holiday = appearancecore.Holiday
type Palette = appearancecore.Palette

const Normal = appearancecore.Normal
const Seasonal = appearancecore.Seasonal
const Automatic = appearancecore.Automatic
const preferenceKey = appearancecore.PreferenceKey

func Default() Settings                         { return appearancecore.Default() }
func Holidays() []Holiday                       { return appearancecore.Holidays() }
func Styles(holiday string) []Style             { return appearancecore.Styles(holiday) }
func Load(p fyne.Preferences) (Settings, error) { return appearancecore.Load(p) }
func Save(p fyne.Preferences, s Settings) error { return appearancecore.Save(p, s) }
func Icon(style Style, action string, palette Palette) (fyne.Resource, error) {
	data, err := appearancecore.IconPNG(style, action, palette)
	if err != nil {
		return nil, err
	}
	return fyne.NewStaticResource(style.ID+"-"+action+"-"+fmt.Sprint(palette.Accent)+".png", data), nil
}
