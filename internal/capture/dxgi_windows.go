//go:build windows

package capture

// DXGI Desktop Duplication capture.
//
// The GDI path (BitBlt) copies the whole screen through the CPU every frame and
// then has to diff it to find what changed — at 4K that is ~33MB of memory
// traffic per frame before any encoding happens. Desktop Duplication instead
// hands us the frame on the GPU, tells us which rectangles actually changed, and
// blocks until there is something new, so an idle desktop costs nothing.
//
// This is plain syscall COM: the agent is built CGO-free, so interfaces are
// called through their vtables by index.

import (
	"fmt"
	"image"
	"syscall"
	"unsafe"
)

var (
	d3d11              = syscall.NewLazyDLL("d3d11.dll")
	procD3D11CreateDev = d3d11.NewProc("D3D11CreateDevice")
	errDXGIUnavailable = fmt.Errorf("dxgi: desktop duplication unavailable")
	iidIDXGIDevice     = guid{0x54ec77fa, 0x1377, 0x44e6, [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
	iidIDXGIOutput1    = guid{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2D = guid{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// vtable slot indices (IUnknown occupies 0..2 in every interface).
const (
	vtRelease = 2

	vtDeviceCreateTexture2D     = 5
	vtDeviceGetImmediateContext = 40

	vtCtxMap          = 14
	vtCtxUnmap        = 15
	vtCtxCopyResource = 47

	vtDXGIDeviceGetAdapter = 7
	vtAdapterEnumOutputs   = 7
	vtOutputGetDesc        = 7
	vtOutput1DuplicateOut  = 22

	vtDuplAcquireNextFrame   = 8
	vtDuplGetFrameDirtyRects = 9
	vtDuplReleaseFrame       = 14
)

const (
	d3dDriverTypeHardware = 1
	d3d11SDKVersion       = 7

	d3d11UsageStaging  = 3
	d3d11CPUAccessRead = 0x20000
	d3d11MapRead       = 1
	dxgiFormatB8G8R8A8 = 87
	dxgiErrWaitTimeout = 0x887A0027
	dxgiErrAccessLost  = 0x887A0026
	acquireTimeoutMS   = 150
)

type rect struct{ Left, Top, Right, Bottom int32 }

type dxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates rect
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type outduplPointerPosition struct {
	Position struct{ X, Y int32 }
	Visible  int32
}

type outduplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerPosition           outduplPointerPosition
	TotalMetadataBufferSize   uint32
	PointerShapeBufferSize    uint32
}

type texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

// mappedSubresource mirrors D3D11_MAPPED_SUBRESOURCE. Data is typed as an
// unsafe.Pointer (same width as the C void*) so the driver-owned frame buffer is
// never laundered through a uintptr.
type mappedSubresource struct {
	Data       unsafe.Pointer
	RowPitch   uint32
	DepthPitch uint32
}

// call invokes the slot'th method of a COM object, passing it as the receiver.
//
// obj points at COM-owned memory, never into the Go heap, so reading its vtable
// through unsafe.Pointer is safe despite what `go vet` infers from the uintptr.
func call(obj uintptr, slot int, args ...uintptr) uintptr {
	vtbl := *(**[64]uintptr)(unsafe.Pointer(obj)) //nolint:govet // COM-owned memory
	all := append([]uintptr{obj}, args...)
	r, _, _ := syscall.SyscallN(vtbl[slot], all...)
	return r
}

func release(obj *uintptr) {
	if *obj != 0 {
		call(*obj, vtRelease, 0)
		*obj = 0
	}
}

func queryInterface(obj uintptr, iid *guid, out *uintptr) error {
	if r := call(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(out))); r != 0 {
		return fmt.Errorf("dxgi: QueryInterface: 0x%x", r)
	}
	return nil
}

// dxgiSource duplicates one output. It is created lazily on the thread that
// captures, because the device and duplication belong to the desktop they were
// made on — which for the console worker changes at lock and sign-in.
type dxgiSource struct {
	device  uintptr
	ctx     uintptr
	dupl    uintptr
	staging uintptr

	region image.Rectangle // the output this duplication covers
	buf    *image.RGBA     // persistent frame; only dirty rows are refreshed
	meta   []byte          // reusable dirty-rect metadata buffer
	held   bool            // a frame is acquired and awaiting ReleaseFrame
	// last is what changed in the most recent frame, converted to image
	// coordinates for the encoder. nil means "assume everything".
	last []image.Rectangle
}

// dirty reports the regions changed by the last grab.
func (d *dxgiSource) dirty() []image.Rectangle { return d.last }

func newFastSource() frameSource { return &dxgiSource{} }

func (d *dxgiSource) close() {
	d.releaseFrame()
	release(&d.staging)
	release(&d.dupl)
	release(&d.ctx)
	release(&d.device)
	d.buf = nil
}

func (d *dxgiSource) releaseFrame() {
	if d.held && d.dupl != 0 {
		call(d.dupl, vtDuplReleaseFrame, 0)
		d.held = false
	}
}

// grab returns the current desktop image for region, or ErrNoChange.
func (d *dxgiSource) grab(region image.Rectangle) (*image.RGBA, error) {
	if d.dupl == 0 || d.region != region {
		d.close()
		if err := d.start(region); err != nil {
			return nil, err
		}
	}

	var info outduplFrameInfo
	var resource uintptr
	r := call(d.dupl, vtDuplAcquireNextFrame, acquireTimeoutMS,
		uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&resource)))
	switch uint32(r) {
	case 0:
	case dxgiErrWaitTimeout:
		return nil, ErrNoChange
	case dxgiErrAccessLost:
		// Mode change, desktop switch, or a full-screen app took over. Rebuild
		// on the next call rather than failing the session.
		d.close()
		return nil, ErrNoChange
	default:
		return nil, fmt.Errorf("dxgi: AcquireNextFrame: 0x%x", uint32(r))
	}
	d.held = true
	defer d.releaseFrame()

	// A frame with no accumulated updates is a pointer-only change; the cursor is
	// streamed separately, so there is nothing new to encode.
	if info.AccumulatedFrames == 0 || info.LastPresentTime == 0 {
		release(&resource)
		d.last = nil
		return nil, ErrNoChange
	}

	var tex uintptr
	if err := queryInterface(resource, &iidID3D11Texture2D, &tex); err != nil {
		release(&resource)
		return nil, err
	}
	release(&resource)
	call(d.ctx, vtCtxCopyResource, d.staging, tex)
	release(&tex)

	var mapped mappedSubresource
	if r := call(d.ctx, vtCtxMap, d.staging, 0, d3d11MapRead, 0, uintptr(unsafe.Pointer(&mapped))); r != 0 {
		return nil, fmt.Errorf("dxgi: Map: 0x%x", uint32(r))
	}
	rects := d.dirtyRects(info)
	// convert fills the whole buffer when it has just been (re)created — after a
	// mode change or ACCESS_LOST. Report "everything" then, or the encoder would
	// send only the driver's rectangles and leave the rest of the viewer's image
	// stale until the next keyframe.
	rebuilt := d.buf == nil
	d.convert(mapped, rects)
	call(d.ctx, vtCtxUnmap, d.staging, 0)
	if rebuilt {
		d.last = nil
	} else {
		d.last = toImageRects(rects)
	}
	return d.buf, nil
}

// dirtyRects returns the rectangles that changed this frame, or nil to mean
// "assume everything" (first frame, or metadata unavailable).
func (d *dxgiSource) dirtyRects(info outduplFrameInfo) []rect {
	if info.TotalMetadataBufferSize == 0 {
		return nil
	}
	if cap(d.meta) < int(info.TotalMetadataBufferSize) {
		d.meta = make([]byte, info.TotalMetadataBufferSize)
	}
	d.meta = d.meta[:cap(d.meta)]
	var used uint32
	r := call(d.dupl, vtDuplGetFrameDirtyRects, uintptr(len(d.meta)),
		uintptr(unsafe.Pointer(&d.meta[0])), uintptr(unsafe.Pointer(&used)))
	if r != 0 || used < uint32(unsafe.Sizeof(rect{})) {
		return nil
	}
	n := int(used) / int(unsafe.Sizeof(rect{}))
	return unsafe.Slice((*rect)(unsafe.Pointer(&d.meta[0])), n)
}

// toImageRects converts DXGI rectangles to image.Rectangle. A nil result means
// the driver gave no metadata, which the encoder reads as "assume everything".
func toImageRects(rs []rect) []image.Rectangle {
	if rs == nil {
		return nil
	}
	out := make([]image.Rectangle, 0, len(rs))
	for _, r := range rs {
		out = append(out, image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)))
	}
	return out
}

