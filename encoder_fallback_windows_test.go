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

func TestUnavailableQSVFallsBackToLiveAV(t *testing.T) {
	if os.Getenv("LB_TEST_AUDIO") != "1" {
		t.Skip("use LB_TEST_AUDIO=1; captures local screen/audio on private ports")
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
	sink.SetReadBuffer(4 << 20)
	port, err := freeUDPLocalPort()
	if err != nil {
		t.Fatal(err)
	}
	c := defaultConfig()
	c.FFmpegPath = ffmpeg
	c.Encoder = "h264_qsv"
	c.ScreenAudioMode = "wasapi"
	c.ScreenFPS = 30
	c.ScreenWidth = 1280
	c.ScreenHeight = 720
	c.ScreenFPSMode = "adaptive"
	c.ScreenBitrateK = 2000
	logs := &LogBuffer{}
	c.MediaBackend = "ffmpeg" // Exercise the native FFmpeg capture path.
	p, err := startPipeline("screen", port, c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	p.SetTargets([]Endpoint{{IP: "127.0.0.1", Port: sink.LocalAddr().(*net.UDPAddr).Port}})
	buf := make([]byte, 65535)
	video, audio := 0, 0
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sink.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, _ := sink.ReadFromUDP(buf)
		s := p.Snapshot()
		if s["encoder"] == "h264_qsv" && n > 0 {
			t.Skip("QSV works on this hardware; missing-device fallback cannot be exercised")
		}
		if s["encoder"] != "libx264" {
			continue
		}
		for i := 0; i+188 <= n; i += 188 {
			if buf[i] != 0x47 {
				continue
			}
			pid := int(buf[i+1]&31)<<8 | int(buf[i+2])
			if pid == 256 {
				video++
			}
			if pid == 257 {
				audio++
			}
		}
		if video >= 30 && audio >= 12 {
			if s["width"] != 1280 || s["height"] != 720 || s["fpsMode"] != "adaptive" {
				t.Fatal("fallback lost screen quality profile")
			}
			if s["requestedEncoder"] != "h264_qsv" || c.Encoder != "h264_qsv" {
				t.Fatal("fallback overwrote requested configuration")
			}
			if strings.Count(strings.Join(logs.List(), "\n"), "переходим на CPU") != 1 {
				t.Fatal("fallback repeated or missing")
			}
			t.Logf("real QSV failure → x264: video TS packets=%d audio=%d; requested=%v effective=%v", video, audio, s["requestedEncoder"], s["encoder"])
			return
		}
	}
	t.Fatalf("no recovered A/V after QSV failure: %v", logs.List())
}
