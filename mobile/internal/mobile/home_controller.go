package mobile

import (
	"context"
	"fyne.io/fyne/v2"
	"time"
)

func (s *Screen) updateFeatureAvailability() {
	for id, b := range s.quickButtons {
		if s.preview.Load() || s.allowedActions[id] {
			b.Show()
			if (s.commandPending.Load() || s.busy.Load()) && !s.preview.Load() {
				b.Disable()
			} else {
				b.Enable()
			}
		} else {
			b.Hide()
		}
	}
	if s.home != nil {
		objects := []fyne.CanvasObject{}
		for _, id := range []string{"start", "unlock", "lock"} {
			if b := s.quickButtons[id]; b != nil && b.Visible() {
				objects = append(objects, b)
			}
		}
		s.home.actions.Layout = nil
		s.home.actions.Objects = objects
		s.home.actions.Refresh()
		s.home.root.Refresh()
	}
}
func (s *Screen) showPreviewOverview() {
	s.status.SetText("Preview")
	s.detail.SetText("Examples only")
	s.commandStatus.SetText("")
	if s.retryCommand != nil {
		s.retryCommand.Hide()
	}
	s.updateFeatureAvailability()
	s.paintHome()
}
func (s *Screen) latestActivity(ctx context.Context) error {
	if s.preview.Load() {
		return nil
	}
	if s.saved.Session.RefreshHandle == "" {
		return ErrUnauthorized
	}
	if time.Until(s.saved.Session.AccessExpiresAt) < time.Minute {
		if e := s.rotate(ctx); e != nil {
			return e
		}
	}
	result, e := s.client.CommandStatus(ctx, s.saved.Session, "")
	if e != nil {
		return e
	}
	fyne.Do(func() {
		s.activityDetail = commandText(result)
		if s.activity != nil {
			s.activity.SetText(compactCommandText(result))
		}
	})
	return nil
}
