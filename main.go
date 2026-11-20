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
	"sync/atomic"
	"syscall"
	"time"
)

var instanceID = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

type Application struct {
	updates        *updateManager
	updateStopping atomic.Bool
	startupBlocked atomic.Bool
	uiReady        chan struct{}
	desktopReady   chan struct{}
	store          *ConfigStore
	coordinator    *Coordinator
	agent          *Agent
	discover       *Discover
	media          *MediaManager
	logs           *LogBuffer
	bootMode       string
	obsMu          sync.RWMutex
	obsReady       bool
	obsError       string
	restartMu      sync.Mutex
	restarting     bool
	stop           context.CancelFunc
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
	updateHelper := flag.String("update-helper", "", "внутренний режим обновлятора")
	updateRecover := flag.Bool("update-recover", false, "восстановить незавершённое обновление")
	updateCandidate := flag.String("update-candidate", "", "проверка кандидата обновления")
	makeUpdate := flag.String("make-update-package", "", "собрать пакет обновления")
	updateNotes := flag.String("update-notes", "RELEASE_NOTES.md", "описание изменений для пакета")
	validateUpdate := flag.String("validate-update-package", "", "проверить локальный пакет обновления")
	flag.IntVar(&uiPort, "ui-port", 39888, "порт локальной панели (для изолированной диагностики)")
	packageVersion := flag.String("package-version", "", "ожидаемая версия сборки пакета")
	gstPackage := flag.String("prepare-gstreamer", "", "распаковать проверенный MSI GStreamer в профиль пользователя")
	flag.Parse()
	if *gstPackage != "" {
		if err := prepareGStreamer(context.Background(), *gstPackage); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *packageVersion != "" && *packageVersion != appVersion {
		log.Fatal("версия EXE отличается от версии пакета")
	}
	if uiPort < 1024 || uiPort > 65535 {
		log.Fatal("неверный UI port")
	}
	configureUpdateDesktopInstance(uiPort)
	if *makeUpdate != "" {
		exe, err := os.Executable()
		if err == nil {
			err = buildUpdatePackage(exe, *updateNotes, appVersion, *makeUpdate)
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if *validateUpdate != "" {
		hash, size, err := fileDigest(*validateUpdate)
		if err == nil {
			_, _, err = validateUpdatePackage(*validateUpdate, appVersion, hash, size)
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if *updateHelper != "" {
		if err := runUpdateProgress(func(report func(string)) error { return runUpdateHelper(*updateHelper, *updateRecover, report) }); err != nil {
			os.Exit(1)
		}
		return
	}
	if *updateCandidate != "" {
		if _, err := validateUpdateCandidate(*updateCandidate, *conf); err != nil {
			log.Fatal(err)
		}
	} else if !*initConfig {
		exe, _ := os.Executable()
		if err := updateStartupGate(exe); err != nil {
			showDesktopError(err.Error())
			return
		}
	}

	if *parent != 0 {
		if err := waitForParent(*parent); err != nil {
			log.Fatal(err)
		}
	}
	if !*initConfig && *updateCandidate == "" && activateExistingDesktop() {
		return
	}
	path := *conf
	if path == "" {
		path = configPath()
	}
	store, err := newConfigStore(path, *updateCandidate != "")
	if err != nil {
		log.Fatal(err)
	}
	if *initConfig {
		return
	}
	logs := &LogBuffer{}
	media := newMediaManager(logs)
	discovery := &Discover{}
	app := &Application{store: store, discover: discovery, media: media, logs: logs, bootMode: store.Get().Mode, updates: newUpdateManager(), uiReady: make(chan struct{}), desktopReady: make(chan struct{})}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.stop = stop
	if app.bootMode == "master" {
		app.coordinator = newCoordinator(store, logs)
	} else {
		app.agent = newAgent(store, discovery, media, logs)
	}
	app.startupBlocked.Store(*updateCandidate != "")
	go func() {
		if *updateCandidate != "" {
			if err := app.candidateHandshake(ctx, *updateCandidate); err != nil {
				app.logs.Add("Обновление: " + err.Error())
				stop()
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		app.startRole(ctx)
		if *updateCandidate != "" {
			if err := app.markUpdateRunning(*updateCandidate); err != nil {
				app.logs.Add(err.Error())
				stop()
			}
		}
	}()
	go func() {
		if err := app.serveUI(ctx); err != nil {
			logs.Add("Ошибка веб-панели: " + err.Error())
			log.Println(err)
			stop()
		}
	}()
	if err := app.runDesktop(ctx, *updateCandidate == "" && os.Getenv("LB_UPDATE_TEST_NO_UI") != "1" && (!*noBrowser || *showControl)); err != nil {
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

func (app *Application) startRole(ctx context.Context) {
	if os.Getenv("LB_UPDATE_TEST_OFFLINE") == "1" {
		return
	}
	cfg := app.store.Get()
	if app.coordinator != nil {
		go func() {
			if err := app.coordinator.run(ctx); err != nil {
				app.logs.Add("Критическая ошибка координатора: " + err.Error())
			}
		}()
		go advertiseMaster(ctx, app.logs)
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
					app.logs.Add("Автозапуск OBS: " + err.Error())
				}
			}()
		}
	} else {
		go listenDiscovery(ctx, app.discover, app.logs)
		go app.agent.run(ctx)
	}
	app.logs.Add("Local Broadcast " + appVersion + "; режим: " + cfg.Mode)
}
