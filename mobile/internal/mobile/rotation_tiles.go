package mobile

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/homescene"
)

func rotationSelectionLabel(name string, selected bool) string {
	if selected {
		return "Enabled · " + name
	}
	return "Disabled · " + name
}

func rotationTileLabel(name string, selected bool) string {
	if selected {
		return "[x] " + name
	}
	return "[ ] " + name
}

// Keep the same three-icon renderer as the command selector, with an explicit
// enabled state for accessibility rather than consuming the short name row.
type rotationStyleTile struct{ styleTile }

func (t *rotationStyleTile) AccessibilityLabel() string {
	return rotationSelectionLabel(t.style.Name, t.selected) + " · Start, Unlock, Lock"
}
func newRotationStyleTile() *rotationStyleTile {
	t := &rotationStyleTile{}
	t.ExtendBaseWidget(t)
	return t
}

// Four reusable tiles keep image decoding bounded as the owner browses all lists.
func rotationTilePages(objects []fyne.CanvasObject, count int, show func(int)) fyne.CanvasObject {
	page := 0
	label := widget.NewLabel("")
	label.Alignment = fyne.TextAlignCenter
	var previous, next *widget.Button
	redraw := func() {
		show(page * len(objects))
		label.SetText(fmt.Sprintf("%d / %d", page+1, (count+len(objects)-1)/len(objects)))
		previous.Enable()
		next.Enable()
		if page == 0 {
			previous.Disable()
		}
		if (page+1)*len(objects) >= count {
			next.Disable()
		}
	}
	previous = widget.NewButton("Previous", func() {
		if page > 0 {
			page--
			redraw()
		}
	})
	next = widget.NewButton("Next", func() {
		if (page+1)*len(objects) < count {
			page++
			redraw()
		}
	})
	redraw()
	return container.NewVBox(container.NewGridWithColumns(2, objects...), container.NewGridWithColumns(3, previous, label, next))
}

func rotationStyleGallery(styles []appearance.Style, palette appearance.Palette, selected func(string) bool, choose func(string, bool)) fyne.CanvasObject {
	tiles := []*rotationStyleTile{newRotationStyleTile(), newRotationStyleTile(), newRotationStyleTile(), newRotationStyleTile()}
	objects := make([]fyne.CanvasObject, len(tiles))
	for i, tile := range tiles {
		objects[i] = tile
	}
	return rotationTilePages(objects, len(styles), func(start int) {
		for i, tile := range tiles {
			if start+i >= len(styles) {
				tile.Hide()
				continue
			}
			style := styles[start+i]
			if err := tile.setStyle(style, palette, selected(style.ID)); err != nil {
				tile.Text = style.Name + " · Preview unavailable"
				tile.icons = [3]fyne.Resource{}
				tile.Disable()
				tile.Show()
				tile.Refresh()
				continue
			}
			tile.Enable()
			tile.Text = rotationTileLabel(style.Name, tile.selected)
			tile.Refresh()
			tile.OnTapped = func() {
				choose(style.ID, !selected(style.ID))
				tile.selected = selected(style.ID)
				tile.Text = rotationTileLabel(style.Name, tile.selected)
				tile.Refresh()
			}
		}
	})
}

type rotationBackgroundTile struct {
	widget.Button
	resource fyne.Resource
	name     string
	selected bool
	palette  appearance.Palette
}

func (t *rotationBackgroundTile) AccessibilityLabel() string {
	return rotationSelectionLabel(t.name, t.selected)
}
func (t *rotationBackgroundTile) CreateRenderer() fyne.WidgetRenderer {
	r := &rotationBackgroundRenderer{tile: t, background: canvas.NewRectangle(t.palette.Surface), label: widget.NewLabel(t.Text), photo: canvas.NewImageFromResource(t.resource)}
	r.background.CornerRadius = 8
	r.label.Truncation = fyne.TextTruncateEllipsis
	r.photo.FillMode = canvas.ImageFillContain
	r.Refresh()
	return r
}

type rotationBackgroundRenderer struct {
	tile       *rotationBackgroundTile
	background *canvas.Rectangle
	label      *widget.Label
	photo      *canvas.Image
}

func (r *rotationBackgroundRenderer) MinSize() fyne.Size { return fyne.NewSize(120, 144) }
func (r *rotationBackgroundRenderer) Layout(size fyne.Size) {
	r.background.Resize(size)
	r.label.Move(fyne.NewPos(2, 0))
	r.label.Resize(fyne.NewSize(size.Width-4, 28))
	r.photo.Move(fyne.NewPos(4, 30))
	r.photo.Resize(fyne.NewSize(size.Width-8, size.Height-34))
}
func (r *rotationBackgroundRenderer) Refresh() {
	r.background.FillColor = r.tile.palette.Surface
	r.background.StrokeWidth = 1
	r.background.StrokeColor = color.NRGBA{R: 48, G: 93, B: 69, A: 255}
	if r.tile.selected {
		r.background.StrokeWidth = 2
		r.background.StrokeColor = r.tile.palette.Accent
	}
	r.label.SetText(r.tile.Text)
	r.photo.Resource = r.tile.resource
	r.photo.Refresh()
	r.background.Refresh()
}
func (r *rotationBackgroundRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.background, r.label, r.photo}
}
func (*rotationBackgroundRenderer) Destroy() {}

func rotationBackgroundGallery(draft *appearance.Settings, palette appearance.Palette) fyne.CanvasObject {
	backgrounds := appearancecore.Backdrops()
	tiles := make([]*rotationBackgroundTile, 4)
	objects := make([]fyne.CanvasObject, 4)
	for i := range tiles {
		tile := &rotationBackgroundTile{palette: palette}
		tile.ExtendBaseWidget(tile)
		tiles[i] = tile
		objects[i] = tile
	}
	return rotationTilePages(objects, len(backgrounds), func(start int) {
		for i, tile := range tiles {
			if start+i >= len(backgrounds) {
				tile.Hide()
				continue
			}
			background := backgrounds[start+i]
			tile.name = background.Name
			data, err := homescene.BackdropJPEG(background.ID)
			if err != nil {
				tile.Text = background.Name + " · Preview unavailable"
				tile.resource = nil
				tile.Disable()
				tile.Show()
				tile.Refresh()
				continue
			}
			tile.Enable()
			tile.resource = fyne.NewStaticResource(background.ID+"-rotation.jpg", data)
			tile.selected = hasID(draft.BackgroundRotation.Selected, background.ID)
			tile.Text = rotationTileLabel(background.Name, tile.selected)
			tile.Show()
			tile.Refresh()
			tile.OnTapped = func() {
				tile.selected = !hasID(draft.BackgroundRotation.Selected, background.ID)
				draft.BackgroundRotation.Selected = selectID(draft.BackgroundRotation.Selected, background.ID, tile.selected)
				tile.Text = rotationTileLabel(background.Name, tile.selected)
				tile.Refresh()
			}
		}
	})
}
