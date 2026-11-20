//go:build windows

package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Unlike short -t smoke tests, this uses the production supervisor and an
// indefinite FFmpeg invocation. The current field issue stalls PCM at ~0.35s
// while video remains live; continued AAC packet delivery alone is insufficient.
func TestPacketPCMLivePipelineConsumesForMinute(t *testing.T) {
	if os.Getenv("LB_TEST_AUDIO_LONG") != "1" {
		t.Skip("opt-in minute-long capture; plays a quiet control tone")
	}
	hc := http.Client{Timeout: 5 * time.Second}
	response, err := hc.Get("http://127.0.0.1:39888/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Config Configuration `json:"config"`
	}
	err = json.NewDecoder(response.Body).Decode(&state)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	c := state.Config
	player := exec.Command(binaryPath(c.FFplayPath), "-hide_banner", "-loglevel", "error", "-nodisp", "-autoexit", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=70", "-af", "volume=0.02")
	prepareHeadlessCommand(player)
	if err = player.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { player.Process.Kill(); player.Wait() }()

	sink, err := listenMediaTestUDP()
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	sink.SetReadBuffer(4 << 20)
	port, err := freeUDPLocalPort()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "last-eight-seconds.ts")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	captureDone := make(chan struct{})
	var packets atomic.Uint64
	go func() {
		defer close(captureDone)
		defer file.Close()
		b := make([]byte, 65535)
		for {
			n, _, err := sink.ReadFromUDP(b)
			if err != nil {
				return
			}
			if time.Since(start) > 52*time.Second {
				file.Write(b[:n])
			}
			for i := 0; i+188 <= n; i += 188 {
				if b[i] == 71 && (int(b[i+1]&31)<<8|int(b[i+2])) == 257 {
					packets.Add(1)
				}
			}
		}
	}()
	logs := &LogBuffer{}
	c.MediaBackend = "ffmpeg" // Exercise the native FFmpeg capture path.
	p, err := startPipeline("screen", port, c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	p.SetTargets([]Endpoint{{IP: "127.0.0.1", Port: sink.LocalAddr().(*net.UDPAddr).Port}})
	lastFrames := int64(0)
	lastPackets := uint64(0)
	for i := 0; i < 6; i++ {
		time.Sleep(10 * time.Second)
		p.mu.RLock()
		audio := p.audio
		p.mu.RUnlock()
		if audio == nil {
			t.Fatal("native timeline missing", logs.List())
		}
		s := audio.Snapshot()
		frames := s["clockFrames"].(int64)
		current := packets.Load()
		if frames-lastFrames < int64(captureSampleRate(s)*8) || current-lastPackets < 100 {
			t.Fatalf("PCM/AAC stopped after %ds: %+v packets=%d logs=%v", (i+1)*10, s, current, logs.List())
		}
		t.Logf("%ds PCM frames=%d AAC TS packets=%d skipped=%v missing=%v", (i+1)*10, frames, current, s["skippedFrames"], s["missingFrames"])
		lastFrames = frames
		lastPackets = current
	}
	p.Stop()
	sink.Close()
	<-captureDone
	decoder := exec.Command(binaryPath(c.FFmpegPath), "-v", "error", "-i", path, "-map", "0:a:0", "-f", "f32le", "pipe:1")
	prepareHeadlessCommand(decoder)
	pcm, err := decoder.Output()
	if err != nil {
		t.Fatal(err)
	}
	sum := 0.
	for i := 0; i+4 <= len(pcm); i += 4 {
		x := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[i:])))
		sum += x * x
	}
	rms := 0.
	if len(pcm) > 0 {
		rms = math.Sqrt(sum / float64(len(pcm)/4))
	}
	if len(pcm) == 0 || rms < 0.00001 {
		t.Fatalf("AAC carries silence at end of minute: samples=%d RMS=%g", len(pcm)/4, rms)
	}
	t.Logf("last 8s decoded AAC: samples=%d RMS=%g", len(pcm)/4, rms)
}

func captureSampleRate(s map[string]any) int { return s["sampleRate"].(int) }
