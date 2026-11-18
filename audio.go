package main

import (
	"context"
	"io"
)

type AudioFormat struct {
	PCM      string
	Rate     int
	Channels int
}

// openSystemLoopback captures Windows' default rendered output mix directly via
// WASAPI, not via Stereo Mix or a virtual device. The stream is interleaved PCM.
func openSystemLoopback(ctx context.Context, logs *LogBuffer) (io.ReadCloser, AudioFormat, error) {
	return openNativeLoopback(ctx, logs)
}
