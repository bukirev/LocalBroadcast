package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type updatePlan struct {
	Protocol      int           `json:"protocol"`
	ID            string        `json:"id"`
	Root          string        `json:"root"`
	Config        string        `json:"config"`
	UIPort        int           `json:"uiPort"`
	ParentPID     int           `json:"parentPid"`
	ParentCreated uint64        `json:"parentCreated"`
	OldHash       string        `json:"oldHash"`
	Release       updateRelease `json:"release"`
}
type updateJournal struct {
	Protocol int    `json:"protocol"`
	ID       string `json:"id"`
	Root     string `json:"root"`
	Plan     string `json:"plan"`
	OldHash  string `json:"oldHash"`
	NewHash  string `json:"newHash"`
	Version  string `json:"version"`
	Phase    string `json:"phase"`
}
type updateReceipt struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

func atomicUpdateJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp, path)
}
func noUpdateLinks(path string) error {
	path = filepath.Clean(path)
	for {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("обновление через symlink/reparse point запрещено")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
func updateStateDir(root string) string { return filepath.Join(root, ".lb-update") }
func updateJournalPath(root string) string {
	return filepath.Join(updateStateDir(root), "journal.json")
}
func updateTarget(root string) string { return filepath.Join(root, "LocalBroadcast.exe") }
func updateBackup(j updateJournal) string {
	return filepath.Join(updateStateDir(j.Root), "backup-"+j.ID+".exe")
}
func updateIncoming(j updateJournal) string {
	return filepath.Join(updateStateDir(j.Root), "incoming-"+j.ID+".exe")
}
func updateFailed(j updateJournal) string {
	return filepath.Join(updateStateDir(j.Root), "failed-"+j.ID+".exe")
}

func validateUpdatePlan(path string, p updatePlan) error {
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("plan является ссылкой")
	}
	if p.UIPort < 1024 || p.UIPort > 65535 || p.Protocol != 1 || len(p.ID) != 32 || !digestPattern.MatchString(p.ID+p.ID) || !digestPattern.MatchString(p.OldHash) || !digestPattern.MatchString(p.Release.SHA256) || p.Release.Size <= 0 || p.Release.Size > updateMaxZip {
		return fmt.Errorf("неверный update plan")
	}
	if !filepath.IsAbs(path) || !filepath.IsAbs(p.Root) || !filepath.IsAbs(p.Config) || p.Root != filepath.Clean(p.Root) || strings.ContainsAny(p.Root+p.Config, "\x00\r\n\"") {
		return fmt.Errorf("неверные абсолютные пути обновления")
	}
	if filepath.Base(filepath.Dir(path)) != p.ID || filepath.Base(path) != "plan.json" {
		return fmt.Errorf("неверный каталог транзакции")
	}
	rel, err := filepath.Rel(p.Root, filepath.Dir(path))
	if err != nil || filepath.IsLocal(rel) {
		return fmt.Errorf("helper должен находиться вне каталога установки")
	}
	if err = noUpdateLinks(filepath.Dir(path)); err != nil {
		return err
	}
	if err = noUpdateLinks(p.Root); err != nil {
		return err
	}
	_, err = parseReleaseVersion(p.Release.Version)
	return err
}
func ensureUpdateState(root string) error {
	if err := noUpdateLinks(root); err != nil {
		return err
	}
	dir := updateStateDir(root)
	created := false
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	} else if err == nil {
		created = true
	}
	if err := noUpdateLinks(dir); err != nil {
		return err
	}
	marker := filepath.Join(dir, "owner.json")
	if created {
		return atomicUpdateJSON(marker, map[string]string{"product": "LocalBroadcast", "root": root})
	}
	var owner map[string]string
	if err := readUpdateJSON(marker, &owner); err != nil {
		return fmt.Errorf("служебная папка не принадлежит обновлятору: %w", err)
	}
	if owner["product"] != "LocalBroadcast" || owner["root"] != root {
		return fmt.Errorf("чужая служебная папка обновлений")
	}
	return nil
}
func loadUpdateJournal(root string) (updateJournal, error) {
	var j updateJournal
	if err := readUpdateJSON(updateJournalPath(root), &j); err != nil {
		return j, err
	}
	if j.Protocol != 1 || j.Root != root || len(j.ID) != 32 || !digestPattern.MatchString(j.ID+j.ID) || !digestPattern.MatchString(j.OldHash) || !digestPattern.MatchString(j.NewHash) {
		return j, fmt.Errorf("неверный recovery journal")
	}
	switch j.Phase {
	case "prepared", "backed-up", "installed", "committed", "rolled-back":
	default:
		return j, fmt.Errorf("неизвестная фаза recovery journal")
	}
	return j, nil
}
func copyUpdateFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func checkUpdateHash(path, want string) error {
	if err := noUpdateLinks(path); err != nil {
		return err
	}
	got, _, err := fileDigest(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("файл изменён: %s", filepath.Base(path))
	}
	return nil
}

