package capture

import "image"

// Cursor reads the host mouse pointer so the viewer can draw it as an overlay
// (screen capture itself does not include the hardware cursor).
type Cursor interface {
	// Pos returns the pointer position in absolute screen pixels, whether it is
	// visible, and ok=false if it could not be read.
	Pos() (x, y int, visible bool, ok bool)
	// Shape identifies the pointer's current appearance.
	//
	// Separate from the image because it is read every tick and the image is
	// not: what the pointer looks like carries real information — a text caret
	// over a field, a resize arrow on an edge, and in a game a different icon
	// for every kind of target — but it changes far less often than the
	// position does. ok=false means this platform cannot report shapes, and the
	// viewer keeps its generic arrow.
	Shape() (id uint64, ok bool)
	// ShapeImage renders one shape, with the point within it that does the
	// pointing. Called once per shape the viewer has not seen.
	ShapeImage(id uint64) (img *image.RGBA, hotX, hotY int, ok bool)
	Close() error
}

// NewCursor constructs a cursor reader for this platform.
func NewCursor() (Cursor, error) { return newCursor() }
