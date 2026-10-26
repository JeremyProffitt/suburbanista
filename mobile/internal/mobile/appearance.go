package mobile

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"image/color"
	"strings"
	"time"
)

func (s *Screen) appearanceSettings() appearance.Settings {
	if s.appearance.Mode == "" {
		s.appearance = appearance.Default()
		if s.app != nil {
			settings, err := s.loadAppearance()
			if err == nil {
				s.appearance = settings
			} else if s.status != nil {
				s.status.SetText(err.Error())
			}
		}
	}
	return s.appearance
}

// styleTile keeps the Button keyboard/accessibility behavior while showing the
// complete Start/Unlock/Lock set. The same renderer is used for the large preview.
type styleTile struct {
	widget.Button
	style      appearance.Style
	icons      [3]fyne.Resource
	selected   bool
	accent     color.Color
	background color.Color
}

func newStyleTile() *styleTile {
	t := &styleTile{}
	t.ExtendBaseWidget(t)
	return t
}
func (t *styleTile) setStyle(style appearance.Style, palette appearance.Palette, selected bool) error {
	t.style = style
	t.Text = style.Name
	t.selected = selected
	t.accent = palette.Accent
	t.background = palette.Surface
	for i, action := range []string{"start", "unlock", "lock"} {
		icon, err := appearance.Icon(style, action, palette)
		if err != nil {
			return err
		}
		t.icons[i] = icon
	}
	t.Show()
	t.Refresh()
	return nil
}
func (t *styleTile) AccessibilityLabel() string { return t.Text + " · Start, Unlock, Lock" }
func (t *styleTile) CreateRenderer() fyne.WidgetRenderer {
	r := &styleTileRenderer{owner: t, background: canvas.NewRectangle(color.NRGBA{R: 8, G: 29, B: 20, A: 255}), label: widget.NewLabel(t.Text)}
	r.background.CornerRadius = 8
	r.label.Truncation = fyne.TextTruncateEllipsis
	for i := range r.images {
		r.images[i] = canvas.NewImageFromResource(t.icons[i])
		r.images[i].FillMode = canvas.ImageFillContain
	}
	r.Refresh()
	return r
}

type styleTileRenderer struct {
	owner      *styleTile
	background *canvas.Rectangle
	label      *widget.Label
	images     [3]*canvas.Image
}

func (r *styleTileRenderer) MinSize() fyne.Size { return fyne.NewSize(120, 72) }
func (r *styleTileRenderer) Layout(size fyne.Size) {
	r.background.Resize(size)
	r.label.Move(fyne.NewPos(2, 0))
	r.label.Resize(fyne.NewSize(size.Width-4, 28))
	for i, img := range r.images {
		img.Move(fyne.NewPos(4+float32(i)*(size.Width-8)/3, 28))
		img.Resize(fyne.NewSize((size.Width-8)/3, size.Height-32))
	}
}
func (r *styleTileRenderer) Refresh() {
	r.background.FillColor = r.owner.background
	r.label.SetText(r.owner.Text)
	r.background.StrokeWidth = 1
	r.background.StrokeColor = color.NRGBA{R: 48, G: 93, B: 69, A: 255}
	if r.owner.selected {
		r.background.StrokeWidth = 2
		r.background.StrokeColor = r.owner.accent
	}
	for i, img := range r.images {
		img.Resource = r.owner.icons[i]
		img.Refresh()
	}
	r.background.Refresh()
}
func (r *styleTileRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.background, r.label, r.images[0], r.images[1], r.images[2]}
}
func (r *styleTileRenderer) Destroy() {}

