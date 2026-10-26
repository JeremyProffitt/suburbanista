package mobile

// activationTouch requires a primary touch to begin and end on the same
// vehicle command. The Android driver can dispatch Tapped to the object under
// the release point even when that touch began on another object.
type activationTouch struct{ down, armed bool }

func (s *Screen) cancelCommandTouches() {
	for _, id := range []string{"start", "unlock", "lock"} {
		if b := s.quickButtons[id]; b != nil {
			b.activationTouch.cancel(0)
		}
	}
}

func (t *activationTouch) begin(id int) {
	if id != 0 {
		t.down, t.armed = false, false
		return
	}
	t.down = true
	t.armed = false
}
func (t *activationTouch) end(id int) {
	if id != 0 {
		t.down, t.armed = false, false
		return
	}
	t.armed = t.down
	t.down = false
}
func (t *activationTouch) cancel(id int) {
	if id != 0 {
		t.down, t.armed = false, false
		return
	}
	t.down, t.armed = false, false
}
func (t *activationTouch) accept() bool {
	armed := t.armed
	t.armed = false
	return armed
}
