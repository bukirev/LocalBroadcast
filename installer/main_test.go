//go:build setup

package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func testArchive(t *testing.T, name string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	file, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("installer payload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestUnpackRejectsEscapingPaths(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/outside", "normal.txt:stream"} {
		if err := unpack(testArchive(t, name), t.TempDir()); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestUnpackNestedFile(t *testing.T) {
	directory := t.TempDir()
	if err := unpack(testArchive(t, "setup/install.ps1"), directory); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "setup", "install.ps1"))
	if err != nil || string(data) != "installer payload" {
		t.Fatalf("payload: %q, %v", data, err)
	}
}
