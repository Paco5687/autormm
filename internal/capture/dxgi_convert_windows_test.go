//go:build windows

package capture

import (
	"image"
	"testing"
	"unsafe"
)

// convert must swizzle BGRA to RGBA and touch only the dirty rectangles: the
// whole point of Desktop Duplication is not re-processing eight million pixels
// when a caret blinks.
func TestConvertOnlyTouchesDirtyRects(t *testing.T) {
	const w, h = 8, 4
	// Staging texture rows are padded, so exercise a pitch wider than the image.
	pitch := w*4 + 16
	src := make([]byte, pitch*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := y*pitch + x*4
			src[p+0], src[p+1], src[p+2], src[p+3] = 0x11, 0x22, 0x33, 0x44 // B,G,R,A
		}
	}
	d := &dxgiSource{region: image.Rect(0, 0, w, h)}
	d.buf = image.NewRGBA(image.Rect(0, 0, w, h))
	m := mappedSubresource{Data: unsafe.Pointer(&src[0]), RowPitch: uint32(pitch)}

	d.convert(m, []rect{{Left: 2, Top: 1, Right: 4, Bottom: 3}})

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*d.buf.Stride + x*4
			got := [4]byte{d.buf.Pix[i], d.buf.Pix[i+1], d.buf.Pix[i+2], d.buf.Pix[i+3]}
			inDirty := x >= 2 && x < 4 && y >= 1 && y < 3
			if inDirty {
				// R and B swapped from the BGRA source, alpha forced opaque.
				if got != [4]byte{0x33, 0x22, 0x11, 0xff} {
					t.Fatalf("dirty pixel (%d,%d) = %v, want RGBA{33 22 11 ff}", x, y, got)
				}
			} else if got != [4]byte{0, 0, 0, 0} {
				t.Fatalf("clean pixel (%d,%d) was rewritten: %v", x, y, got)
			}
		}
	}
}

// A nil dirty list means "assume everything changed" (first frame, or the
// driver gave us no metadata).
func TestConvertNilDirtyFillsFrame(t *testing.T) {
	const w, h = 4, 2
	pitch := w * 4
	src := make([]byte, pitch*h)
	for i := 0; i+3 < len(src); i += 4 {
		src[i+0], src[i+1], src[i+2] = 0x01, 0x02, 0x03
	}
	d := &dxgiSource{region: image.Rect(0, 0, w, h)}
	d.buf = image.NewRGBA(image.Rect(0, 0, w, h))
	m := mappedSubresource{Data: unsafe.Pointer(&src[0]), RowPitch: uint32(pitch)}

	d.convert(m, nil)

	for i := 0; i < w*h; i++ {
		p := i * 4
		if d.buf.Pix[p] != 0x03 || d.buf.Pix[p+1] != 0x02 || d.buf.Pix[p+2] != 0x01 || d.buf.Pix[p+3] != 0xff {
			t.Fatalf("pixel %d not filled: %v", i, d.buf.Pix[p:p+4])
		}
	}
}

// Rectangles reported outside the frame must be clamped, not panic.
func TestConvertClampsOutOfRangeRects(t *testing.T) {
	const w, h = 4, 2
	pitch := w * 4
	src := make([]byte, pitch*h)
	d := &dxgiSource{region: image.Rect(0, 0, w, h)}
	d.buf = image.NewRGBA(image.Rect(0, 0, w, h))
	m := mappedSubresource{Data: unsafe.Pointer(&src[0]), RowPitch: uint32(pitch)}

	d.convert(m, []rect{{Left: -5, Top: -5, Right: 999, Bottom: 999}}) // must not panic
}
