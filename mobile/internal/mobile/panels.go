package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"strconv"
	"strings"
)

// Text is split into bounded pages. Long provider values remain available without
// making the phone screen or a dialog scroll.
func textPages(lines []string) []string {
	var pages []string
	for _, line := range lines {
		r := []rune(strings.TrimSpace(line))
		for len(r) > 0 {
			n := len(r)
			if n > 120 {
				n = 120
				for i := n; i > 70; i-- {
					if r[i-1] == ' ' {
						n = i
						break
					}
				}
			}
			pages = append(pages, strings.TrimSpace(string(r[:n])))
			r = r[n:]
		}
	}
	if len(pages) == 0 {
		return []string{"No data"}
	}
	return pages
}
func (s *Screen) pagedBody(lines []string, extra fyne.CanvasObject) fyne.CanvasObject {
	pages := textPages(lines)
	index := 0
	label := wrapped(pages[0])
	label.Wrapping = fyne.TextWrapBreak
	count := widget.NewLabel("1 / " + stringCount(len(pages)))
	count.Alignment = fyne.TextAlignCenter
	var previous, next *iconAction
	update := func() {
		label.SetText(pages[index])
		count.SetText(stringCount(index+1) + " / " + stringCount(len(pages)))
		if index == 0 {
			previous.Disable()
		} else {
			previous.Enable()
		}
		if index == len(pages)-1 {
			next.Disable()
		} else {
			next.Enable()
		}
	}
	previous = newGlyph("Previous page", theme.NavigateBackIcon(), func() {
		if index > 0 {
			index--
			update()
		}
	})
	next = newGlyph("Next page", theme.NavigateNextIcon(), func() {
		if index+1 < len(pages) {
			index++
			update()
		}
	})
	update()
	row := container.NewGridWithColumns(3, previous, count, next)
	objects := []fyne.CanvasObject{label, row}
	if extra != nil {
		objects = append(objects, extra)
	}
	return container.NewVBox(objects...)
}
func stringCount(n int) string { return strconv.Itoa(n) }
func (s *Screen) openPanel(title string, body fyne.CanvasObject) *widget.PopUp {
	var popup *widget.PopUp
	close := newGlyph("Close", theme.CancelIcon(), func() { popup.Hide() })
	heading := container.NewBorder(nil, nil, nil, close, wrapped(title))
	popup = widget.NewModalPopUp(surface("", heading, body), s.window.Canvas())
	size := s.window.Canvas().Size()
	width, height := size.Width-32, float32(280)
	if width > 420 {
		width = 420
	}
	if height > size.Height-32 {
		height = size.Height - 32
	}
	popup.Resize(fyne.NewSize(width, height))
	popup.Show()
	return popup
}
func (s *Screen) showPagedInfo(title string, lines []string) {
	s.openPanel(title, s.pagedBody(lines, nil))
}
func (s *Screen) showAccount() {
	state := "Signed out"
	if s.saved.Session.RefreshHandle != "" {
		state = "Signed in"
	}
	if s.preview.Load() {
		state = "Preview · live session unchanged"
	}
	s.openPanel("Account", container.NewVBox(wrapped(state), container.NewGridWithColumns(2, s.signIn, s.logout)))
}
func (s *Screen) showUpdatePanel()   { s.openPanel("Updates", s.settingsContent) }
func (s *Screen) showReminderPanel() { s.openPanel("Reminders", s.reminderSettings()) }
