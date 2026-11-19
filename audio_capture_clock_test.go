package main

import (
	"math"
	"testing"
	"time"
)

func TestCaptureClockKeepsContiguousSamplesDespiteQPCJitter(t *testing.T) {
	s := newPCMTimeline(AudioFormat{PCM: "f32le", Rate: 48000, Channels: 2}, 8)
	now := s.epoch
	s.now = func() time.Time { return now }
	var previous int64
	for i := 0; i < 10000; i++ {
		jitter := time.Duration((i%5)-2) * 8 * time.Microsecond
		qpc := time.Duration(i)*10*time.Millisecond + jitter
		now = s.epoch.Add(qpc + 5*time.Millisecond)
		pos := s.capturePosition(uint64(i*480), qpc, 480, 0)
		if i > 0 && pos != previous+480 {
			t.Fatalf("packet %d: inserted/removed samples, pos=%d previous=%d", i, pos, previous)
		}
		previous = pos
	}
}

func TestCaptureClockTracksDeviceDriftWithoutAccumulatingQueue(t *testing.T) {
	for _, ppm := range []float64{-200, 200} {
		s := newPCMTimeline(AudioFormat{PCM: "f32le", Rate: 48000, Channels: 2}, 8)
		now := s.epoch
		s.now = func() time.Time { return now }
		rate := 48000 * (1 + ppm/1e6)
		var position int64
		// One hour of clock updates, no wall-clock waiting or audio playback.
		for packet := 0; packet < 360000; packet++ {
			device := uint64(packet * 480)
			qpc := time.Duration(float64(device) / rate * float64(time.Second))
			now = s.epoch.Add(qpc)
			position = s.capturePosition(device, qpc, 480, 0)
		}
		if math.Abs(s.captureClock.rate-rate) > 0.02 {
			t.Fatalf("rate not tracked: ppm=%.0f rate=%.6f want=%.6f", ppm, s.captureClock.rate, rate)
		}
		if delta := math.Abs(s.positionAt(now) - float64(position)); delta > 10 {
			t.Fatalf("queue grew after one hour: %.3f frames", delta)
		}
	}
}

func TestCaptureClockReanchorsAfterIdleOrDiscontinuity(t *testing.T) {
	s := newPCMTimeline(AudioFormat{PCM: "f32le", Rate: 48000, Channels: 2}, 8)
	now := s.epoch
	s.now = func() time.Time { return now }
	s.capturePosition(0, 0, 480, 0)
	now = s.epoch.Add(2 * time.Second)
	pos := s.capturePosition(480, 2*time.Second, 480, 1)
	if pos < 95000 || pos > 97000 {
		t.Fatalf("resumed audio overlaps idle silence: %d", pos)
	}
	if s.captureClock.reanchors != 2 || s.captureClock.discontinuities != 1 {
		t.Fatal("missing discontinuity counters")
	}
}

func TestCaptureClockIgnoresInvalidPacketTimestamps(t *testing.T) {
	s := newPCMTimeline(AudioFormat{PCM: "f32le", Rate: 48000, Channels: 2}, 8)
	now := s.epoch
	s.now = func() time.Time { return now }
	first := s.capturePosition(1000, 0, 480, 0)
	now = now.Add(10 * time.Millisecond)
	// TIMESTAMP_ERROR: a nonsensical QPC must not move continuous samples.
	second := s.capturePosition(1480, -time.Hour, 480, 4)
	now = now.Add(10 * time.Millisecond)
	third := s.capturePosition(1960, 20*time.Millisecond, 480, 0)
	if second != first+480 || third != second+480 {
		t.Fatalf("invalid QPC broke continuity: %d %d %d", first, second, third)
	}
	if s.captureClock.timestampErrors != 1 || s.captureClock.reanchors != 1 {
		t.Fatal("invalid timestamp changed anchors or was not counted")
	}
}
