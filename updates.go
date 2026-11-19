package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type updateStatus struct {
	Installed  string         `json:"installed"`
	Channel    string         `json:"channel"`
	Phase      string         `json:"phase"`
	Message    string         `json:"message"`
	Release    *updateRelease `json:"release,omitempty"`
	Received   int64          `json:"received"`
	Executable string         `json:"executable"`
}
type updateManager struct {
	mu        sync.Mutex
	status    updateStatus
	client    *updateClient
	cancel    context.CancelFunc
	directory string
	base      string
}

func newUpdateManager() *updateManager {
	exe, _ := os.Executable()
	exe, _ = filepath.Abs(exe)
	base, _ := os.UserCacheDir()
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Dir(exe))))
	base = filepath.Join(base, "LocalBroadcast", "Updates", hex.EncodeToString(key[:12]))
	channel := "stable"
	if strings.Contains(appVersion, "-") {
		channel = "beta"
	}
	var pref struct {
		Channel string `json:"channel"`
	}
	if readUpdateJSON(filepath.Join(base, "preferences.json"), &pref) == nil && (pref.Channel == "stable" || pref.Channel == "beta") {
		channel = pref.Channel
	}
	return &updateManager{client: newUpdateClient(), base: base, status: updateStatus{Installed: appVersion, Channel: channel, Phase: "idle", Message: "Проверка выполняется по вашей команде", Executable: exe}}
}
func (m *updateManager) snapshot() updateStatus { m.mu.Lock(); defer m.mu.Unlock(); return m.status }
func (m *updateManager) fail(err error) {
	m.mu.Lock()
	m.status.Phase = "error"
	m.status.Message = err.Error()
	m.cancel = nil
	m.mu.Unlock()
}
func (m *updateManager) busy() bool {
	return m.status.Phase == "checking" || m.status.Phase == "downloading" || m.status.Phase == "installing"
}
func (m *updateManager) check(channel string) error {
	if channel != "stable" && channel != "beta" {
		return fmt.Errorf("неверный канал")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy() {
		return fmt.Errorf("операция обновления уже выполняется")
	}
	discardUpdateDownload(m.directory, m.base, filepath.Dir(m.status.Executable))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	m.cancel = cancel
	m.status.Channel = channel
	m.status.Release = nil
	m.status.Received = 0
	m.directory = ""
	m.status.Phase = "checking"
	m.status.Message = "Проверка GitHub Releases…"
	go func() {
		defer cancel()
		// Preferences are separate from role, routing and pairing configuration.
		if checkExistingUpdateParents(m.base) == nil && os.MkdirAll(m.base, 0700) == nil && noUpdateLinks(m.base) == nil {
			_ = atomicUpdateJSON(filepath.Join(m.base, "preferences.json"), map[string]string{"channel": channel})
		}
		release, err := m.client.discover(ctx, appVersion, channel)
		if err != nil {
			m.fail(err)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		m.cancel = nil
		m.status.Release = release
		if release == nil {
			m.status.Phase = "current"
			m.status.Message = "Подходящих обновлений в этом канале нет"
		} else {
			m.status.Phase = "available"
			m.status.Message = "Доступна версия " + release.Version
		}

	}()
	return nil
}
func (m *updateManager) download() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy() || m.status.Release == nil || (m.status.Phase != "available" && m.status.Phase != "error") {
		return fmt.Errorf("сначала проверьте обновления")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	dir := filepath.Join(m.base, hex.EncodeToString(id[:]))
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("недоступен каталог cache")
	}
	if err := checkExistingUpdateParents(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := noUpdateLinks(dir); err != nil {
		return err
	}
	if err := checkUpdateSpace(dir, uint64(4*updateMaxExpanded)); err != nil {
		return err
	}
	if err := atomicUpdateJSON(filepath.Join(dir, "download-owner.json"), updateDownloadOwner{Product: "LocalBroadcast", Root: filepath.Dir(m.status.Executable), ID: filepath.Base(dir), SHA256: m.status.Release.SHA256}); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.status.Phase = "downloading"
	m.status.Message = "Загрузка без остановки сервиса…"
	m.status.Received = 0
	release := *m.status.Release
	m.directory = dir
	go func() {
		defer cancel()
		completed := false
		defer func() {
			if !completed {
				discardUpdateDownload(dir, m.base, filepath.Dir(m.snapshot().Executable))
			}
		}()
		path := filepath.Join(dir, "package.zip")
		if err := m.client.download(ctx, release, path, func(n int64) { m.mu.Lock(); m.status.Received = n; m.mu.Unlock() }); err != nil {
			m.fail(err)
			return
		}
		if _, _, err := validateUpdatePackage(path, release.Version, release.SHA256, release.Size); err != nil {
			m.fail(err)
			return
		}
		if err := ctx.Err(); err != nil {
			m.fail(err)
			return
		}
		m.mu.Lock()
		m.cancel = nil
		completed = true
		m.status.Phase = "prepared"
		m.status.Message = "Пакет проверен. Можно установить и перезапустить программу."
		m.mu.Unlock()
	}()
	return nil
}
func (m *updateManager) cancelDownload() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

func updateHealth(c Configuration) error {
	for _, path := range []string{c.FFmpegPath, c.FFplayPath} {
		if !binaryAvailable(path) {
			return fmt.Errorf("не найден %s; сначала запустите полный установщик", filepath.Base(path))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath(c.FFmpegPath), "-hide_banner", "-protocols")
	prepareHeadlessCommand(cmd)
	b, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("не удалось проверить FFmpeg")
	}
	if !strings.Contains(strings.ReplaceAll(string(b), "\r", ""), "srt\n") {
		return fmt.Errorf("FFmpeg не поддерживает SRT: нужен полный установщик")
	}
	return nil
}
func (a *Application) updateSafety(interrupt bool) error {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("обновление поддерживает Windows x64")
	}
	if a.coordinator != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		obs, err := connectOBS(ctx, a.store.Get())
		if err != nil {
			running, inspectErr := updateOBSRunning()
			if inspectErr != nil || running {
				return fmt.Errorf("не получен статус работающего OBS; проверьте WebSocket и завершите запись/эфир")
			}
		} else {
			defer obs.ws.Close()
			for _, kind := range []string{"GetStreamStatus", "GetRecordStatus"} {
				b, err := obs.request(kind, nil)
				if err != nil {
					return fmt.Errorf("не удалось проверить запись/эфир OBS")
				}
				var s struct {
					Active bool `json:"outputActive"`
				}
				if err = jsonUnmarshalUpdate(b, &s); err != nil {
					return err
				}
				if s.Active {
					return fmt.Errorf("завершите запись и трансляцию OBS перед установкой")
				}
			}
		}
	}
	if !interrupt {
		s := a.media.Snapshot()
		active := s["preview"] == true
		for _, kind := range []string{"screen", "camera"} {
			if _, ok := s[kind].(map[string]any); ok {
				active = true
			}
		}
		if n, ok := s["receivers"].(int); ok && n > 0 {
			active = true
		}
		if active {
			return fmt.Errorf("передача активна: подтвердите её прерывание для обновления")
		}
	}
	return nil
}