// convert copies BGRA rows out of the mapped staging texture into the persistent
// RGBA buffer, touching only the rectangles that changed. Restricting the swizzle
// to dirty regions is most of the win: a typing cursor repaints a few hundred
// pixels instead of eight million.
func (d *dxgiSource) convert(m mappedSubresource, dirty []rect) {
	w, h := d.region.Dx(), d.region.Dy()
	if d.buf == nil {
		d.buf = image.NewRGBA(image.Rect(0, 0, w, h))
		dirty = nil // first frame: fill everything
	}
	if dirty == nil {
		dirty = []rect{{0, 0, int32(w), int32(h)}}
	}
	// m.Data is the mapped staging texture, owned by the driver between Map and
	// Unmap — not Go heap memory the collector could move.
	src := unsafe.Slice((*byte)(m.Data), int(m.RowPitch)*h)
	for _, rc := range dirty {
		x0, y0 := int(rc.Left), int(rc.Top)
		x1, y1 := int(rc.Right), int(rc.Bottom)
		if x0 < 0 {
			x0 = 0
		}
		if y0 < 0 {
			y0 = 0
		}
		if x1 > w {
			x1 = w
		}
		if y1 > h {
			y1 = h
		}
		for y := y0; y < y1; y++ {
			s := src[y*int(m.RowPitch)+x0*4 : y*int(m.RowPitch)+x1*4]
			dst := d.buf.Pix[y*d.buf.Stride+x0*4 : y*d.buf.Stride+x1*4]
			// BGRA -> RGBA
			for i := 0; i+3 < len(s); i += 4 {
				dst[i+0] = s[i+2]
				dst[i+1] = s[i+1]
				dst[i+2] = s[i+0]
				dst[i+3] = 0xff
			}
		}
	}
}

