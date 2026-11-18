//go:build setup

package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed payload.zip
var payload []byte

func unpack(data []byte, destination string) error {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, entry := range archive.File {
		name := filepath.FromSlash(entry.Name)
		if !filepath.IsLocal(name) || strings.Contains(name, ":") {
			return fmt.Errorf("invalid archive path: %s", entry.Name)
		}
		path := filepath.Join(destination, name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := extractFile(entry, path); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(entry *zip.File, path string) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func run() error {
	directory, err := os.MkdirTemp("", "LocalBroadcast-Setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	if err := unpack(payload, directory); err != nil {
		return err
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(directory, "install.ps1"))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Setup failed:", err, "\nPress Enter to close.")
		_, _ = fmt.Scanln()
		os.Exit(1)
	}
}
