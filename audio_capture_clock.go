package main

import "time"

// Device sample positions are exact for contiguous packets. Packet QPC anchors
// their timing, but rounding every QPC independently creates one-sample holes
// and overlaps. Track a continuous sample clock and estimate its rate slowly.
type pcmCaptureClock struct {
	active                                      bool
	offset                                      int64
	lastDevice                                  int64
	lastFrames                                  uint32
	lastQPC                                     time.Duration
	rateDevice                                  int64
	rateQPC                                     time.Duration
	baseTime                                    time.Time
	baseFrame                                   float64
	rate                                        float64
	reanchors, discontinuities, timestampErrors uint64
}

func (s *pcmTimeline) positionAt(now time.Time) float64 {
	if !s.captureClock.active {
		return now.Sub(s.epoch).Seconds() * float64(s.rate)
	}
	c := &s.captureClock
	return c.baseFrame + now.Sub(c.baseTime).Seconds()*c.rate
}

func (s *pcmTimeline) capturePosition(device uint64, qpc time.Duration, frames, flags uint32) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &s.captureClock
	d := int64(device)
	now := s.now()
	valid := flags&4 == 0
	if !valid {
		c.timestampErrors++
	}
	if flags&1 != 0 {
		c.discontinuities++
	}
	continuous := c.active && d == c.lastDevice+int64(c.lastFrames) && flags&1 == 0
	if valid && c.active && (qpc < c.lastQPC || qpc-c.lastQPC > 100*time.Millisecond) {
		continuous = false
	}
	if !continuous {
		at := now.Add(-time.Duration(frames) * time.Second / time.Duration(s.rate))
		if valid {
			at = s.epoch.Add(qpc)
		}
		frame := s.positionAt(at)
		if !c.active {
			c.rate = float64(s.rate)
			c.baseTime = s.epoch
			c.baseFrame = 0
			c.active = true
		}
		c.offset = int64(frame) - d
		c.rateDevice = d
		c.rateQPC = qpc
		c.reanchors++
	} else if valid && qpc-c.rateQPC >= time.Second {
		rate := float64(d-c.rateDevice) / (qpc - c.rateQPC).Seconds()
		if rate > float64(s.rate)*0.998 && rate < float64(s.rate)*1.002 {
			predicted := s.positionAt(now)
			at := s.epoch.Add(qpc)
			errorFrames := float64(d+c.offset) - s.positionAt(at)
			correction := max(-0.1, min(0.1, errorFrames*0.01))
			c.baseFrame = predicted + correction
			c.baseTime = now
			c.rate = c.rate*0.8 + rate*0.2
		}
		c.rateDevice = d
		c.rateQPC = qpc
	}
	c.lastDevice = d
	c.lastFrames = frames
	if valid {
		c.lastQPC = qpc
	}
	return d + c.offset
}
