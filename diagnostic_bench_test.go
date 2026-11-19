package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in laboratory bridge. Python controls the OBS screenshot probe; the media
// forwarding and SRT workers below are the actual production implementations.
func TestMediaDiagnosticBridge(t *testing.T) {
	if os.Getenv("LB_DIAGNOSTIC_BRIDGE") != "1" {
		t.Skip("laboratory control server")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	fmt.Printf("LB_BENCH_CONTROL=%d\n", listener.Addr().(*net.TCPAddr).Port)
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	encoder := json.NewEncoder(conn)
	var pipeline *Pipeline
	var receiver *transportWorker
	stop := func() {
		if pipeline != nil {
			pipeline.Stop()
			pipeline = nil
		}
		if receiver != nil {
			receiver.stop()
			receiver = nil
		}
	}
	defer stop()
	for scanner.Scan() {
		var rq struct {
			Action    string `json:"action"`
			Transport string `json:"transport"`
			Port      int    `json:"port"`
			Latency   int    `json:"latency"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &rq); err != nil {
			t.Fatal(err)
		}
		if rq.Action == "quit" {
			encoder.Encode(map[string]any{"ok": true})
			return
		}
		if rq.Action == "stats" {
			if pipeline != nil {
				encoder.Encode(pipeline.Snapshot())
			} else {
				encoder.Encode(map[string]any{})
			}
			continue
		}
		if rq.Action == "stop" {
			stop()
			encoder.Encode(map[string]any{"ok": true})
			continue
		}
		if rq.Action != "start" || rq.Port < 20000 || rq.Port > 40000 || (rq.Transport != "srt" && rq.Transport != "udp") {
			t.Fatal("invalid laboratory request")
		}
		stop()
		logs := &LogBuffer{}
		c := defaultConfig()
		c.FFmpegPath = ffmpeg
		c.Transport = rq.Transport
		c.SRTLatencyMS = rq.Latency
		c.SRTAdaptive = false
		if rq.Transport == "srt" {
			receiver = startSRTReceiver(rq.Port, c, logs)
		}
		input, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if e != nil {
			t.Fatal(e)
		}
		output, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if e != nil {
			input.Close()
			t.Fatal(e)
		}
		input.SetReadBuffer(4 << 20)
		output.SetWriteBuffer(4 << 20)
		ctx, cancel := context.WithCancel(context.Background())
		pipeline = &Pipeline{incoming: input, outgoing: output, cancel: cancel, logs: logs, relayCancel: map[string]context.CancelFunc{}, relayPorts: map[string]int{}}
		pipeline.SetTransport([]Endpoint{{IP: "127.0.0.1", Port: rq.Port}}, c)
		go pipeline.forwardLoop(ctx)
		encoder.Encode(map[string]any{"inputPort": input.LocalAddr().(*net.UDPAddr).Port})
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

type diagnosticTone struct {
	ctx   context.Context
	epoch time.Time
	next  int64
}

func (r *diagnosticTone) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, io.EOF
		}
		due := int64(time.Since(r.epoch).Seconds() * 48000)
		frames := min(due-r.next, int64(len(p)/8), 240)
		if frames > 0 {
			for i := int64(0); i < frames; i++ {
				value := float32(0.1 * math.Sin(2*math.Pi*1000*float64(r.next+i)/48000))
				bits := math.Float32bits(value)
				binary.LittleEndian.PutUint32(p[i*8:], bits)
				binary.LittleEndian.PutUint32(p[i*8+4:], bits)
			}
			r.next += frames
			return int(frames) * 8, nil
		}
		select {
		case <-r.ctx.Done():
			return 0, io.EOF
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// Capture clean known PCM before and after the release's exact timing filter.
// This test is silent: audio is encoded to a file, never played to speakers.
func TestDiagnosticAudioTimestamps(t *testing.T) {
	if os.Getenv("LB_DIAGNOSTIC_AUDIO") != "1" {
		t.Skip("use LB_DIAGNOSTIC_AUDIO=1")
	}
	dir := os.Getenv("LB_DIAGNOSTIC_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute diagnostic output directory required")
	}
	os.MkdirAll(dir, 0700)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"release-wallclock", "sample-clock", "fixed-filter-pts"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			wallclock := "1"
			filter := "aresample=async=1000:first_pts=0"
			if mode == "sample-clock" {
				wallclock = "0"
			}
			if mode == "fixed-filter-pts" {
				filter = "asetpts=N/SR/TB," + filter
			}
			path := filepath.Join(dir, mode+".ts")
			args := []string{"-y", "-hide_banner", "-loglevel", "warning", "-nostdin", "-re", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30", "-thread_queue_size", "8", "-use_wallclock_as_timestamps", wallclock, "-f", "f32le", "-ar", "48000", "-ac", "2", "-i", "pipe:0", "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-bf", "0", "-g", "15", "-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2", "-af", filter, "-t", "20", "-muxdelay", "0", "-muxpreload", "0", "-flush_packets", "1", "-f", "mpegts", path}
			cmd := exec.CommandContext(ctx, ffmpeg, args...)
			prepareHeadlessCommand(cmd)
			raw, err := os.Create(filepath.Join(dir, mode+"-input.f32"))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			cmd.Stdin = io.TeeReader(&diagnosticTone{ctx: ctx, epoch: time.Now()}, raw)
			cmd.WaitDelay = time.Second
			log, err := os.Create(filepath.Join(dir, mode+".log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.Stderr = log
			start := time.Now()
			if err = cmd.Run(); err != nil {
				t.Fatal(err)
			}
			decode := exec.CommandContext(ctx, ffmpeg, "-y", "-v", "error", "-i", path, "-vn", "-f", "f32le", "-ac", "2", "-ar", "48000", filepath.Join(dir, mode+"-decoded.f32"))
			prepareHeadlessCommand(decode)
			if output, err := decode.CombinedOutput(); err != nil {
				t.Fatalf("decode: %v %s", err, output)
			}
			t.Logf("%s elapsed=%.3fs path=%s", mode, time.Since(start).Seconds(), path)
		})
	}
}

func TestDiagnosticNativeAudioFile(t *testing.T) {
	if os.Getenv("LB_DIAGNOSTIC_NATIVE_AUDIO") != "1" {
		t.Skip("native loopback diagnostic capture")
	}
	dir := os.Getenv("LB_DIAGNOSTIC_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute directory required")
	}
	os.MkdirAll(dir, 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, f, err := openSystemLoopback(ctx, &LogBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if f.PCM != "f32le" || f.Channels != 2 || f.Rate != 48000 {
		t.Fatalf("diagnostic expects f32/48k/stereo: %+v", f)
	}
	player := exec.CommandContext(ctx, binaryPath("ffplay"), "-hide_banner", "-loglevel", "error", "-nodisp", "-autoexit", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=32", "-af", "volume=0.4")
	prepareHeadlessCommand(player)
	if err = player.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { player.Process.Kill(); player.Wait() }()
	file, err := os.Create(filepath.Join(dir, "native-loopback.f32"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = io.CopyN(file, r, int64(34*f.Rate*f.Channels*4)); err != nil {
		t.Fatal(err)
	}
	if s, ok := r.(*pcmTimeline); ok {
		b, _ := json.MarshalIndent(s.Snapshot(), "", "  ")
		os.WriteFile(filepath.Join(dir, "native-stats.json"), b, 0600)
		t.Log(string(b))
	}
	t.Log("native loopback seconds=" + strconv.Itoa(34))
}

func TestDiagnosticNativeEncodedAudio(t *testing.T) {
	if os.Getenv("LB_DIAGNOSTIC_ENCODED_AUDIO") != "1" {
		t.Skip("real screen and encoded loopback diagnostic")
	}
	dir := os.Getenv("LB_DIAGNOSTIC_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute directory required")
	}
	os.MkdirAll(dir, 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	captureLogs := &LogBuffer{}
	r, f, err := openSystemLoopback(ctx, captureLogs)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := defaultConfig()
	c.ScreenAudioMode = "wasapi"
	c.ScreenFPS = 30
	c.ScreenBitrateK = 6000
	c.Encoder = "libx264"
	args, err := ffmpegArgsPCM("screen", c, 16900, &f)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "native-encoded.ts")
	args = append(args[:len(args)-1], "-y", "-t", "30", path)
	player := exec.CommandContext(ctx, binaryPath("ffplay"), "-hide_banner", "-loglevel", "error", "-nodisp", "-autoexit", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=32", "-af", "volume=0.25")
	prepareHeadlessCommand(player)
	if err = player.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { player.Process.Kill(); player.Wait() }()
	raw, err := os.Create(filepath.Join(dir, "native-encoder-input.f32"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	log, err := os.Create(filepath.Join(dir, "native-encoder.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, binaryPath("ffmpeg"), args...)
	prepareHeadlessCommand(cmd)
	cmd.Stdin = io.TeeReader(r, raw)
	cmd.Stderr = log
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		t.Fatal(err)
	}
	decode := exec.CommandContext(ctx, binaryPath("ffmpeg"), "-y", "-v", "error", "-i", path, "-vn", "-f", "f32le", "-ac", "2", "-ar", "48000", filepath.Join(dir, "native-encoded-decoded.f32"))
	prepareHeadlessCommand(decode)
	if b, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("decode: %v %s", err, b)
	}
	os.WriteFile(filepath.Join(dir, "native-capture.log"), []byte(strings.Join(captureLogs.List(), "\n")), 0600)
	if stream, ok := r.(*pcmTimeline); ok {
		b, _ := json.MarshalIndent(stream.Snapshot(), "", "  ")
		os.WriteFile(filepath.Join(dir, "native-encoder-stats.json"), b, 0600)
		t.Log(string(b))
	}
}
