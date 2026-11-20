package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func packetPCMFixture(t *testing.T, f AudioFormat, block int, gap bool) ([]byte, []byte) {
	t.Helper()
	s := newPCMTimeline(f, block)
	now := s.epoch
	s.now = func() time.Time { return now }
	r, err := newPCMPacketStream(s, f)
	if err != nil {
		t.Fatal(err)
	}
	var stream, raw bytes.Buffer
	if _, err = io.CopyN(&stream, r, int64(len(r.pending))); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128<<10)
	for packet := 0; packet < 100; packet++ {
		pos := int64(packet * f.Rate / 100)
		now = s.epoch.Add(time.Duration(packet+5)*10*time.Millisecond + time.Nanosecond)
		if gap && packet >= 2 {
			now = now.Add(time.Second)
		}
		pcm := make([]byte, f.Rate/100*block)
		for i := range pcm {
			pcm[i] = byte(i + packet)
		}
		s.push(pos, pcm)
		n, err := r.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		stream.Write(buf[:n])
		raw.Write(pcm)
		if gap && packet == 2 {
			break
		}
	}
	r.Close()
	return stream.Bytes(), raw.Bytes()
}

func TestPacketPCMWaitsForCompleteTenMilliseconds(t *testing.T) {
	s, now := testTimeline()
	s.push(0, bytes.Repeat([]byte{42}, 10))
	for ms := 41; ms < 50; ms++ {
		*now = s.epoch.Add(time.Duration(ms) * time.Millisecond)
		if got := s.takeFrames(10); len(got) != 0 {
			t.Fatalf("partial packet at %d ms: %d frames", ms, len(got))
		}
	}
	*now = s.epoch.Add(50 * time.Millisecond)
	if got := s.takeFrames(10); !bytes.Equal(got, bytes.Repeat([]byte{42}, 10)) {
		t.Fatalf("expected full packet after hold: %v", got)
	}
}

func TestPacketPCMRejectsInvalidFormat(t *testing.T) {
	if _, err := newPCMPacketStream(nil, AudioFormat{}); err == nil {
		t.Fatal("nil timeline accepted")
	}
	s := newPCMTimeline(AudioFormat{PCM: "f32le", Rate: 48000, Channels: 2}, 8)
	if _, err := newPCMPacketStream(s, AudioFormat{PCM: "s16le", Rate: 48000, Channels: 2}); err == nil {
		t.Fatal("wrong block alignment accepted")
	}
}

func TestPacketPCMRoundTripRealFFmpeg(t *testing.T) {
	if os.Getenv("LB_TEST_MEDIA") != "1" {
		t.Skip("use LB_TEST_MEDIA=1")
	}
	for _, tc := range []struct {
		pcm   string
		block int
	}{{"u8", 2}, {"s16le", 4}, {"s24le", 6}, {"s32le", 8}, {"f32le", 8}, {"f64le", 16}} {
		t.Run(tc.pcm, func(t *testing.T) {
			stream, raw := packetPCMFixture(t, AudioFormat{PCM: tc.pcm, Rate: 48000, Channels: 2}, tc.block, false)
			path := filepath.Join(t.TempDir(), "audio.mka")
			if err := os.WriteFile(path, stream, 0600); err != nil {
				t.Fatal(err)
			}
			metadata := qualityRun(t, binaryPath("ffprobe"), "-v", "error", "-show_entries", "stream=codec_name,sample_rate,channels", "-of", "json", path)
			var info struct {
				Streams []struct {
					Codec    string `json:"codec_name"`
					Rate     int    `json:"sample_rate,string"`
					Channels int    `json:"channels"`
				}
			}
			if err := json.Unmarshal(metadata, &info); err != nil {
				t.Fatal(err)
			}
			if len(info.Streams) != 1 || info.Streams[0].Codec != "pcm_"+tc.pcm || info.Streams[0].Rate != 48000 || info.Streams[0].Channels != 2 {
				t.Fatalf("wrong PCM declaration: %s", metadata)
			}
			decoded := qualityRun(t, binaryPath("ffmpeg"), "-v", "error", "-i", path, "-map", "0:a:0", "-c:a", "copy", "-f", tc.pcm, "pipe:1")
			if !bytes.Equal(decoded, raw) {
				t.Fatalf("PCM bytes changed: got=%d want=%d", len(decoded), len(raw))
			}
		})
	}
}

func TestPacketPCMPreservesTimestampGap(t *testing.T) {
	if os.Getenv("LB_TEST_MEDIA") != "1" {
		t.Skip("use LB_TEST_MEDIA=1")
	}
	stream, _ := packetPCMFixture(t, AudioFormat{PCM: "s16le", Rate: 48000, Channels: 2}, 4, true)
	path := filepath.Join(t.TempDir(), "gap.mka")
	os.WriteFile(path, stream, 0600)
	b := qualityRun(t, binaryPath("ffprobe"), "-v", "error", "-show_packets", "-show_entries", "packet=pts_time", "-of", "json", path)
	var result struct {
		Packets []struct {
			PTS float64 `json:"pts_time,string"`
		}
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Packets) != 3 || result.Packets[0].PTS != 0 || result.Packets[1].PTS != 0.01 || result.Packets[2].PTS < 1 {
		t.Fatalf("time gap lost: %s", b)
	}
}
