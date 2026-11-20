package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	discoveryPort   = 39721
	coordinatorPort = 39780
	presenterPort   = 5000
	cameraPortStart = 5101
)

var appVersion = "0.3.9-beta.2"
var uiPort = 39888

type Configuration struct {
	Schema              int                        `json:"schema"`
	NodeID              string                     `json:"nodeId"`
	Name                string                     `json:"name"`
	Mode                string                     `json:"mode"` // master, presenter, camera, camera-view, viewer, custom
	Secret              string                     `json:"secret"`
	MasterIP            string                     `json:"masterIp"` // optional fallback to discovery
	AutoStart           bool                       `json:"autoStart"`
	LaunchAtLogin       bool                       `json:"launchAtLogin"`
	SendScreen          bool                       `json:"sendScreen"`
	SendCamera          bool                       `json:"sendCamera"`
	WatchPresenter      bool                       `json:"watchPresenter"`
	MediaBackend        string                     `json:"mediaBackend"`
	ScreenMonitor       string                     `json:"screenMonitor"`
	ScreenAudioEndpoint string                     `json:"screenAudioEndpoint"`
	ScreenWidth         int                        `json:"screenWidth"`
	ScreenHeight        int                        `json:"screenHeight"`
	ScreenFPSMode       string                     `json:"screenFpsMode"`
	ScreenFPS           int                        `json:"screenFps"`
	ScreenBitrateK      int                        `json:"screenBitrateK"`
	CameraFPS           int                        `json:"cameraFps"`
	CameraBitrateK      int                        `json:"cameraBitrateK"`
	CameraWidth         int                        `json:"cameraWidth"`
	CameraHeight        int                        `json:"cameraHeight"`
	CameraDevice        string                     `json:"cameraDevice"`
	ScreenAudioDevice   string                     `json:"screenAudioDevice"` // optional legacy DirectShow device
	ScreenAudioMode     string                     `json:"screenAudioMode"`   // wasapi, dshow, off
	CameraAudioDevice   string                     `json:"cameraAudioDevice"`
	Transport           string                     `json:"transport"` // udp or srt
	SRTLatencyMS        int                        `json:"srtLatencyMs"`
	SRTAdaptive         bool                       `json:"srtAdaptive"`
	SyncEnabled         bool                       `json:"syncEnabled"`
	SyncOffsets         map[string]SyncTuning      `json:"syncOffsets,omitempty"`
	Encoder             string                     `json:"encoder"` // libx264, h264_nvenc, h264_qsv, h264_amf
	FFmpegPath          string                     `json:"ffmpegPath"`
	FFplayPath          string                     `json:"ffplayPath"`
	ObsPassword         string                     `json:"obsPassword"`
	AutoLaunchOBS       bool                       `json:"autoLaunchObs"`
	ObsExePath          string                     `json:"obsExePath"`
	ObsURL              string                     `json:"obsUrl"`
	ObsScene            string                     `json:"obsScene"`
	ObsAutoSync         bool                       `json:"obsAutoSync"`
	Routing             map[string]map[string]bool `json:"routing,omitempty"`     // sourceID -> targetID -> enabled
	CameraPorts         map[string]int             `json:"cameraPorts,omitempty"` // stable mapping of nodeID -> OBS port
}

