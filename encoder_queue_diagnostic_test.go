package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type encoderQueueEvent struct {
	AtMS float64 `json:"atMs"`
	Line string  `json:"line"`
}
type encoderQueueTrace struct {
	start   time.Time
	pending string
	events  []encoderQueueEvent
}

func (w *encoderQueueTrace) Write(b []byte) (int, error) {
	lines := strings.Split(w.pending+string(b), "\n")
	w.pending = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		if strings.Contains(line, "demuxer") || strings.Contains(line, "muxer <-") {
			w.events = append(w.events, encoderQueueEvent{float64(time.Since(w.start)) / float64(time.Millisecond), line})
		}
	}
	return len(b), nil
}

// Opt-in capture-to-encoder trace. Does not contact OBS or persist media. Only
// FFmpeg's frame timestamps and their local arrival times are saved.
func TestDiagnosticCaptureEncoderQueue(t *testing.T) {
	if os.Getenv("LB_DIAGNOSTIC_ENCODER_QUEUE") != "1" {
		t.Skip("explicit desktop/audio timing diagnostic")
	}
	dir := os.Getenv("LB_DIAGNOSTIC_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute output directory required")
	}
	os.MkdirAll(dir, 0700)
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
	sink, err := listenMediaTestUDP()
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	sink.SetReadBuffer(4 << 20)
	go func() {
		buf := make([]byte, 65535)
		for {
			if _, _, err := sink.ReadFromUDP(buf); err != nil {
				return
			}
		}
	}()
	for _, audio := range []bool{false, true} {
		c := state.Config
		name := "video-only"
		c.ScreenAudioMode = "off"
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		var reader io.ReadCloser
		var format *AudioFormat
		if audio {
			name = "video-wasapi"
			c.ScreenAudioMode = "wasapi"
			var f AudioFormat
			reader, f, err = openSystemLoopback(ctx, &LogBuffer{}, c.ScreenAudioEndpoint)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			format = &f
		}
		args, err := ffmpegArgsPCM("screen", c, sink.LocalAddr().(*net.UDPAddr).Port, format)
		if audio {
			args, err = ffmpegArgsPacketPCM("screen", c, sink.LocalAddr().(*net.UDPAddr).Port, format)
		}
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		args = append([]string{"-debug_ts"}, args...)
		for i := range args {
			if args[i] == "warning" {
				args[i] = "info"
			}
		}
		args = append(args[:len(args)-1], "-t", "12", args[len(args)-1])
		trace := &encoderQueueTrace{start: time.Now()}
		cmd := exec.CommandContext(ctx, binaryPath(c.FFmpegPath), args...)
		cmd.Stderr = trace
		cmd.WaitDelay = time.Second
		if reader != nil {
			packets, e := newPCMPacketStream(reader.(*pcmTimeline), *format)
			if e != nil {
				t.Fatal(e)
			}
			cmd.Stdin = packets
		}
		prepareHeadlessCommand(cmd)
		err = cmd.Run()
		if reader != nil {
			reader.Close()
		}
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := json.MarshalIndent(trace.events, "", "  ")
		if err = os.WriteFile(filepath.Join(dir, name+"-trace.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d timing events, fps=%d bitrate=%d encoder=%s", name, len(trace.events), c.ScreenFPS, c.ScreenBitrateK, c.Encoder)
		for i := 0; i < min(4, len(trace.events)); i++ {
			t.Log(trace.events[i])
		}
	}
}
