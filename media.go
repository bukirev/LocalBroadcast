package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type MediaManager struct {
	mu           sync.Mutex
	closed       bool
	viewerLoops  []chan struct{}
	pipelines    map[string]*Pipeline
	viewer       *exec.Cmd
	viewerCancel context.CancelFunc
	viewerWanted bool
	lastPlan     Plan
	receivers    map[int]*transportWorker
	logs         *LogBuffer
}

func newMediaManager(logs *LogBuffer) *MediaManager {
	return &MediaManager{pipelines: map[string]*Pipeline{}, receivers: map[int]*transportWorker{}, logs: logs}
}
func (m *MediaManager) Summary() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := []string{}
	for kind, p := range m.pipelines {
		items = append(items, fmt.Sprintf("%s:%d", kind, p.TargetCount()))
	}
	if m.viewer != nil {
		items = append(items, "preview")
	}
	if len(items) == 0 {
		return "idle"
	}
	return strings.Join(items, ", ")
}
func (m *MediaManager) Snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	receiverHealth := map[string]any{}
	for port, w := range m.receivers {
		receiverHealth[strconv.Itoa(port)] = map[string]any{"reconnects": w.reconnects.Load(), "effectiveLatencyMs": w.effectiveLatency.Load()}
	}
	result := map[string]any{"screen": false, "camera": false, "preview": m.viewer != nil, "receivers": len(m.receivers), "srtHealth": receiverHealth}
	for kind, p := range m.pipelines {
		result[kind] = p.Snapshot()
	}
	return result
}
func (m *MediaManager) Apply(p Plan, c Configuration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.lastPlan = p
	// The coordinator owns the transport mode; all members must use the same one.
	if p.Transport == "srt" || p.Transport == "udp" {
		c.Transport = p.Transport
		c.SRTLatencyMS = p.SRTLatencyMS
		c.SRTAdaptive = p.SRTAdaptive
	}
	m.applySource("screen", p.Presenter, c)
	m.applySource("camera", p.Camera, c)
	m.applyViewer(p.ReceivePresenter, c)
	receiverPorts := []int{}
	if p.ReceivePresenter && c.Transport == "srt" {
		receiverPorts = append(receiverPorts, presenterPort)
	}
	m.syncReceiversLocked(receiverPorts, c)
}
func (m *MediaManager) applySource(kind string, source SourcePlan, c Configuration) {
	desired := source.Enabled && len(source.Targets) > 0
	old := m.pipelines[kind]
	if !desired {
		if old != nil {
			old.Stop()
			delete(m.pipelines, kind)
			m.logs.Add("Остановлена передача: " + kind)
		}
		return
	}
	// Capture settings are immutable per pipeline; subscribers can change without restarting FFmpeg.
	signature := pipelineSignature(kind, c)
	if old != nil && old.signature == signature {
		old.SetTransport(source.Targets, c)
		return
	}
	if old != nil {
		old.Stop()
		delete(m.pipelines, kind)
	}
	internalPort := 16900
	if kind == "camera" {
		internalPort = 16901
	}
	pipe, err := startPipeline(kind, internalPort, c, m.logs)
	if err != nil {
		m.logs.Add("Ошибка запуска " + kind + ": " + err.Error())
		return
	}
	pipe.signature = signature
	pipe.SetTransport(source.Targets, c)
	m.pipelines[kind] = pipe
	m.logs.Add("Запущена передача " + kind + fmt.Sprintf(" на %d получателей", len(source.Targets)))
}
func (m *MediaManager) applyViewer(needed bool, c Configuration) {
	if needed && !m.viewerWanted {
		m.viewerWanted = true
		ctx, cancel := context.WithCancel(context.Background())
		m.viewerCancel = cancel
		done := make(chan struct{})
		m.viewerLoops = append(m.viewerLoops, done)
		go func() { defer close(done); m.previewLoop(ctx, c) }()
	} else if !needed && m.viewerWanted {
		m.viewerWanted = false
		m.viewerCancel()
		m.viewerCancel = nil
		// CommandContext handles both cancellation before Start and a live child.
		m.viewer = nil
		m.logs.Add("Просмотр презентера остановлен")
	}
}
func (m *MediaManager) previewLoop(ctx context.Context, c Configuration) {
	for ctx.Err() == nil {
		args := []string{"-hide_banner", "-loglevel", "warning", "-fflags", "nobuffer", "-flags", "low_delay", "-framedrop",
			"-analyzeduration", "0", "-probesize", "32768", "-window_title", "Local Broadcast - Presenter",
			"-f", "mpegts", "-i", fmt.Sprintf("udp://0.0.0.0:%d?fifo_size=100000&overrun_nonfatal=1", presenterPort)}
		cmd := exec.CommandContext(ctx, binaryPath(c.FFplayPath), args...)
		cmd.WaitDelay = 2 * time.Second
		m.mu.Lock()
		if ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		m.viewer = cmd
		m.mu.Unlock()
		m.logs.Add("Запуск окна просмотра экрана презентера")
		err := cmd.Run()
		m.mu.Lock()
		if m.viewer == cmd {
			m.viewer = nil
		}
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		m.logs.Add("Окно просмотра закрылось: " + fmt.Sprint(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
func (m *MediaManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for kind, pipeline := range m.pipelines {
		pipeline.Stop()
		delete(m.pipelines, kind)
	}
	m.applyViewer(false, Configuration{})
	m.syncReceiversLocked(nil, Configuration{})
	viewers := append([]chan struct{}(nil), m.viewerLoops...)
	m.mu.Unlock()
	// Viewer cleanup takes m.mu; join after releasing it.
	for _, done := range viewers {
		<-done
	}
}

// OBS master listens to all remote SRT inputs and remuxes them to the same
// local UDP endpoints which the OBS sources have already been configured for.
func (m *MediaManager) SyncMasterReceivers(peers []Peer, c Configuration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	ports := []int{}
	if c.Transport == "srt" {
		gotPresenter := false
		for _, p := range peers {
			if !p.Online() {
				continue
			}
			if p.SendScreen && !gotPresenter {
				ports = append(ports, presenterPort)
				gotPresenter = true
			}
			if p.SendCamera {
				ports = append(ports, p.CameraPort)
			}
		}
	}
	m.syncReceiversLocked(ports, c)
}
func (m *MediaManager) syncReceiversLocked(ports []int, c Configuration) {
	desired := map[int]bool{}
	for _, p := range ports {
		desired[p] = true
	}
	for port, w := range m.receivers {
		if !desired[port] || w.latencyBase != c.SRTLatencyMS || w.ffmpeg != c.FFmpegPath || w.secret != c.Secret || w.adaptive != c.SRTAdaptive {
			w.stop()
			delete(m.receivers, port)
		}
	}
	for port := range desired {
		if m.receivers[port] == nil {
			w := startSRTReceiver(port, c, m.logs)
			m.receivers[port] = w
		}
	}
}
func pipelineSignature(kind string, c Configuration) string {
	var selected any
	if kind == "screen" {
		selected = []any{c.MediaBackend, c.FFmpegPath, c.ScreenFPS, c.ScreenFPSMode, c.ScreenWidth, c.ScreenHeight, c.ScreenBitrateK, c.ScreenAudioMode, c.ScreenAudioDevice, c.ScreenAudioEndpoint, c.ScreenMonitor, c.Encoder}
	}
	if kind == "camera" {
		selected = []any{c.MediaBackend, c.FFmpegPath, c.CameraFPS, c.CameraBitrateK, c.CameraWidth, c.CameraHeight, c.CameraDevice, c.CameraAudioDevice, c.Encoder}
	}
	b, _ := json.Marshal(selected)
	return string(b)
}
func binaryPath(cmd string) string {
	if strings.ContainsAny(cmd, "/\\") || filepath.IsAbs(cmd) {
		return cmd
	}
	// Prefer FFmpeg next to LocalBroadcast.exe; otherwise use PATH.
	me, err := os.Executable()
	if err == nil {
		name := cmd
		if filepath.Ext(name) == "" && strings.EqualFold(filepath.Ext(me), ".exe") {
			name += ".exe"
		}
		local := filepath.Join(filepath.Dir(me), name)
		if st, err := os.Stat(local); err == nil && !st.IsDir() {
			return local
		}
	}
	return cmd
}
func codecArgs(c Configuration, bitrateK, fps int) []string {
	args := []string{"-c:v", c.Encoder}
	switch c.Encoder {
	case "libx264":
		args = append(args, "-preset", "ultrafast", "-tune", "zerolatency")
	case "h264_nvenc":
		args = append(args, "-preset", "p2", "-tune", "ll")
	case "h264_qsv":
		args = append(args, "-preset", "veryfast")
	case "h264_amf":
		args = append(args, "-quality", "speed")
	}
	return append(args, "-pix_fmt", "yuv420p", "-bf", "0", "-g", strconv.Itoa(max(1, fps/2)),
		"-b:v", fmt.Sprintf("%dk", bitrateK), "-maxrate", fmt.Sprintf("%dk", bitrateK),
		"-bufsize", fmt.Sprintf("%dk", bitrateK/2))
}
func ffmpegArgs(kind string, c Configuration, localPort int) ([]string, error) {
	return ffmpegArgsPCM(kind, c, localPort, nil)
}

func ffmpegArgsPacketPCM(kind string, c Configuration, localPort int, pcm *AudioFormat) ([]string, error) {
	args, err := ffmpegArgsPCM(kind, c, localPort, pcm)
	if err != nil {
		return nil, err
	}
	start, end := -1, -1
	for i, v := range args {
		if v == "-thread_queue_size" && start < 0 {
			start = i
		}
		if v == "pipe:0" {
			end = i + 1
			break
		}
	}
	if start < 0 || end < 0 {
		return nil, fmt.Errorf("timestamped PCM input missing")
	}
	out := append([]string{}, args[:start]...)
	// Up to 320 ms for transient encoder stalls; the queue does not wait to fill.
	out = append(out, "-thread_queue_size", "32", "-f", "matroska", "-i", "pipe:0")
	return append(out, args[end:]...), nil
}
func ffmpegArgsPCM(kind string, c Configuration, localPort int, pcm *AudioFormat) ([]string, error) {
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-fflags", "+genpts"}
	var audioDevice string
	if kind == "screen" {
		args = append(args, "-f", "gdigrab", "-framerate", strconv.Itoa(screenCaptureFPS(c)), "-draw_mouse", "1")
		if c.ScreenMonitor != "" {
			displays, err := captureDisplays()
			if err != nil {
				return nil, err
			}
			region, err := captureRegionArgs(c.ScreenMonitor, displays)
			if err != nil {
				return nil, err
			}
			args = append(args, region...)
		}
		args = append(args, "-i", "desktop")
		audioDevice = c.ScreenAudioDevice
		if c.ScreenAudioMode == "off" {
			audioDevice = ""
		}
		if c.ScreenAudioMode == "wasapi" {
			audioDevice = ""
		}
	} else if kind == "camera" {
		if strings.TrimSpace(c.CameraDevice) == "" {
			return nil, fmt.Errorf("не выбрана камера: укажите имя DirectShow в настройках")
		}
		args = append(args, "-thread_queue_size", "256", "-f", "dshow", "-rtbufsize", "128M",
			"-video_size", fmt.Sprintf("%dx%d", c.CameraWidth, c.CameraHeight), "-framerate", strconv.Itoa(c.CameraFPS), "-i", "video="+c.CameraDevice)
		audioDevice = c.CameraAudioDevice
	} else {
		return nil, fmt.Errorf("unknown source %q", kind)
	}
	nativeAudio := kind == "screen" && c.ScreenAudioMode == "wasapi"
	haveAudio := strings.TrimSpace(audioDevice) != "" || nativeAudio
	if nativeAudio {
		spec := AudioFormat{PCM: "f32le", Rate: 48000, Channels: 2}
		if pcm != nil {
			spec = *pcm
		}
		args = append(args, "-thread_queue_size", "8", "-use_wallclock_as_timestamps", "1", "-f", spec.PCM, "-ar", strconv.Itoa(spec.Rate), "-ac", strconv.Itoa(spec.Channels), "-i", "pipe:0")
	} else if haveAudio {
		args = append(args, "-thread_queue_size", "256", "-f", "dshow", "-i", "audio="+audioDevice)
	}
	args = append(args, "-map", "0:v:0")
	if haveAudio {
		args = append(args, "-map", "1:a:0", "-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2", "-af", "aresample=async=1000:first_pts=0")
	} else {
		args = append(args, "-an")
	}
	bitrate, fps := c.ScreenBitrateK, screenCaptureFPS(c)
	if kind == "camera" {
		bitrate, fps = c.CameraBitrateK, c.CameraFPS
	}
	args = append(args, codecArgs(c, bitrate, fps)...)
	// Scale only presenter capture (avoid source desktop resolution varying per monitor).
	if kind == "screen" {
		args = append(args, "-vf", screenVideoFilter(c))
		if c.ScreenFPSMode == "adaptive" {
			args = append(args, "-fps_mode:v", "vfr", "-force_key_frames", "expr:gte(t,n_forced*0.5)")
		}
	}
	args = append(args, "-muxdelay", "0", "-muxpreload", "0", "-flush_packets", "1",
		"-f", "mpegts", fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", localPort))
	return args, nil
}

type Pipeline struct {
	kind                     string
	signature                string
	mu                       sync.RWMutex
	targets                  []*net.UDPAddr
	running                  bool
	cancel                   context.CancelFunc
	captureDone              chan struct{}
	incoming                 *net.UDPConn
	outgoing                 *net.UDPConn
	logs                     *LogBuffer
	relayCancel              map[string]context.CancelFunc
	relayPorts               map[string]int
	transport                string
	packetsForwarded         atomic.Uint64
	sendErrors               atomic.Uint64
	audio                    *pcmTimeline
	backend                  string
	gstAudioAt               time.Time
	gstAudioDB               float64
	requestedEncoder         string
	encoder                  string
	width, height, targetFPS int
	fpsMode                  string
}

func startPipeline(kind string, port int, c Configuration, logs *LogBuffer) (*Pipeline, error) {
	_, err := ffmpegArgs(kind, c, port)
	if err != nil {
		return nil, err
	}
	input, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return nil, err
	}
	output, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	// Socket capacity absorbs scheduler jitter; it does not wait to fill.
	if err := input.SetReadBuffer(4 * 1024 * 1024); err != nil {
		logs.Add("UDP receive buffer: " + err.Error())
	}
	if err := output.SetWriteBuffer(4 * 1024 * 1024); err != nil {
		logs.Add("UDP send buffer: " + err.Error())
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{kind: kind, cancel: cancel, captureDone: make(chan struct{}), incoming: input, outgoing: output, logs: logs, relayCancel: map[string]context.CancelFunc{}, relayPorts: map[string]int{}}
	if kind == "screen" {
		p.width, p.height = screenOutputSize(c)
		p.targetFPS = screenCaptureFPS(c)
		p.fpsMode = c.ScreenFPSMode
		if p.fpsMode == "" {
			p.fpsMode = "fixed"
		}
	} else {
		p.width = c.CameraWidth
		p.height = c.CameraHeight
		p.targetFPS = c.CameraFPS
		p.fpsMode = "fixed"
	}
	go p.forwardLoop(ctx)
	var backend MediaBackend = ffmpegBackend{}
	if c.MediaBackend == "gstreamer" {
		backend = gstBackend{}
	}
	go func() { defer close(p.captureDone); backend.Run(ctx, p, c, port) }()
	return p, nil
}
func (p *Pipeline) SetTargets(endpoints []Endpoint) {
	targets := make([]*net.UDPAddr, 0, len(endpoints))
	seen := map[string]bool{}
	for _, e := range endpoints {
		ip := net.ParseIP(e.IP)
		if ip == nil || ip.To4() == nil || e.Port < 1024 || e.Port > 65535 {
			continue
		}
		addr := &net.UDPAddr{IP: ip.To4(), Port: e.Port}
		if !seen[addr.String()] {
			targets = append(targets, addr)
			seen[addr.String()] = true
		}
	}
	p.mu.Lock()
	p.targets = targets
	p.mu.Unlock()
}
func (p *Pipeline) TargetCount() int { p.mu.RLock(); defer p.mu.RUnlock(); return len(p.targets) }
func (p *Pipeline) Running() bool    { p.mu.RLock(); defer p.mu.RUnlock(); return p.running }
func (p *Pipeline) Stop() {
	p.cancel()
	p.mu.Lock()
	var relays []context.CancelFunc
	for _, cancel := range p.relayCancel {
		relays = append(relays, cancel)
	}
	p.relayCancel = map[string]context.CancelFunc{}
	p.mu.Unlock()
	for _, cancel := range relays {
		cancel()
	}
	_ = p.incoming.Close()
	_ = p.outgoing.Close()
	if p.captureDone != nil {
		<-p.captureDone
	}
}
func (p *Pipeline) forwardLoop(ctx context.Context) {
	packet := make([]byte, 2048)
	for {
		_ = p.incoming.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, _, err := p.incoming.ReadFromUDP(packet)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			continue
		}
		p.packetsForwarded.Add(1)
		p.mu.RLock()
		peers := p.targets
		p.mu.RUnlock()
		// MPEG-TS datagrams are immutable; one encoding is copied to each recipient.
		for _, dst := range peers {
			if _, err := p.outgoing.WriteToUDP(packet[:n], dst); err != nil {
				p.sendErrors.Add(1)
			}
		}
	}
}
func (p *Pipeline) captureLoop(ctx context.Context, binary string, c Configuration, port int) {

	retry := time.Second
	p.mu.Lock()
	p.requestedEncoder = c.Encoder
	p.encoder = c.Encoder
	p.mu.Unlock()
	for ctx.Err() == nil {
		loopArgs, argsErr := ffmpegArgs(p.kind, c, port)
		if argsErr != nil {
			p.logs.Add("Настройки захвата: " + argsErr.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry):
				continue
			}
		}
		var pcmReader io.ReadCloser
		var audioCancel context.CancelFunc
		var audioDone <-chan struct{}
		if p.kind == "screen" && c.ScreenAudioMode == "wasapi" {
			audioCtx, cancel := context.WithCancel(ctx)
			audioCancel = cancel
			r, spec, err := openSystemLoopback(audioCtx, p.logs, c.ScreenAudioEndpoint)
			if err != nil {
				p.logs.Add("WASAPI: " + err.Error())
				cancel()
				select {
				case <-ctx.Done():
					return
				case <-time.After(retry):
					continue
				}
			}
			pcmReader = r
			p.mu.Lock()
			p.audio, _ = r.(*pcmTimeline)
			if p.audio != nil {
				audioDone = p.audio.done
			}
			p.mu.Unlock()
			packetStream, packetErr := newPCMPacketStream(p.audio, spec)
			if packetErr != nil {
				p.logs.Add("WASAPI packets: " + packetErr.Error())
				cancel()
				r.Close()
				return
			}
			pcmReader = packetStream
			p.logs.Add("WASAPI: передаём PCM с метками времени захвата")
			loopArgs, err = ffmpegArgsPacketPCM(p.kind, c, port, &spec)
			if err != nil {
				p.logs.Add("WASAPI args: " + err.Error())
				cancel()
				_ = r.Close()
				return
			}
		}
		cmd := exec.Command(binary, loopArgs...)
		cmd.WaitDelay = 2 * time.Second
		if pcmReader != nil {
			cmd.Stdin = pcmReader
		}
		prepareHeadlessCommand(cmd)
		// Keep last FFmpeg diagnostics without allocating unbounded memory.
		encoderFailed := false
		cmd.Stderr = &lineWriter{logs: p.logs, prefix: "FFmpeg/" + p.kind, observe: func(line string) {
			if hardwareEncoderInitFailed(c.Encoder, line) {
				encoderFailed = true
			}
		}}
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
		if err := cmd.Start(); err != nil {
			p.logs.Add("FFmpeg " + p.kind + " не стартовал: " + err.Error())
		} else {
			p.mu.Lock()
			p.running = true
			p.mu.Unlock()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-ctx.Done():
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				<-done
			case <-audioDone:
				// FFmpeg keeps encoding video after PCM EOF. Reopen WASAPI and
				// restart the encoder instead of silently sending video forever.
				if ctx.Err() == nil {
					p.logs.Add("WASAPI: захват звука завершён; перезапускаем захват и передачу")
				}
				_ = cmd.Process.Kill()
				<-done
			case err := <-done:
				if ctx.Err() == nil {
					p.logs.Add(fmt.Sprintf("FFmpeg %s завершился: %v", p.kind, err))
					if err != nil && encoderFailed {
						p.logs.Add(fmt.Sprintf("Кодировщик %s недоступен; переходим на CPU — x264 для %s", c.Encoder, p.kind))
						c.Encoder = "libx264"
						p.mu.Lock()
						p.encoder = c.Encoder
						p.mu.Unlock()
					}
				}
			}
		}
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
		if audioCancel != nil {
			audioCancel()
		}
		if pcmReader != nil {
			_ = pcmReader.Close()
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		if retry < 8*time.Second {
			retry *= 2
		}
	}
}

type lineWriter struct {
	logs    *LogBuffer
	prefix  string
	pending string
	observe func(string)
}

var sensitiveURL = regexp.MustCompile(`(?i)(passphrase=)[^&\s]+`)

func (w *lineWriter) Write(b []byte) (int, error) {
	s := w.pending + string(b)
	chunks := strings.Split(s, "\n")
	for _, line := range chunks[:len(chunks)-1] {
		line = strings.TrimSpace(line)
		if line != "" {
			if w.observe != nil {
				w.observe(line)
			}
			w.logs.Add(w.prefix + ": " + sensitiveURL.ReplaceAllString(line, "${1}[hidden]"))
		}
	}
	w.pending = chunks[len(chunks)-1]
	if len(w.pending) > 1500 {
		w.pending = w.pending[len(w.pending)-1500:]
	}
	return len(b), nil
}

func (p *Pipeline) Snapshot() map[string]any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := map[string]any{"mediaBackend": p.backend, "targets": len(p.targets), "running": p.running, "packetsForwarded": p.packetsForwarded.Load(), "sendErrors": p.sendErrors.Load(), "encoder": p.encoder, "requestedEncoder": p.requestedEncoder, "width": p.width, "height": p.height, "targetFPS": p.targetFPS, "fpsMode": p.fpsMode}
	if p.backend == "gstreamer" && !p.gstAudioAt.IsZero() {
		out["audio"] = map[string]any{"peakDB": p.gstAudioDB, "lastBufferAgeMs": time.Since(p.gstAudioAt).Milliseconds()}
	}
	if p.audio != nil {
		out["audio"] = p.audio.Snapshot()
	}
	return out
}
