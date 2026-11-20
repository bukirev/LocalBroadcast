//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

type captureRect struct{ Left, Top, Right, Bottom int32 }
type captureMonitorInfo struct {
	Size         uint32
	Bounds, Work captureRect
	Flags        uint32
	Device       [32]uint16
}

var captureUser32 = syscall.NewLazyDLL("user32.dll")
var captureMonitorInfoProc = captureUser32.NewProc("GetMonitorInfoW")
var captureMonitorCallback = syscall.NewCallback(func(monitor, dc, rect, data uintptr) uintptr {
	info := captureMonitorInfo{Size: uint32(unsafe.Sizeof(captureMonitorInfo{}))}
	if ok, _, _ := captureMonitorInfoProc.Call(monitor, uintptr(unsafe.Pointer(&info))); ok != 0 {
		out := (*[]CaptureDisplay)(unsafe.Pointer(data))
		id := syscall.UTF16ToString(info.Device[:])
		w, h := int(info.Bounds.Right-info.Bounds.Left), int(info.Bounds.Bottom-info.Bounds.Top)
		name := fmt.Sprintf("Экран %s · %d × %d", strings.TrimPrefix(id, `\\.\DISPLAY`), w, h)
		if info.Flags&1 != 0 {
			name += " · основной"
		}
		*out = append(*out, CaptureDisplay{handle: monitor, ID: id, Name: name, Left: int(info.Bounds.Left), Top: int(info.Bounds.Top), Width: w, Height: h, Primary: info.Flags&1 != 0})
	}
	return 1
})

func captureDisplays() ([]CaptureDisplay, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// gdigrab consumes physical pixel coordinates even with mixed DPI scaling.
	dpi := captureUser32.NewProc("SetThreadDpiAwarenessContext")
	if dpi.Find() == nil {
		old, _, _ := dpi.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
		if old != 0 {
			defer dpi.Call(old)
		}
	}
	out := []CaptureDisplay{}
	ok, _, err := captureUser32.NewProc("EnumDisplayMonitors").Call(0, 0, captureMonitorCallback, uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(&out)
	if ok == 0 {
		return nil, fmt.Errorf("список экранов: %v", err)
	}
	return out, nil
}

func captureUTF16String(p uintptr) string {
	if p == 0 {
		return ""
	}
	s := make([]uint16, 0, 128)
	for i := uintptr(0); i < 32768; i++ {
		v := *(*uint16)(unsafe.Pointer(p + 2*i))
		if v == 0 {
			break
		}
		s = append(s, v)
	}
	return syscall.UTF16ToString(s)
}

func captureDeviceID(device uintptr) (string, error) {
	var p uintptr
	if err := callCOM(device, 5, uintptr(unsafe.Pointer(&p))); err != nil {
		return "", err
	}
	defer coFree.Call(p)
	return captureUTF16String(p), nil
}

func captureDeviceName(device uintptr) string {
	var store uintptr
	if callCOM(device, 4, 0, uintptr(unsafe.Pointer(&store))) != nil {
		return ""
	}
	defer releaseCOM(store)
	key := struct {
		ID  comGUID
		PID uint32
	}{comGUID{0xa45c254e, 0xdf1c, 0x4efd, [8]byte{0x80, 0x20, 0x67, 0xd1, 0x46, 0xa8, 0x50, 0xe0}}, 14}
	value := struct {
		Type         uint16
		Reserved     [3]uint16
		Value, Extra uintptr
	}{}
	if callCOM(store, 5, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&value))) != nil {
		return ""
	}
	defer ole32.NewProc("PropVariantClear").Call(uintptr(unsafe.Pointer(&value)))
	if value.Type == 31 {
		return captureUTF16String(value.Value)
	}
	return ""
}

func captureAudioOutputs() ([]CaptureAudioOutput, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := coInit.Call(0, 0)
	if int32(hr) < 0 {
		return nil, fmt.Errorf("COM initialization: 0x%08x", uint32(hr))
	}
	defer coUninit.Call()
	var enumerator, collection, def uintptr
	hr, _, _ = coCreate.Call(uintptr(unsafe.Pointer(&clsidEnumerator)), 0, 23, uintptr(unsafe.Pointer(&iidEnumerator)), uintptr(unsafe.Pointer(&enumerator)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("audio enumerator: 0x%08x", uint32(hr))
	}
	defer releaseCOM(enumerator)
	defaultID := ""
	if callCOM(enumerator, 4, 0, 0, uintptr(unsafe.Pointer(&def))) == nil {
		defaultID, _ = captureDeviceID(def)
		releaseCOM(def)
	}
	if err := callCOM(enumerator, 3, 0, 1, uintptr(unsafe.Pointer(&collection))); err != nil {
		return nil, err
	}
	defer releaseCOM(collection)
	var count uint32
	if err := callCOM(collection, 3, uintptr(unsafe.Pointer(&count))); err != nil {
		return nil, err
	}
	out := []CaptureAudioOutput{}
	for i := uint32(0); i < count; i++ {
		var device uintptr
		if callCOM(collection, 4, uintptr(i), uintptr(unsafe.Pointer(&device))) != nil {
			continue
		}
		id, err := captureDeviceID(device)
		name := captureDeviceName(device)
		releaseCOM(device)
		if err != nil {
			continue
		}
		if name == "" {
			name = id
		}
		out = append(out, CaptureAudioOutput{ID: id, Name: name, Default: id == defaultID})
	}
	return out, nil
}