func (s *Screen) showAppearance() *widget.PopUp {
	draft := s.appearanceSettings().Clone()
	selectedPreview := newStyleTile()
	tiles := [4]*styleTile{newStyleTile(), newStyleTile(), newStyleTile(), newStyleTile()}
	gallery := container.NewGridWithColumns(2, tiles[0], tiles[1], tiles[2], tiles[3])
	page := 0
	background := canvas.NewRectangle(color.Black)
	effective, local, _ := s.presentationAt(draft, time.Now())
	currentStyles := appearance.Styles(effective.ActiveHoliday(local))
	pageLabel := widget.NewLabel("")
	pageLabel.Alignment = fyne.TextAlignCenter
	heading := widget.NewLabel("Appearance")
	heading.TextStyle = fyne.TextStyle{Bold: true}
	var previous, next *widget.Button
	var redraw func()
	redraw = func() {
		effective, local, available := s.presentationAt(draft, time.Now())
		if draft.Mode == appearance.Automatic && !available {
			heading.SetText("Automatic unavailable · device time")
		} else {
			heading.SetText("Appearance")
		}
		selected, err := effective.SelectedStyleAt(local)
		if err != nil {
			heading.SetText(err.Error())
			return
		}
		if len(currentStyles) == 0 || currentStyles[0].Holiday != selected.Holiday {
			currentStyles = appearance.Styles(selected.Holiday)
			page = 0
			for i, style := range currentStyles {
				if style.ID == selected.ID {
					page = i / 4
				}
			}
		}
		palette, _ := effective.PaletteAt(local)
		background.FillColor = palette.Background
		background.Refresh()
		if err = selectedPreview.setStyle(selected, palette, true); err != nil {
			heading.SetText(err.Error())
			return
		}
		for i, tile := range tiles {
			index := page*4 + i
			if index >= len(currentStyles) {
				tile.Hide()
				continue
			}
			style := currentStyles[index]
			if err = tile.setStyle(style, palette, style.ID == selected.ID); err != nil {
				heading.SetText(err.Error())
				return
			}
			tile.OnTapped = func() {
				if style.Holiday == "" {
					draft.NormalStyle = style.ID
				} else {
					draft.HolidayStyles[style.Holiday] = style.ID
				}
				redraw()
			}
		}
		pageLabel.SetText(fmt.Sprintf("%d / %d", page+1, (len(currentStyles)+3)/4))
		if previous != nil {
			if page == 0 {
				previous.Disable()
			} else {
				previous.Enable()
			}
		}
		if next != nil {
			if (page+1)*4 >= len(currentStyles) {
				next.Disable()
			} else {
				next.Enable()
			}
		}
	}
	previous = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		if page > 0 {
			page--
			redraw()
		}
	})
	next = widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() {
		if (page+1)*4 < len(currentStyles) {
			page++
			redraw()
		}
	})
	previous.SetText("Previous")
	next.SetText("Next")
	pager := container.NewGridWithColumns(3, previous, pageLabel, next)
	selection := []string{"Automatic", "Normal"}
	for _, holiday := range appearance.Holidays() {
		selection = append(selection, holiday.Name)
	}
	mode := widget.NewSelect(selection, func(name string) {
		switch name {
		case "Automatic":
			draft.Mode = appearance.Automatic
		case "Normal":
			draft.Mode = appearance.Normal
		default:
			for _, h := range appearance.Holidays() {
				if h.Name == name {
					draft.Mode = appearance.Seasonal
					draft.Holiday = h.ID
				}
			}
		}
		effective, local, _ := s.presentationAt(draft, time.Now())
		currentStyles = appearance.Styles(effective.ActiveHoliday(local))
		page = 0
		selected, _ := effective.SelectedStyleAt(local)
		for i, style := range currentStyles {
			if style.ID == selected.ID {
				page = i / 4
			}
		}
		redraw()
	})
	modeName := "Normal"
	if draft.Mode == appearance.Automatic {
		modeName = "Automatic"
	} else if draft.Mode == appearance.Seasonal {
		for _, h := range appearance.Holidays() {
			if h.ID == draft.Holiday {
				modeName = h.Name
			}
		}
	}
	mode.SetSelected(modeName)
	colorEntry := widget.NewEntry()
	colorEntry.SetText(draft.Background)
	colorEntry.SetPlaceHolder("#RRGGBB")
	applyColor := func(value string) {
		candidate := draft.Clone()
		candidate.Background = strings.ToUpper(strings.TrimSpace(value))
		if err := candidate.Validate(); err != nil {
			heading.SetText("Color needs #RRGGBB")
			return
		}
		draft = candidate
		colorEntry.SetText(draft.Background)
		heading.SetText("Appearance")
		redraw()
	}
	swatches := container.NewGridWithColumns(8)
	for _, value := range []string{"#173F2A", "#12606A", "#17324F", "#362149", "#611C30", "#704020", "#6E5815", "#242424"} {
		v := value
		icon := fyne.NewStaticResource("swatch-"+v[1:]+".svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32"><circle cx="16" cy="16" r="13" fill="`+v+`" stroke="#d0ddd5" stroke-width="2"/></svg>`))
		button := newGlyph("Color "+v, icon, func() { applyColor(v) })
		button.rawIcon = true
		swatches.Add(button)
	}
	custom := container.NewBorder(nil, nil, nil, widget.NewButton("Custom color", func() { applyColor(colorEntry.Text) }), colorEntry)
	var popup *widget.PopUp
	buttons := container.NewGridWithColumns(2, widget.NewButton("Cancel", func() { popup.Hide() }), widget.NewButton("Save", func() {
		_, local, _ := s.presentationAt(draft, time.Now())
		normalizeRotationEpochs(&draft, s.appearanceSettings(), local)
		if err := s.persistAppearance(draft); err != nil {
			heading.SetText(err.Error())
			return
		}
		s.appearance = draft.Clone()
		s.appearanceKey = ""
		s.paintHome()
		popup.Hide()
	}))
	header := container.NewBorder(nil, nil, nil, widget.NewButton("Rotation", func() { s.showRotationDraft(&draft) }), heading)
	body := container.New(&appearanceLayout{}, header, mode, selectedPreview, gallery, pager, swatches, custom, buttons)
	popup = widget.NewModalPopUp(container.NewStack(background, body), s.window.Canvas())
	size := s.window.Canvas().Size()
	popup.Resize(fyne.NewSize(size.Width-16, size.Height-16))
	s.appearanceRefresh = func() {
		if popup.Visible() {
			redraw()
		}
	}
	redraw()
	popup.Show()
	return popup
}

func (s *Screen) applyAppearance(now time.Time) {
	settings, now, available := s.displayAppearance(now)
	if s.home != nil && s.appearanceSettings().Mode == appearance.Automatic && !available {
		s.home.mode.Text = "Automatic unavailable · device time"
		s.home.mode.Refresh()
	}
	style, err := settings.SelectedStyleAt(now)
	if err != nil {
		return
	}
	key := style.ID + settings.Background
	if s.appearanceKey == key {
		return
	}
	palette, err := settings.PaletteAt(now)
	if err != nil {
		return
	}
	for action, button := range s.quickButtons {
		// The shared raster owns visible artwork. Transparent hit controls keep
		// their action metadata without decoding/recoloring the same PNG twice.
		if button.hitOnly {
			continue
		}
		icon, err := appearance.Icon(style, action, palette)
		if err != nil {
			s.status.SetText(err.Error())
			return
		}
		button.SetIcon(icon)
	}
	s.appearanceKey = key
}
