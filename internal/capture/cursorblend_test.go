package capture

import "testing"

// bgra builds the two renderings a cursor pixel would produce, given its true
// colour and coverage. This is the compositing the OS does; the code under test
// has to invert it.
func composite(r, g, b uint8, a int, bg uint8) [4]byte {
	f := func(c uint8) uint8 {
		return uint8((int(c)*a + int(bg)*(255-a) + 127) / 255)
	}
	return [4]byte{f(b), f(g), f(r), 0} // BGRA, alpha deliberately left at zero
}

func pair(px [][4]int) (black, white []byte) {
	for _, p := range px {
		b := composite(uint8(p[0]), uint8(p[1]), uint8(p[2]), p[3], 0x00)
		w := composite(uint8(p[0]), uint8(p[1]), uint8(p[2]), p[3], 0xFF)
		black = append(black, b[:]...)
		white = append(white, w[:]...)
	}
	return black, white
}

// The old kind of cursor is opaque where it draws and fully transparent
// elsewhere, and the drawing call reports no alpha at all — so a single
// rendering onto a transparent surface would produce nothing.
func TestOpaqueAndTransparentPixelsAreRecovered(t *testing.T) {
	black, white := pair([][4]int{
		{255, 255, 255, 255}, // opaque white — the body of an arrow
		{0, 0, 0, 255},       // opaque black — its outline
		{9, 9, 9, 0},         // transparent — colour here means nothing
		{255, 0, 0, 255},     // opaque red
	})
	img := blendFromBackgrounds(black, white, 4, 1)
	if img == nil {
		t.Fatal("nothing recovered")
	}
	for i, want := range [][4]uint8{
		{255, 255, 255, 255},
		{0, 0, 0, 255},
		{0, 0, 0, 0},
		{255, 0, 0, 255},
	} {
		got := [4]uint8{img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3]}
		if got != want {
			t.Errorf("pixel %d = %v, want %v", i, got, want)
		}
	}
}

// A modern cursor has soft edges, and those have to survive as partial coverage
// rather than being rounded to on or off — an antialiased outline snapped to
// opaque is a cursor with a black halo.
func TestPartialCoverageSurvives(t *testing.T) {
	black, white := pair([][4]int{
		{255, 255, 255, 128},
		{255, 255, 255, 64},
		{0, 0, 0, 200},
	})
	img := blendFromBackgrounds(black, white, 3, 1)
	if img == nil {
		t.Fatal("nothing recovered")
	}
	for i, want := range []int{128, 64, 200} {
		got := int(img.Pix[i*4+3])
		if got < want-2 || got > want+2 {
			t.Errorf("pixel %d alpha = %d, want about %d", i, got, want)
		}
	}
	// Stored premultiplied, which is what image.RGBA means by these bytes: a
	// half-covered white pixel is half-bright, not white with a flag on it.
	if r := img.Pix[0]; r < 120 || r > 136 {
		t.Errorf("premultiplied red = %d, want about 128", r)
	}
}

// A cursor whose colour happens to match one background on one channel must not
// read as more opaque than it is. This is why the largest of the three channel
// differences is taken rather than any single one.
func TestAChannelMatchingTheBackgroundDoesNotFakeOpacity(t *testing.T) {
	// Pure blue at half coverage: on a black background the red and green
	// channels are zero either way, so those two alone would claim full
	// coverage.
	black, white := pair([][4]int{{0, 0, 255, 128}})
	img := blendFromBackgrounds(black, white, 1, 1)
	if img == nil {
		t.Fatal("nothing recovered")
	}
	if a := int(img.Pix[3]); a < 126 || a > 130 {
		t.Errorf("alpha = %d, want about 128", a)
	}
}

// A cursor that came back empty is not a cursor, and drawing nothing is better
// than drawing an invisible box where the pointer is.
func TestFullyTransparentYieldsNothing(t *testing.T) {
	black, white := pair([][4]int{{0, 0, 0, 0}, {255, 255, 255, 0}})
	if img := blendFromBackgrounds(black, white, 2, 1); img != nil {
		t.Errorf("an empty cursor produced an image: %v", img.Pix)
	}
}

// Short or mismatched buffers are a failed render, not something to index into.
func TestShortBuffersAreRejected(t *testing.T) {
	for _, c := range []struct {
		name   string
		w, h   int
		nb, nw int
	}{
		{"black short", 4, 4, 8, 64},
		{"white short", 4, 4, 64, 8},
		{"zero width", 0, 4, 64, 64},
		{"negative height", 4, -1, 64, 64},
	} {
		if img := blendFromBackgrounds(make([]byte, c.nb), make([]byte, c.nw), c.w, c.h); img != nil {
			t.Errorf("%s: expected no image", c.name)
		}
	}
}
