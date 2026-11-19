//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type exactUpdateParent struct{ handle syscall.Handle }

func updateProcessCreated(pid int) (uint64, error) {
	h, err := syscall.OpenProcess(0x1000, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(h)
	var created, exit, kernel, user syscall.Filetime
	if err = syscall.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}
func openExactUpdateParent(pid int, created uint64) (*exactUpdateParent, error) {
	h, err := syscall.OpenProcess(0x1000|0x100000, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	var start, exit, kernel, user syscall.Filetime
	if err = syscall.GetProcessTimes(h, &start, &exit, &kernel, &user); err != nil {
		syscall.CloseHandle(h)
		return nil, err
	}
	if uint64(start.HighDateTime)<<32|uint64(start.LowDateTime) != created {
		syscall.CloseHandle(h)
		return nil, fmt.Errorf("PID родителя переиспользован")
	}
	return &exactUpdateParent{h}, nil
}
func (p *exactUpdateParent) close() { syscall.CloseHandle(p.handle) }
func (p *exactUpdateParent) wait(timeout time.Duration) error {
	r, err := syscall.WaitForSingleObject(p.handle, uint32(timeout/time.Millisecond))
	if err != nil {
		return err
	}
	if r != 0 {
		return fmt.Errorf("родитель не завершился; файлы не заменены")
	}
	return nil
}

func lockUpdateInstall(root string) (func(), error) {
	hash := sha256.Sum256([]byte(strings.ToLower(root)))
	name := `Local\LocalBroadcast.Update.` + hex.EncodeToString(hash[:16])
	k := syscall.NewLazyDLL("kernel32.dll")
	h, _, err := k.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(name))))
	if h == 0 {
		return nil, err
	}
	r, e := syscall.WaitForSingleObject(syscall.Handle(h), 0)
	if e != nil || (r != 0 && r != 0x80) {
		syscall.CloseHandle(syscall.Handle(h))
		return nil, fmt.Errorf("другая операция обновления уже работает")
	}
	return func() { k.NewProc("ReleaseMutex").Call(h); syscall.CloseHandle(syscall.Handle(h)) }, nil
}
func checkUpdateSpace(path string, required uint64) error {
	var available, total, free uint64
	r, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW").Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(path))), uintptr(unsafe.Pointer(&available)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return err
	}
	if available < required {
		return fmt.Errorf("недостаточно места для обновления и резервной копии")
	}
	return nil
}

func updateConfirm(message string) bool {
	r, _, _ := desktopUser32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(desktopString(message))), uintptr(unsafe.Pointer(desktopString("Обновление Local Broadcast"))), 0x4|0x20|0x100)
	return r == 6
}

// This small native progress shell never runs from the replaced executable.
func runUpdateProgress(work func(func(string)) error) error {
	if os.Getenv("LB_UPDATE_TEST_NO_UI") == "1" {
		return work(func(string) {})
	}
	return updateProgressWindow(work)
}

func showUpdateNotice(message string) {
	desktopUser32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(desktopString(message))), uintptr(unsafe.Pointer(desktopString("Обновление Local Broadcast"))), 0x40)
}

func configureUpdateDesktopInstance(port int) {
	if port != 39888 {
		desktopWindowClass += fmt.Sprintf("-%d", port)
	}
}

func updateOBSRunning() (bool, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer syscall.CloseHandle(snapshot)
	var currentSession uint32
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("ProcessIdToSessionId")
	if r, _, err := proc.Call(uintptr(os.Getpid()), uintptr(unsafe.Pointer(&currentSession))); r == 0 {
		return false, err
	}
	entry := syscall.ProcessEntry32{Size: uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))}
	for err = syscall.Process32First(snapshot, &entry); err == nil; err = syscall.Process32Next(snapshot, &entry) {
		name := strings.ToLower(syscall.UTF16ToString(entry.ExeFile[:]))
		if name == "obs64.exe" || name == "obs32.exe" {
			var session uint32
			if r, _, _ := proc.Call(uintptr(entry.ProcessID), uintptr(unsafe.Pointer(&session))); r != 0 && session == currentSession {
				return true, nil
			}
		}
	}
	if err != syscall.ERROR_NO_MORE_FILES {
		return false, err
	}
	return false, nil
}
