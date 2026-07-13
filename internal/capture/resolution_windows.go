//go:build windows

package capture

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/kbinani/screenshot"

	"github.com/Paco5687/autormm/internal/protocol"
)

var (
	procEnumDisplayDevicesW      = user32.NewProc("EnumDisplayDevicesW")
	procEnumDisplaySettingsW     = user32.NewProc("EnumDisplaySettingsW")
	procChangeDisplaySettingsExW = user32.NewProc("ChangeDisplaySettingsExW")
)

const (
	enumCurrentSettings            = 0xFFFFFFFF
	dmPelsWidthFlag                = 0x00080000
	dmPelsHeightFlag               = 0x00100000
	cdsUpdateRegistry              = 0x00000001
	dispChangeSuccessful           = 0
	displayDeviceAttachedToDesktop = 0x00000001
)

type displayDeviceW struct {
	cb           uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

// devModeW mirrors the Win32 DEVMODEW layout (Unicode). Only the fields up to
// dmPelsHeight matter for our read/write; the rest keep the struct the right size.
type devModeW struct {
	dmDeviceName         [32]uint16
	dmSpecVersion        uint16
	dmDriverVersion      uint16
	dmSize               uint16
	dmDriverExtra        uint16
	dmFields             uint32
	dmPositionX          int32
	dmPositionY          int32
	dmDisplayOrientation uint32
	dmDisplayFixedOutput uint32
	dmColor              int16
	dmDuplex             int16
	dmYResolution        int16
	dmTTOption           int16
	dmCollate            int16
	dmFormName           [32]uint16
	dmLogPixels          uint16
	dmBitsPerPel         uint32
	dmPelsWidth          uint32
	dmPelsHeight         uint32
	dmDisplayFlags       uint32
	dmDisplayFrequency   uint32
	dmICMMethod          uint32
	dmICMIntent          uint32
	dmMediaType          uint32
	dmDitherType         uint32
	dmReserved1          uint32
	dmReserved2          uint32
	dmPanningWidth       uint32
	dmPanningHeight      uint32
}

// deviceForDisplay maps a screenshot display index to its \\.\DISPLAYn device
// name by matching the current position of each attached adapter — robust to
// enumeration-order differences.
func deviceForDisplay(index int) (string, bool) {
	b := screenshot.GetDisplayBounds(index)
	var i uint32
	for {
		var dd displayDeviceW
		dd.cb = uint32(unsafe.Sizeof(dd))
		r, _, _ := procEnumDisplayDevicesW.Call(0, uintptr(i), uintptr(unsafe.Pointer(&dd)), 0)
		if r == 0 {
			break
		}
		i++
		if dd.StateFlags&displayDeviceAttachedToDesktop == 0 {
			continue
		}
		var dm devModeW
		dm.dmSize = uint16(unsafe.Sizeof(dm))
		if r2, _, _ := procEnumDisplaySettingsW.Call(uintptr(unsafe.Pointer(&dd.DeviceName[0])), enumCurrentSettings, uintptr(unsafe.Pointer(&dm))); r2 == 0 {
			continue
		}
		if int(dm.dmPositionX) == b.Min.X && int(dm.dmPositionY) == b.Min.Y {
			return utf16ToString(dd.DeviceName[:]), true
		}
	}
	return "", false
}

func displayModes(index int) []protocol.Mode {
	name, ok := deviceForDisplay(index)
	if !ok {
		return nil
	}
	namePtr, _ := syscall.UTF16PtrFromString(name)
	var modes []protocol.Mode
	for iMode := uint32(0); ; iMode++ {
		var dm devModeW
		dm.dmSize = uint16(unsafe.Sizeof(dm))
		if r, _, _ := procEnumDisplaySettingsW.Call(uintptr(unsafe.Pointer(namePtr)), uintptr(iMode), uintptr(unsafe.Pointer(&dm))); r == 0 {
			break
		}
		modes = append(modes, protocol.Mode{W: int(dm.dmPelsWidth), H: int(dm.dmPelsHeight)})
	}
	return dedupeSortModes(modes)
}

func setDisplayMode(index, w, h int) error {
	name, ok := deviceForDisplay(index)
	if !ok {
		return errUnsupportedRes
	}
	namePtr, _ := syscall.UTF16PtrFromString(name)
	// Reuse an enumerated mode's fully-populated DEVMODE, then flip only the
	// resolution fields — avoids hand-constructing the whole struct.
	for iMode := uint32(0); ; iMode++ {
		var dm devModeW
		dm.dmSize = uint16(unsafe.Sizeof(dm))
		if r, _, _ := procEnumDisplaySettingsW.Call(uintptr(unsafe.Pointer(namePtr)), uintptr(iMode), uintptr(unsafe.Pointer(&dm))); r == 0 {
			break
		}
		if int(dm.dmPelsWidth) != w || int(dm.dmPelsHeight) != h {
			continue
		}
		dm.dmFields = dmPelsWidthFlag | dmPelsHeightFlag
		rc, _, _ := procChangeDisplaySettingsExW.Call(uintptr(unsafe.Pointer(namePtr)), uintptr(unsafe.Pointer(&dm)), 0, cdsUpdateRegistry, 0)
		if int32(rc) == dispChangeSuccessful {
			return nil
		}
		return fmt.Errorf("ChangeDisplaySettingsEx failed (code %d)", int32(rc))
	}
	return fmt.Errorf("no %dx%d mode for display %d", w, h, index)
}

func utf16ToString(u []uint16) string {
	for i, c := range u {
		if c == 0 {
			return string(utf16.Decode(u[:i]))
		}
	}
	return string(utf16.Decode(u))
}
