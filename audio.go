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
func openSystemLoopback(ctx context.Context, logs *LogBuffer, selected ...string) (io.ReadCloser, AudioFormat, error) {
	if len(selected) > 0 && selected[0] != "" {
		return openNativeLoopbackDevice(ctx, logs, selected[0])
	}
	return openNativeLoopback(ctx, logs)
}
