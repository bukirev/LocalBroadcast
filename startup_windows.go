//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func setAutoRun(enable bool) error {
	const key = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	const value = `LocalBroadcast`
	var cmd *exec.Cmd
	if enable {
		path, err := os.Executable()
		if err != nil {
			return err
		}
		cmd = exec.Command("reg.exe", "ADD", key, "/v", value, "/t", "REG_SZ", "/d", fmt.Sprintf("\"%s\" -no-browser", path), "/f")
	} else {
		cmd = exec.Command("reg.exe", "DELETE", key, "/v", value, "/f")
	}
	prepareHeadlessCommand(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil && enable {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	if err != nil && !enable {
		return nil
	} // deleting an absent entry is harmless
	return nil
}
