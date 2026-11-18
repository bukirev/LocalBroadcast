package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

var instanceID = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

type Application struct {
	store       *ConfigStore
	coordinator *Coordinator
	agent       *Agent
	discover    *Discover
	media       *MediaManager
	logs        *LogBuffer
	bootMode    string
	obsMu       sync.RWMutex
	obsReady    bool
	obsError    string
	restartMu   sync.Mutex
	restarting  bool
	stop        context.CancelFunc
}

func configPath() string {
	var base string
	if runtime.GOOS == "windows" {
		base = os.Getenv("APPDATA")
	}
	if base == "" {
		home, err := os.UserConfigDir()
		if err == nil {
			base = home
		}
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "LocalBroadcast", "config.json")
}
func main() {
	noBrowser := flag.Bool("no-browser", false, "запустить в фоне без открытия окна управления или браузера")
	showControl := flag.Bool("show-control", false, "показать окно управления Windows")
	conf := flag.String("config", "", "путь к отдельному файлу конфигурации (для тестов)")
	parent := flag.Int("restart-parent", 0, "ожидать завершения предыдущего процесса")
	initConfig := flag.Bool("init-config", false, "создать или проверить конфигурацию без запуска приложения")
	flag.Parse()
	if *parent != 0 {
		if err := waitForParent(*parent); err != nil {
			log.Fatal(err)
		}
	}
	if !*initConfig && activateExistingDesktop() {
		return
	}
	path := *conf
	if path == "" {
		path = configPath()
	}
	store, err := newConfigStore(path)
	if err != nil {
		log.Fatal(err)
	}
	if *initConfig {
		return
	}
	logs := &LogBuffer{}
	media := newMediaManager(logs)
	discovery := &Discover{}
	app := &Application{store: store, discover: discovery, media: media, logs: logs, bootMode: store.Get().Mode}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.stop = stop
	cfg := store.Get()
	if cfg.Mode == "master" {
		app.coordinator = newCoordinator(store, logs)
		go func() {
			if err := app.coordinator.run(ctx); err != nil {
				logs.Add("Критическая ошибка координатора: " + err.Error())
			}
		}()
		go advertiseMaster(ctx, logs)
		go app.obsLoop(ctx)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				app.media.SyncMasterReceivers(app.coordinator.onlinePeers(), app.store.Get())
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		if cfg.AutoLaunchOBS {
			go func() {
				if err := openOBS(cfg); err != nil {
					logs.Add("Автозапуск OBS: " + err.Error())
				}
			}()
		}
	} else {
		app.agent = newAgent(store, discovery, media, logs)
		go listenDiscovery(ctx, discovery, logs)
		go app.agent.run(ctx)
	}
	logs.Add("Local Broadcast " + appVersion + "; режим: " + cfg.Mode)
	go func() {
		if err := app.serveUI(ctx); err != nil {
			logs.Add("Ошибка веб-панели: " + err.Error())
			log.Println(err)
			stop()
		}
	}()
	if err := app.runDesktop(ctx, !*noBrowser || *showControl); err != nil {
		logs.Add("Ошибка окна управления: " + err.Error())
		showDesktopError(err.Error())
		stop()
	}
	media.Close()
}
func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		_ = exec.Command("open", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}
func (app *Application) obsLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	lastErr := ""
	run := func() {
		c := app.store.Get()
		if !c.ObsAutoSync || app.coordinator == nil {
			return
		}
		cc, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		err := syncOBS(cc, c, app.coordinator.onlinePeers())
		if err != nil {
			app.setOBSStatus(false, err.Error())
			msg := err.Error()
			if msg != lastErr {
				app.logs.Add("OBS: " + msg)
			}
			lastErr = msg
		} else {
			app.setOBSStatus(true, "")
			if lastErr != "" {
				app.logs.Add("OBS восстановлен и источники синхронизированы")
			}
			lastErr = ""
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
func cleanName(s string) string { return strings.TrimSpace(s) }

func (app *Application) setOBSStatus(ready bool, errorText string) {
	app.obsMu.Lock()
	app.obsReady = ready
	app.obsError = errorText
	app.obsMu.Unlock()
}
func (app *Application) obsStatus() map[string]any {
	app.obsMu.RLock()
	defer app.obsMu.RUnlock()
	return map[string]any{"ready": app.obsReady, "error": app.obsError}
}
