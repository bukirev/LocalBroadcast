//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func prepareHeadlessCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
