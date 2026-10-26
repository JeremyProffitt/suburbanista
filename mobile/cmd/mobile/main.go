package main

import (
	"fyne.io/fyne/v2/app"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mobile"
)

func main() {
	a := app.NewWithID("ninja.jeremy.suburbanremote")
	w := a.NewWindow("Suburbanista")
	version := a.Metadata().Version
	if version == "" {
		version = "development"
	}
	mobile.Show(a, w, version)
	a.Run()
}
