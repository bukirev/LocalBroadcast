package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGSTScreenOnWindows(t *testing.T) {
	if os.Getenv("LB_TEST_GST_CAPTURE") != "1" {
		t.Skip("opt-in physical Windows screen and loopback capture")
	}
	c := defaultConfig()
	c.ScreenWidth, c.ScreenHeight, c.ScreenFPS = 1280, 720, 30
	c.ScreenAudioMode = "wasapi"
	c.MediaBackend = "gstreamer"
	displays, err := captureDisplays()
	if err != nil || len(displays) == 0 {
		t.Fatal("monitor unavailable", err)
	}
	c.ScreenMonitor = displays[0].ID
	sink, err := listenMediaTestUDP()
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	port, err := freeUDPLocalPort()
	if err != nil {
		t.Fatal(err)
	}
	logs := &LogBuffer{}
	player := exec.Command("ffplay", "-nodisp", "-autoexit", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=20", "-af", "volume=0.1")
	prepareHeadlessCommand(player)
	if err := player.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { player.Process.Kill(); player.Wait() }()
	p, err := startPipeline("screen", port, c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	second, err := listenMediaTestUDP()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	targets := []Endpoint{{IP: "127.0.0.1", Port: sink.LocalAddr().(*net.UDPAddr).Port}, {IP: "127.0.0.1", Port: second.LocalAddr().(*net.UDPAddr).Port}}
	if os.Getenv("LB_TEST_GST_SRT") == "1" {
		c.Transport = "srt"
		for _, target := range targets {
			worker := startSRTReceiver(target.Port, c, logs)
			defer worker.stop()
		}
		p.SetTransport(targets, c)
	} else {
		p.SetTargets(targets)
	}
	file := filepath.Join(t.TempDir(), "sample.ts")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 65536)
	until := time.Now().Add(12 * time.Second)
	sink.SetReadDeadline(until)
	for time.Now().Before(until) {
		n, _, err := sink.ReadFromUDP(b)
		if err != nil {
			break
		}
		if _, err = f.Write(b[:n]); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	second.SetReadDeadline(time.Now().Add(time.Second))
	if n, _, e := second.ReadFromUDP(b); e != nil || n < 188 || b[0] != 0x47 {
		t.Fatal("second recipient missing MPEG-TS", e)
	}
	s := p.Snapshot()
	t.Log(s, logs.List())
	if s["audio"] == nil {
		t.Fatal("missing GStreamer audio telemetry")
	}
	if s["mediaBackend"] != "gstreamer" {
		t.Fatal("fallback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	meta, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "json", file).Output()
	var q struct {
		Streams []struct {
			Codec         string `json:"codec_name"`
			Width, Height int
		}
	}
	if err != nil || json.Unmarshal(meta, &q) != nil {
		t.Fatal(err, string(meta))
	}
	video, audio := false, false
	for _, v := range q.Streams {
		video = video || (v.Codec == "h264" && v.Width == 1280 && v.Height == 720)
		audio = audio || v.Codec == "aac"
	}
	if !video || !audio {
		t.Fatal(string(meta))
	}
	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-i", file, "-map", "0:a:0", "-af", "volumedetect", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "mean_volume:") {
			t.Log(line)
			var db float64
			if _, err := fmt.Sscanf(strings.Split(line, "mean_volume: ")[1], "%f", &db); err == nil && db > -80 {
				return
			}
		}
	}
	t.Fatal("tone not detected")
}
