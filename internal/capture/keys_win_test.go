//go:build windows

package capture

import "testing"

// Every key we mark extended must actually be a key we can send, or the flag is
// silently dead code.
func TestExtendedKeysAreMapped(t *testing.T) {
	for code := range extendedKeys {
		if _, ok := codeToVK[code]; !ok {
			t.Errorf("%q is marked extended but has no virtual-key mapping", code)
		}
	}
}

// The modifiers the viewer releases on focus loss must all be injectable, or a
// stuck Shift could never be cleared.
func TestModifiersAreMapped(t *testing.T) {
	for _, code := range []string{
		"ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight",
		"AltLeft", "AltRight", "MetaLeft", "MetaRight",
	} {
		if _, ok := codeToVK[code]; !ok {
			t.Errorf("modifier %q has no virtual-key mapping", code)
		}
	}
}

// The main Enter key must NOT be flagged extended (that is NumpadEnter), or
// Shift+Enter and plain Enter land as keypad input in some apps.
func TestMainEnterIsNotExtended(t *testing.T) {
	if extendedKeys["Enter"] {
		t.Error(`"Enter" must not be extended; only "NumpadEnter" is`)
	}
	if !extendedKeys["NumpadEnter"] {
		t.Error(`"NumpadEnter" should be extended`)
	}
}
