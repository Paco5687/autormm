package capture

import (
	"image"
	"image/color"
	"testing"
)

func filled(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// The whole point of damage-driven encoding: a small change must not cause the
// encoder to examine the entire frame.
func TestTilesToScanLimitsToDirtyRegions(t *testing.T) {
	s := NewStreamer(128, 60)
	b := image.Rect(0, 0, 1280, 640) // 10x5 tiles = 50
	cols, rows := 10, 5

	all := s.tilesToScan(false, nil, cols, rows, b)
	if len(all) != cols*rows {
		t.Fatalf("nil dirty should scan every tile, got %d of %d", len(all), cols*rows)
	}
	forced := s.tilesToScan(true, []image.Rectangle{image.Rect(0, 0, 1, 1)}, cols, rows, b)
	if len(forced) != cols*rows {
		t.Fatalf("a keyframe must scan every tile, got %d", len(forced))
	}

	// One caret-sized change inside a single tile.
	one := s.tilesToScan(false, []image.Rectangle{image.Rect(300, 200, 310, 220)}, cols, rows, b)
	if len(one) != 1 {
		t.Fatalf("a change inside one tile scanned %d tiles, want 1", len(one))
	}
	if one[0] != (tileCoord{tx: 2, ty: 1}) {
		t.Errorf("wrong tile: %+v, want {2 1}", one[0])
	}
}

// Rectangles straddling a boundary must cover both tiles, and overlapping
// rectangles must not queue the same tile twice.
func TestTilesToScanSpansAndDedupes(t *testing.T) {
	s := NewStreamer(128, 60)
	b := image.Rect(0, 0, 1280, 640)

	span := s.tilesToScan(false, []image.Rectangle{image.Rect(120, 10, 140, 20)}, 10, 5, b)
	if len(span) != 2 {
		t.Errorf("a rect crossing a tile edge scanned %d tiles, want 2", len(span))
	}

	dup := s.tilesToScan(false, []image.Rectangle{
		image.Rect(10, 10, 20, 20),
		image.Rect(12, 12, 24, 24), // same tile
	}, 10, 5, b)
	if len(dup) != 1 {
		t.Errorf("overlapping rects scanned %d tiles, want 1", len(dup))
	}
}

// A backend may report a rectangle running past the frame; an out-of-range tile
// index would panic or encode garbage.
func TestTilesToScanClampsOutOfRange(t *testing.T) {
	s := NewStreamer(128, 60)
	b := image.Rect(0, 0, 256, 256) // 2x2 tiles
	got := s.tilesToScan(false, []image.Rectangle{
		image.Rect(-50, -50, 5000, 5000),
		image.Rect(9000, 9000, 9001, 9001), // entirely outside
	}, 2, 2, b)
	if len(got) != 4 {
		t.Fatalf("clamped scan covered %d tiles, want 4", len(got))
	}
	for _, c := range got {
		if c.tx < 0 || c.tx > 1 || c.ty < 0 || c.ty > 1 {
			t.Fatalf("tile index out of range: %+v", c)
		}
	}
}

// End to end: a changed region outside the reported damage must NOT be sent,
// and one inside it must be. This is what makes the optimisation correct rather
// than merely fast.
func TestEncodeHonoursDirtyRegions(t *testing.T) {
	s := NewStreamer(128, 60)
	base := filled(256, 256, color.RGBA{10, 10, 10, 255})
	if _, err := s.Encode(base, true, nil); err != nil { // keyframe primes the hashes
		t.Fatalf("keyframe: %v", err)
	}

	// Change the bottom-right tile but claim the top-left changed.
	changed := filled(256, 256, color.RGBA{10, 10, 10, 255})
	for y := 200; y < 250; y++ {
		for x := 200; x < 250; x++ {
			changed.SetRGBA(x, y, color.RGBA{250, 0, 0, 255})
		}
	}
	msgs, err := s.Encode(changed, false, []image.Rectangle{image.Rect(0, 0, 20, 20)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(msgs) != 0 {
		t.Error("encoded a region the backend never reported as damaged")
	}

	// Report it correctly and it goes out.
	msgs, err = s.Encode(changed, false, []image.Rectangle{image.Rect(200, 200, 250, 250)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(msgs) == 0 {
		t.Error("a genuinely damaged tile was not sent")
	}
}

// Damage that is reported but visually identical must not be re-sent: DXGI's
// rectangles are conservative, and re-encoding costs bandwidth for nothing.
func TestEncodeSkipsUnchangedDirtyTiles(t *testing.T) {
	s := NewStreamer(128, 60)
	img := filled(256, 256, color.RGBA{7, 7, 7, 255})
	if _, err := s.Encode(img, true, nil); err != nil {
		t.Fatalf("keyframe: %v", err)
	}
	msgs, err := s.Encode(img, false, []image.Rectangle{image.Rect(0, 0, 256, 256)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(msgs) != 0 {
		t.Error("re-sent tiles that were reported dirty but did not change")
	}
}
