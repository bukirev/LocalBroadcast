//go:build windows

package main

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

func TestWASAPIMixFormats(t *testing.T) {
	for _, tc := range []struct {
		tag, bits uint16
		format    string
	}{{3, 32, "f32le"}, {1, 16, "s16le"}, {1, 24, "s24le"}} {
		b := make([]byte, 40)
		binary.LittleEndian.PutUint16(b[0:2], tc.tag)
		binary.LittleEndian.PutUint16(b[2:4], 2)
		binary.LittleEndian.PutUint32(b[4:8], 48000)
		binary.LittleEndian.PutUint16(b[12:14], tc.bits/8*2)
		binary.LittleEndian.PutUint16(b[14:16], tc.bits)
		f, _, err := formatFromWave(uintptr(unsafe.Pointer(&b[0])))
		if err != nil || f.PCM != tc.format {
			t.Fatalf("fmt %+v err %v", f, err)
		}
	}
}
