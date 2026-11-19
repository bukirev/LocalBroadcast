package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestScreenQualityLegacyConfigAndPersistence(t *testing.T) {
	c := defaultConfig()
	c.ScreenWidth = 0
	c.ScreenHeight = 0
	c.ScreenFPSMode = ""
	c.ScreenFPS = 25
	if err := validateConfig(&c); err != nil {
		t.Fatal(err)
	}
	if c.ScreenWidth != 1920 || c.ScreenHeight != 1080 || c.ScreenFPSMode != "fixed" || c.ScreenFPS != 25 {
		t.Fatal("legacy capture settings changed")
	}
	store, err := newConfigStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	c = store.Get()
	node := c.NodeID
	c.ScreenWidth = 1280
	c.ScreenHeight = 720
	c.ScreenFPSMode = "adaptive"
	if err = store.Update(c); err != nil {
		t.Fatal(err)
	}
	loaded, err := newConfigStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Get()
	if got.ScreenWidth != 1280 || got.ScreenHeight != 720 || got.ScreenFPSMode != "adaptive" || got.NodeID != node {
		t.Fatal("quality/identity not preserved")
	}
}

func TestScreenQualityDoesNotChangeCameraPipeline(t *testing.T) {
	c := defaultConfig()
	screen, camera := pipelineSignature("screen", c), pipelineSignature("camera", c)
	c.ScreenWidth = 1280
	c.ScreenHeight = 720
	c.ScreenFPSMode = "adaptive"
	if pipelineSignature("screen", c) == screen || pipelineSignature("camera", c) != camera {
		t.Fatal("only the screen pipeline should change")
	}
	for _, edit := range []func(*Configuration){func(c *Configuration) { c.ScreenWidth = 1280; c.ScreenHeight = 0 }, func(c *Configuration) { c.ScreenWidth = 1279; c.ScreenHeight = 720 }, func(c *Configuration) { c.ScreenFPSMode = "unknown" }} {
		x := defaultConfig()
		edit(&x)
		if validateConfig(&x) == nil {
			t.Fatal("invalid capture profile accepted")
		}
	}
}

// Replace only hardware-specific inputs with deterministic sources. Retain the
// production mapping, scale/adaptive filters, encoder and AAC/mux arguments.
func syntheticScreenQualityArgs(t *testing.T, c Configuration, source, path string, seconds int) []string {
	t.Helper()
	args, err := ffmpegArgsPCM("screen", c, 16900, nil)
	if err != nil {
		t.Fatal(err)
	}
	begin, end := -1, -1
	for i, v := range args {
		if v == "gdigrab" {
			begin = i - 1
		}
		if v == "desktop" {
			end = i + 1
			break
		}
	}
	if begin < 0 || end < 0 {
		t.Fatal("screen input missing")
	}
	args = append(append(append([]string{}, args[:begin]...), "-f", "lavfi", "-i", source), args[end:]...)
	begin, end = -1, -1
	for i, v := range args {
		if v == "-thread_queue_size" && begin < 0 {
			begin = i
		}
		if v == "pipe:0" {
			end = i + 1
			break
		}
	}
	if begin < 0 || end < 0 {
		t.Fatal("PCM input missing")
	}
	args = append(append(append([]string{}, args[:begin]...), "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000"), args[end:]...)
	return append(args[:len(args)-1], "-t", strconv.Itoa(seconds), "-y", path)
}

func qualityRun(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	prepareHeadlessCommand(cmd)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("media command: %v\n%s", err, b)
	}
	return b
}

func TestScreenQualityRealFFmpegProfiles(t *testing.T) {
	if os.Getenv("LB_TEST_MEDIA") != "1" {
		t.Skip("use LB_TEST_MEDIA=1, FFmpeg/ffprobe on PATH")
	}
	for _, size := range [][2]int{{854, 480}, {1280, 720}, {1920, 1080}} {
		for _, fps := range []int{15, 30, 60} {
			t.Run(fmt.Sprintf("%dx%d-%dfps", size[0], size[1], fps), func(t *testing.T) {
				c := defaultConfig()
				c.ScreenWidth = size[0]
				c.ScreenHeight = size[1]
				c.ScreenFPS = fps
				c.ScreenBitrateK = 2000
				path := filepath.Join(t.TempDir(), "profile.ts")
				args := syntheticScreenQualityArgs(t, c, fmt.Sprintf("testsrc2=size=640x360:rate=%d", fps), path, 2)
				qualityRun(t, append([]string{binaryPath("ffmpeg")}, args...)...)
				b := qualityRun(t, binaryPath("ffprobe"), "-v", "error", "-count_frames", "-show_entries", "stream=codec_name,width,height,sample_aspect_ratio,duration,nb_read_frames", "-of", "json", path)
				var p struct {
					Streams []struct {
						Codec         string `json:"codec_name"`
						Width, Height int
						SAR           string  `json:"sample_aspect_ratio"`
						Duration      float64 `json:"duration,string"`
						Frames        int     `json:"nb_read_frames,string"`
					}
				}
				if err := json.Unmarshal(b, &p); err != nil {
					t.Fatal(err)
				}
				if len(p.Streams) != 2 || p.Streams[0].Width != size[0] || p.Streams[0].Height != size[1] || p.Streams[0].SAR != "1:1" || p.Streams[0].Frames != fps*2 || p.Streams[1].Codec != "aac" {
					t.Fatalf("wrong profile: %s", b)
				}
			})
		}
	}
}

