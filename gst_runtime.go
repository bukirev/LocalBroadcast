package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const gstMSIName = "gstreamer-1.0-msvc-x86_64-1.26.10.msi"
const gstMSIHash = "a863bf3faa49e9f33bd3cc42967b473482d4dc98655ed95cba1ac59f26fb0cfb"

var gstInstallSlot = make(chan struct{}, 1)

func gstCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "LocalBroadcast", "Runtime", "gstreamer-1.26.10-x64")
}

// Install privately from a pinned distribution. Publishing the extracted directory
// in one rename keeps a cancelled or concurrent setup from exposing a partial runtime.
func prepareGStreamer(ctx context.Context, offlineMSI string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	select {
	case gstInstallSlot <- struct{}{}:
		defer func() { <-gstInstallSlot }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, err := exec.LookPath(gstBinary("gst-launch-1.0")); err == nil && offlineMSI == "" {
		return nil
	}
	dst := gstCacheDir()
	if runtime.GOOS != "windows" || dst == "" {
		return fmt.Errorf("GStreamer runtime requires Windows")
	}
	if _, err := os.Stat(filepath.Join(dst, "bin", "gst-launch-1.0.exe")); err == nil {
		return nil
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, "prepare-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	var input io.ReadCloser
	if offlineMSI != "" {
		input, err = os.Open(offlineMSI)
	} else {
		req, e := http.NewRequestWithContext(ctx, "GET", "https://gstreamer.freedesktop.org/data/pkg/windows/1.26.10/msvc/"+gstMSIName, nil)
		if e != nil {
			return e
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			return e
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("GStreamer download: HTTP %d", resp.StatusCode)
		}
		input = resp.Body
	}
	if err != nil {
		return err
	}
	defer input.Close()
	msi := filepath.Join(stage, "runtime.msi")
	f, err := os.Create(msi)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, hash), io.LimitReader(input, 100<<20))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != gstMSIHash {
		return fmt.Errorf("GStreamer MSI SHA256 mismatch")
	}
	expanded := filepath.Join(stage, "expanded")
	cmd := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "msiexec.exe"), "/a", msi, "/qn", "TARGETDIR="+expanded)
	prepareHeadlessCommand(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("GStreamer extraction: %w", err)
	}
	source := filepath.Join(expanded, "PFiles64", "gstreamer", "1.0", "msvc_x86_64")
	inspect := filepath.Join(source, "bin", "gst-inspect-1.0.exe")
	for _, el := range []string{"wasapi2src", "d3d11screencapturesrc", "mfvideosrc", "x264enc", "avenc_aac", "mpegtsmux", "watchdog"} {
		check := exec.CommandContext(ctx, inspect, "--exists", el)
		prepareHeadlessCommand(check)
		if err := check.Run(); err != nil {
			return fmt.Errorf("missing GStreamer element %s: %w", el, err)
		}
	}
	if err := os.Rename(source, dst); err != nil {
		if _, race := os.Stat(filepath.Join(dst, "bin", "gst-launch-1.0.exe")); race != nil {
			return err
		}
	}
	return nil
}
