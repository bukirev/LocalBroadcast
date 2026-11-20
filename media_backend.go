package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MediaBackend owns capture and encoding for one source. Routing and SRT stay in Pipeline.
type MediaBackend interface {
	Run(context.Context, *Pipeline, Configuration, int)
}
type ffmpegBackend struct{}

func (ffmpegBackend) Run(ctx context.Context, p *Pipeline, c Configuration, port int) {
	p.mu.Lock()
	p.backend = "ffmpeg"
	p.mu.Unlock()
	p.captureLoop(ctx, binaryPath(c.FFmpegPath), c, port)
}

type gstBackend struct{}

func gstBinary(name string) string {
	exe, _ := os.Executable()
	local := filepath.Join(filepath.Dir(exe), "gstreamer", "bin", name+".exe")
	if _, err := os.Stat(local); err == nil {
		return local
	}
	return name
}

func gstArgs(kind string, c Configuration, port int, encoder string) ([]string, error) {
	w, h := screenOutputSize(c)
	fps := screenCaptureFPS(c)
	rate := c.ScreenBitrateK
	var vid, audio []string
	if kind == "screen" {
		if c.ScreenFPSMode == "adaptive" {
			return nil, fmt.Errorf("адаптивный FPS использует FFmpeg")
		}
		displays, err := captureDisplays()
		if err != nil {
			return nil, err
		}
		vid = []string{"d3d11screencapturesrc", "show-cursor=true"}
		if c.ScreenMonitor != "" {
			d, err := selectedGSTDisplay(displays, c.ScreenMonitor)
			if err != nil {
				return nil, err
			}
			vid = append(vid, "monitor-handle="+strconv.FormatUint(uint64(d.handle), 10))
		} else if len(displays) > 1 {
			return nil, fmt.Errorf("захват рабочего стола из нескольких экранов использует FFmpeg; выберите отдельный экран")
		}
		vid = append(vid, "!", fmt.Sprintf("video/x-raw,framerate=%d/1", fps))
		switch c.ScreenAudioMode {
		case "wasapi":
			audio = []string{"wasapi2src", "loopback=true", "low-latency=true"}
			if c.ScreenAudioEndpoint != "" {
				audio = append(audio, "device="+c.ScreenAudioEndpoint)
			}
		case "dshow":
			audio = []string{"dshowaudiosrc", "device-name=" + c.ScreenAudioDevice}
		}
	} else {
		w, h, fps, rate = c.CameraWidth, c.CameraHeight, c.CameraFPS, c.CameraBitrateK
		caps := fmt.Sprintf(",width=%d,height=%d,framerate=%d/1", w, h, fps)
		vid = []string{"mfvideosrc", "device-name=" + c.CameraDevice, "!", "video/x-raw" + caps + ";image/jpeg" + caps, "!", "decodebin"}
		if c.CameraAudioDevice != "" {
			audio = []string{"dshowaudiosrc", "device-name=" + c.CameraAudioDevice}
		}
	}
	args := []string{"-m", "mpegtsmux", "name=mux", "alignment=7", "latency=40000000", "!", "udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", port), "sync=false", "async=false"}
	args = append(args, vid...)
	args = append(args, "!", "watchdog", "timeout=10000", "!", "queue", "max-size-buffers=3", "leaky=downstream", "!", "videoconvert", "!", "videoscale", "!", fmt.Sprintf("video/x-raw,format=NV12,width=%d,height=%d,pixel-aspect-ratio=1/1", w, h), "!", encoder, "bitrate="+strconv.Itoa(rate))
	if encoder == "x264enc" {
		args = append(args, "tune=zerolatency", "speed-preset=ultrafast", fmt.Sprintf("key-int-max=%d", max(1, fps/2)))
	} else {
		args = append(args, "low-latency=true", fmt.Sprintf("gop-size=%d", max(1, fps/2)))
	}
	args = append(args, "!", "h264parse", "config-interval=-1", "!", "queue", "max-size-time=200000000", "!", "mux.")
	if len(audio) > 0 {
		args = append(args, audio...)
		args = append(args, "provide-clock=false", "slave-method=resample", "!", "audioconvert", "!", "audioresample", "!", "audio/x-raw,rate=48000,channels=2", "!", "queue", "max-size-time=200000000", "!", "level", "name=lb_audio", "interval=500000000", "!", "avenc_aac", "bitrate=128000", "!", "aacparse", "!", "mux.")
	}
	return args, nil
}

func selectedGSTDisplay(ds []CaptureDisplay, id string) (CaptureDisplay, error) {
	for _, d := range ds {
		if d.ID == id {
			return d, nil
		}
	}
	return CaptureDisplay{}, fmt.Errorf("выбранный монитор недоступен")
}

func (gstBackend) Run(ctx context.Context, p *Pipeline, c Configuration, port int) {
	enc := "mfh264enc"
	for attempt := 0; ctx.Err() == nil; attempt++ {
		args, err := gstArgs(p.kind, c, port, enc)
		if err != nil {
			p.logs.Add("GStreamer: " + err.Error())
			break
		}
		cmd := exec.CommandContext(ctx, gstBinary("gst-launch-1.0"), args...)
		prepareHeadlessCommand(cmd)
		cmd.Stderr = &lineWriter{logs: p.logs, prefix: "GStreamer/" + p.kind}
		p.mu.Lock()
		p.backend = "gstreamer"
		p.gstAudioAt = time.Time{}
		p.requestedEncoder = c.Encoder
		p.encoder = enc
		p.mu.Unlock()
		reader, writer := io.Pipe()
		cmd.Stdout = writer
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer reader.Close()
			scan := bufio.NewScanner(reader)
			for scan.Scan() {
				line := scan.Text()
				if !strings.Contains(line, `from element "lb_audio" (element): level,`) {
					continue
				}
				_, peak, ok := strings.Cut(line, "peak=(GValueArray)<")
				if !ok {
					continue
				}
				peak, _, ok = strings.Cut(peak, ">")
				if !ok {
					continue
				}
				maxDB := -1000.0
				for _, ch := range strings.Split(peak, ",") {
					if db, e := strconv.ParseFloat(strings.TrimSpace(ch), 64); e == nil && db > maxDB {
						maxDB = db
					}
				}
				p.mu.Lock()
				p.gstAudioAt = time.Now()
				p.gstAudioDB = maxDB
				p.mu.Unlock()
			}
		}()
		started := time.Now()
		if err = cmd.Start(); err != nil {
			writer.Close()
			<-done
			p.logs.Add("GStreamer: " + err.Error())
			break
		}
		p.mu.Lock()
		p.running = true
		p.mu.Unlock()
		err = cmd.Wait()
		writer.Close()
		<-done
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		p.logs.Add(fmt.Sprintf("GStreamer %s завершился: %v", p.kind, err))
		if time.Since(started) > 30*time.Second {
			attempt = 0
		}
		if attempt >= 2 {
			break
		}
		enc = "x264enc"
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	if ctx.Err() == nil {
		p.logs.Add("Переходим на резервный захват FFmpeg")
		ffmpegBackend{}.Run(ctx, p, c, port)
	}
}
