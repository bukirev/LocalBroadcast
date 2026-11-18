//go:build windows

package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var desktopWindowClass = "LocalBroadcastControlWindow"

const (
	desktopCommandOpen    = 201
	desktopCommandRestart = 202
	desktopCommandExit    = 203
	desktopCommandHide    = 204
	desktopTrayMessage    = 0x8001
	desktopStopMessage    = 0x8002
)

var (
	desktopUser32        = syscall.NewLazyDLL("user32.dll")
	desktopShell32       = syscall.NewLazyDLL("shell32.dll")
	desktopCreateWindow  = desktopUser32.NewProc("CreateWindowExW")
	desktopDefWindowProc = desktopUser32.NewProc("DefWindowProcW")
	desktopShowWindow    = desktopUser32.NewProc("ShowWindow")
	desktopForeground    = desktopUser32.NewProc("SetForegroundWindow")
	desktopPostMessage   = desktopUser32.NewProc("PostMessageW")
	desktopDestroyWindow = desktopUser32.NewProc("DestroyWindow")
	desktopSetText       = desktopUser32.NewProc("SetWindowTextW")
	desktopNotifyIcon    = desktopShell32.NewProc("Shell_NotifyIconW")
)

type desktopWindowClassEx struct {
	Size, Style                                                        uint32
	Procedure                                                          uintptr
	ClassExtra, WindowExtra                                            int32
	Instance, Icon, Cursor, Background, MenuName, ClassName, SmallIcon uintptr
}
type desktopPoint struct{ X, Y int32 }
type desktopMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          desktopPoint
	Private        uint32
}
type desktopNotifyData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                [16]byte
	BalloonIcon         uintptr
}

func desktopString(s string) *uint16 { return syscall.StringToUTF16Ptr(s) }
func desktopRestartArgs() []string   { return []string{"-show-control"} }

func newDesktopIcon(instance uintptr) (uintptr, bool) {
	var mask [128]byte
	var pixels [32 * 32 * 4]byte
	for i := 0; i < len(pixels); i += 4 {
		pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 145, 77, 24, 255
	}
	for index, glyph := range []string{"10000100001000010000100001000011111", "11110100011000111110100011000111110"} {
		for cell, bit := range glyph {
			if bit != '1' {
				continue
			}
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					x, y := 4+index*13+cell%5*2+dx, 8+cell/5*2+dy
					offset := ((31-y)*32 + x) * 4
					pixels[offset], pixels[offset+1], pixels[offset+2] = 255, 255, 255
				}
			}
		}
	}
	icon, _, _ := desktopUser32.NewProc("CreateIcon").Call(instance, 32, 32, 1, 32, uintptr(unsafe.Pointer(&mask[0])), uintptr(unsafe.Pointer(&pixels[0])))
	if icon != 0 {
		return icon, true
	}
	icon, _, _ = desktopUser32.NewProc("LoadIconW").Call(0, 32516)
	return icon, false
}

func showDesktopError(message string) {
	desktopUser32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(desktopString(message))), uintptr(unsafe.Pointer(desktopString("Local Broadcast"))), 0x10)
}

func activateExistingDesktop() bool {
	window, _, _ := desktopUser32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(desktopString(desktopWindowClass))), 0)
	if window == 0 {
		return false
	}
	desktopShowWindow.Call(window, 9) // SW_RESTORE
	desktopForeground.Call(window)
	return true
}

func desktopRole(mode string) string {
	roles := map[string]string{"master": "Главный OBS", "presenter": "Презентер", "camera": "Камера", "camera-view": "Камера + просмотр", "viewer": "Просмотр", "custom": "Пользовательский"}
	if role, ok := roles[mode]; ok {
		return role
	}
	return mode
}

func (a *Application) desktopStatus() string {
	status := "Сервис работает"
	if a.coordinator != nil {
		status += fmt.Sprintf(" · устройств: %d", len(a.coordinator.onlinePeers()))
	} else if a.agent != nil {
		if connected, _ := a.agent.Status()["connected"].(bool); connected {
			status += " · связь с главным ПК есть"
		} else {
			status += " · ожидание главного ПК"
		}
	}
	if restartRequired(a.bootMode, a.store.Get().Mode) {
		status += "\r\nНастройки сохранены. Нажмите «Перезапустить»."
	}
	return status
}

