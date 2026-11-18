//go:build windows

package main

import "syscall"

func waitForParent(pid int) error {
	handle, err := syscall.OpenProcess(0x00100000, false, uint32(pid)) // SYNCHRONIZE
	if err == syscall.Errno(87) {                                      // Parent has already exited.
		return nil
	}
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)
	_, err = syscall.WaitForSingleObject(handle, syscall.INFINITE)
	return err
}
