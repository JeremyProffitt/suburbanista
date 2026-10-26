package mobile

import (
	"context"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"image/color"
)

func wrapped(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	return label
}
func surface(title string, body ...fyne.CanvasObject) fyne.CanvasObject {
	objects := body
	if title != "" {
		objects = append([]fyne.CanvasObject{wrapped(title)}, body...)
	}
	background := canvas.NewRectangle(color.NRGBA{R: 10, G: 24, B: 42, A: 245})
	background.CornerRadius = 12
	return container.NewStack(background, container.NewPadded(container.NewVBox(objects...)))
}
func (s *Screen) buildLayout(version string) fyne.CanvasObject {
	if s.window != nil {
		s.window.SetPadded(false)
	}
	s.installedVersion = version
	s.settingsContent = s.updateSettings()
	s.modeLabel = wrapped("")
	s.activity = wrapped("")
	s.quickButtons = map[string]*iconAction{}
	for _, id := range []string{"start", "unlock", "lock"} {
		action := id
		b := newIconAction(featureCaption(id), featureIcon(id), func() { s.chooseCommand(action) })
		b.round = true
		s.quickButtons[id] = b
	}
	s.start = s.quickButtons["start"]
	s.unlock = s.quickButtons["unlock"]
	s.lock = s.quickButtons["lock"]
	return s.buildSingleHome()
}
func (s *Screen) switchMode(preview bool) {
	if s.commandPending.Load() {
		s.showPagedInfo("Command pending", []string{"Wait for the current command result before changing mode."})
		return
	}
	s.preview.Store(preview)
	if preview {
		s.stopBackgroundWork()
		s.pauseLocationRefresh()
		s.phoneLocation.cancelWork()
		s.showPreviewOverview()
	} else {
		s.allowedActions = nil
		if s.busy.Load() {
			s.restorePending.Store(true)
		} else {
			s.run(s.restore)
		}
	}
	s.updateControlButtons()
	s.paintHome()
}
func (s *Screen) showSettings() {
	var panel *widget.PopUp
	item := func(label string, icon fyne.Resource, action func()) *iconAction {
		return newTool(label, icon, func() {
			if panel != nil {
				panel.Hide()
			}
			action()
		})
	}
	buttons := container.NewGridWithColumns(3, item("Appearance", theme.ColorPaletteIcon(), func() { s.showAppearance() }), item("Account", theme.AccountIcon(), s.showAccount), item("Refresh", theme.ViewRefreshIcon(), func() {
		s.run(func(ctx context.Context) error {
			if e := s.dashboard(ctx); e != nil {
				return e
			}
			s.refreshHomeTelemetry(ctx, true)
			s.queueEnvironment(true)
			return nil
		})
	}), item("Weather", theme.VisibilityIcon(), s.showWeatherPanel), item("Updates", theme.DownloadIcon(), s.showUpdatePanel), item("Reminder", theme.MailSendIcon(), s.showReminderPanel), item("Preview", theme.VisibilityIcon(), func() { s.switchMode(!s.preview.Load()) }), item("Verify", theme.ConfirmIcon(), s.showVerification), item("About", theme.InfoIcon(), func() {
		s.environmentMu.Lock()
		weatherState := s.weatherState
		s.environmentMu.Unlock()
		if weatherState == "" {
			weatherState = "Not checked"
		}
		s.showPagedInfo("Suburbanista", []string{"Version " + s.installedVersion, "Weather: Open-Meteo. Approximate phone location only.", "Weather status: " + weatherState, "Map imagery: Amazon Location. Provider notices remain on the map; the marker uses the reported vehicle fix."})
	}))
	panel = s.openPanel("Settings", buttons)
}
