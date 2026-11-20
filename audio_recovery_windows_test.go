//go:build windows

package main

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Inject capture EOF while the real encoder is still producing video. The
// process supervisor must reopen WASAPI and resume actual AAC packet delivery.
func TestWASAPICaptureEOFRecoversLiveAudio(t *testing.T) {
	if os.Getenv("LB_TEST_AUDIO") != "1" {
		t.Skip("use LB_TEST_AUDIO=1; captures the desktop on private local ports")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	sink, err := listenMediaTestUDP()
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	_ = sink.SetReadBuffer(4 << 20)
	port, err := freeUDPLocalPort()
	if err != nil {
		t.Fatal(err)
	}
	c := defaultConfig()
	c.FFmpegPath = ffmpeg
	c.ScreenAudioMode = "wasapi"
	outputs, err := captureAudioOutputs()
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range outputs {
		if output.Default {
			c.ScreenAudioEndpoint = output.ID
			t.Log("explicit audio output:", output.Name)
		}
	}
	displays, err := captureDisplays()
	if err != nil {
		t.Fatal(err)
	}
	for _, display := range displays {
		if display.Primary {
			c.ScreenMonitor = display.ID
			t.Log("explicit monitor:", display.Name)
		}
	}
	c.ScreenFPS = 15
	c.ScreenBitrateK = 2000
	logs := &LogBuffer{}
	c.MediaBackend = "ffmpeg" // Exercise the native FFmpeg capture path.
	p, err := startPipeline("screen", port, c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	p.SetTargets([]Endpoint{{IP: "127.0.0.1", Port: sink.LocalAddr().(*net.UDPAddr).Port}})
	var previous *pcmTimeline
	for phase := 0; phase < 3; phase++ {
		deadline := time.Now().Add(8 * time.Second)
		audioPackets := 0
		var current *pcmTimeline
		buf := make([]byte, 65535)
		for time.Now().Before(deadline) {
			p.mu.RLock()
			current = p.audio
			p.mu.RUnlock()
			_ = sink.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, _, _ := sink.ReadFromUDP(buf)
			if current == nil || current == previous {
				continue
			}
			for i := 0; i+188 <= n; i += 188 {
				if buf[i] == 0x47 && (int(buf[i+1]&31)<<8|int(buf[i+2])) == 257 {
					audioPackets++
				}
			}
			if audioPackets >= 12 {
				break
			}
		}
		if audioPackets < 12 {
			t.Fatalf("phase %d: no resumed AAC packets; logs=%v", phase, logs.List())
		}
		t.Logf("phase %d: %d real AAC TS packets received", phase, audioPackets)
		previous = current
		if phase < 2 {
			_ = current.Close()
			// Discard old queued datagrams before measuring the new encoder.
			time.Sleep(200 * time.Millisecond)
		}
	}
	if count := strings.Count(strings.Join(logs.List(), "\n"), "захват звука завершён"); count != 2 {
		t.Fatalf("expected two capture recovery events, got %d: %v", count, logs.List())
	}
}
