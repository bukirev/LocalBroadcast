package main

import (
	"strings"
	"testing"
	"time"
)

func TestNativeWASAPIFFmpegCommandUsesPCMPipe(t *testing.T) {
	c := defaultConfig()
	if c.ScreenAudioMode != "wasapi" {
		t.Fatal("new configuration should select native WASAPI")
	}
	format := AudioFormat{PCM: "s16le", Rate: 44100, Channels: 2}
	args, err := ffmpegArgsPCM("screen", c, 16900, &format)
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, wanted := range []string{" -f s16le ", " -ar 44100 ", " -ac 2 ", " -i pipe:0 ", " aresample=async=1000:first_pts=0 "} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("missing %q", wanted)
		}
	}
	if strings.Contains(joined, "-f dshow") {
		t.Fatal("must not require DirectShow capture of system audio")
	}
	c.ScreenAudioMode = "off"
	args, err = ffmpegArgs("screen", c, 16900)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "pipe:0") {
		t.Fatal("audio off must not pipe PCM")
	}
}
func TestRTTSynchronizationAndSavedCalibration(t *testing.T) {
	now := time.Now()
	c := defaultConfig()
	peers := []Peer{
		{ID: "presenter123", Name: "Presenter", SendScreen: true, RTTMS: 100, LastSeen: now},
		{ID: "camera123", Name: "Camera", SendCamera: true, RTTMS: 20, LastSeen: now},
	}
	sync := plannedSyncOffsets(c, peers)
	cam := sync["camera123:camera"]
	if cam.RenderDelayMS != 40 || cam.AudioOffsetMS != 40 {
		t.Fatalf("expected network difference 40ms: %+v", cam)
	}
	c.SyncOffsets["camera123:camera"] = SyncTuning{VideoMS: 25, AudioMS: -15}
	sync = plannedSyncOffsets(c, peers)
	cam = sync["camera123:camera"]
	if cam.RenderDelayMS != 65 || cam.AudioOffsetMS != 50 {
		t.Fatalf("expected calibrated result 65ms video 50ms audio: %+v", cam)
	}
	c.SyncEnabled = false
	if got := plannedSyncOffsets(c, peers)["camera123:camera"].RenderDelayMS; got != 25 {
		t.Fatalf("manual offset must remain %d", got)
	}
}
func TestSRTURLsAndMasterPlan(t *testing.T) {
	c := defaultConfig()
	if !strings.Contains(srtURL("192.168.1.5", 11000, 200, c.Secret, "caller"), "latency=200000") {
		t.Fatal("SRT latency units should be microseconds")
	}
	if srtPort(5102) != 11102 {
		t.Fatal("unexpected SRT port mapping")
	}
	f := coordinatorFixture(t)
	cfg := f.cfg.Get()
	cfg.Transport = "srt"
	cfg.SRTLatencyMS = 220
	cfg.SRTAdaptive = true
	if err := f.cfg.Update(cfg); err != nil {
		t.Fatal(err)
	}
	peer := registerPeer(t, f, "presenter01", "Presenter", "192.168.1.20", true, false, false)
	plan, err := f.computePlan(peer.ID, "192.168.1.10")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Transport != "srt" || plan.SRTLatencyMS != 220 {
		t.Fatalf("master must control protocol: %+v", plan)
	}
}

func TestSyncIgnoresSlowViewer(t *testing.T) {
	peers := []Peer{{ID: "presenter", SendScreen: true, RTTMS: 10, LastSeen: time.Now()}, {ID: "viewer", RTTMS: 800, LastSeen: time.Now()}}
	got := plannedSyncOffsets(defaultConfig(), peers)["presenter:screen"]
	if got.RenderDelayMS != 0 {
		t.Fatalf("viewer delayed presenter: %+v", got)
	}
}
