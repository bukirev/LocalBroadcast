//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func openOBS(c Configuration) error {
	tasklist := exec.Command("tasklist.exe", "/FI", "IMAGENAME eq obs64.exe", "/NH")
	prepareHeadlessCommand(tasklist)
	if data, err := tasklist.CombinedOutput(); err == nil && strings.Contains(strings.ToLower(string(data)), "obs64.exe") {
		return nil
	}
	paths := []string{}
	if c.ObsExePath != "" {
		paths = append(paths, c.ObsExePath)
	}
	for _, key := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if base := os.Getenv(key); base != "" {
			paths = append(paths, filepath.Join(base, "obs-studio", "bin", "64bit", "obs64.exe"))
		}
	}
	if p, err := exec.LookPath("obs64.exe"); err == nil {
		paths = append(paths, p)
	}
	for _, p := range paths {
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		cmd := exec.Command(p)
		cmd.Dir = filepath.Dir(p)
		if err := cmd.Start(); err != nil {
			return err
		}
		// Start without waiting; OBS is an independent production application.
		return cmd.Process.Release()
	}
	return fmt.Errorf("OBS Studio не найден. Установите OBS или задайте путь к obs64.exe")
}
