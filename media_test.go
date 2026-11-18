package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFFmpegWindowsScreenAudioAndCameraArgs(t *testing.T) {
	c := defaultConfig()
	c.ScreenAudioDevice = "Stereo Mix (Test Device)"
	c.ScreenAudioMode = "dshow"
	c.CameraDevice = "USB Camera"
	c.CameraAudioDevice = "USB Mic"
	screen, err := ffmpegArgs("screen", c, 16900)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(screen, " ")
	for _, required := range []string{"gdigrab", "desktop", "audio=Stereo Mix (Test Device)", "-c:v libx264", "-f mpegts", "127.0.0.1:16900"} {
		if !strings.Contains(s, required) {
			t.Fatalf("screen command missing %q: %s", required, s)
		}
	}
	cam, err := ffmpegArgs("camera", c, 16901)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(cam, " ")
	for _, required := range []string{"dshow", "video=USB Camera", "audio=USB Mic", "1280x720", "127.0.0.1:16901"} {
		if !strings.Contains(text, required) {
			t.Fatalf("camera command missing %q: %s", required, text)
		}
	}
}
func TestDatagramFanoutAndDynamicRoutes(t *testing.T) {
	recipientA, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer recipientA.Close()
	recipientB, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer recipientB.Close()
	incoming, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	outgoing, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Pipeline{incoming: incoming, outgoing: outgoing, cancel: cancel, logs: &LogBuffer{}}
	defer p.Stop()
	go p.forwardLoop(ctx)
	portA := recipientA.LocalAddr().(*net.UDPAddr).Port
	portB := recipientB.LocalAddr().(*net.UDPAddr).Port
	p.SetTargets([]Endpoint{{IP: "127.0.0.1", Port: portA}, {IP: "127.0.0.1", Port: portB}})
	send, err := net.DialUDP("udp", nil, incoming.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer send.Close()
	packet := []byte{0x47, 0x00, 0x00, 0x01, 0xff}
	if _, err := send.Write(packet); err != nil {
		t.Fatal(err)
	}
	for _, receiver := range []*net.UDPConn{recipientA, recipientB} {
		buf := make([]byte, 32)
		_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := receiver.ReadFromUDP(buf)
		if err != nil {
			t.Fatal("missing datagram", err)
		}
		if n != len(packet) || buf[0] != 0x47 {
			t.Fatalf("corrupted packet %v", buf[:n])
		}
	}
	p.SetTargets([]Endpoint{{IP: "127.0.0.1", Port: portA}})
	if got := p.TargetCount(); got != 1 {
		t.Fatalf("route count = %d", got)
	}
	_, _ = send.Write(packet)
	buf := make([]byte, 32)
	_ = recipientA.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := recipientA.ReadFromUDP(buf); err != nil {
		t.Fatal(err)
	}
	_ = recipientB.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := recipientB.ReadFromUDP(buf); err == nil {
		t.Fatal("removed recipient still received packet")
	}
}
func TestOBSAuthenticationDeterministic(t *testing.T) {
	got := obsAuth("password", "salt", "challenge")
	if got != "zTM5ki6L2vVvBQiTG9ckH1Lh64AbnCf6XZ226UmnkIA=" {
		t.Fatalf("unexpected OBS hash %s", got)
	}
}
