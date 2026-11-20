package main

import "testing"

func TestAutomaticScreenBackend(t *testing.T) {
	for _, tc := range []struct {
		kind, choice, mode, audio, monitor string
		count                              int
		gst                                bool
	}{
		{"screen", "", "fixed", "wasapi", "", 1, true}, {"screen", "", "fixed", "off", "", 1, true},
		{"screen", "", "fixed", "wasapi", "", 2, false}, {"screen", "", "fixed", "wasapi", "display", 2, true},
		{"screen", "", "adaptive", "wasapi", "", 1, false}, {"screen", "", "fixed", "dshow", "", 1, false},
		{"camera", "", "fixed", "wasapi", "", 1, false}, {"camera", "gstreamer", "fixed", "wasapi", "", 1, true},
		{"screen", "ffmpeg", "fixed", "wasapi", "", 1, false}, {"screen", "", "fixed", "wasapi", "", 0, false},
	} {
		c := Configuration{MediaBackend: tc.choice, ScreenFPSMode: tc.mode, ScreenAudioMode: tc.audio, ScreenMonitor: tc.monitor}
		if got := preferGStreamer(tc.kind, c, func() ([]CaptureDisplay, error) { return make([]CaptureDisplay, tc.count), nil }); got != tc.gst {
			t.Errorf("%+v selected GStreamer=%v", tc, got)
		}
	}
}
