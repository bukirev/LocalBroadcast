//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDiagnosticNativePacketClock(t *testing.T) {
	if os.Getenv("LB_DIAGNOSTIC_PACKET_CLOCK") != "1" {
		t.Skip("native packet clock observer")
	}
	dir := os.Getenv("LB_DIAGNOSTIC_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute directory required")
	}
	os.MkdirAll(dir, 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var mu sync.Mutex
	var raw bytes.Buffer
	packets := []audioCaptureProbe{}
	r, f, err := openNativeLoopback(ctx, &LogBuffer{}, func(p audioCaptureProbe) {
		mu.Lock()
		raw.Write(p.PCM)
		p.PCM = nil
		packets = append(packets, p)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if f.PCM != "f32le" || f.Rate != 48000 || f.Channels != 2 {
		t.Fatalf("unexpected format %+v", f)
	}
	player := exec.CommandContext(ctx, binaryPath("ffplay"), "-hide_banner", "-loglevel", "error", "-nodisp", "-autoexit", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=22", "-af", "volume=0.4")
	prepareHeadlessCommand(player)
	if err = player.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { player.Process.Kill(); player.Wait() }()
	file, err := os.Create(filepath.Join(dir, "packet-clock-timeline.f32"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.CopyN(file, r, 24*48000*8); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	cancel()
	mu.Lock()
	data := append([]byte(nil), raw.Bytes()...)
	metadata := append([]audioCaptureProbe(nil), packets...)
	mu.Unlock()
	os.WriteFile(filepath.Join(dir, "packet-clock-device.f32"), data, 0600)
	b, _ := json.MarshalIndent(metadata, "", "  ")
	os.WriteFile(filepath.Join(dir, "packet-clock-trace.json"), b, 0600)
	b, _ = json.MarshalIndent(r.(*pcmTimeline).Snapshot(), "", "  ")
	os.WriteFile(filepath.Join(dir, "packet-clock-stats.json"), b, 0600)
	t.Log(string(b))
}
