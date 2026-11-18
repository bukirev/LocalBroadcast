//go:build !windows

package main

import "os/exec"

func prepareHeadlessCommand(cmd *exec.Cmd) {}
