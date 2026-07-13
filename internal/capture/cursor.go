package capture

// Cursor reads the host mouse pointer position so the viewer can draw it as an
// overlay (screen capture itself does not include the hardware cursor).
type Cursor interface {
	// Pos returns the pointer position in absolute screen pixels, whether it is
	// visible, and ok=false if it could not be read.
	Pos() (x, y int, visible bool, ok bool)
	Close() error
}

// NewCursor constructs a cursor reader for this platform.
func NewCursor() (Cursor, error) { return newCursor() }