// A single executable is the only owned replacement. Journal precedes its first
// rename; recovery derives every filename from a validated ID, never a manifest.
func installUpdateFiles(j *updateJournal, exe []byte, checkpoint func(string) error) error {
	if err := ensureUpdateState(j.Root); err != nil {
		return err
	}
	if err := checkUpdateHash(updateTarget(j.Root), j.OldHash); err != nil {
		return err
	}
	for _, path := range []string{updateBackup(*j), updateIncoming(*j), updateFailed(*j)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("служебный файл уже существует: %s", filepath.Base(path))
		}
	}
	j.Phase = "prepared"
	if err := atomicUpdateJSON(updateJournalPath(j.Root), j); err != nil {
		return err
	}
	step := func(phase string) error {
		if checkpoint != nil {
			return checkpoint(phase)
		}
		return nil
	}
	if err := step("journal"); err != nil {
		return err
	}
	f, err := os.OpenFile(updateIncoming(*j), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = f.Write(exe)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = checkUpdateHash(updateIncoming(*j), j.NewHash); err != nil {
		return err
	}
	if err = step("staged"); err != nil {
		return err
	}
	if err = os.Rename(updateTarget(j.Root), updateBackup(*j)); err != nil {
		return err
	}
	if err = step("renamed-old"); err != nil {
		return err
	}
	j.Phase = "backed-up"
	if err = atomicUpdateJSON(updateJournalPath(j.Root), j); err != nil {
		return err
	}
	if err = step("backed-up"); err != nil {
		return err
	}
	if err = os.Rename(updateIncoming(*j), updateTarget(j.Root)); err != nil {
		return err
	}
	if err = step("renamed-new"); err != nil {
		return err
	}
	j.Phase = "installed"
	if err = atomicUpdateJSON(updateJournalPath(j.Root), j); err != nil {
		return err
	}
	return step("installed")
}

func rollbackUpdate(j *updateJournal) error {
	if j.Phase == "committed" {
		return fmt.Errorf("обновление уже подтверждено; автоматический откат запрещён")
	}
	if err := ensureUpdateState(j.Root); err != nil {
		return err
	}
	target := updateTarget(j.Root)
	if err := checkUpdateHash(target, j.OldHash); err == nil {
		j.Phase = "rolled-back"
		return atomicUpdateJSON(updateJournalPath(j.Root), j)
	}
	if err := checkUpdateHash(updateBackup(*j), j.OldHash); err != nil {
		return fmt.Errorf("нет проверенной прежней версии: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		if err = checkUpdateHash(target, j.NewHash); err != nil {
			return fmt.Errorf("откат не заменяет посторонний EXE: %w", err)
		}
		if _, err = os.Lstat(updateFailed(*j)); !os.IsNotExist(err) {
			return fmt.Errorf("failed EXE уже существует; требуется проверка вручную")
		}
		if err = os.Rename(target, updateFailed(*j)); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(updateBackup(*j), target); err != nil {
		return err
	}
	j.Phase = "rolled-back"
	return atomicUpdateJSON(updateJournalPath(j.Root), j)
}

func checkExistingUpdateParents(path string) error {
	for {
		_, err := os.Lstat(path)
		if err == nil {
			return noUpdateLinks(path)
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(path)
		if path == parent {
			return err
		}
		path = parent
	}
}
