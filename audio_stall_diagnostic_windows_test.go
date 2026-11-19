//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiagnosticNativeAudioWithoutDurationLimit(t *testing.T) {
	if os.Getenv("LB_DIAGNOSTIC_AUDIO_STALL") != "1" {
		t.Skip("opt-in live PCM stall diagnosis")
	}
	dir := os.Getenv("LB_DIAGNOSTIC_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute output path required")
	}
	os.MkdirAll(dir, 0700)
	hc := http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Get("http://127.0.0.1:39888/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Config Configuration `json:"config"`
	}
	err = json.NewDecoder(resp.Body).Decode(&state)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"wallclock", "sampleclock", "packetclock"} {
		if filter := os.Getenv("LB_DIAGNOSTIC_AUDIO_MODE"); filter != "" && filter != mode {
			continue
		}
		wallclock := mode == "wallclock"
		ctx, cancel := context.WithCancel(context.Background())
		logs := &LogBuffer{}
		r, format, err := openSystemLoopback(ctx, logs, state.Config.ScreenAudioEndpoint)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		sink, err := listenMediaTestUDP()
		if err != nil {
			cancel()
			r.Close()
			t.Fatal(err)
		}
		sink.SetReadBuffer(4 << 20)
		streamPath := filepath.Join(t.TempDir(), mode+".ts")
		capture, err := os.Create(streamPath)
		if err != nil {
			t.Fatal(err)
		}
		captureDone := make(chan struct{})
		var video, audio atomic.Uint64
		go func() {
			defer close(captureDone)
			defer capture.Close()
			b := make([]byte, 65535)
			for {
				n, _, err := sink.ReadFromUDP(b)
				if err != nil {
					return
				}
				capture.Write(b[:n])
				for i := 0; i+188 <= n; i += 188 {
					if b[i] != 71 {
						continue
					}
					pid := int(b[i+1]&31)<<8 | int(b[i+2])
					if pid == 256 {
						video.Add(1)
					}
					if pid == 257 {
						audio.Add(1)
					}
				}
			}
		}()
		args, err := ffmpegArgsPCM("screen", state.Config, sink.LocalAddr().(*net.UDPAddr).Port, &format)
		if err != nil {
			t.Fatal(err)
		}
		if !wallclock {
			for i, v := range args {
				if v == "-use_wallclock_as_timestamps" {
					args[i+1] = "0"
				}
			}
		}
		name := mode
		var input io.Reader = r
		if mode == "packetclock" {
			args, err = ffmpegArgsPacketPCM("screen", state.Config, sink.LocalAddr().(*net.UDPAddr).Port, &format)
			if err != nil {
				t.Fatal(err)
			}
			input, err = newPCMPacketStream(r.(*pcmTimeline), format)
			if err != nil {
				t.Fatal(err)
			}
		}
		file, err := os.Create(filepath.Join(dir, name+".log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, binaryPath(state.Config.FFmpegPath), args...)
		prepareHeadlessCommand(cmd)
		cmd.Stderr = file
		pipe, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		copyDone := make(chan error, 1)
		go func() { _, err := io.Copy(pipe, input); copyDone <- err; pipe.Close() }()
		wait := make(chan error, 1)
		go func() { wait <- cmd.Wait() }()
		var snapshots []map[string]any
		for i := 0; i < 12; i++ {
			time.Sleep(time.Second)
			s := r.(*pcmTimeline).Snapshot()
			s["videoPackets"] = video.Load()
			s["audioPackets"] = audio.Load()
			snapshots = append(snapshots, s)
			if i == 2 || i == 11 {
				t.Log(name, i+1, s)
			}
		}
		cancel()
		r.Close()
		pipe.Close()
		<-wait
		t.Log(name, "PCM pipe copy:", <-copyDone)
		file.Close()
		sink.Close()
		<-captureDone
		decoder := exec.Command(binaryPath(state.Config.FFmpegPath), "-v", "error", "-i", streamPath, "-map", "0:a:0", "-f", "f32le", "pipe:1")
		prepareHeadlessCommand(decoder)
		decoded, decodeErr := decoder.Output()
		sum := 0.
		for i := 0; i+4 <= len(decoded); i += 4 {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(decoded[i:])))
			sum += x * x
		}
		rms := 0.
		if len(decoded) > 0 {
			rms = math.Sqrt(sum / float64(len(decoded)/4))
		}
		t.Logf("%s decoded AAC samples=%d RMS=%g error=%v", mode, len(decoded)/4, rms, decodeErr)

		b, _ := json.MarshalIndent(snapshots, "", "  ")
		os.WriteFile(filepath.Join(dir, name+".json"), b, 0600)
	}
}

func TestDiagnosticActiveOutputLevels(t *testing.T) {
	if os.Getenv("LB_DIAGNOSTIC_AUDIO_STALL") != "1" {
		t.Skip("opt-in device levels diagnosis")
	}
	outputs, err := captureAudioOutputs()
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range outputs {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		var mu sync.Mutex
		sum := 0.
		samples := 0
		peak := 0.
		r, f, err := openNativeLoopbackSelected(ctx, &LogBuffer{}, output.ID, func(p audioCaptureProbe) {
			mu.Lock()
			defer mu.Unlock()
			for i := 0; i+4 <= len(p.PCM); i += 4 {
				x := float64(math.Float32frombits(binary.LittleEndian.Uint32(p.PCM[i:])))
				sum += x * x
				samples++
				peak = max(peak, math.Abs(x))
			}
		})
		if err != nil {
			cancel()
			t.Log(output.Name, err)
			continue
		}
		if f.PCM != "f32le" {
			r.Close()
			cancel()
			t.Log("unsupported measurement format", f)
			continue
		}
		io.CopyN(io.Discard, r, int64(f.Rate*f.Channels*4*2))
		r.Close()
		cancel()
		mu.Lock()
		rms := 0.
		if samples > 0 {
			rms = math.Sqrt(sum / float64(samples))
		}
		t.Logf("output=%s default=%v samples=%d RMS=%g peak=%g", output.Name, output.Default, samples, rms, peak)
		mu.Unlock()
	}
}
