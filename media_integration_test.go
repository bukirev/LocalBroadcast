package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSRTTransportWithRealFFmpeg(t *testing.T) {
	if os.Getenv("LB_TEST_MEDIA") != "1" {
		t.Skip("use LB_TEST_MEDIA=1 for real FFmpeg integration tests")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg absent")
	}
	result, err := exec.Command(ffmpeg, "-hide_banner", "-protocols").Output()
	if err != nil || !strings.Contains(string(result), "srt\n") {
		t.Skip("FFmpeg built without libsrt")
	}
	var recv *net.UDPConn
	for i := 0; i < 8; i++ {
		recv, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatal(err)
		}
		if recv.LocalAddr().(*net.UDPAddr).Port < 58000 {
			break
		}
		recv.Close()
		recv = nil
	}
	if recv == nil {
		t.Skip("no usable receiver port")
	}
	defer recv.Close()
	localPort := recv.LocalAddr().(*net.UDPAddr).Port
	c := defaultConfig()
	c.SRTLatencyMS = 120
	c.FFmpegPath = ffmpeg
	w := startSRTReceiver(localPort, c, &LogBuffer{})
	defer w.stop()
	time.Sleep(750 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10",
		"-t", "3", "-c:v", "mpeg2video", "-b:v", "300k", "-f", "mpegts",
		srtURL("127.0.0.1", srtPort(localPort), c.SRTLatencyMS, c.Secret, "caller")}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	buf := make([]byte, 4096)
	_ = recv.SetReadDeadline(time.Now().Add(7 * time.Second))
	n, _, err := recv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("missing decoded SRT stream: %v", err)
	}
	if n < 188 || buf[0] != 0x47 {
		t.Fatalf("invalid MPEG TS packet length=%d first=%x", n, buf[0])
	}
}

func TestSRTFanoutTwoReceiversWithRealFFmpeg(t *testing.T) {
	if os.Getenv("LB_TEST_MEDIA") != "1" {
		t.Skip("use LB_TEST_MEDIA=1")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg absent")
	}
	out, err := exec.Command(ffmpeg, "-protocols").Output()
	if err != nil || !strings.Contains(string(out), "srt\n") {
		t.Skip("libsrt absent")
	}
	c := defaultConfig()
	c.Transport = "srt"
	c.SRTLatencyMS = 150
	c.FFmpegPath = ffmpeg
	logs := &LogBuffer{}
	var receivers []*net.UDPConn
	var workers []*transportWorker
	defer func() {
		for _, w := range workers {
			w.stop()
		}
		for _, r := range receivers {
			r.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		var r *net.UDPConn
		for j := 0; j < 8; j++ {
			r, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
			if err != nil {
				t.Fatal(err)
			}
			if r.LocalAddr().(*net.UDPAddr).Port < 58000 {
				break
			}
			r.Close()
			r = nil
		}
		if r == nil {
			t.Skip("no usable ports")
		}
		receivers = append(receivers, r)
		workers = append(workers, startSRTReceiver(r.LocalAddr().(*net.UDPAddr).Port, c, logs))
	}
	input, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	output, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		input.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{incoming: input, outgoing: output, cancel: cancel, relayCancel: map[string]context.CancelFunc{}, relayPorts: map[string]int{}, logs: logs}
	defer p.Stop()
	targets := []Endpoint{}
	for _, r := range receivers {
		targets = append(targets, Endpoint{IP: "127.0.0.1", Port: r.LocalAddr().(*net.UDPAddr).Port})
	}
	p.SetTransport(targets, c)
	if p.TargetCount() != 2 {
		t.Fatalf("expected two SRT fanout destinations, got %d", p.TargetCount())
	}
	go p.forwardLoop(ctx)
	time.Sleep(1000 * time.Millisecond)
	sendCtx, stop := context.WithTimeout(context.Background(), 8*time.Second)
	defer stop()
	args := []string{"-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10", "-t", "4", "-c:v", "mpeg2video", "-b:v", "300k", "-f", "mpegts", fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", input.LocalAddr().(*net.UDPAddr).Port)}
	cmd := exec.CommandContext(sendCtx, ffmpeg, args...)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	for i, r := range receivers {
		buf := make([]byte, 4096)
		_ = r.SetReadDeadline(time.Now().Add(6 * time.Second))
		n, _, er := r.ReadFromUDP(buf)
		if er != nil {
			t.Fatalf("receiver %d did not get data: %v\nlogs: %v", i, er, append(logs.List(), fmt.Sprintf("forwarded=%d", p.packetsForwarded.Load())))
		}
		if n < 188 || buf[0] != 0x47 {
			t.Fatalf("receiver %d got malformed MPEG TS n=%d", i, n)
		}
	}
}
