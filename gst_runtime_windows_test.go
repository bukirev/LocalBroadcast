package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGSTPrepareAfterCoreUpdate(t *testing.T) {
	if os.Getenv("LB_TEST_GST_DOWNLOAD") != "1" {
		t.Skip("downloads official MSI into disposable user profile")
	}
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("PATH", filepath.Join(os.Getenv("SystemRoot"), "System32"))
	if err := prepareGStreamer(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	exe := gstBinary("gst-launch-1.0")
	if exe != filepath.Join(root, "LocalBroadcast", "Runtime", "gstreamer-1.26.10-x64", "bin", "gst-launch-1.0.exe") {
		t.Fatal("Unexpected runtime:", exe)
	}

	if err := prepareGStreamer(context.Background(), ""); err != nil {
		t.Fatal("Cannot reuse prepared runtime:", err)
	}

}
