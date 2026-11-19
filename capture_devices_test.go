package main

import (
	"reflect"
	"testing"
)

func TestCaptureRegionSupportsSecondaryMonitorLeftOfPrimary(t *testing.T) {
	d := CaptureDisplay{ID: "left", Left: -1920, Top: -240, Width: 1920, Height: 1080}
	got, err := captureRegionArgs("left", []CaptureDisplay{d})
	want := []string{"-offset_x", "-1920", "-offset_y", "-240", "-video_size", "1920x1080"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("region=%v err=%v", got, err)
	}
	if _, err = captureRegionArgs("disconnected", []CaptureDisplay{d}); err == nil {
		t.Fatal("disconnected monitor must not silently expose the entire desktop")
	}
}

func TestCaptureSelectionsPersistAndRestartOnlyTheirPipeline(t *testing.T) {
	store, err := newConfigStore(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	c := store.Get()
	oldScreen, oldCamera := pipelineSignature("screen", c), pipelineSignature("camera", c)
	c.ScreenAudioEndpoint = "{0.0.0.00000000}.test-device"
	c.ScreenMonitor = `\\.\DISPLAY2`
	if err = store.Update(c); err != nil {
		t.Fatal(err)
	}
	loaded, err := newConfigStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Get()
	if got.ScreenAudioEndpoint != c.ScreenAudioEndpoint || got.ScreenMonitor != c.ScreenMonitor {
		t.Fatal("selection lost on reload")
	}
	if oldScreen == pipelineSignature("screen", got) || oldCamera != pipelineSignature("camera", got) {
		t.Fatal("capture change must restart screen only")
	}
}
