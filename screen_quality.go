package main

import "fmt"

func screenOutputSize(c Configuration) (int, int) {
	if c.ScreenWidth == 0 && c.ScreenHeight == 0 {
		return 1920, 1080
	}
	return c.ScreenWidth, c.ScreenHeight
}

func screenCaptureFPS(c Configuration) int {
	if c.ScreenFPSMode == "adaptive" {
		return 30
	}
	return c.ScreenFPS
}

func screenVideoFilter(c Configuration) string {
	w, h := screenOutputSize(c)
	// Scale to fit without stretching portrait or multi-monitor captures. Keep
	// even dimensions for H.264 4:2:0, then pad to the chosen output resolution.
	f := fmt.Sprintf("scale=%d:%d:flags=fast_bilinear:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1", w, h, w, h)
	if c.ScreenFPSMode == "adaptive" {
		// At 30 input FPS, remove at most one consecutive near-duplicate frame:
		// static content approaches 15 FPS, changing content approaches 30 FPS.
		// Preserve original timestamps; audio is neither restarted nor retimed.
		f += ",mpdecimate=max=1"
	}
	return f
}
