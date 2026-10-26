package mobile

import (
	"context"
	"errors"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"time"
)

func (s *Screen) verificationUI(records []CommandVerification) {
	s.verificationDisplay = append([]CommandVerification{}, records...)
	for _, action := range []string{"start", "unlock", "lock"} {
		var latest *CommandVerification
		for i := len(s.verificationDisplay) - 1; i >= 0; i-- {
			if s.verificationDisplay[i].Action == action {
				latest = &s.verificationDisplay[i]
				break
			}
		}
		if label := s.verificationLabels[action]; label != nil {
			value := "No test"
			if latest != nil {
				value = latest.ProviderState
			}
			if s.preview.Load() {
				value = "Preview only"
			}
			label.SetText(value)
		}
		if b := s.verificationObserved[action]; b != nil {
			b.selected = latest != nil && latest.PhysicalObserved != nil && *latest.PhysicalObserved
			if latest == nil || s.preview.Load() {
				b.Disable()
			} else {
				b.Enable()
			}
			b.Refresh()
		}
		if b := s.verificationActions[action]; b != nil {
			if !s.preview.Load() && !s.allowedActions[action] {
				b.Hide()
			} else {
				b.Show()
			}
			if s.commandPending.Load() && !s.preview.Load() {
				b.Disable()
			} else {
				b.Enable()
			}
		}
	}
}
func (s *Screen) showVerification() {
	s.verificationLabels = map[string]*widget.Label{}
	s.verificationObserved = map[string]*iconAction{}
	s.verificationActions = map[string]*iconAction{}
	actions := container.NewGridWithColumns(3)
	states := container.NewGridWithColumns(3)
	physical := container.NewGridWithColumns(3)
	for _, id := range []string{"start", "unlock", "lock"} {
		action := id
		b := newTool(featureCaption(id), featureIcon(id), func() { s.chooseCommand(action) })
		s.verificationActions[id] = b
		if !s.preview.Load() && !s.allowedActions[id] {
			b.Hide()
		}
		actions.Add(b)
		label := wrapped("No test")
		label.Alignment = fyne.TextAlignCenter
		s.verificationLabels[id] = label
		states.Add(label)
		observed := newTool("Observed", theme.ConfirmIcon(), func() { s.run(func(ctx context.Context) error { return s.setPhysicalObserved(ctx, action) }) })
		observed.accessibleName = "Owner observed " + id
		s.verificationObserved[id] = observed
		physical.Add(observed)
	}
	s.verificationUI(s.verificationDisplay)
	s.openPanel("Verify · owner observations", container.NewVBox(actions, states, physical))
}
func (s *Screen) setPhysicalObserved(_ context.Context, action string) error {
	if s.preview.Load() {
		return nil
	}
	records := append([]CommandVerification{}, s.saved.Verifications...)
	found := false
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Action == action {
			value := records[i].PhysicalObserved == nil || !*records[i].PhysicalObserved
			at := time.Now().UTC()
			records[i].PhysicalObserved = &value
			records[i].PhysicalObservedAt = &at
			found = true
			break
		}
	}
	if !found {
		return errors.New("Send a test first")
	}
	previous := s.saved.Verifications
	s.saved.Verifications = records
	if e := s.vault.Save(s.saved); e != nil {
		s.saved.Verifications = previous
		return e
	}
	fyne.Do(func() { s.verificationUI(records) })
	return nil
}
