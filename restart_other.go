//go:build !windows

package main

import (
	"syscall"
	"time"
)

func waitForParent(pid int) error {
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		if err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
