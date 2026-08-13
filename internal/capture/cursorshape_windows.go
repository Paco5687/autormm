//go:build windows

package capture

import (
	"image"
	"syscall"
	"unsafe"
)

// Reading what the pointer looks like.
//
// The shape carries real information — a caret over a text field, a resize
// arrow on a window edge, and in a game a different icon for every kind of
// thing you can point at. Screen capture never includes it: the cursor is
// composited by the display hardware, not drawn into the framebuffer, which is
// why a remote desktop has to send it separately.

var (
	gdi32                  = syscall.NewLazyDLL("gdi32.dll")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procPatBlt             = gdi32.NewProc("PatBlt")
	procCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	procGetObjectW         = gdi32.NewProc("GetObjectW")

	procGetIconInfo = user32.NewProc("GetIconInfo")
	procDrawIconEx  = user32.NewProc("DrawIconEx")
	procGetDC       = user32.NewProc("GetDC")
	procReleaseDC   = user32.NewProc("ReleaseDC")
)

const (
	diNormal      = 0x0003
	biRGB         = 0
	dibRGBColors  = 0
	patCopy       = 0x00F00021
	objBitmapSize = 32 // sizeof(BITMAP) is larger; we read the fields we need
)

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

type bitmapStruct struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapInfo struct {
	header bitmapInfoHeader
	colors [3]uint32
}

// Shape identifies the pointer by its handle. Windows reuses the handle for a
// given loaded cursor, so it doubles as a cache key: the viewer is told the
// image once and thereafter only the id.
func (winCursor) Shape() (uint64, bool) {
	var ci cursorInfo
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci)))
	if r == 0 || ci.hCursor == 0 {
		return 0, false
	}
	return uint64(ci.hCursor), true
}

// ShapeImage renders a cursor handle into pixels.
//
// Drawn twice, on black and on white, because Windows has two kinds of cursor
// and one pass cannot tell them apart. A modern cursor carries an alpha
// channel; an older one is a pair of masks that invert the background, and
// DrawIconEx leaves alpha at zero for those — so a single pass onto a
// transparent surface renders the old kind completely invisible. Two passes
// recover the coverage from the difference between the backgrounds, which works
// for both: where the two agree the pixel is opaque, and where they differ by
// the full range it is transparent.
func (winCursor) ShapeImage(id uint64) (*image.RGBA, int, int, bool) {
	if id == 0 {
		return nil, 0, 0, false
	}
	h := uintptr(id)
	var ii iconInfo
	if r, _, _ := procGetIconInfo.Call(h, uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil, 0, 0, false
	}
	// GetIconInfo hands back bitmap copies that belong to the caller.
	defer func() {
		if ii.hbmMask != 0 {
			procDeleteObject.Call(ii.hbmMask)
		}
		if ii.hbmColor != 0 {
			procDeleteObject.Call(ii.hbmColor)
		}
	}()

	w, hgt, ok := cursorSize(ii)
	if !ok || w <= 0 || hgt <= 0 || w > 256 || hgt > 256 {
		return nil, 0, 0, false
	}

	onBlack, ok1 := drawCursorOn(h, w, hgt, 0x000000)
	onWhite, ok2 := drawCursorOn(h, w, hgt, 0xFFFFFF)
	if !ok1 || !ok2 {
		return nil, 0, 0, false
	}

	img := blendFromBackgrounds(onBlack, onWhite, w, hgt)
	if img == nil {
		return nil, 0, 0, false
	}
	return img, int(ii.xHotspot), int(ii.yHotspot), true
}

// cursorSize reads the dimensions from whichever bitmap the cursor has. A
// monochrome cursor has no colour bitmap and stores its AND and XOR masks
// stacked in one mask bitmap, so that one is twice as tall as the cursor.
func cursorSize(ii iconInfo) (int, int, bool) {
	var bm bitmapStruct
	src, mono := ii.hbmColor, false
	if src == 0 {
		src, mono = ii.hbmMask, true
	}
	if src == 0 {
		return 0, 0, false
	}
	r, _, _ := procGetObjectW.Call(src, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm)))
	if r == 0 {
		return 0, 0, false
	}
	h := int(bm.bmHeight)
	if mono {
		h /= 2
	}
	return int(bm.bmWidth), h, true
}

// drawCursorOn renders the cursor over a solid background and returns the raw
// BGRA bytes.
func drawCursorOn(hCursor uintptr, w, h int, bgr uint32) ([]byte, bool) {
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, false
	}
	defer procReleaseDC.Call(0, screen)
	dc, _, _ := procCreateCompatibleDC.Call(screen)
	if dc == 0 {
		return nil, false
	}
	defer procDeleteDC.Call(dc)

	bi := bitmapInfo{header: bitmapInfoHeader{
		biSize:  uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth: int32(w),
		// Negative height asks for a top-down DIB, so row 0 is the top and the
		// pixels come out in the order an image expects.
		biHeight:      int32(-h),
		biPlanes:      1,
		biBitCount:    32,
		biCompression: biRGB,
	}}
	var bits uintptr
	dib, _, _ := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == 0 {
		return nil, false
	}
	defer procDeleteObject.Call(dib)
	old, _, _ := procSelectObject.Call(dc, dib)
	defer procSelectObject.Call(dc, old)

	brush, _, _ := procCreateSolidBrush.Call(uintptr(bgr))
	if brush != 0 {
		oldBrush, _, _ := procSelectObject.Call(dc, brush)
		procPatBlt.Call(dc, 0, 0, uintptr(w), uintptr(h), patCopy)
		procSelectObject.Call(dc, oldBrush)
		procDeleteObject.Call(brush)
	}
	if r, _, _ := procDrawIconEx.Call(dc, 0, 0, hCursor, uintptr(w), uintptr(h), 0, 0, diNormal); r == 0 {
		return nil, false
	}

	// Copy out: the DIB's memory belongs to the bitmap and dies with it.
	out := make([]byte, w*h*4)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(bits)), w*h*4))
	return out, true
}
