//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestDesktopWindowLifecycle(t *testing.T) {
	if os.Getenv("LB_DESKTOP_TEST_CHILD") == "1" {
		desktopWindowClass = "LocalBroadcastDesktopTest-" + strconv.Itoa(os.Getpid())
		store, err := newConfigStore(filepath.Join(t.TempDir(), "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(20*time.Second, cancel)
		defer timer.Stop()
		app := &Application{store: store, logs: &LogBuffer{}, bootMode: store.Get().Mode, stop: cancel}
		app.logs.Add("Тест окна управления")
		if err := app.runDesktop(ctx, true); err != nil {
			t.Fatal(err)
		}
		if !timer.Stop() {
			t.Fatal("Native window did not respond to exit command")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDesktopWindowLifecycle$", "-test.v")
	cmd.Env = append(os.Environ(), "LB_DESKTOP_TEST_CHILD=1")
	prepareHeadlessCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	class := syscall.StringToUTF16Ptr("LocalBroadcastDesktopTest-" + strconv.Itoa(cmd.Process.Pid))
	var window uintptr
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		window, _, _ = desktopUser32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(class)), 0)
		if window != 0 {
			ready, _, _ := desktopUser32.NewProc("GetPropW").Call(window, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("LocalBroadcastInitialized"))))
			if ready != 0 {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if window == 0 {
		t.Fatal("Native control window was not created")
	}
	visible, _, _ := desktopUser32.NewProc("IsWindowVisible").Call(window)
	if visible == 0 {
		t.Fatal("Control window was hidden by the launcher's startup flags")
	}
	// Check the real Unicode button text, not a parallel model of the controls.
	button, _, _ := desktopUser32.NewProc("GetDlgItem").Call(window, desktopCommandRestart)
	var text [128]uint16
	desktopUser32.NewProc("GetWindowTextW").Call(button, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)))
	if syscall.UTF16ToString(text[:]) != "Перезапустить" {
		t.Fatal("Restart button is missing")
	}
	// WM_CLOSE hides a working tray app; cancellation still terminates the loop.
	ready, _, _ := desktopUser32.NewProc("GetPropW").Call(window, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("LocalBroadcastTrayReady"))))
	if ready != 0 {
		desktopPostMessage.Call(window, 0x10, 0, 0)
		time.Sleep(100 * time.Millisecond)
		visible, _, _ := desktopUser32.NewProc("IsWindowVisible").Call(window)
		if visible != 0 {
			t.Fatal("Close did not hide the window")
		}
		if !activateTestDesktop(class) {
			t.Fatal("Hidden window could not be reopened")
		}
	}
	desktopPostMessage.Call(window, 0x111, desktopCommandExit, 0)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(fmt.Errorf("native exit: %w", err))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exit button did not terminate the native loop")
	}
}

func activateTestDesktop(class *uint16) bool {
	window, _, _ := desktopUser32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(class)), 0)
	if window == 0 {
		return false
	}
	desktopShowWindow.Call(window, 9)
	visible, _, _ := desktopUser32.NewProc("IsWindowVisible").Call(window)
	return visible != 0
}
