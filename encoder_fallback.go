package main

import "strings"

// Only encoder initialization failures trigger fallback. Capture/device/network
// errors must retain their own diagnosis instead of silently changing codecs.
func hardwareEncoderInitFailed(encoder, line string) bool {
	switch encoder {
	case "h264_qsv", "h264_nvenc", "h264_amf":
	default:
		return false
	}
	s := strings.ToLower(line)
	if !strings.Contains(s, encoder) {
		return false
	}
	for _, message := range []string{
		"error while opening encoder", "error creating a mfx session",
		"error initializing an mfx session", "mfx implementation is not supported",
		"cannot load nvcuda", "cannot load nvencodeapi", "no nvenc capable devices found",
		"does not support the required nvenc api", "openencodesessionex failed",
		"amfrt64.dll failed", "amf failed to initialise", "amf failed to initialize",
	} {
		if strings.Contains(s, message) {
			return true
		}
	}
	return false
}
