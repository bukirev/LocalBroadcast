//go:build !windows

package main

import (
	"context"
	"errors"
	"io"
)

func openNativeLoopback(context.Context, *LogBuffer) (io.ReadCloser, AudioFormat, error) {
	return nil, AudioFormat{}, errors.New("захват WASAPI доступен только в Windows")
}

func openNativeLoopbackDevice(ctx context.Context, logs *LogBuffer, _ string) (io.ReadCloser, AudioFormat, error) {
	return openNativeLoopback(ctx, logs)
}
