package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// Start the replacement before stopping this process. It waits for our exit,
// so media processes and listening ports are released before it starts work.
func (a *Application) requestRestart() error {
	a.restartMu.Lock()
	defer a.restartMu.Unlock()
	if a.restarting {
		return nil
	}
	if a.stop == nil {
		return fmt.Errorf("перезапуск недоступен")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "-no-browser", "-config", a.store.path, "-restart-parent", strconv.Itoa(os.Getpid()))
	prepareHeadlessCommand(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить программу: %w", err)
	}
	_ = cmd.Process.Release()
	a.restarting = true
	a.logs.Add("Перезапуск LocalBroadcast из панели управления")
	return nil
}

func restartRequired(bootMode, mode string) bool {
	return bootMode != mode && (bootMode == "master" || mode == "master")
}
