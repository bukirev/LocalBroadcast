package main

import (
	"fmt"
	"strconv"
)

type CaptureDisplay struct {
	handle  uintptr
	ID      string `json:"id"`
	Name    string `json:"name"`
	Left    int    `json:"left"`
	Top     int    `json:"top"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Primary bool   `json:"primary"`
}

type CaptureAudioOutput struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

func captureRegionArgs(id string, displays []CaptureDisplay) ([]string, error) {
	for _, d := range displays {
		if d.ID == id && d.Width > 0 && d.Height > 0 {
			return []string{"-offset_x", strconv.Itoa(d.Left), "-offset_y", strconv.Itoa(d.Top), "-video_size", fmt.Sprintf("%dx%d", d.Width, d.Height)}, nil
		}
	}
	return nil, fmt.Errorf("выбранный экран недоступен; обновите список устройств и выберите экран заново")
}
