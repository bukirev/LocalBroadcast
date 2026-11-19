package main

import (
	"io"
	"sync"
	"time"
)

// pcmTimeline separates endpoint capture from the encoder. All positions are
// frames on one monotonic clock: silence fills missing time, never adds time.
// Old data is discarded after encoder stalls instead of accumulating latency.
type pcmTimeline struct {
	mu                     sync.Mutex
	rate, block            int
	capacity               int64
	data                   []byte
	tags                   []int64
	next                   int64
	pending                []byte
	silence                byte
	epoch                  time.Time
	now                    func() time.Time
	done                   chan struct{}
	closed                 bool
	late, missing, skipped uint64
	captureClock           pcmCaptureClock
}

func newPCMTimeline(f AudioFormat, block int) *pcmTimeline {
	n := f.Rate / 4 // 250 ms maximum storage, not a minimum fill requirement.
	s := &pcmTimeline{rate: f.Rate, block: block, capacity: int64(n), data: make([]byte, n*block), tags: make([]int64, n), epoch: time.Now(), now: time.Now, done: make(chan struct{})}
	for i := range s.tags {
		s.tags[i] = -1
	}
	if f.PCM == "u8" {
		s.silence = 128
	}
	return s
}

func (s *pcmTimeline) push(start int64, pcm []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for i := 0; i < len(pcm)/s.block; i++ {
		pos := start + int64(i)
		if pos < s.next || pos < 0 {
			s.late++
			continue
		}
		// Ignore implausibly future timestamps rather than overwriting live data.
		if pos >= s.next+s.capacity {
			continue
		}
		idx := pos % s.capacity
		copy(s.data[int(idx)*s.block:], pcm[i*s.block:(i+1)*s.block])
		s.tags[idx] = pos
	}
}

// take is called under mu. The short hold lets WASAPI publish a packet before
// its time is rendered, including when delivery is batched by the audio engine.
func (s *pcmTimeline) take() []byte {
	due := int64(s.positionAt(s.now())) - int64(s.rate)*40/1000
	if due <= s.next {
		return nil
	}
	if due-s.next > s.capacity/2 {
		resume := due - int64(s.rate)/100
		s.skipped += uint64(resume - s.next)
		s.next = resume
	}
	n := min(due-s.next, int64(s.rate)/100)
	out := make([]byte, int(n)*s.block)
	for i := range out {
		out[i] = s.silence
	}
	for i := int64(0); i < n; i++ {
		pos := s.next + i
		idx := pos % s.capacity
		if s.tags[idx] == pos {
			copy(out[int(i)*s.block:], s.data[int(idx)*s.block:int(idx+1)*s.block])
			s.tags[idx] = -1
		} else {
			s.missing++
		}
	}
	s.next += n
	return out
}

func (s *pcmTimeline) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, io.EOF
		}
		if len(s.pending) == 0 {
			s.pending = s.take()
		}
		if len(s.pending) > 0 {
			n := copy(p, s.pending)
			s.pending = s.pending[n:]
			s.mu.Unlock()
			return n, nil
		}
		s.mu.Unlock()
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-s.done:
			timer.Stop()
			return 0, io.EOF
		case <-timer.C:
		}
	}
}

func (s *pcmTimeline) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	return nil
}

func (s *pcmTimeline) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"clockFrames": s.next, "missingFrames": s.missing, "lateFrames": s.late, "skippedFrames": s.skipped, "sampleRate": s.rate, "holdMs": 40, "clockRateHz": s.captureClock.rate, "clockReanchors": s.captureClock.reanchors, "deviceDiscontinuities": s.captureClock.discontinuities, "timestampErrors": s.captureClock.timestampErrors}
}
