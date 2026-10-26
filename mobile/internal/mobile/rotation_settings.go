package mobile

import (
	"errors"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"slices"
	"strconv"
	"time"
)

func rotationControls(r *appearancecore.Rotation) fyne.CanvasObject {
	enabled := newRowCheck("Auto change", func(v bool) { r.Enabled = v })
	enabled.SetChecked(r.Enabled)
	opening := newRowCheck("Rotate on app opening", func(v bool) { r.OnOpening = v })
	opening.SetChecked(r.OnOpening)
	interval := widget.NewEntry()
	interval.SetText(strconv.Itoa(r.Minutes))
	interval.Validator = func(value string) error {
		n, e := strconv.Atoi(value)
		if e != nil || n < 1 || n > 1440 {
			return errors.New("Enter 1 to 1440 minutes")
		}
		return nil
	}
	// Entry validation and persistence use the same 1–1440 minute bound.
	interval.OnChanged = func(value string) {
		n, err := strconv.Atoi(value)
		if err != nil {
			r.Minutes = 0
		} else {
			r.Minutes = n
		}
	}
	return container.NewVBox(enabled, container.NewBorder(nil, nil, widget.NewLabel("Minutes"), nil, interval), opening)
}
func (s *Screen) rotationEditor(draft *appearance.Settings) fyne.CanvasObject {
	palette, _ := draft.PaletteAt(time.Now())
	backgrounds := container.NewVBox(rotationControls(&draft.BackgroundRotation), rotationBackgroundGallery(draft, palette))
	buttons := container.NewVBox(rotationControls(&draft.ButtonRotation))
	normalSettings := draft.Clone()
	normalSettings.Mode = appearance.Normal
	normalPalette, _ := normalSettings.PaletteAt(time.Now())
	normal := rotationStyleGallery(appearance.Styles(""), normalPalette, func(id string) bool { return hasID(draft.ButtonRotation.Selected, id) }, func(id string, on bool) {
		draft.ButtonRotation.Selected = selectID(draft.ButtonRotation.Selected, id, on)
	})
	lists := []*widget.AccordionItem{widget.NewAccordionItem("Normal button list", normal)}
	for _, holiday := range appearance.Holidays() {
		holidayID := holiday.ID
		holidaySettings := draft.Clone()
		holidaySettings.Mode, holidaySettings.Holiday = appearance.Seasonal, holidayID
		holidayPalette, _ := holidaySettings.PaletteAt(time.Now())
		list := rotationStyleGallery(appearance.Styles(holiday.ID), holidayPalette, func(id string) bool { return hasID(draft.HolidayRotation[holidayID], id) }, func(id string, on bool) {
			draft.HolidayRotation[holidayID] = selectID(draft.HolidayRotation[holidayID], id, on)
		})
		lists = append(lists, widget.NewAccordionItem(holiday.Name+" button list", list))
	}
	buttons.Add(widget.NewAccordion(lists...))
	automatic := newRowCheck("Automatic sky and weather", func(v bool) { draft.AutomaticSky = v })
	automatic.SetChecked(draft.AutomaticSky)
	return container.NewVBox(automatic, widget.NewAccordion(widget.NewAccordionItem("Backgrounds", backgrounds), widget.NewAccordionItem("Buttons", buttons)))
}
func hasID(values []string, id string) bool {
	for _, v := range values {
		if v == id {
			return true
		}
	}
	return false
}
func selectID(values []string, id string, on bool) []string {
	out := []string{}
	for _, v := range values {
		if v != id {
			out = append(out, v)
		}
	}
	if on {
		out = append(out, id)
	}
	return out
}
func normalizeRotationEpochs(draft *appearance.Settings, previous appearance.Settings, now time.Time) {
	if draft.BackgroundRotation.Epoch.IsZero() || draft.BackgroundRotation.Minutes != previous.BackgroundRotation.Minutes || draft.BackgroundRotation.Enabled != previous.BackgroundRotation.Enabled || !slices.Equal(draft.BackgroundSelection(), previous.BackgroundSelection()) {
		selected := draft.BackgroundSelection()
		current := previous.BackdropAt(now)
		if hasID(selected, current) {
			draft.Backdrop = current
		} else if len(selected) > 0 {
			draft.Backdrop = selected[0]
		}
		draft.BackgroundRotation.Epoch = now
	}
	if draft.ButtonRotation.Minutes != previous.ButtonRotation.Minutes || draft.ButtonRotation.Enabled != previous.ButtonRotation.Enabled || (draft.ActiveHoliday(now) == previous.ActiveHoliday(now) && !slices.Equal(draft.ButtonSelection(now), previous.ButtonSelection(now))) {
		selected := draft.ButtonSelection(now)
		style, err := previous.SelectedStyleAt(now)
		id := style.ID
		if err != nil || !hasID(selected, id) {
			if len(selected) > 0 {
				id = selected[0]
			}
		}
		holiday := draft.ActiveHoliday(now)
		if holiday == "" {
			draft.NormalStyle = id
		} else {
			draft.HolidayStyles[holiday] = id
		}
		draft.ButtonRotation.Epoch = now
	}
	if draft.ButtonRotation.Epoch.IsZero() {
		draft.ButtonRotation.Epoch = now
	}
}
func (s *Screen) showRotationDraft(draft *appearance.Settings) {
	copy := draft.Clone()
	message := widget.NewLabel("Background and button rotation are independent.")
	message.Wrapping = fyne.TextWrapWord
	var popup *widget.PopUp
	buttons := container.NewGridWithColumns(2, widget.NewButton("Cancel", func() { popup.Hide() }), widget.NewButton("Apply", func() {
		if err := copy.Validate(); err != nil {
			message.SetText(err.Error())
			return
		}
		*draft = copy.Clone()
		popup.Hide()
	}))
	popup = widget.NewModalPopUp(container.NewBorder(message, buttons, nil, nil, container.NewVScroll(s.rotationEditor(&copy))), s.window.Canvas())
	size := s.window.Canvas().Size()
	popup.Resize(fyne.NewSize(size.Width-16, size.Height-16))
	popup.Show()
}
