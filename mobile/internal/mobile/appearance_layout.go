package mobile

import "fyne.io/fyne/v2"

type appearanceLayout struct{}

func (*appearanceLayout) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(320, 320) }
func (*appearanceLayout) Layout(o []fyne.CanvasObject, size fyne.Size) {
	w, h := size.Width-16, size.Height-16
	place := func(i int, x, y, width, height float32) {
		o[i].Move(fyne.NewPos(x+8, y+8))
		o[i].Resize(fyne.NewSize(width, height))
	}
	place(0, 0, 0, w, 28)
	if w > h {
		left := (w - 12) * .4
		right := w - left - 12
		place(1, 0, 32, left, 40)
		place(2, 0, 76, left, max(float32(76), h-76-52-44-48-12))
		place(5, 0, h-52-44-48-8, left, 52)
		place(6, 0, h-44-48-4, left, 44)
		place(7, 0, h-48, left, 48)
		place(3, left+12, 32, right, h-32-48-4)
		place(4, left+12, h-48, right, 48)
	} else {
		place(1, 0, 32, w, 40)
		free := h - 32 - 40 - 48 - 52 - 44 - 48 - 24
		preview := free * .36
		gallery := free - preview
		place(2, 0, 76, w, preview)
		place(3, 0, 80+preview, w, gallery)
		place(4, 0, 84+preview+gallery, w, 48)
		place(5, 0, h-52-44-48-8, w, 52)
		place(6, 0, h-44-48-4, w, 44)
		place(7, 0, h-48, w, 48)
	}
}
