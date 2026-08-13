package capture

import "image"

// Recovering a cursor's shape from two renderings of it.
//
// Windows has two kinds of cursor and one rendering cannot tell them apart. A
// modern one carries an alpha channel; an older one is a pair of masks that
// invert whatever is behind them, and the drawing call leaves alpha at zero for
// those — so drawing once onto a transparent surface renders the old kind
// completely invisible.
//
// Drawing over black and over white settles it without having to know which
// kind it is. Compositing gives out = src·a + bg·(1−a), so the difference
// between the two backgrounds is 255·(1−a) and depends on nothing else: where
// they agree the pixel is opaque, where they differ by the full range it is
// transparent, and in between is a real edge. The black rendering is then
// already the colour premultiplied by coverage, which is what image.RGBA holds.
//
// Kept out of the platform file so it can be tested on any machine: this is the
// part with arithmetic in it.
func blendFromBackgrounds(onBlack, onWhite []byte, w, h int) *image.RGBA {
	if w <= 0 || h <= 0 || len(onBlack) < w*h*4 || len(onWhite) < w*h*4 {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	any := false
	for i := 0; i < w*h; i++ {
		// The source rows are BGRA, which is what a Windows DIB hands back.
		bb, bg, br := onBlack[i*4], onBlack[i*4+1], onBlack[i*4+2]
		wb, wg, wr := onWhite[i*4], onWhite[i*4+1], onWhite[i*4+2]

		// Take the largest difference of the three channels: a single channel
		// can coincide across both backgrounds and would report a pixel as more
		// opaque than it is.
		d := maxOf(int(wr)-int(br), int(wg)-int(bg), int(wb)-int(bb))
		if d < 0 {
			d = 0
		} else if d > 255 {
			d = 255
		}
		a := 255 - d
		if a == 0 {
			continue // transparent; the pixel stays zeroed
		}
		any = true
		img.Pix[i*4+0] = br
		img.Pix[i*4+1] = bg
		img.Pix[i*4+2] = bb
		img.Pix[i*4+3] = uint8(a)
	}
	if !any {
		return nil
	}
	return img
}

func maxOf(a, b, c int) int {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}