// Offsets can be calibrated with a visible flash and clap on each camera.
// Baseline network-delay equalisation is applied automatically when enabled.
type SyncTuning struct {
	VideoMS int `json:"videoMs"`
	AudioMS int `json:"audioMs"`
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func defaultConfig() Configuration {
	name, _ := os.Hostname()
	if name == "" {
		name = "Компьютер"
	}
	return Configuration{
		Schema: 2, NodeID: randomHex(8), Name: name, Mode: "viewer", Secret: randomHex(16),
		AutoStart: true, ScreenAudioMode: "wasapi", Transport: "udp", SRTLatencyMS: 180, SRTAdaptive: true, SyncEnabled: true, SyncOffsets: map[string]SyncTuning{}, ScreenWidth: 1920, ScreenHeight: 1080, ScreenFPSMode: "fixed", ScreenFPS: 30, ScreenBitrateK: 6000, CameraFPS: 30, CameraBitrateK: 2500,
		CameraWidth: 1280, CameraHeight: 720, Encoder: "libx264", FFmpegPath: "ffmpeg", FFplayPath: "ffplay",
		ObsURL: "ws://127.0.0.1:4455", ObsScene: "Local Broadcast", ObsAutoSync: true, AutoLaunchOBS: true,
		Routing: map[string]map[string]bool{}, CameraPorts: map[string]int{},
	}
}
func modeDefaults(c *Configuration) {
	switch c.Mode {
	case "master":
		c.SendScreen = false
		c.SendCamera = false
		c.WatchPresenter = false
	case "presenter":
		c.SendScreen = true
		c.SendCamera = false
		c.WatchPresenter = false
	case "camera":
		c.SendScreen = false
		c.SendCamera = true
		c.WatchPresenter = false
	case "camera-view":
		c.SendScreen = false
		c.SendCamera = true
		c.WatchPresenter = true
	case "viewer":
		c.SendScreen = false
		c.SendCamera = false
		c.WatchPresenter = true
	}
}
func validateConfig(c *Configuration) error {
	switch c.Mode {
	case "master", "presenter", "camera", "camera-view", "viewer", "custom":
	default:
		return fmt.Errorf("неизвестная роль: %q", c.Mode)
	}
	if len(strings.TrimSpace(c.Name)) == 0 || len(c.Name) > 72 {
		return errors.New("имя должно содержать 1–72 символа")
	}
	if len(c.Secret) < 16 {
		return errors.New("ключ системы должен содержать не менее 16 символов")
	}
	if c.ScreenWidth == 0 && c.ScreenHeight == 0 {
		c.ScreenWidth = 1920
		c.ScreenHeight = 1080
	}
	if c.ScreenFPSMode == "" {
		c.ScreenFPSMode = "fixed"
	}
	if c.ScreenFPSMode != "fixed" && c.ScreenFPSMode != "adaptive" {
		return errors.New("неподдерживаемый режим FPS экрана")
	}
	if !((c.ScreenWidth == 854 && c.ScreenHeight == 480) || (c.ScreenWidth == 1280 && c.ScreenHeight == 720) || (c.ScreenWidth == 1920 && c.ScreenHeight == 1080)) {
		return errors.New("выберите разрешение экрана 480p, 720p или 1080p")
	}
	if c.ScreenFPS < 5 || c.ScreenFPS > 60 || c.CameraFPS < 5 || c.CameraFPS > 60 {
		return errors.New("FPS должен быть в пределах 5–60")
	}
	if c.ScreenBitrateK < 300 || c.ScreenBitrateK > 25000 || c.CameraBitrateK < 300 || c.CameraBitrateK > 25000 {
		return errors.New("битрейт должен быть в пределах 300–25000 Кбит/с")
	}
	if c.CameraWidth < 320 || c.CameraWidth > 3840 || c.CameraHeight < 240 || c.CameraHeight > 2160 {
		return errors.New("некорректное разрешение камеры")
	}
	if c.MediaBackend != "" && c.MediaBackend != "ffmpeg" && c.MediaBackend != "gstreamer" {
		return errors.New("неизвестный медиадвижок")
	}
	switch c.Encoder {
	case "libx264", "h264_nvenc", "h264_qsv", "h264_amf":
	default:
		return errors.New("неподдерживаемый кодировщик")
	}
	if len(c.ScreenMonitor) > 128 || len(c.ScreenAudioEndpoint) > 1024 || strings.ContainsRune(c.ScreenMonitor, 0) || strings.ContainsRune(c.ScreenAudioEndpoint, 0) {
		return errors.New("некорректный идентификатор устройства захвата")
	}
	switch c.ScreenAudioMode {
	case "wasapi", "dshow", "off":
	default:
		return errors.New("неизвестный режим захвата системного звука")
	}
	if c.Transport != "udp" && c.Transport != "srt" {
		return errors.New("транспорт должен быть udp или srt")
	}
	if c.SRTLatencyMS < 80 || c.SRTLatencyMS > 1500 {
		return errors.New("SRT latency должна быть 80–1500 мс")
	}
	for id, v := range c.SyncOffsets {
		if len(id) > 140 || v.VideoMS < -2000 || v.VideoMS > 10000 || v.AudioMS < -950 || v.AudioMS > 10000 {
			return errors.New("некорректное смещение синхронизации")
		}
	}
	if c.FFmpegPath == "" || c.FFplayPath == "" {
		return errors.New("не заданы пути к FFmpeg/FFplay")
	}
	if c.ObsURL == "" || c.ObsScene == "" {
		return errors.New("не заданы настройки OBS")
	}
	if c.NodeID == "" {
		return errors.New("отсутствует ID компьютера")
	}
	if c.Routing == nil {
		c.Routing = map[string]map[string]bool{}
	}
	if c.CameraPorts == nil {
		c.CameraPorts = map[string]int{}
	}
	if c.SyncOffsets == nil {
		c.SyncOffsets = map[string]SyncTuning{}
	}
	return nil
}

type ConfigStore struct {
	mu   sync.RWMutex
	path string
	cfg  Configuration
}

func newConfigStore(path string, readOnly ...bool) (*ConfigStore, error) {
	create := len(readOnly) == 0 || !readOnly[0]
	s := &ConfigStore{path: path, cfg: defaultConfig()}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &s.cfg); err != nil {
			return nil, fmt.Errorf("ошибка чтения настроек %s: %w", path, err)
		}
	} else if !create || !os.IsNotExist(err) {
		return nil, err
	}
	if s.cfg.Routing == nil {
		s.cfg.Routing = map[string]map[string]bool{}
	}
	if s.cfg.CameraPorts == nil {
		s.cfg.CameraPorts = map[string]int{}
	}
	if s.cfg.SyncOffsets == nil {
		s.cfg.SyncOffsets = map[string]SyncTuning{}
	}
	// Upgrade 0.1.0 configuration without destroying device identities and routing.
	if s.cfg.Schema < 2 {
		s.cfg.Schema = 2
		s.cfg.ScreenAudioMode = "wasapi"
		s.cfg.Transport = "udp"
		s.cfg.SRTLatencyMS = 180
		s.cfg.SRTAdaptive = true
		s.cfg.SyncEnabled = true
	}
	if err = validateConfig(&s.cfg); err != nil {
		return nil, err
	}
	if create && os.IsNotExist(errForFile(path)) {
		if err := s.writeLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func errForFile(path string) error { _, err := os.Stat(path); return err }
func cloneConfig(c Configuration) Configuration {
	out := c
	out.Routing = make(map[string]map[string]bool, len(c.Routing))
	for k, v := range c.Routing {
		out.Routing[k] = make(map[string]bool, len(v))
		for a, b := range v {
			out.Routing[k][a] = b
		}
	}
	out.SyncOffsets = make(map[string]SyncTuning, len(c.SyncOffsets))
	for k, v := range c.SyncOffsets {
		out.SyncOffsets[k] = v
	}
	out.CameraPorts = make(map[string]int, len(c.CameraPorts))
	for k, v := range c.CameraPorts {
		out.CameraPorts[k] = v
	}
	return out
}
func (s *ConfigStore) Get() Configuration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.cfg)
}
func (s *ConfigStore) Update(c Configuration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.NodeID = s.cfg.NodeID
	c.Schema = 2
	// The master-only route/port registries are not editable through generic settings.
	c.Routing = s.cfg.Routing
	c.CameraPorts = s.cfg.CameraPorts
	if err := validateConfig(&c); err != nil {
		return err
	}
	prev := s.cfg
	s.cfg = cloneConfig(c)
	if err := s.writeLocked(); err != nil {
		s.cfg = prev
		return err
	}
	return nil
}
func (s *ConfigStore) SetRoute(source, target string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Routing[source] == nil {
		s.cfg.Routing[source] = map[string]bool{}
	}
	s.cfg.Routing[source][target] = enabled
	return s.writeLocked()
}
func (s *ConfigStore) SetSyncOffset(id string, tune SyncTuning) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(id) < 4 || len(id) > 140 || tune.VideoMS < -2000 || tune.VideoMS > 10000 || tune.AudioMS < -950 || tune.AudioMS > 10000 {
		return errors.New("некорректная синхронизация")
	}
	if s.cfg.SyncOffsets == nil {
		s.cfg.SyncOffsets = map[string]SyncTuning{}
	}
	s.cfg.SyncOffsets[id] = tune
	return s.writeLocked()
}
func (s *ConfigStore) AssignCameraPort(id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.cfg.CameraPorts[id]; p != 0 {
		return p, nil
	}
	used := map[int]bool{}
	for _, v := range s.cfg.CameraPorts {
		used[v] = true
	}
	p := cameraPortStart
	for used[p] {
		p++
	}
	if p > 5999 {
		return 0, errors.New("закончились порты камер")
	}
	s.cfg.CameraPorts[id] = p
	return p, s.writeLocked()
}
func (s *ConfigStore) writeLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(s.path)
		return os.Rename(tmp, s.path)
	}
	return nil
}

