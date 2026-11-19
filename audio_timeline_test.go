package main

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func testTimeline() (*pcmTimeline, *time.Time) {
	s := newPCMTimeline(AudioFormat{PCM: "u8", Rate: 1000, Channels: 1}, 1)
	now := s.epoch
	s.now = func() time.Time { return now }
	return s, &now
}

func TestPCMTimelineBatchedPacketsDoNotDuplicateSilence(t *testing.T) {
	s, now := testTimeline()
	// A delayed 20-ms batch arrives after an empty poll at 10 ms.
	*now = s.epoch.Add(20 * time.Millisecond)
	if got := s.take(); len(got) != 0 {
		t.Fatal("rendered before jitter hold")
	}
	s.push(0, bytes.Repeat([]byte{42}, 20))
	*now = s.epoch.Add(60 * time.Millisecond)
	got := append(s.take(), s.take()...)
	if !bytes.Equal(got, bytes.Repeat([]byte{42}, 20)) || s.next != 20 {
		t.Fatalf("duplicated/missing frames: %v next=%d", got, s.next)
	}
}

func TestPCMTimelineIdleResumeAndLatePackets(t *testing.T) {
	s, now := testTimeline()
	*now = s.epoch.Add(50 * time.Millisecond)
	if got := s.take(); !bytes.Equal(got, bytes.Repeat([]byte{128}, 10)) {
		t.Fatalf("bad u8 silence: %v", got)
	}
	s.push(0, bytes.Repeat([]byte{42}, 20))
	*now = s.epoch.Add(60 * time.Millisecond)
	if got := s.take(); !bytes.Equal(got, bytes.Repeat([]byte{42}, 10)) {
		t.Fatalf("late data was duplicated or new data lost: %v", got)
	}
	if s.next != 20 || s.late != 10 {
		t.Fatalf("next=%d late=%d", s.next, s.late)
	}
}

func TestPCMTimelineSlowConsumerKeepsBoundedLatency(t *testing.T) {
	s, now := testTimeline()
	s.push(0, bytes.Repeat([]byte{42}, 100))
	*now = s.epoch.Add(2 * time.Second)
	s.take()
	if s.next != 1960 || s.skipped != 1950 {
		t.Fatalf("backlog retained: next=%d skipped=%d", s.next, s.skipped)
	}
	if len(s.data) != 250 {
		t.Fatal("unbounded storage")
	}
}

func TestPCMTimelinePartialReadsAndClose(t *testing.T) {
	s, now := testTimeline()
	s.push(0, []byte{1, 2, 3, 4})
	*now = s.epoch.Add(44 * time.Millisecond)
	for want := byte(1); want <= 4; want++ {
		var b [1]byte
		if n, err := s.Read(b[:]); n != 1 || err != nil || b[0] != want {
			t.Fatalf("partial read: %v %d %v", b, n, err)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := s.Read(make([]byte, 10)); done <- err }()
	s.Close()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake Read")
	}
}
