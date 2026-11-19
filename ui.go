package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

//go:embed assets/*
var uiFiles embed.FS

func (a *Application) serveUI(ctx context.Context) error {
	mux := http.NewServeMux()
	a.registerUpdateUI(mux)
	mux.Handle("/assets/", http.FileServer(http.FS(uiFiles)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		body, err := uiFiles.ReadFile("assets/index.html")
		if err != nil {
			http.Error(w, "UI assets missing", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method", 405)
			return
		}
		cfg := a.store.Get()
		status := map[string]any{"config": cfg, "version": appVersion, "bootMode": a.bootMode, "readyForWork": !a.startupBlocked.Load() && !a.updateStopping.Load(), "restartRequired": restartRequired(a.bootMode, cfg.Mode), "instanceId": instanceID,
			"localIP": lanIP(), "logs": a.logs.List(), "media": a.media.Snapshot(), "ffmpegFound": binaryAvailable(cfg.FFmpegPath), "ffplayFound": binaryAvailable(cfg.FFplayPath)}
		if a.updates != nil {
			status["updates"] = a.updates.snapshot()
		}
		if a.coordinator != nil {
			status["peers"] = a.coordinator.peersSnapshot()
			status["routes"] = a.coordinator.routes()
			status["sync"] = plannedSyncOffsets(cfg, a.coordinator.onlinePeers())
			status["obs"] = a.obsStatus()
			status["system"] = "master"
		} else if a.agent != nil {
			status["agent"] = a.agent.Status()
			status["discovered"] = a.discover.Get()
			status["system"] = "client"
		}
		jsonWrite(w, status)
	})
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if !requireUIWrite(w, r) {
			return
		}
		var c Configuration
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		previous := a.store.Get()
		if previous.LaunchAtLogin != c.LaunchAtLogin {
			if err := setAutoRun(c.LaunchAtLogin); err != nil {
				http.Error(w, "Автозапуск Windows: "+err.Error(), 500)
				return
			}
		}
		// A selected preset controls capabilities; custom mode allows manual toggles.
		if c.Mode != "custom" {
			modeDefaults(&c)
		}
		if err := a.store.Update(c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		changedBootRole := restartRequired(a.bootMode, c.Mode)
		if changedBootRole {
			a.logs.Add("Нужен перезапуск LocalBroadcast для переключения главного режима")
		}
		jsonWrite(w, map[string]any{"ok": true, "restartRequired": changedBootRole})
	})
	mux.HandleFunc("/api/route", func(w http.ResponseWriter, r *http.Request) {
		if !requireUIWrite(w, r) {
			return
		}
		if a.coordinator == nil {
			http.Error(w, "маршруты доступны только на главном ПК", 403)
			return
		}
		var rq struct {
			SourceID string `json:"sourceId"`
			TargetID string `json:"targetId"`
			Enabled  bool   `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&rq); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if len(rq.SourceID) < 4 || len(rq.SourceID) > 140 || len(rq.TargetID) < 2 || len(rq.TargetID) > 128 {
			http.Error(w, "invalid route", 400)
			return
		}
		if err := a.store.SetRoute(rq.SourceID, rq.TargetID, rq.Enabled); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonWrite(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/sync", func(w http.ResponseWriter, r *http.Request) {
		if !requireUIWrite(w, r) {
			return
		}
		if a.coordinator == nil {
			http.Error(w, "синхронизация только на главном ПК", 403)
			return
		}
		var rq struct {
			SourceID string `json:"sourceId"`
			VideoMS  int    `json:"videoMs"`
			AudioMS  int    `json:"audioMs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&rq); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		valid := false
		for id := range plannedSyncOffsets(a.store.Get(), a.coordinator.onlinePeers()) {
			if id == rq.SourceID {
				valid = true
			}
		}
		if !valid {
			http.Error(w, "источник не найден", 404)
			return
		}
		if err := a.store.SetSyncOffset(rq.SourceID, SyncTuning{VideoMS: rq.VideoMS, AudioMS: rq.AudioMS}); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		jsonWrite(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		if !requireUIWrite(w, r) {
			return
		}
		c := a.store.Get()
		cc, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cc, binaryPath(c.FFmpegPath), "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
		output, _ := cmd.CombinedOutput() // FFmpeg exits nonzero after listing devices.
		out := string(output)
		if len(out) > 12000 {
			out = out[len(out)-12000:]
		}
		displays, displayErr := captureDisplays()
		audioOutputs, audioErr := captureAudioOutputs()
		warnings := []string{}
		if displayErr != nil {
			warnings = append(warnings, displayErr.Error())
		}
		if audioErr != nil {
			warnings = append(warnings, audioErr.Error())
		}
		jsonWrite(w, map[string]any{"output": out, "displays": displays, "audioOutputs": audioOutputs, "warnings": warnings})
	})
	mux.HandleFunc("/api/action", func(w http.ResponseWriter, r *http.Request) {
		if !requireUIWrite(w, r) {
			return
		}
		var rq struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&rq); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		switch rq.Action {
		case "shutdown":
			jsonWrite(w, map[string]bool{"ok": true})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if a.stop != nil {
				a.stop()
			}
			return
		case "restart":
			if err := a.requestRestart(); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			jsonWrite(w, map[string]bool{"ok": true})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			a.stop()
			return
		case "obs-sync":
			if a.coordinator == nil {
				http.Error(w, "OBS доступен только на главном ПК", 403)
				return
			}
			cc, cancel := context.WithTimeout(r.Context(), 6*time.Second)
			defer cancel()
			if err := syncOBS(cc, a.store.Get(), a.coordinator.onlinePeers()); err != nil {
				a.setOBSStatus(false, err.Error())
				http.Error(w, err.Error(), 502)
				return
			}
			a.setOBSStatus(true, "")
			a.logs.Add("Источники OBS синхронизированы вручную")
		case "open-obs":
			// This only opens the OBS interface if it is installed and in PATH.
			if err := openOBS(a.store.Get()); err != nil {
				http.Error(w, "OBS не найден в PATH: "+err.Error(), 400)
				return
			}
		default:
			http.Error(w, "unknown action", 400)
			return
		}
		jsonWrite(w, map[string]bool{"ok": true})
	})
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", uiPort), Handler: securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && (a.startupBlocked.Load() || a.updateStopping.Load()) {
			http.Error(w, "Программа ожидает завершения обновления", 503)
			return
		}
		mux.ServeHTTP(w, r)
	})), ReadHeaderTimeout: 4 * time.Second}
	lis, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	if a.uiReady != nil {
		close(a.uiReady)
	}
	a.logs.Add(fmt.Sprintf("Панель управления: http://127.0.0.1:%d", uiPort))
	go func() {
		<-ctx.Done()
		cc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(cc)
	}()
	if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
func binaryAvailable(path string) bool { _, err := exec.LookPath(binaryPath(path)); return err == nil }
func requireUIWrite(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return false
	}
	// Require a non-simple header: cross-origin forms cannot silently mutate localhost.
	if r.Header.Get("X-LB-UI") != "1" {
		http.Error(w, "missing local UI header", 403)
		return false
	}
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "JSON required", 415)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !strings.EqualFold(origin, fmt.Sprintf("http://127.0.0.1:%d", uiPort)) {
		http.Error(w, "origin not allowed", 403)
		return false
	}
	return true
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != fmt.Sprintf("127.0.0.1:%d", uiPort) && r.Host != fmt.Sprintf("localhost:%d", uiPort) {
			http.Error(w, "untrusted host", 403)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
func readUpTo(r io.Reader, max int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, max))
	return string(b)
}
