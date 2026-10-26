package mobile

import "testing"

func TestVehicleTouchRequiresSameControlStart(t *testing.T) {
	var command activationTouch
	// Fyne's release routing sends TouchUp and then Tapped to a command even
	// though the gesture began on the map. No command TouchDown occurred.
	command.end(0)
	if command.accept() {
		t.Fatal("map drag release authorized a command")
	}
	command.begin(0)
	command.cancel(0)
	command.end(0)
	if command.accept() {
		t.Fatal("cancelled command press authorized a command")
	}
	command.begin(1)
	command.end(1)
	if command.accept() {
		t.Fatal("secondary pinch finger authorized a command")
	}
	command.begin(0)
	command.end(0) // a long press has TouchUp but no Tapped in Fyne
	command.end(1)
	if command.accept() {
		t.Fatal("secondary release consumed a stale long-press arm")
	}
	command.begin(0)
	command.end(0)
	if !command.accept() {
		t.Fatal("explicit primary tap rejected")
	}
	if command.accept() {
		t.Fatal("one tap authorized twice")
	}
}

func TestForegroundExitClearsCommandTouches(t *testing.T) {
	s := &Screen{quickButtons: map[string]*iconAction{"start": {}, "lock": {}, "unlock": {}}}
	for _, b := range s.quickButtons {
		b.activationTouch.begin(0)
	}
	s.appearanceClosed()
	for _, b := range s.quickButtons {
		b.activationTouch.end(0)
		if b.activationTouch.accept() {
			t.Fatal("backgrounding preserved command touch")
		}
	}
}
