package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

type rowCheck struct{ widget.Check }

func newRowCheck(text string, changed func(bool)) *rowCheck {
	c := &rowCheck{Check: widget.Check{Text: text, OnChanged: changed}}
	c.ExtendBaseWidget(c)
	return c
}

// The full allocated row is a single target, including the label and its padding.
func (c *rowCheck) Tapped(*fyne.PointEvent) {
	if !c.Disabled() {
		c.SetChecked(!c.Checked)
	}
}