// The window and tray share this process with the media agent. No browser or
// extra runtime is needed for process control. All Win32 calls stay on one thread.
func (a *Application) runDesktop(ctx context.Context, show bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instance, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	icon, ownedIcon := newDesktopIcon(instance)
	if ownedIcon {
		defer desktopUser32.NewProc("DestroyIcon").Call(icon)
	}
	cursor, _, _ := desktopUser32.NewProc("LoadCursorW").Call(0, 32512)
	taskbarCreated, _, _ := desktopUser32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(desktopString("TaskbarCreated"))))
	var window, statusControl, logsControl uintptr
	var tray desktopNotifyData
	var trayReady bool
	var lastStatus, lastLogs string
	showWindow := func() { desktopShowWindow.Call(window, 9); desktopForeground.Call(window) }
	addTray := func() {
		tray = desktopNotifyData{Window: window, ID: 1, Flags: 0x1 | 0x2 | 0x4 | 0x80, Callback: desktopTrayMessage, Icon: icon}
		tray.Size = uint32(unsafe.Sizeof(tray))
		copy(tray.Tip[:], syscall.StringToUTF16("Local Broadcast — управление сервисом"))
		ok, _, _ := desktopNotifyIcon.Call(0, uintptr(unsafe.Pointer(&tray))) // NIM_ADD
		trayReady = ok != 0
		ready := uintptr(0)
		if trayReady {
			ready = 1
		}
		desktopUser32.NewProc("SetPropW").Call(window, uintptr(unsafe.Pointer(desktopString("LocalBroadcastTrayReady"))), ready)
		if trayReady {
			tray.Version = 4
			desktopNotifyIcon.Call(4, uintptr(unsafe.Pointer(&tray)))
		}
	}
	command := func(id uintptr) {
		switch id {
		case desktopCommandOpen:
			go openBrowser(fmt.Sprintf("http://127.0.0.1:%d", uiPort))
		case desktopCommandRestart:
			if err := a.requestRestart(); err != nil {
				a.logs.Add("Перезапуск: " + err.Error())
				showDesktopError(err.Error())
				return
			}
			a.stop()
		case desktopCommandExit:
			a.logs.Add("Завершение из окна управления Windows")
			a.stop()
		case desktopCommandHide:
			if trayReady {
				desktopShowWindow.Call(window, 0)
			}
		}
	}
	popup := func() {
		menu, _, _ := desktopUser32.NewProc("CreatePopupMenu").Call()
		if menu == 0 {
			showWindow()
			return
		}
		defer desktopUser32.NewProc("DestroyMenu").Call(menu)
		appendMenu := desktopUser32.NewProc("AppendMenuW")
		for _, item := range []struct {
			id   uintptr
			text string
		}{{desktopCommandHide, "Окно управления"}, {desktopCommandOpen, "Панель в браузере"}, {desktopCommandRestart, "Перезапустить"}, {desktopCommandExit, "Завершить Local Broadcast"}} {
			appendMenu.Call(menu, 0, item.id, uintptr(unsafe.Pointer(desktopString(item.text))))
		}
		var point desktopPoint
		desktopUser32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&point)))
		desktopForeground.Call(window)
		id, _, _ := desktopUser32.NewProc("TrackPopupMenu").Call(menu, 0x100|0x2, uintptr(int64(point.X)), uintptr(int64(point.Y)), 0, window, 0)
		if id == desktopCommandHide {
			showWindow()
		} else {
			command(id)
		}
		desktopPostMessage.Call(window, 0, 0, 0)
	}
	procedure := syscall.NewCallback(func(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
		switch message {
		case 0x10: // WM_CLOSE: keep background work running in the tray.
			if trayReady {
				desktopShowWindow.Call(hwnd, 0)
			} else {
				a.stop()
			}
			return 0
		case 0x111:
			command(wparam & 0xffff)
			return 0
		case desktopTrayMessage:
			switch lparam & 0xffff {
			case 0x7b, 0x205:
				popup() // WM_CONTEXTMENU / WM_RBUTTONUP
			case 0x400, 0x401, 0x203:
				showWindow() // NIN_SELECT, keyboard select, double click
			}
			return 0
		case 0x113: // WM_TIMER
			status := a.desktopStatus()
			if status != lastStatus {
				desktopSetText.Call(statusControl, uintptr(unsafe.Pointer(desktopString(status))))
				lastStatus = status
			}
			logs := a.logs.List()
			if len(logs) > 12 {
				logs = logs[len(logs)-12:]
			}
			text := strings.Join(logs, "\r\n")
			if text != lastLogs {
				desktopSetText.Call(logsControl, uintptr(unsafe.Pointer(desktopString(text))))
				lastLogs = text
			}
			return 0
		case 0x11:
			return 1 // WM_QUERYENDSESSION
		case 0x16:
			if wparam != 0 {
				a.stop()
			}
			return 0
		case desktopStopMessage:
			desktopDestroyWindow.Call(hwnd)
			return 0
		case 0x2: // WM_DESTROY
			desktopNotifyIcon.Call(2, uintptr(unsafe.Pointer(&tray)))
			desktopUser32.NewProc("PostQuitMessage").Call(0)
			return 0
		}
		if taskbarCreated != 0 && uintptr(message) == taskbarCreated {
			addTray()
			return 0
		}
		result, _, _ := desktopDefWindowProc.Call(hwnd, uintptr(message), wparam, lparam)
		return result
	})
	className := desktopString(desktopWindowClass)
	class := desktopWindowClassEx{Procedure: procedure, Instance: instance, Icon: icon, SmallIcon: icon, Cursor: cursor, Background: 6, ClassName: uintptr(unsafe.Pointer(className))}
	class.Size = uint32(unsafe.Sizeof(class))
	atom, _, err := desktopUser32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return fmt.Errorf("RegisterClassExW: %w", err)
	}
	defer func() {
		desktopUser32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(className)), instance)
	}()
	window, _, err = desktopCreateWindow.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(desktopString("Local Broadcast — управление"))), 0x00ca0000, 0x80000000, 0x80000000, 650, 430, 0, 0, instance, 0)
	if window == 0 {
		return fmt.Errorf("CreateWindowExW: %w", err)
	}
	// Consume a launcher's SW_HIDE before intentionally showing our own window.
	desktopShowWindow.Call(window, 0)
	font, _, _ := syscall.NewLazyDLL("gdi32.dll").NewProc("GetStockObject").Call(17)
	control := func(class, text string, style uintptr, x, y, width, height int, id uintptr) uintptr {
		handle, _, _ := desktopCreateWindow.Call(0, uintptr(unsafe.Pointer(desktopString(class))), uintptr(unsafe.Pointer(desktopString(text))), 0x50000000|style, uintptr(x), uintptr(y), uintptr(width), uintptr(height), window, id, instance, 0)
		desktopUser32.NewProc("SendMessageW").Call(handle, 0x30, font, 1)
		return handle
	}
	control("STATIC", "Local Broadcast "+appVersion+"  ·  "+desktopRole(a.bootMode), 0, 20, 18, 600, 25, 100)
	statusControl = control("STATIC", a.desktopStatus(), 0, 20, 50, 600, 48, 101)
	control("BUTTON", "Панель в браузере", 0x10000, 20, 110, 185, 36, desktopCommandOpen)
	control("BUTTON", "Перезапустить", 0x10000, 220, 110, 185, 36, desktopCommandRestart)
	control("BUTTON", "Завершить", 0x10000, 420, 110, 185, 36, desktopCommandExit)
	control("STATIC", "Последние события", 0, 20, 163, 600, 22, 102)
	logsControl = control("EDIT", "", 0x800000|0x200000|0x10000|0x200|0x4|0x40|0x800, 20, 190, 585, 130, 103)
	footer := control("STATIC", "Крестик скрывает окно в трей. «Завершить» останавливает программу.", 0, 20, 345, 600, 24, 104)
	addTray()
	if !trayReady {
		a.logs.Add("Системный трей недоступен. Управление доступно в окне.")
		desktopSetText.Call(footer, uintptr(unsafe.Pointer(desktopString("Трей недоступен. Закрытие окна завершает программу."))))
		show = true
	}
	desktopUser32.NewProc("SetTimer").Call(window, 1, 1000, 0)
	desktopPostMessage.Call(window, 0x113, 1, 0)
	if show {
		showWindow()
	}
	desktopUser32.NewProc("SetPropW").Call(window, uintptr(unsafe.Pointer(desktopString("LocalBroadcastInitialized"))), 1)
	go func() { <-ctx.Done(); desktopPostMessage.Call(window, desktopStopMessage, 0, 0) }()
	var message desktopMessage
	for {
		result, _, err := desktopUser32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) == -1 {
			desktopDestroyWindow.Call(window)
			return fmt.Errorf("GetMessageW: %w", err)
		}
		if result == 0 {
			return nil
		}
		translated, _, _ := desktopUser32.NewProc("IsDialogMessageW").Call(window, uintptr(unsafe.Pointer(&message)))
		if translated == 0 {
			desktopUser32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&message)))
			desktopUser32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&message)))
		}
	}
}
