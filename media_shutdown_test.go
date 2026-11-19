package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A real subprocess owns a UDP socket. Stop must reap it before returning,
// otherwise the next service instance cannot reuse its media port on Windows.
func TestMediaReceiverProcessHelper(t *testing.T) {
	ready := os.Getenv("LB_MEDIA_PROCESS_READY")
	if ready == "" {
		t.Skip("subprocess helper")
	}
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.WriteFile(ready, []byte(listener.LocalAddr().String()), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestTransportStopReleasesProcessPort(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("LB_MEDIA_PROCESS_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	w := &transportWorker{cancel: cancel, done: make(chan struct{}), ffmpeg: os.Args[0]}
	go runSRTProcess(ctx, w, &LogBuffer{}, "test", func(int) []string { return []string{"-test.run=^TestMediaReceiverProcessHelper$"} })
	defer w.stop()
	var address []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		address, _ = os.ReadFile(ready)
		if len(address) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(address) == 0 {
		t.Fatal("Media subprocess did not start")
	}
	w.stop()
	listener, err := net.ListenPacket("udp4", string(address))
	if err != nil {
		t.Fatalf("Media port was not released before Stop returned: %v", err)
	}
	listener.Close()
}

func TestClosedMediaManagerCannotRestartReceivers(t *testing.T) {
	m := newMediaManager(&LogBuffer{})
	m.Close()
	c := defaultConfig()
	c.Transport = "srt"
	m.SyncMasterReceivers([]Peer{{ID: "presenter", SendScreen: true, LastSeen: time.Now()}}, c)
	if len(m.receivers) != 0 {
		t.Fatal("Heartbeat recreated a receiver during shutdown")
	}
}
