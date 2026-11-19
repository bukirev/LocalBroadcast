//go:build windows

package main

// Native WASAPI loopback capture, using COM through Go's Windows syscall ABI.
// No external audio driver, virtual cable or third-party DLL is required.

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

type comGUID struct {
	A    uint32
	B, C uint16
	D    [8]byte
}

var (
	ole32           = syscall.NewLazyDLL("ole32.dll")
	coInit          = ole32.NewProc("CoInitializeEx")
	coUninit        = ole32.NewProc("CoUninitialize")
	coCreate        = ole32.NewProc("CoCreateInstance")
	coFree          = ole32.NewProc("CoTaskMemFree")
	clsidEnumerator = comGUID{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidEnumerator   = comGUID{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidAudioClient  = comGUID{0x1CB9AD4C, 0xDBFA, 0x4C32, [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidCapture      = comGUID{0xC8ADBD64, 0xE71E, 0x48A0, [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

func comMethod(obj uintptr, i int) uintptr {
	return (*[20]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))[i]
}
func callCOM(obj uintptr, i int, args ...uintptr) error {
	a := append([]uintptr{obj}, args...)
	hr, _, _ := syscall.SyscallN(comMethod(obj, i), a...)
	if int32(hr) < 0 {
		return fmt.Errorf("COM method %d: HRESULT 0x%08x", i, uint32(hr))
	}
	return nil
}
func releaseCOM(obj uintptr) {
	if obj != 0 {
		_ = callCOM(obj, 2)
	}
}
func formatFromWave(p uintptr) (AudioFormat, int, error) {
	b := unsafe.Slice((*byte)(unsafe.Pointer(p)), 18)
	tag := binary.LittleEndian.Uint16(b[0:2])
	ch := int(binary.LittleEndian.Uint16(b[2:4]))
	rate := int(binary.LittleEndian.Uint32(b[4:8]))
	block := int(binary.LittleEndian.Uint16(b[12:14]))
	bits := int(binary.LittleEndian.Uint16(b[14:16]))
	if tag == 0xFFFE { // WAVE_FORMAT_EXTENSIBLE: GUID subtype starts at offset 24
		b = unsafe.Slice((*byte)(unsafe.Pointer(p)), 40)
		tag = uint16(binary.LittleEndian.Uint32(b[24:28]))
	}
	if ch < 1 || ch > 8 || rate < 8000 || rate > 192000 || block < ch {
		return AudioFormat{}, 0, fmt.Errorf("неподдерживаемый аудиоформат %d Гц, %d каналов", rate, ch)
	}
	pcm := ""
	switch {
	case tag == 3 && bits == 32:
		pcm = "f32le"
	case tag == 3 && bits == 64:
		pcm = "f64le"
	case tag == 1 && bits == 16:
		pcm = "s16le"
	case tag == 1 && bits == 24:
		pcm = "s24le"
	case tag == 1 && bits == 32:
		pcm = "s32le"
	case tag == 1 && bits == 8:
		pcm = "u8"
	}
	if pcm == "" || block != bits/8*ch {
		return AudioFormat{}, 0, fmt.Errorf("WASAPI mix format не поддержан: tag=%d bits=%d block=%d channels=%d", tag, bits, block, ch)
	}
	return AudioFormat{PCM: pcm, Rate: rate, Channels: ch}, block, nil
}

type audioCaptureProbe struct {
	Device uint64 `json:"device"`
	QPC    uint64 `json:"qpc"`
	Mapped int64  `json:"mapped"`
	Frames uint32 `json:"frames"`
	Flags  uint32 `json:"flags"`
	PCM    []byte `json:"-"`
}

func openNativeLoopback(ctx context.Context, logs *LogBuffer, probes ...func(audioCaptureProbe)) (io.ReadCloser, AudioFormat, error) {
	return openNativeLoopbackSelected(ctx, logs, "", probes...)
}

func openNativeLoopbackDevice(ctx context.Context, logs *LogBuffer, deviceID string) (io.ReadCloser, AudioFormat, error) {
	return openNativeLoopbackSelected(ctx, logs, deviceID)
}

func openNativeLoopbackSelected(ctx context.Context, logs *LogBuffer, deviceID string, probes ...func(audioCaptureProbe)) (io.ReadCloser, AudioFormat, error) {
	captureCtx, cancel := context.WithCancel(ctx)
	deferOnError := true
	defer func() {
		if deferOnError {
			cancel()
		}
	}()
	type result struct {
		f      AudioFormat
		stream *pcmTimeline
		err    error
	}
	ready := make(chan result, 1)
	// COM interface lifetime and all calls remain on the same MTA OS thread.
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		r, _, _ := coInit.Call(0, 0)
		if int32(r) < 0 {
			ready <- result{err: fmt.Errorf("CoInitializeEx: 0x%08x", uint32(r))}
			return
		}
		defer coUninit.Call()
		var enumerator, device, client, capture, fmtptr uintptr
		defer func() {
			releaseCOM(capture)
			releaseCOM(client)
			releaseCOM(device)
			releaseCOM(enumerator)
			if fmtptr != 0 {
				coFree.Call(fmtptr)
			}
		}()
		r, _, _ = coCreate.Call(uintptr(unsafe.Pointer(&clsidEnumerator)), 0, 23, uintptr(unsafe.Pointer(&iidEnumerator)), uintptr(unsafe.Pointer(&enumerator)))
		if int32(r) < 0 {
			ready <- result{err: fmt.Errorf("MMDeviceEnumerator: 0x%08x", uint32(r))}
			return
		}
		var deviceErr error
		if deviceID == "" {
			deviceErr = callCOM(enumerator, 4, 0, 0, uintptr(unsafe.Pointer(&device)))
		} else {
			id, err := syscall.UTF16PtrFromString(deviceID)
			if err != nil {
				ready <- result{err: err}
				return
			}
			deviceErr = callCOM(enumerator, 5, uintptr(unsafe.Pointer(id)), uintptr(unsafe.Pointer(&device)))
			runtime.KeepAlive(id)
		}
		if deviceErr != nil {
			ready <- result{err: deviceErr}
			return
		}

		if err := callCOM(device, 3, uintptr(unsafe.Pointer(&iidAudioClient)), 23, 0, uintptr(unsafe.Pointer(&client))); err != nil {
			ready <- result{err: err}
			return
		}
		if err := callCOM(client, 8, uintptr(unsafe.Pointer(&fmtptr))); err != nil {
			ready <- result{err: err}
			return
		}
		f, block, err := formatFromWave(fmtptr)
		if err != nil {
			ready <- result{err: err}
			return
		}
		// Shared-mode capacity is 100 ms, drained every 5 ms. Duration is in
		// 100-ns units (not Go nanoseconds); capacity does not delay packet reads.
		if err = callCOM(client, 3, 0, 0x00020000, uintptr(100*time.Millisecond/(100*time.Nanosecond)), 0, fmtptr, 0); err != nil {
			ready <- result{err: err}
			return
		}
		if err = callCOM(client, 14, uintptr(unsafe.Pointer(&iidCapture)), uintptr(unsafe.Pointer(&capture))); err != nil {
			ready <- result{err: err}
			return
		}
		stream := newPCMTimeline(f, block)
		defer stream.Close()
		var counter, frequency int64
		kernel := syscall.NewLazyDLL("kernel32.dll")
		kernel.NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&frequency)))
		kernel.NewProc("QueryPerformanceCounter").Call(uintptr(unsafe.Pointer(&counter)))
		qpcStart := int64(0)
		if frequency > 0 {
			qpcStart = counter/frequency*10000000 + counter%frequency*10000000/frequency
		}
		if err = callCOM(client, 10); err != nil {
			ready <- result{err: err}
			return
		}
		defer callCOM(client, 11)
		logs.Add(fmt.Sprintf("WASAPI: захват %s · %d Гц · %d каналов", captureDeviceName(device), f.Rate, f.Channels))
		ready <- result{f: f, stream: stream}
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		initialDeviceID, _ := captureDeviceID(device)
		nextDeviceCheck := time.Now().Add(time.Second)
		for {
			select {
			case <-captureCtx.Done():
				return
			case <-ticker.C:
			}
			if deviceID == "" && time.Now().After(nextDeviceCheck) {
				nextDeviceCheck = time.Now().Add(time.Second)
				var current uintptr
				if err := callCOM(enumerator, 4, 0, 0, uintptr(unsafe.Pointer(&current))); err == nil {
					id, idErr := captureDeviceID(current)
					releaseCOM(current)
					if idErr == nil && initialDeviceID != "" && id != initialDeviceID {
						logs.Add("WASAPI: устройство Windows по умолчанию изменилось; переключаем захват")
						return
					}
				}
			}
			for {
				var size uint32
				if err = callCOM(capture, 5, uintptr(unsafe.Pointer(&size))); err != nil {
					logs.Add("WASAPI: " + err.Error())
					return
				}
				if size == 0 {
					break
				}
				var data uintptr
				var frames, flags uint32
				var devicePos, qpc uint64
				if err = callCOM(capture, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&devicePos)), uintptr(unsafe.Pointer(&qpc))); err != nil {
					logs.Add("WASAPI: " + err.Error())
					return
				}
				if int(frames) > f.Rate {
					_ = callCOM(capture, 4, uintptr(frames))
					logs.Add("WASAPI: oversized packet")
					return
				}
				pcm := make([]byte, int(frames)*block)
				if stream.silence != 0 {
					for i := range pcm {
						pcm[i] = stream.silence
					}
				}
				if flags&2 == 0 && data != 0 {
					copy(pcm, unsafe.Slice((*byte)(unsafe.Pointer(data)), len(pcm)))
				}
				_ = callCOM(capture, 4, uintptr(frames))
				// QPC anchors time; device positions preserve exact continuity.
				elapsed := time.Duration(int64(qpc)-qpcStart) * 100 * time.Nanosecond
				clockFlags := flags
				if qpcStart == 0 || qpc == 0 {
					clockFlags |= 4
				}
				position := stream.capturePosition(devicePos, elapsed, frames, clockFlags)
				if len(probes) > 0 && probes[0] != nil {
					probes[0](audioCaptureProbe{Device: devicePos, QPC: qpc, Mapped: position, Frames: frames, Flags: flags, PCM: pcm})
				}
				stream.push(position, pcm)
			}
		}
	}()
	select {
	case r := <-ready:
		if r.err != nil {
			return nil, AudioFormat{}, r.err
		}
		deferOnError = false
		go func() {
			select {
			case <-ctx.Done():
				r.stream.Close()
			case <-r.stream.done:
			}
			cancel()
		}()
		return r.stream, r.f, nil
	case <-ctx.Done():
		return nil, AudioFormat{}, ctx.Err()
	}
}
