package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"image/color"
)

type actionControl interface {
	fyne.CanvasObject
	Enable()
	Disable()
	Disabled() bool
	SetText(string)
	SetIcon(fyne.Resource)
}

type touchEntry struct{ widget.Entry }

func newTouchEntry() *touchEntry { entry := &touchEntry{}; entry.ExtendBaseWidget(entry); return entry }
func (e *touchEntry) MinSize() fyne.Size {
	size := e.Entry.MinSize()
	if size.Height < 48 {
		size.Height = 48
	}
	return size
}

// iconAction retains Fyne's keyboard, accessibility and disabled-button behavior,
// while presenting a large symbol above a short caption rather than a form row.
type iconAction struct {
	widget.Button
	compact, glyph, selected, focused, round, rawIcon, hitOnly bool
	accessibleName                                             string
	touchGuarded                                               bool
	activationTouch                                            activationTouch
	onTouchStart                                               func()
}

func newIconAction(text string, icon fyne.Resource, tap func()) *iconAction {
	b := &iconAction{Button: widget.Button{Text: text, Icon: icon, OnTapped: tap}}
	b.ExtendBaseWidget(b)
	return b
}
func newTool(text string, icon fyne.Resource, tap func()) *iconAction {
	b := newIconAction(text, icon, tap)
	b.compact = true
	return b
}
func newGlyph(text string, icon fyne.Resource, tap func()) *iconAction {
	b := newTool(text, icon, tap)
	b.glyph = true
	return b
}
func (b *iconAction) AccessibilityLabel() string {
	if b.accessibleName != "" {
		return b.accessibleName
	}
	return b.Text
}
func (b *iconAction) FocusGained() { b.focused = true; b.Button.FocusGained() }
func (b *iconAction) FocusLost()   { b.focused = false; b.Button.FocusLost() }
func (b *iconAction) CreateRenderer() fyne.WidgetRenderer {
	if b.hitOnly {
		box := canvas.NewRectangle(color.NRGBA{})
		return &hitRenderer{WidgetRenderer: widget.NewSimpleRenderer(box), owner: b, box: box}
	}
	r := &iconActionRenderer{owner: b, background: canvas.NewRectangle(nil), icon: canvas.NewImageFromResource(b.Icon), caption: canvas.NewText(b.Text, nil)}
	r.background.CornerRadius = 12
	r.icon.FillMode = canvas.ImageFillContain
	r.caption.Alignment = fyne.TextAlignCenter
	r.caption.TextStyle = fyne.TextStyle{Bold: true}
	r.Refresh()
	return r
}

type hitRenderer struct {
	fyne.WidgetRenderer
	owner *iconAction
	box   *canvas.Rectangle
}

func (r *hitRenderer) Refresh() {
	r.box.StrokeWidth = 0
	if r.owner.focused {
		r.box.StrokeWidth = 2
		r.box.StrokeColor = color.NRGBA{215, 225, 235, 255}
	}
	r.WidgetRenderer.Refresh()
}

func (*hitRenderer) MinSize() fyne.Size { return fyne.NewSize(48, 48) }

type iconActionRenderer struct {
	owner      *iconAction
	background *canvas.Rectangle
	icon       *canvas.Image
	caption    *canvas.Text
}

func (r *iconActionRenderer) Layout(size fyne.Size) {
	if r.owner.round {
		diameter := min(size.Height, size.Width) - 4
		if size.Width-8 < diameter {
			diameter = size.Width - 8
		}
		r.background.Move(fyne.NewPos((size.Width-diameter)/2, 0))
		r.background.Resize(fyne.NewSize(diameter, diameter))
		r.background.CornerRadius = diameter / 2
		r.icon.Move(fyne.NewPos((size.Width-diameter)/2, 0))
		r.icon.Resize(fyne.NewSize(diameter, diameter))
		r.caption.Move(fyne.NewPos(2, diameter+4))
		r.caption.Resize(fyne.NewSize(size.Width-4, 20))
		return
	}
	r.background.Resize(size)
	iconSize := float32(36)
	if r.owner.compact {
		iconSize = 24
	}
	top := float32(10)
	if r.owner.compact {
		top = 8
	}
	if r.owner.glyph {
		top = (size.Height - iconSize) / 2
	}
	r.icon.Move(fyne.NewPos((size.Width-iconSize)/2, top))
	r.icon.Resize(fyne.NewSize(iconSize, iconSize))
	r.caption.Move(fyne.NewPos(2, top+iconSize+4))
	captionHeight := float32(20)
	if r.owner.compact {
		captionHeight = 16
	}
	r.caption.Resize(fyne.NewSize(size.Width-4, captionHeight))
}
func (r *iconActionRenderer) MinSize() fyne.Size {
	if r.owner.round {
		return fyne.NewSize(88, 88)
	}
	if r.owner.glyph {
		return fyne.NewSize(48, 48)
	}
	if r.owner.compact {
		return fyne.NewSize(56, 56)
	}
	return fyne.NewSize(100, 72)
}