func (a *Application) beginUpdate(interrupt bool) error {
	m := a.updates
	if m == nil {
		return fmt.Errorf("обновлятор недоступен")
	}
	m.mu.Lock()
	if m.status.Phase != "prepared" {
		m.mu.Unlock()
		return fmt.Errorf("сначала скачайте и проверьте пакет")
	}
	m.mu.Unlock()
	if err := a.updateSafety(interrupt); err != nil {
		return err
	}
	a.restartMu.Lock()
	defer a.restartMu.Unlock()
	if a.restarting {
		return fmt.Errorf("перезапуск уже выполняется")
	}
	m.mu.Lock()
	if m.status.Phase != "prepared" {
		m.mu.Unlock()
		return fmt.Errorf("пакет больше не готов")
	}
	m.status.Phase = "installing"
	m.status.Message = "Подготовка установки…"
	directory := m.directory
	release := *m.status.Release
	m.mu.Unlock()
	a.restarting = true
	go func() {
		if err := a.handoffUpdate(directory, release, interrupt); err != nil {
			a.updateStopping.Store(false)
			a.restartMu.Lock()
			a.restarting = false
			a.restartMu.Unlock()
			m.fail(err)
			a.logs.Add("Обновление: " + err.Error())
		}
	}()
	return nil
}
