package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

func jsonUnmarshalUpdate(b []byte, v any) error { return json.Unmarshal(b, v) }
func updateWaitReceipt(ctx context.Context, path, id, version string, pid int) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var r updateReceipt
		if readUpdateJSON(path, &r) == nil && r.ID == id && (version == "" || r.Version == version) && (pid == 0 || r.PID == pid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("нет подтверждения запуска/передачи управления: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
func writeRecoveryCommand(p updatePlan, planPath string) error {
	helper := filepath.Join(filepath.Dir(planPath), "LocalBroadcast.Update.exe")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := "& " + quote(helper) + " '-update-helper' " + quote(planPath) + " '-update-recover'"
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	cmd := ":: LocalBroadcast updater recovery v1\r\n@echo off\r\npowershell.exe -NoProfile -ExecutionPolicy Bypass -EncodedCommand " + base64.StdEncoding.EncodeToString(b) + "\r\n"
	path := filepath.Join(updateStateDir(p.Root), "Recover.cmd")
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("recovery command является ссылкой")
	}
	if old, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(old), ":: LocalBroadcast updater recovery v1\r\n") {
		return fmt.Errorf("не заменяем неизвестный Recover.cmd")
	}
	return os.WriteFile(path, []byte(cmd), 0600)
}

func (a *Application) handoffUpdate(directory string, release updateRelease, interrupt bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	root := filepath.Dir(exe)
	if !strings.EqualFold(filepath.Base(exe), "LocalBroadcast.exe") {
		return fmt.Errorf("для обновления EXE должен называться LocalBroadcast.exe")
	}
	if err = ensureUpdateState(root); err != nil {
		return err
	}
	if err = checkUpdateSpace(root, uint64(3*updateMaxExpanded)); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".lb-write-test-")
	if err != nil {
		return fmt.Errorf("нет прав записи в папку программы; приложение не закрывалось")
	}
	f.Close()
	os.Remove(f.Name())
	if j, e := loadUpdateJournal(root); e == nil && j.Phase != "committed" && j.Phase != "rolled-back" {
		return fmt.Errorf("сначала завершите восстановление предыдущего обновления")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	if _, _, err = validateUpdatePackage(filepath.Join(directory, "package.zip"), release.Version, release.SHA256, release.Size); err != nil {
		return err
	}
	if err = updateHealth(a.store.Get()); err != nil {
		return err
	}
	configData, err := readBounded(a.store.path, 64<<10)
	var rawConfig Configuration
	if err == nil {
		err = json.Unmarshal(configData, &rawConfig)
	}
	if err != nil || rawConfig.Schema != 2 {
		return fmt.Errorf("нужны существующие настройки schema 2; используйте полный установщик")
	}
	created, err := updateProcessCreated(os.Getpid())
	if err != nil {
		return err
	}
	oldHash, _, err := fileDigest(exe)
	if err != nil {
		return err
	}
	config, err := filepath.Abs(a.store.path)
	if err != nil {
		return err
	}
	p := updatePlan{Protocol: 1, ID: filepath.Base(directory), Root: root, Config: config, UIPort: uiPort, ParentPID: os.Getpid(), ParentCreated: created, OldHash: oldHash, Release: release}
	p.Release.Notes = ""
	p.Release.URL = ""
	planPath := filepath.Join(directory, "plan.json")
	if err = validateUpdatePlan(planPath, p); err != nil {
		return err
	}
	if err = atomicUpdateJSON(planPath, p); err != nil {
		return err
	}
	helper := filepath.Join(directory, "LocalBroadcast.Update.exe")
	if err = copyUpdateFile(exe, helper); err != nil {
		return err
	}
	if err = checkUpdateHash(helper, oldHash); err != nil {
		return err
	}
	if err = writeRecoveryCommand(p, planPath); err != nil {
		return err
	}
	cmd := exec.Command(helper, "-update-helper", planPath)
	cmd.Dir = directory
	prepareHeadlessCommand(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("не удалось открыть обновлятор: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	accepted := false
	defer func() {
		if !accepted {
			cmd.Process.Kill()
			<-done
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = updateWaitReceipt(ctx, filepath.Join(directory, "helper-ready.json"), p.ID, "", cmd.Process.Pid); err != nil {
		return err
	}
	// Authoritative check immediately before the handoff, after download and UI.
	if err = a.updateSafety(interrupt); err != nil {
		return err
	}
	a.updateStopping.Store(true)
	if err = atomicUpdateJSON(filepath.Join(directory, "go.json"), updateReceipt{ID: p.ID}); err != nil {
		return err
	}
	accepted = true
	a.logs.Add("Обновление подготовлено. Сервис завершает работу для установки " + release.Version)
	a.stop()
	return nil
}

func runUpdateHelper(planPath string, recoverOnly bool, report func(string)) error {
	var p updatePlan
	if err := readUpdateJSON(planPath, &p); err != nil {
		return err
	}
	if err := validateUpdatePlan(planPath, p); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(self), filepath.Join(filepath.Dir(planPath), "LocalBroadcast.Update.exe")) {
		return fmt.Errorf("helper запущен не из служебного каталога")
	}
	if err = checkUpdateHash(self, p.OldHash); err != nil {
		return err
	}
	unlock, err := lockUpdateInstall(p.Root)
	if err != nil {
		return err
	}
	defer unlock()
	if err = ensureUpdateState(p.Root); err != nil {
		return err
	}
	if recoverOnly {
		j, err := loadUpdateJournal(p.Root)
		if err != nil {
			return err
		}
		if j.ID != p.ID || j.Plan != planPath {
			return fmt.Errorf("recovery plan не соответствует журналу")
		}
		if j.Phase == "committed" {
			return fmt.Errorf("эта установка уже подтверждена; recovery не откатывает работающую версию")
		}
		report("Восстановление прежней версии…")
		if err = rollbackUpdate(&j); err != nil {
			return fmt.Errorf("восстановление не завершено: %w", err)
		}
		return launchRestoredUpdate(p)
	}
	if j, e := loadUpdateJournal(p.Root); e == nil && j.Phase != "committed" && j.Phase != "rolled-back" {
		return fmt.Errorf("обнаружена незавершённая транзакция")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	installed, _ := parseReleaseVersion(appVersion)
	candidate, err := parseReleaseVersion(p.Release.Version)
	if err != nil || compareReleaseVersion(candidate, installed) <= 0 {
		return fmt.Errorf("обновление должно иметь более новую версию")
	}
	parent, err := openExactUpdateParent(p.ParentPID, p.ParentCreated)
	if err != nil {
		return err
	}
	defer parent.close()
	_, files, err := validateUpdatePackage(filepath.Join(filepath.Dir(planPath), "package.zip"), p.Release.Version, p.Release.SHA256, p.Release.Size)
	if err != nil {
		return err
	}
	if err = checkUpdateHash(updateTarget(p.Root), p.OldHash); err != nil {
		return err
	}
	if err = atomicUpdateJSON(filepath.Join(filepath.Dir(planPath), "helper-ready.json"), updateReceipt{ID: p.ID, PID: os.Getpid()}); err != nil {
		return err
	}
	report("Ожидание разрешения и завершения сервиса…")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = updateWaitReceipt(ctx, filepath.Join(filepath.Dir(planPath), "go.json"), p.ID, "", 0); err != nil {
		return err
	}
	if err = parent.wait(30 * time.Second); err != nil {
		return err
	}
	newHash := func() string {
		m := updateManifest{}
		_ = json.Unmarshal(files["update-manifest.json"], &m)
		return m.Files["LocalBroadcast.exe"].SHA256
	}()
	j := updateJournal{Protocol: 1, ID: p.ID, Root: p.Root, Plan: planPath, OldHash: p.OldHash, NewHash: newHash, Version: p.Release.Version}
	previous, _ := loadUpdateJournal(p.Root)
	report("Сохранение прежней версии и замена программы…")
	if err = installUpdateFiles(&j, files["LocalBroadcast.exe"], nil); err != nil {
		return recoverFailedUpdate(p, &j, err)
	}
	report("Проверка окна, конфигурации и запуска новой версии…")
	cmd := exec.Command(updateTarget(p.Root), "-update-candidate", planPath, "-ui-port", strconv.Itoa(p.UIPort), "-config", p.Config, "-no-browser")
	cmd.Dir = p.Root
	prepareHeadlessCommand(cmd)
	if err = cmd.Start(); err != nil {
		return recoverFailedUpdate(p, &j, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer readyCancel()
	wait := make(chan error, 1)
	go func() {
		wait <- updateWaitReceipt(readyCtx, filepath.Join(filepath.Dir(planPath), "candidate-ready.json"), p.ID, p.Release.Version, cmd.Process.Pid)
	}()
	select {
	case err = <-done:
		readyCancel()
		err = fmt.Errorf("кандидат завершился до готовности: %v", err)
	case err = <-wait:
		if err != nil {
			cmd.Process.Kill()
			<-done
		}
	}
	if err != nil {
		return recoverFailedUpdate(p, &j, err)
	}
	// Readiness must still refer to a live process at the durable commit point.
	select {
	case <-done:
		return recoverFailedUpdate(p, &j, fmt.Errorf("кандидат завершился до commit"))
	default:
	}
	j.Phase = "committed"
	if err = atomicUpdateJSON(updateJournalPath(p.Root), j); err != nil {
		cmd.Process.Kill()
		<-done
		j.Phase = "installed"
		return recoverFailedUpdate(p, &j, err)
	}
	// From this point the candidate may write settings. Never auto-rollback.
	runCtx, runCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer runCancel()
	if err = updateWaitReceipt(runCtx, filepath.Join(filepath.Dir(planPath), "running.json"), p.ID, p.Release.Version, cmd.Process.Pid); err != nil {
		return fmt.Errorf("версия подтверждена, но запуск не завершён. Журнал сохранён; проверьте LocalBroadcast.exe")
	}
	archive := filepath.Join(filepath.Dir(planPath), "package.zip")
	if checkUpdateHash(archive, p.Release.SHA256) == nil {
		_ = os.Remove(archive)
	}
	cleanupPreviousUpdate(previous, p.Root)
	report("Обновление установлено; прежняя версия сохранена.")
	return nil
}
func launchRestoredUpdate(p updatePlan) error {
	cmd := exec.Command(updateTarget(p.Root), "-config", p.Config, "-ui-port", strconv.Itoa(p.UIPort), "-no-browser", "-show-control")
	cmd.Dir = p.Root
	prepareHeadlessCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
func recoverFailedUpdate(p updatePlan, j *updateJournal, cause error) error {
	// No journal means the first replacement never began.
	actual, err := loadUpdateJournal(p.Root)
	if err == nil && actual.ID == p.ID {
		*j = actual
		if err = rollbackUpdate(j); err != nil {
			return fmt.Errorf("%v. Восстановление не завершено: %v. Запустите .lb-update\\Recover.cmd", cause, err)
		}
		if err = launchRestoredUpdate(p); err != nil {
			return fmt.Errorf("%v. Прежний EXE восстановлен, но не запущен: %v", cause, err)
		}
		return fmt.Errorf("%v. Прежняя версия восстановлена и запущена", cause)
	}
	return fmt.Errorf("%v. Замена не началась; запустите прежний LocalBroadcast.exe", cause)
}

func cleanupPreviousUpdate(j updateJournal, root string) {
	if j.Root != root || j.Phase != "committed" || len(j.ID) != 32 || !digestPattern.MatchString(j.ID+j.ID) {
		return
	}
	// Only known files with proven hashes. Never recursively delete a directory.
	if checkUpdateHash(updateBackup(j), j.OldHash) == nil {
		_ = os.Remove(updateBackup(j))
	}
	var p updatePlan
	if readUpdateJSON(j.Plan, &p) != nil || validateUpdatePlan(j.Plan, p) != nil || p.ID != j.ID || p.Root != root {
		return
	}
	dir := filepath.Dir(j.Plan)
	if checkUpdateHash(filepath.Join(dir, "package.zip"), p.Release.SHA256) == nil {
		_ = os.Remove(filepath.Join(dir, "package.zip"))
	}
	if checkUpdateHash(filepath.Join(dir, "LocalBroadcast.Update.exe"), p.OldHash) == nil {
		_ = os.Remove(filepath.Join(dir, "LocalBroadcast.Update.exe"))
	}
	for _, name := range []string{"helper-ready.json", "go.json", "candidate-ready.json", "running.json"} {
		var r updateReceipt
		path := filepath.Join(dir, name)
		if readUpdateJSON(path, &r) == nil && r.ID == p.ID {
			_ = os.Remove(path)
		}
	}
	var owner updateDownloadOwner
	ownerFile := filepath.Join(dir, "download-owner.json")
	if readUpdateJSON(ownerFile, &owner) == nil && owner.ID == p.ID && owner.Root == p.Root && owner.Product == "LocalBroadcast" {
		_ = os.Remove(ownerFile)
	}
	_ = os.Remove(j.Plan)
	_ = os.Remove(dir) // Fails safely if unknown files remain.
}
