package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func (a *Application) registerUpdateUI(mux *http.ServeMux) {
	mux.HandleFunc("/api/updates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method", 405)
			return
		}
		if a.updates == nil {
			http.Error(w, "updater unavailable", 503)
			return
		}
		jsonWrite(w, a.updates.snapshot())
	})
	for _, action := range []string{"check", "download", "cancel", "install"} {
		mux.HandleFunc("/api/updates/"+action, func(w http.ResponseWriter, r *http.Request) {
			if !requireUIWrite(w, r) {
				return
			}
			if a.updates == nil {
				http.Error(w, "updater unavailable", 503)
				return
			}
			var input struct {
				Channel   string `json:"channel"`
				Interrupt bool   `json:"interrupt"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			var err error
			switch action {
			case "check":
				err = a.updates.check(input.Channel)
			case "download":
				err = a.updates.download()
			case "cancel":
				a.updates.cancelDownload()
			case "install":
				err = a.beginUpdate(input.Interrupt)
			}
			if err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			jsonWrite(w, a.updates.snapshot())
		})
	}
}
func (a *Application) nativeUpdateFlow() {
	m := a.updates
	if m == nil {
		return
	}
	s := m.snapshot()
	if s.Phase == "checking" || s.Phase == "downloading" {
		if updateConfirm("Выполняется " + s.Message + "\nОтменить операцию?") {
			m.cancelDownload()
		}
		return
	}
	if s.Phase != "prepared" && s.Phase != "available" {
		if err := m.check(s.Channel); err != nil {
			showDesktopError(err.Error())
			return
		}
		s = waitUpdatePhase(m, "checking", 65*time.Second)
	}
	if s.Phase == "error" {
		showDesktopError(s.Message)
		return
	}
	if s.Release == nil {
		showUpdateNotice(s.Message)
		return
	}
	if s.Phase != "prepared" {
		if !updateConfirm(fmt.Sprintf("Установлена: %s\nКанал: %s\nДоступна: %s · %.1f МБ\n\nСкачать? Сервис продолжит работать.", appVersion, s.Channel, s.Release.Version, float64(s.Release.Size)/(1<<20))) {
			return
		}
		if err := m.download(); err != nil {
			showDesktopError(err.Error())
			return
		}
		s = waitUpdatePhase(m, "downloading", 16*time.Minute)
	}
	if s.Phase != "prepared" {
		showDesktopError(s.Message)
		return
	}
	if !updateConfirm("Пакет " + s.Release.Version + " проверен.\nУстановить и перезапустить Local Broadcast?\n\nСохраните изменения в панели браузера. Текущая передача/приём временно прервутся. Роль, ключ и маршруты сохранятся. Запись/эфир OBS нужно предварительно завершить.") {
		return
	}
	if err := a.beginUpdate(true); err != nil {
		showDesktopError(err.Error())
	}
}
func waitUpdatePhase(m *updateManager, phase string, timeout time.Duration) updateStatus {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := m.snapshot()
		if s.Phase != phase {
			return s
		}
		time.Sleep(100 * time.Millisecond)
	}
	return updateStatus{Phase: "error", Message: "Операция не завершилась вовремя; повторите проверку"}
}