// start builds the D3D11 device and duplicates the output whose desktop
// coordinates match region.
func (d *dxgiSource) start(region image.Rectangle) error {
	if region.Empty() {
		return errDXGIUnavailable
	}
	var device, ctx uintptr
	var level uint32
	r, _, _ := procD3D11CreateDev.Call(0, d3dDriverTypeHardware, 0, 0, 0, 0,
		d3d11SDKVersion, uintptr(unsafe.Pointer(&device)),
		uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&ctx)))
	if r != 0 {
		return fmt.Errorf("dxgi: D3D11CreateDevice: 0x%x", uint32(r))
	}
	d.device, d.ctx = device, ctx

	var dxgiDev, adapter uintptr
	if err := queryInterface(device, &iidIDXGIDevice, &dxgiDev); err != nil {
		d.close()
		return err
	}
	if r := call(dxgiDev, vtDXGIDeviceGetAdapter, uintptr(unsafe.Pointer(&adapter))); r != 0 {
		release(&dxgiDev)
		d.close()
		return fmt.Errorf("dxgi: GetAdapter: 0x%x", uint32(r))
	}
	release(&dxgiDev)
	defer release(&adapter)

	// Outputs are not enumerated in the same order as the display list, so match
	// on desktop coordinates instead of trusting the index.
	var output uintptr
	for i := uintptr(0); ; i++ {
		var candidate uintptr
		if r := call(adapter, vtAdapterEnumOutputs, i, uintptr(unsafe.Pointer(&candidate))); r != 0 {
			break
		}
		var desc dxgiOutputDesc
		if r := call(candidate, vtOutputGetDesc, uintptr(unsafe.Pointer(&desc))); r == 0 {
			c := desc.DesktopCoordinates
			if int(c.Left) == region.Min.X && int(c.Top) == region.Min.Y &&
				int(c.Right) == region.Max.X && int(c.Bottom) == region.Max.Y {
				output = candidate
				break
			}
		}
		release(&candidate)
	}
	if output == 0 {
		d.close()
		return errDXGIUnavailable // region spans several outputs, or none matched
	}
	defer release(&output)

	var output1 uintptr
	if err := queryInterface(output, &iidIDXGIOutput1, &output1); err != nil {
		d.close()
		return err
	}
	defer release(&output1)

	var dupl uintptr
	if r := call(output1, vtOutput1DuplicateOut, device, uintptr(unsafe.Pointer(&dupl))); r != 0 {
		d.close()
		// E_ACCESSDENIED here usually means another duplication already owns the
		// output, or we are on a desktop we may not duplicate.
		return fmt.Errorf("dxgi: DuplicateOutput: 0x%x", uint32(r))
	}
	d.dupl = dupl

	desc := texture2DDesc{
		Width: uint32(region.Dx()), Height: uint32(region.Dy()),
		MipLevels: 1, ArraySize: 1, Format: dxgiFormatB8G8R8A8,
		SampleCount: 1, Usage: d3d11UsageStaging, CPUAccessFlags: d3d11CPUAccessRead,
	}
	var staging uintptr
	if r := call(device, vtDeviceCreateTexture2D, uintptr(unsafe.Pointer(&desc)), 0,
		uintptr(unsafe.Pointer(&staging))); r != 0 {
		d.close()
		return fmt.Errorf("dxgi: CreateTexture2D: 0x%x", uint32(r))
	}
	d.staging = staging
	d.region = region
	d.buf = nil
	return nil
}
