//go:build !windows

package main

import "errors"

func captureDisplays() ([]CaptureDisplay, error) {
	return nil, errors.New("выбор экрана доступен только в Windows")
}

func captureAudioOutputs() ([]CaptureAudioOutput, error) {
	return nil, errors.New("выбор WASAPI доступен только в Windows")
}