type Peer struct {
	RTTMS          int       `json:"rttMs"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Mode           string    `json:"mode"`
	IP             string    `json:"ip"`
	CameraPort     int       `json:"cameraPort"`
	WatchPresenter bool      `json:"watchPresenter"`
	SendScreen     bool      `json:"sendScreen"`
	SendCamera     bool      `json:"sendCamera"`
	LastSeen       time.Time `json:"lastSeen"`
	MediaStatus    string    `json:"mediaStatus"`
	Version        string    `json:"version"`
}

func (p Peer) Online() bool { return time.Since(p.LastSeen) < 8*time.Second }

type Endpoint struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	ID   string `json:"id"`
	Name string `json:"name"`
}
type SourcePlan struct {
	Enabled bool       `json:"enabled"`
	Targets []Endpoint `json:"targets"`
}
type Plan struct {
	Presenter        SourcePlan `json:"presenter"`
	Camera           SourcePlan `json:"camera"`
	ReceivePresenter bool       `json:"receivePresenter"`
	PresenterPort    int        `json:"presenterPort"`
	MasterIP         string     `json:"masterIp"`
	Transport        string     `json:"transport"`
	SRTLatencyMS     int        `json:"srtLatencyMs"`
	SRTAdaptive      bool       `json:"srtAdaptive"`
	Generation       int64      `json:"generation"`
}
type DisplayRoute struct {
	SourceID string `json:"sourceId"`
	Source   string `json:"source"`
	TargetID string `json:"targetId"`
	Target   string `json:"target"`
	Enabled  bool   `json:"enabled"`
	Default  bool   `json:"default"`
}

func sortedPeers(m map[string]Peer) []Peer {
	out := make([]Peer, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}
