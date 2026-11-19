package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func updateStartupGate(exe string) error {
	root := filepath.Dir(exe)
	if _, err := os.Lstat(updateStateDir(root)); os.IsNotExist(err) {
		return nil
	}
	if err := ensureUpdateState(root); err != nil {
		return err
	}
	j, err := loadUpdateJournal(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Phase == "committed" || j.Phase == "rolled-back" {
		return nil
	}
	return fmt.Errorf("незавершённое обновление. Дождитесь обновлятора или запустите восстановление: %s", filepath.Join(updateStateDir(root), "Recover.cmd"))
}
func validateUpdateCandidate(planPath, config string) (updatePlan, error) {
	var p updatePlan
	if err := readUpdateJSON(planPath, &p); err != nil {
		return p, err
	}
	if err := validateUpdatePlan(planPath, p); err != nil {
		return p, err
	}
	exe, err := os.Executable()
	if err != nil {
		return p, err
	}
	absoluteConfig, err := filepath.Abs(config)
	if err != nil {
		return p, err
	}
	if !strings.EqualFold(exe, updateTarget(p.Root)) || p.Release.Version != appVersion || p.UIPort != uiPort || absoluteConfig != p.Config {
		return p, fmt.Errorf("неверная версия/путь кандидата")
	}
	var c Configuration
	configData, err := readBounded(p.Config, 64<<10)
	if err == nil {
		err = jsonUnmarshalUpdate(configData, &c)
	}
	if err != nil {
		return p, err
	}
	if c.Schema != 2 {
		return p, fmt.Errorf("обновлятор не выполняет миграции настроек; нужен полный установщик")
	}
	j, err := loadUpdateJournal(p.Root)
	if err != nil {
		return p, err
	}
	if j.ID != p.ID || j.Phase != "installed" || j.Version != appVersion {
		return p, fmt.Errorf("нет ожидающей установки кандидата")
	}
	return p, checkUpdateHash(exe, j.NewHash)
}
func (a *Application) candidateHandshake(ctx context.Context, planPath string) error {
	p, err := validateUpdateCandidate(planPath, a.store.path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, ready := range []chan struct{}{a.uiReady, a.desktopReady} {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err = updateHealth(a.store.Get()); err != nil {
		return err
	}
	dir := filepath.Dir(planPath)
	r := updateReceipt{ID: p.ID, Version: appVersion, PID: os.Getpid()}
	if err = atomicUpdateJSON(filepath.Join(dir, "candidate-ready.json"), r); err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		j, err := loadUpdateJournal(p.Root)
		if err == nil && j.ID == p.ID && j.Phase == "committed" {
			a.startupBlocked.Store(false)
			if os.Getenv("LB_UPDATE_TEST_NO_UI") != "1" {
				activateExistingDesktop()
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("нет подтверждения commit: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
func (a *Application) markUpdateRunning(planPath string) error {
	var p updatePlan
	if err := readUpdateJSON(planPath, &p); err != nil {
		return err
	}
	return atomicUpdateJSON(filepath.Join(filepath.Dir(planPath), "running.json"), updateReceipt{ID: p.ID, Version: appVersion, PID: os.Getpid()})
}
