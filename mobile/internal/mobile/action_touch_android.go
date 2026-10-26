package mobile

import (
	"fyne.io/fyne/v2"
	touch "fyne.io/fyne/v2/driver/mobile"
)

func (b *iconAction) TouchDown(e *touch.TouchEvent) {
	if b.onTouchStart != nil {
		b.onTouchStart()
	}
	if !b.Disabled() {
		b.activationTouch.begin(e.ID)
	}
}
func (b *iconAction) TouchUp(e *touch.TouchEvent)     { b.activationTouch.end(e.ID) }
func (b *iconAction) TouchCancel(e *touch.TouchEvent) { b.activationTouch.cancel(e.ID) }
func (b *iconAction) Tapped(e *fyne.PointEvent) {
	if b.touchGuarded && !b.activationTouch.accept() {
		return
	}
	b.Button.Tapped(e)
}
