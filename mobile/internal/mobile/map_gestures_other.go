//go:build !android

package mobile

import "fyne.io/fyne/v2"

// Desktop uses pointer drags. Android must not implement Draggable: the pinned
// Fyne driver suppresses TouchUp after a drag and adds inertial drag callbacks.
// Android instead uses only Movable's explicit finger IDs and TouchUp/Cancel.
func (m *mapInteraction) Dragged(e *fyne.DragEvent) {
	if len(m.fingers) == 0 {
		m.pan(e.Dragged.DX, e.Dragged.DY)
	}
}
func (m *mapInteraction) DragEnd() { clear(m.fingers) }
