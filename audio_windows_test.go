//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

func TestWASAPILiveAVEncoding(t *testing.T) {
	if os.Getenv("LB_TEST_AUDIO") != "1" {
		t.Skip("use LB_TEST_AUDIO=1")
	}
	c := defaultConfig()
	c.ScreenAudioMode = "wasapi"
	c.Encoder = "libx264"
	c.ScreenFPS = 15
	c.ScreenBitrateK = 3000
	testWASAPILiveAVProfile(t, c, 55)
}
func TestWASAPILive720Adaptive(t *testing.T) {
	if os.Getenv("LB_TEST_AUDIO") != "1" {
		t.Skip("use LB_TEST_AUDIO=1")
	}
	c := defaultConfig()
	c.ScreenAudioMode = "wasapi"
	c.Encoder = "libx264"
	c.ScreenWidth = 1280
	c.ScreenHeight = 720
	c.ScreenFPSMode = "adaptive"
	c.ScreenBitrateK = 2000
	testWASAPILiveAVProfile(t, c, 8)
}

func testWASAPILiveAVProfile(t *testing.T, c Configuration, minVideoFrames int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r, f, err := openNativeLoopback(ctx, &LogBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if c.ScreenFPSMode == "adaptive" {
		player := exec.CommandContext(ctx, binaryPath("ffplay"), "-hide_banner", "-loglevel", "error", "-nodisp", "-autoexit", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=6", "-af", "volume=0.08")
		prepareHeadlessCommand(player)
		if err = player.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { player.Process.Kill(); player.Wait() }()
	}
	args, err := ffmpegArgsPCM("screen", c, 16900, &f)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "av.ts")
	args = append(args[:len(args)-1], "-t", "4", file)
	cmd := exec.CommandContext(ctx, binaryPath("ffmpeg"), args...)
	cmd.Stdin = r
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	prepareHeadlessCommand(cmd)
	// Close the source when the process exits; WaitDelay bounds the stdin copier.
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		t.Fatalf("encode: %v %s", err, diagnostics.String())
	}
	probe := exec.CommandContext(ctx, binaryPath("ffprobe"), "-v", "error", "-count_frames", "-show_entries", "stream=codec_name,start_time,duration,nb_read_frames,width,height", "-of", "json", file)
	data, err := probe.Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Streams []struct {
			Codec    string  `json:"codec_name"`
			Width    int     `json:"width"`
			Height   int     `json:"height"`
			Start    float64 `json:"start_time,string"`
			Duration float64 `json:"duration,string"`
			Frames   int     `json:"nb_read_frames,string"`
		} `json:"streams"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Streams) != 2 {
		t.Fatalf("expected H264 and AAC: %s", data)
	}
	v, a := result.Streams[0], result.Streams[1]
	w, h := screenOutputSize(c)
	if v.Width != w || v.Height != h {
		t.Fatalf("wrong output size: %s", data)
	}
	if v.Codec != "h264" || a.Codec != "aac" || math.Abs(v.Start-a.Start) > 0.15 || math.Abs(v.Duration-a.Duration) > 0.2 || v.Frames < minVideoFrames {
		t.Fatalf("bad A/V clock: %s; diagnostics=%s", data, diagnostics.String())
	}
	decode := exec.CommandContext(ctx, binaryPath("ffmpeg"), "-hide_banner", "-loglevel", "error", "-xerror", "-i", file, "-f", "null", "-")
	if out, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("decode: %v %s", err, out)
	}
	if c.ScreenFPSMode == "adaptive" {
		decodeAudio := exec.CommandContext(ctx, binaryPath("ffmpeg"), "-v", "error", "-i", file, "-map", "0:a:0", "-f", "f32le", "pipe:1")
		prepareHeadlessCommand(decodeAudio)
		pcm, err := decodeAudio.Output()
		if err != nil {
			t.Fatal(err)
		}
		sum := 0.
		for i := 0; i+4 <= len(pcm); i += 4 {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[i:])))
			sum += x * x
		}
		rms := math.Sqrt(sum / float64(len(pcm)/4))
		if len(pcm) == 0 || rms < 0.00001 {
			t.Fatalf("adaptive AAC lost real loopback tone: rms=%g", rms)
		}
		t.Logf("adaptive native AAC decoded tone RMS=%g", rms)
	}
	t.Logf("live gdigrab+WASAPI: video %d frames / %.3fs, audio %d frames / %.3fs, start difference %.1fms; timeline=%v", v.Frames, v.Duration, a.Frames, a.Duration, 1000*math.Abs(v.Start-a.Start), r.(*pcmTimeline).Snapshot())
}

// Opt-in hardware smoke test. Plays a quiet three-second test tone through the
// default Windows output, then checks live loopback clock rate and missing audio.
func TestNativeWASAPILiveClock(t *testing.T) {
	if os.Getenv("LB_TEST_AUDIO") != "1" {
		t.Skip("use LB_TEST_AUDIO=1 (plays a quiet tone)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	r, f, err := openNativeLoopback(ctx, &LogBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if f.PCM != "f32le" {
		t.Skipf("RMS decoder expects f32le, got %s", f.PCM)
	}
	player := exec.CommandContext(ctx, binaryPath("ffplay"), "-hide_banner", "-loglevel", "error", "-nodisp", "-autoexit", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3", "-af", "volume=0.08")
	prepareHeadlessCommand(player)
	if err = player.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if player.Process != nil {
			player.Process.Kill()
		}
		player.Wait()
	}()
	started := time.Now()
	block := f.Channels * 4
	pcm := make([]byte, 4*f.Rate*block)
	if _, err = io.ReadFull(r, pcm); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if math.Abs(elapsed.Seconds()-4.04) > 0.15 {
		t.Fatalf("wrong audio clock: four seconds delivered in %s", elapsed)
	}
	windows, audible := 0, 0
	// Ignore playback startup and tail; inspect the stable middle 1.5 seconds.
	for start := f.Rate; start < 5*f.Rate/2; start += f.Rate / 50 {
		sum := 0.0
		for i := start; i < start+f.Rate/50; i++ {
			off := i * block
			sample := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[off : off+4])))
			sum += sample * sample
		}
		windows++
		if math.Sqrt(sum/float64(f.Rate/50)) > 0.0001 {
			audible++
		}
	}
	t.Logf("format=%+v four-second PCM delivered in %s; audible windows=%d/%d; timeline=%v", f, elapsed, audible, windows, func() any {
		if stream, ok := r.(*pcmTimeline); ok {
			return stream.Snapshot()
		}
		return "original io.Pipe"
	}())
	if audible < windows-1 {
		t.Fatalf("missing tone in %d/%d windows", windows-audible, windows)
	}
}

func TestWASAPIMixFormats(t *testing.T) {
	for _, tc := range []struct {
		tag, bits uint16
		format    string
	}{{3, 32, "f32le"}, {1, 16, "s16le"}, {1, 24, "s24le"}} {
		b := make([]byte, 40)
		binary.LittleEndian.PutUint16(b[0:2], tc.tag)
		binary.LittleEndian.PutUint16(b[2:4], 2)
		binary.LittleEndian.PutUint32(b[4:8], 48000)
		binary.LittleEndian.PutUint16(b[12:14], tc.bits/8*2)
		binary.LittleEndian.PutUint16(b[14:16], tc.bits)
		f, _, err := formatFromWave(uintptr(unsafe.Pointer(&b[0])))
		if err != nil || f.PCM != tc.format {
			t.Fatalf("fmt %+v err %v", f, err)
		}
	}
}