type badgeLabel struct {
	widget.BaseWidget
	text string
}

func newBadge(text string) *badgeLabel    { b := &badgeLabel{text: text}; b.ExtendBaseWidget(b); return b }
func (b *badgeLabel) SetText(text string) { b.text = text; b.Refresh() }
func (b *badgeLabel) CreateRenderer() fyne.WidgetRenderer {
	label := canvas.NewText(b.text, vehicleTheme{}.Color(theme.ColorNameForeground, theme.VariantDark))
	label.TextSize = 12
	label.Alignment = fyne.TextAlignCenter
	return &badgeRenderer{owner: b, label: label}
}

type badgeRenderer struct {
	owner *badgeLabel
	label *canvas.Text
}

func (r *badgeRenderer) Layout(size fyne.Size)        { r.label.Resize(size) }
func (r *badgeRenderer) MinSize() fyne.Size           { return fyne.NewSize(0, 20) }
func (r *badgeRenderer) Refresh()                     { r.label.Text = r.owner.text; r.label.Refresh() }
func (r *badgeRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.label} }
func (r *badgeRenderer) Destroy()                     {}
func (r *iconActionRenderer) Refresh() {
	t := vehicleTheme{}
	bg := theme.ColorNameButton
	fg := theme.ColorNameForeground
	if r.owner.Disabled() {
		bg = theme.ColorNameDisabledButton
		fg = theme.ColorNameDisabled
	}
	r.background.FillColor = t.Color(bg, theme.VariantDark)
	r.background.StrokeWidth = 0
	if r.owner.selected || r.owner.focused {
		r.background.StrokeWidth = 2
		r.background.StrokeColor = t.Color(theme.ColorNamePrimary, theme.VariantDark)
	}
	if r.owner.Icon != nil {
		if r.owner.round || r.owner.rawIcon {
			r.icon.Resource = r.owner.Icon
		} else {
			res := theme.NewThemedResource(r.owner.Icon)
			res.ColorName = fg
			r.icon.Resource = res
		}
	}
	r.caption.Text = iconCaption(r.owner.Text)
	r.caption.TextSize = 13
	if r.owner.compact {
		r.caption.TextSize = 11
	}
	r.caption.Color = t.Color(fg, theme.VariantDark)
	if r.owner.glyph || r.owner.round {
		r.caption.Hide()
	} else {
		r.caption.Show()
	}
	if r.owner.round && !r.owner.focused {
		r.background.FillColor = nil
	}
	r.background.Refresh()
	r.icon.Refresh()
	r.caption.Refresh()
}
func (r *iconActionRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.background, r.icon, r.caption}
}
func (r *iconActionRenderer) Destroy() {}
func iconCaption(text string) string {
	switch text {
	case "Refresh connection status", "Reset preview":
		return "Refresh"
	case "Refreshing…":
		return "Refreshing"
	case "Sign in with Amazon":
		return "Sign in"
	case "Cancel sign-in":
		return "Cancel"
	case "Check or retry the same request":
		return "Retry"
	case "Explore Preview":
		return "Preview"
	case "Return to Live":
		return "Live"
	case "Check for updates":
		return "Check"
	case "Open update download":
		return "Download"
	case "Android notification settings":
		return "Permission"
	}
	r := []rune(text)
	if len(r) > 14 {
		return string(r[:13]) + "…"
	}
	return text
}
