package mobile

import (
	"context"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"time"
)

func (s *Screen) loadAppearance() (appearance.Settings, error) {
	if platformWidgetsAvailable() {
		return appearancecore.LoadShared(s.app.Storage().RootURI().Path())
	}
	return appearance.Load(s.app.Preferences())
}
func (s *Screen) persistAppearance(settings appearance.Settings) error {
	if platformWidgetsAvailable() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return appearancecore.SaveShared(ctx, s.app.Storage().RootURI().Path(), settings)
	}
	return appearance.Save(s.app.Preferences(), settings)
}
func (s *Screen) mutateAppearance(fn func(*appearance.Settings)) bool {
	if s.app == nil {
		return false
	}
	settings := s.appearanceSettings().Clone()
	if platformWidgetsAvailable() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var err error
		settings, err = appearancecore.UpdateShared(ctx, s.app.Storage().RootURI().Path(), func(value *appearancecore.Settings) error { fn(value); return nil })
		if err != nil {
			s.status.SetText(err.Error())
			return false
		}
	} else {
		fn(&settings)
		if err := s.persistAppearance(settings); err != nil {
			s.status.SetText(err.Error())
			return false
		}
	}
	s.appearance = settings
	s.appearanceKey = ""
	s.paintHome()
	if s.home != nil {
		s.home.root.Refresh()
	}
	return true
}
