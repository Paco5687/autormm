package capture

import "testing"

// The context-menu key must be mapped on every platform that injects input.
//
// It matters more than it looks: on touch it is the only way to open a context
// menu for a *selection* without moving the pointer, which is the case a right
// click handles badly — moving the mouse to click can disturb what is selected.
func TestContextMenuKeyIsMapped(t *testing.T) {
	tbl := keyTable()
	if tbl == nil {
		t.Skip("no input injection on this platform")
	}
	if !tbl["ContextMenu"] {
		t.Error("ContextMenu is unmapped; the Menu key in the viewer's key strip does nothing")
	}
}

// The keys offered by the viewer's on-screen strip must all be injectable, or a
// button does nothing and there is no error anywhere to explain why.
func TestViewerStripKeysAreMapped(t *testing.T) {
	strip := []string{
		"Escape", "Tab", "ContextMenu", "Delete", "Home", "End", "PageUp", "PageDown",
		"ArrowLeft", "ArrowUp", "ArrowDown", "ArrowRight",
		"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11", "F12",
		"ControlLeft", "AltLeft", "ShiftLeft", "MetaLeft",
	}
	tbl := keyTable()
	if tbl == nil {
		t.Skip("no input injection on this platform")
	}
	for _, k := range strip {
		if !tbl[k] {
			t.Errorf("%q is offered by the viewer but not mapped on this platform", k)
		}
	}
}
