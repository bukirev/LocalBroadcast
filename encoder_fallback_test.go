package main

import "testing"

func TestHardwareEncoderInitFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		encoder, line string
		want          bool
	}{
		{"h264_qsv", "[h264_qsv @ 0001] Error creating a MFX session: -9.", true},
		{"h264_qsv", "[vost#0:0/h264_qsv @ 0001] [enc:h264_qsv] Error while opening encoder - maybe incorrect parameters", true},
		{"h264_nvenc", "[h264_nvenc @ 0001] Cannot load nvcuda.dll", true},
		{"h264_amf", "[h264_amf @ 0001] Error while opening encoder", true},
		{"h264_qsv", "[gdigrab] Could not open display", false},
		{"h264_qsv", "WASAPI: COM method 5: HRESULT 0x88890004", false},
		{"h264_qsv", "[h264_qsv] Could not open encoder before EOF", false},
		{"libx264", "[libx264] Error while opening encoder", false},
	} {
		if got := hardwareEncoderInitFailed(tc.encoder, tc.line); got != tc.want {
			t.Errorf("%s: %q got=%v", tc.encoder, tc.line, got)
		}
	}
}

func TestHardwareFailureObserverHandlesSplitStderr(t *testing.T) {
	seen := false
	w := &lineWriter{logs: &LogBuffer{}, observe: func(line string) {
		if hardwareEncoderInitFailed("h264_qsv", line) {
			seen = true
		}
	}}
	w.Write([]byte("[h264_qsv @ 0001] Error creating a MFX sess"))
	w.Write([]byte("ion: -9.\n[gdigrab] unrelated\n"))
	if !seen {
		t.Fatal("lost encoder failure split across stderr writes")
	}
}
