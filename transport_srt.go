package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The SRT wire port is disjoint from the stable local UDP port read by OBS.
// Keeping the local ports unchanged means OBS scenes need no transport-specific
// changes, and the operator can switch back to UDP without rebuilding scenes.
const srtPortOffset = 6000

func srtPort(port int) int { return port + srtPortOffset }

func srtURL(ip string, port, latencyMS int, secret, mode string) string {
	// Keys are hex strings provisioned during pairing. Never log this URL.
	return fmt.Sprintf("srt://%s:%d?mode=%s&latency=%d&passphrase=%s&pbkeylen=16&transtype=live", ip, port, mode, latencyMS*1000, secret)
}

type transportWorker struct {
	cancel           context.CancelFunc
	latencyBase      int
	ffmpeg           string
	secret           string
	adaptive         bool
	reconnects       atomic.Uint64
	effectiveLatency atomic.Int64
}

func (w *transportWorker) stop() { w.cancel() }

// transportWorker has its own lifecycle, so read and write media do not block
// the HTTP coordinator's heartbeat loop.
func runSRTProcess(ctx context.Context, w *transportWorker, logs *LogBuffer, label string, args func(int) []string) {
	latency := w.latencyBase
	retry := time.Second
	attempts := 0
	for ctx.Err() == nil {
		began := time.Now()
		w.effectiveLatency.Store(int64(latency))
		cmd := exec.CommandContext(ctx, binaryPath(w.ffmpeg), args(latency)...)
		prepareHeadlessCommand(cmd)
		cmd.Stderr = &lineWriter{logs: logs, prefix: "SRT/" + label}
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		attempts++
		w.reconnects.Add(1)
		logs.Add(fmt.Sprintf("SRT %s: переподключение (%v)", label, err))
		if w.adaptive {
			if time.Since(began) < 30*time.Second && attempts >= 2 {
				// Repeated short sessions suggest a weak or unstable network.
				// Increase buffer on NEXT session (not during active delivery).
				latency += 40
				if latency > 600 {
					latency = 600
				}
			} else if time.Since(began) > 3*time.Minute && latency > w.latencyBase {
				latency -= 20
				if latency < w.latencyBase {
					latency = w.latencyBase
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		if retry < 6*time.Second {
			retry *= 2
		}
	}
}

// A receiving process listens for SRT on the LAN, then forwards its MPEG-TS
// payload to local UDP where OBS or the previewer is already listening.
func startSRTReceiver(localPort int, c Configuration, logs *LogBuffer) *transportWorker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &transportWorker{cancel: cancel, latencyBase: c.SRTLatencyMS, ffmpeg: c.FFmpegPath, secret: c.Secret, adaptive: c.SRTAdaptive}
	go runSRTProcess(ctx, w, logs, "приём:"+strconv.Itoa(localPort), func(latency int) []string {
		return []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-fflags", "+genpts", "-analyzeduration", "0", "-probesize", "32768",
			"-f", "mpegts", "-i", srtURL("0.0.0.0", srtPort(localPort), latency, c.Secret, "listener"),
			"-map", "0", "-c", "copy", "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts",
			fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", localPort)}
	})
	logs.Add(fmt.Sprintf("SRT: ожидаем вход на UDP %d, внутренний порт %d", srtPort(localPort), localPort))
	return w
}

func freeUDPLocalPort() (int, error) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return 0, err
	}
	port := listener.LocalAddr().(*net.UDPAddr).Port
	_ = listener.Close()
	return port, nil
}
func startSRTSender(localPort int, dst *net.UDPAddr, c Configuration, logs *LogBuffer) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	w := &transportWorker{cancel: cancel, latencyBase: c.SRTLatencyMS, ffmpeg: c.FFmpegPath, secret: c.Secret, adaptive: false}
	go runSRTProcess(ctx, w, logs, "отправка:"+dst.String(), func(latency int) []string {
		return []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-fflags", "+genpts", "-analyzeduration", "0", "-probesize", "32768",
			"-f", "mpegts", "-i", fmt.Sprintf("udp://127.0.0.1:%d?fifo_size=25000&overrun_nonfatal=1", localPort),
			"-map", "0", "-c", "copy", "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts",
			srtURL(dst.IP.String(), srtPort(dst.Port), latency, c.Secret, "caller")}
	})
	return cancel
}

// SetTransport changes only relay destinations; it never restarts screen or
// camera capture unless encoding settings actually changed.
func (p *Pipeline) SetTransport(endpoints []Endpoint, c Configuration) {
	if c.Transport != "srt" {
		p.mu.Lock()
		for _, cancel := range p.relayCancel {
			cancel()
		}
		p.relayCancel = map[string]context.CancelFunc{}
		p.transport = "udp"
		p.mu.Unlock()
		p.SetTargets(endpoints)
		return
	}
	desired := map[string]*net.UDPAddr{}
	for _, e := range endpoints {
		ip := net.ParseIP(e.IP)
		if ip == nil || ip.To4() == nil || e.Port < 1024 || e.Port > 59000 {
			continue
		}
		dst := &net.UDPAddr{IP: ip.To4(), Port: e.Port}
		desired[dst.String()] = dst
	}
	optionSignature := fmt.Sprintf("srt:%d:%s:%s", c.SRTLatencyMS, c.FFmpegPath, c.Secret)
	p.mu.Lock()
	if p.transport != optionSignature {
		for _, cancel := range p.relayCancel {
			cancel()
		}
		p.relayCancel = map[string]context.CancelFunc{}
		p.relayPorts = map[string]int{}
		p.transport = optionSignature
	}
	for key, cancel := range p.relayCancel {
		if desired[key] == nil {
			cancel()
			delete(p.relayCancel, key)
			delete(p.relayPorts, key)
		}
	}
	for key, dst := range desired {
		if p.relayCancel[key] != nil {
			continue
		}
		port, err := freeUDPLocalPort()
		if err != nil {
			p.logs.Add("SRT relay: " + err.Error())
			continue
		}
		p.relayPorts[key] = port
		p.relayCancel[key] = startSRTSender(port, dst, c, p.logs)
	}
	loopback := make([]*net.UDPAddr, 0, len(p.relayPorts))
	for _, port := range p.relayPorts {
		loopback = append(loopback, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	}
	p.targets = loopback
	p.mu.Unlock()
}
func validateSRTSettings(c Configuration) error {
	if strings.TrimSpace(c.Secret) == "" {
		return fmt.Errorf("для SRT нужен ключ системы")
	}
	if c.SRTLatencyMS < 80 {
		return fmt.Errorf("SRT latency слишком мала")
	}
	return nil
}
