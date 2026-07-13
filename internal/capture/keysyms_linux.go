//go:build linux

package capture

// codeToKeysym maps a browser KeyboardEvent.code (physical key) to an X11
// keysym (unshifted/base). Modifier state is conveyed by separate Shift/Ctrl/Alt
// key events from the viewer, so only base keysyms are needed here.
var codeToKeysym = map[string]uint32{
	// letters
	"KeyA": 0x61, "KeyB": 0x62, "KeyC": 0x63, "KeyD": 0x64, "KeyE": 0x65,
	"KeyF": 0x66, "KeyG": 0x67, "KeyH": 0x68, "KeyI": 0x69, "KeyJ": 0x6a,
	"KeyK": 0x6b, "KeyL": 0x6c, "KeyM": 0x6d, "KeyN": 0x6e, "KeyO": 0x6f,
	"KeyP": 0x70, "KeyQ": 0x71, "KeyR": 0x72, "KeyS": 0x73, "KeyT": 0x74,
	"KeyU": 0x75, "KeyV": 0x76, "KeyW": 0x77, "KeyX": 0x78, "KeyY": 0x79,
	"KeyZ": 0x7a,
	// digits
	"Digit0": 0x30, "Digit1": 0x31, "Digit2": 0x32, "Digit3": 0x33, "Digit4": 0x34,
	"Digit5": 0x35, "Digit6": 0x36, "Digit7": 0x37, "Digit8": 0x38, "Digit9": 0x39,
	// whitespace / editing
	"Space": 0x20, "Enter": 0xff0d, "Tab": 0xff09, "Backspace": 0xff08,
	"Escape": 0xff1b, "Delete": 0xffff, "Insert": 0xff63,
	// navigation
	"ArrowLeft": 0xff51, "ArrowUp": 0xff52, "ArrowRight": 0xff53, "ArrowDown": 0xff54,
	"Home": 0xff50, "End": 0xff57, "PageUp": 0xff55, "PageDown": 0xff56,
	// punctuation
	"Minus": 0x2d, "Equal": 0x3d, "BracketLeft": 0x5b, "BracketRight": 0x5d,
	"Backslash": 0x5c, "Semicolon": 0x3b, "Quote": 0x27, "Backquote": 0x60,
	"Comma": 0x2c, "Period": 0x2e, "Slash": 0x2f,
	// modifiers
	"ShiftLeft": 0xffe1, "ShiftRight": 0xffe2,
	"ControlLeft": 0xffe3, "ControlRight": 0xffe4,
	"AltLeft": 0xffe9, "AltRight": 0xffea,
	"MetaLeft": 0xffeb, "MetaRight": 0xffec,
	"CapsLock": 0xffe5,
	// function keys
	"F1": 0xffbe, "F2": 0xffbf, "F3": 0xffc0, "F4": 0xffc1, "F5": 0xffc2,
	"F6": 0xffc3, "F7": 0xffc4, "F8": 0xffc5, "F9": 0xffc6, "F10": 0xffc7,
	"F11": 0xffc8, "F12": 0xffc9,
	// numpad (common)
	"NumpadEnter": 0xff8d, "NumpadAdd": 0xffab, "NumpadSubtract": 0xffad,
	"NumpadMultiply": 0xffaa, "NumpadDivide": 0xffaf, "NumpadDecimal": 0xffae,
	"Numpad0": 0xffb0, "Numpad1": 0xffb1, "Numpad2": 0xffb2, "Numpad3": 0xffb3,
	"Numpad4": 0xffb4, "Numpad5": 0xffb5, "Numpad6": 0xffb6, "Numpad7": 0xffb7,
	"Numpad8": 0xffb8, "Numpad9": 0xffb9,
}
