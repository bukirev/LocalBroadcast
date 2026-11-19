//go:build windows

package main

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func updateProgressWindow(work func(func(string)) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instance, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	var window, label, button uintptr
	var mu sync.Mutex
	status := "Подготовка обновления…"
	finished := false
	var result error
	var completed time.Time
	procedure := syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
		switch msg {
		case 0x10, 0x111:
			mu.Lock()
			done := finished
			mu.Unlock()
			if done {
				desktopDestroyWindow.Call(hwnd)
			}
			return 0
		case 0x113:
			mu.Lock()
			text := status
			done := finished
			ok := result == nil
			at := completed
			mu.Unlock()
			desktopSetText.Call(label, uintptr(unsafe.Pointer(desktopString(text))))
			if done {
				desktopUser32.NewProc("EnableWindow").Call(button, 1)
				if ok && time.Since(at) > 3*time.Second {
					desktopDestroyWindow.Call(hwnd)
				}
			}
			return 0
		case 2:
			desktopUser32.NewProc("PostQuitMessage").Call(0)
			return 0
		}
		r, _, _ := desktopDefWindowProc.Call(hwnd, uintptr(msg), wp, lp)
		return r
	})
	name := desktopString("LocalBroadcastUpdateProgress")
	class := desktopWindowClassEx{Procedure: procedure, Instance: instance, Background: 6, ClassName: uintptr(unsafe.Pointer(name))}
	class.Size = uint32(unsafe.Sizeof(class))
	if r, _, err := desktopUser32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class))); r == 0 {
		return fmt.Errorf("окно обновления: %w", err)
	}
	defer desktopUser32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(name)), instance)
	window, _, _ = desktopCreateWindow.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(desktopString("Local Broadcast — обновление"))), 0x00c80000, 0x80000000, 0x80000000, 620, 245, 0, 0, instance, 0)
	if window == 0 {
		return fmt.Errorf("не удалось создать окно обновления")
	}
	font, _, _ := syscall.NewLazyDLL("gdi32.dll").NewProc("GetStockObject").Call(17)
	control := func(class, text string, x, y, w, h int, id uintptr) uintptr {
		r, _, _ := desktopCreateWindow.Call(0, uintptr(unsafe.Pointer(desktopString(class))), uintptr(unsafe.Pointer(desktopString(text))), 0x50000000, uintptr(x), uintptr(y), uintptr(w), uintptr(h), window, id, instance, 0)
		desktopUser32.NewProc("SendMessageW").Call(r, 0x30, font, 1)
		return r
	}
	label = control("STATIC", status, 20, 20, 570, 120, 1)
	button = control("BUTTON", "Закрыть", 430, 155, 150, 32, 2)
	desktopUser32.NewProc("EnableWindow").Call(button, 0)
	desktopShowWindow.Call(window, 0)
	desktopShowWindow.Call(window, 5)
	desktopUser32.NewProc("SetTimer").Call(window, 1, 200, 0)
	go func() {
		err := work(func(s string) { mu.Lock(); status = s; mu.Unlock() })
		mu.Lock()
		result = err
		finished = true
		completed = time.Now()
		if err != nil {
			status = err.Error()
		} else {
			status = "Операция завершена. Local Broadcast запущен."
		}
		mu.Unlock()
	}()
	var msg desktopMessage
	for {
		r, _, _ := desktopUser32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		desktopUser32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		desktopUser32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
	mu.Lock()
	defer mu.Unlock()
	return result
}
