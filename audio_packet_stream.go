package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"
)

// A streaming Matroska audio track carries explicit PCM timestamps. Raw PCM
// with wallclock-at-read timestamps can make FFmpeg's async resampler generate
// silence while real samples wait in stdin. Packet PTS also preserve gaps when
// the bounded capture timeline discards stale samples after an encoder stall.
type pcmPacketStream struct {
	source       *pcmTimeline
	pcm, pending []byte
}

func pcmElement(id uint32, parts ...[]byte) []byte {
	var tag [4]byte
	binary.BigEndian.PutUint32(tag[:], id)
	i := 0
	for i < 3 && tag[i] == 0 {
		i++
	}
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	width := 1
	for width < 8 && uint64(n) >= (uint64(1)<<uint(7*width))-1 {
		width++
	}
	value := uint64(n) | (uint64(1) << uint(7*width))
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], value)
	out := make([]byte, 0, 4+8+n)
	out = append(out, tag[i:]...)
	out = append(out, size[8-width:]...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func pcmUint(value uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], value)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return append([]byte(nil), b[i:]...)
}

func newPCMPacketStream(source *pcmTimeline, f AudioFormat) (*pcmPacketStream, error) {
	if source == nil {
		return nil, fmt.Errorf("PCM timeline unavailable")
	}
	codec, bits := "A_PCM/INT/LIT", 0
	switch f.PCM {
	case "f32le":
		codec, bits = "A_PCM/FLOAT/IEEE", 32
	case "f64le":
		codec, bits = "A_PCM/FLOAT/IEEE", 64
	case "u8":
		bits = 8
	case "s16le":
		bits = 16
	case "s24le":
		bits = 24
	case "s32le":
		bits = 32
	default:
		return nil, fmt.Errorf("unsupported timestamped PCM: %s", f.PCM)
	}
	if f.Rate != source.rate || f.Channels*bits/8 != source.block {
		return nil, fmt.Errorf("PCM stream format mismatch")
	}
	var rate [8]byte
	binary.BigEndian.PutUint64(rate[:], math.Float64bits(float64(f.Rate)))
	header := pcmElement(0x1A45DFA3, pcmElement(0x4286, pcmUint(1)), pcmElement(0x42F7, pcmUint(1)), pcmElement(0x42F2, pcmUint(4)), pcmElement(0x42F3, pcmUint(8)), pcmElement(0x4282, []byte("matroska")), pcmElement(0x4287, pcmUint(4)), pcmElement(0x4285, pcmUint(2)))
	header = append(header, 0x18, 0x53, 0x80, 0x67, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff) // unknown-length Segment
	header = append(header, pcmElement(0x1549A966, pcmElement(0x2AD7B1, pcmUint(1000)), pcmElement(0x4D80, []byte("LocalBroadcast")), pcmElement(0x5741, []byte("LocalBroadcast")))...)
	audio := pcmElement(0xE1, pcmElement(0xB5, rate[:]), pcmElement(0x9F, pcmUint(uint64(f.Channels))), pcmElement(0x6264, pcmUint(uint64(bits))))
	track := pcmElement(0xAE, pcmElement(0xD7, pcmUint(1)), pcmElement(0x73C5, pcmUint(1)), pcmElement(0x83, pcmUint(2)), pcmElement(0x9C, pcmUint(0)), pcmElement(0x86, []byte(codec)), audio)
	header = append(header, pcmElement(0x1654AE6B, track)...)
	return &pcmPacketStream{source: source, pcm: make([]byte, (f.Rate/100)*source.block), pending: header}, nil
}

func (s *pcmPacketStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(s.pending) == 0 {
		n, err := s.source.Read(s.pcm)
		if n == 0 {
			return 0, err
		}
		if n%s.source.block != 0 {
			return 0, fmt.Errorf("unaligned PCM block")
		}
		// This is the sole consumer, with a buffer as large as take's maximum
		// block, so Read consumed all pending frames and next marks their end.
		s.source.mu.Lock()
		start := s.source.next - int64(n/s.source.block)
		elapsed := time.Duration(float64(start) * float64(time.Second) / float64(s.source.rate))
		clock := &s.source.captureClock
		if clock.active {
			elapsed = clock.baseTime.Sub(s.source.epoch) + time.Duration((float64(start)-clock.baseFrame)*float64(time.Second)/clock.rate)
		}
		s.source.mu.Unlock()
		pts := uint64(max(time.Duration(0), elapsed) / time.Microsecond)
		// One small cluster per packet keeps relative Block timestamps zero.
		block := pcmElement(0xA3, []byte{0x81, 0, 0, 0x80}, s.pcm[:n])
		s.pending = pcmElement(0x1F43B675, pcmElement(0xE7, pcmUint(pts)), block)
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *pcmPacketStream) Close() error { return s.source.Close() }

var _ io.ReadCloser = (*pcmPacketStream)(nil)