func TestAdaptiveScreenFPSPreservesAudioTimeline(t *testing.T) {
	if os.Getenv("LB_TEST_MEDIA") != "1" {
		t.Skip("use LB_TEST_MEDIA=1")
	}
	c := defaultConfig()
	c.ScreenWidth = 1280
	c.ScreenHeight = 720
	c.ScreenFPSMode = "adaptive"
	c.ScreenBitrateK = 2000
	source := "color=black:size=640x360:rate=30:d=3[a];testsrc2=size=640x360:rate=30:d=3[b];color=black:size=640x360:rate=30:d=3[c];[a][b][c]concat=n=3:v=1:a=0"
	path := filepath.Join(t.TempDir(), "adaptive.ts")
	args := syntheticScreenQualityArgs(t, c, source, path, 9)
	qualityRun(t, append([]string{binaryPath("ffmpeg")}, args...)...)
	b := qualityRun(t, binaryPath("ffprobe"), "-v", "error", "-show_packets", "-show_entries", "packet=codec_type,pts_time,duration_time", "-of", "json", path)
	var p struct {
		Packets []struct {
			Type     string  `json:"codec_type"`
			PTS      float64 `json:"pts_time,string"`
			Duration float64 `json:"duration_time,string"`
		}
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	counts := [3]int{}
	firstVideo := -1.
	lastAudio := -1.
	audioCount := 0
	maxAudioGap := 0.
	for _, packet := range p.Packets {
		if packet.Type == "video" {
			if firstVideo < 0 {
				firstVideo = packet.PTS
			}
			part := int((packet.PTS - firstVideo + 0.0001) / 3)
			if part >= 0 && part < 3 {
				counts[part]++
			}
		}
		if packet.Type == "audio" {
			if lastAudio >= 0 {
				gap := packet.PTS - lastAudio
				if gap <= 0 || gap > 0.03 {
					t.Fatalf("audio gap %.6fs", gap)
				}
				if gap > maxAudioGap {
					maxAudioGap = gap
				}
			}
			lastAudio = packet.PTS
			audioCount++
		}
	}
	if counts[0] < 43 || counts[0] > 47 || counts[1] < 85 || counts[1] > 92 || counts[2] < 43 || counts[2] > 48 || audioCount < 415 {
		t.Fatalf("bad adaptation/audio: bins=%v audio=%d", counts, audioCount)
	}
	t.Logf("one FFmpeg process: 3s static/moving/static video frames=%v; AAC packets=%d max audio gap=%.3fms", counts, audioCount, maxAudioGap*1000)
}

func TestScreenPortraitPreservesAspectWithLetterbox(t *testing.T) {
	if os.Getenv("LB_TEST_MEDIA") != "1" {
		t.Skip("use LB_TEST_MEDIA=1")
	}
	c := defaultConfig()
	c.ScreenWidth = 1280
	c.ScreenHeight = 720
	c.ScreenFPS = 15
	c.ScreenBitrateK = 2000
	path := filepath.Join(t.TempDir(), "portrait.ts")
	args := syntheticScreenQualityArgs(t, c, "color=red:size=360x640:rate=15", path, 2)
	qualityRun(t, append([]string{binaryPath("ffmpeg")}, args...)...)
	rgb := qualityRun(t, binaryPath("ffmpeg"), "-v", "error", "-i", path, "-map", "0:v:0", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	if len(rgb) != 1280*720*3 {
		t.Fatalf("wrong decoded canvas: %d", len(rgb))
	}
	for _, x := range []int{30, 1250} {
		i := (360*1280 + x) * 3
		if rgb[i] > 10 || rgb[i+1] > 10 || rgb[i+2] > 10 {
			t.Fatal("portrait sidebar is not black")
		}
	}
	i := (360*1280 + 640) * 3
	if rgb[i] < 200 || rgb[i+1] > 30 || rgb[i+2] > 30 {
		t.Fatal("portrait content missing from center")
	}
}
