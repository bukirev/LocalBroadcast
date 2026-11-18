package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Agent struct {
	cfg       *ConfigStore
	discover  *Discover
	media     *MediaManager
	logs      *LogBuffer
	mu        sync.RWMutex
	connected bool
	lastError string
	master    string
	plan      Plan
	lastGood  time.Time
	rttMS     int
}

func newAgent(cfg *ConfigStore, d *Discover, media *MediaManager, logs *LogBuffer) *Agent {
	return &Agent{cfg: cfg, discover: d, media: media, logs: logs}
}
func (a *Agent) Status() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return map[string]any{"connected": a.connected, "master": a.master, "error": a.lastError, "plan": a.plan}
}
func (a *Agent) setStatus(connected bool, master, errMsg string, plan Plan) {
	a.mu.Lock()
	oldConnected := a.connected
	oldErr := a.lastError
	a.connected = connected
	a.master = master
	a.lastError = errMsg
	a.plan = plan
	if connected {
		a.lastGood = time.Now()
	}
	a.mu.Unlock()
	if connected != oldConnected {
		if connected {
			a.logs.Add("Подключён к главному ПК: " + master)
		} else {
			a.logs.Add("Нет соединения с главным ПК: " + errMsg)
		}
	}
	if !connected && oldErr != errMsg && errMsg != "" {
		a.logs.Add("Связь: " + errMsg)
	}
}
func (a *Agent) selectedMaster() string {
	c := a.cfg.Get()
	if strings.TrimSpace(c.MasterIP) != "" {
		host := strings.TrimSpace(c.MasterIP)
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil || !(ip.IsPrivate() || ip.IsLoopback()) {
			return ""
		}
		return fmt.Sprintf("http://%s:%d", ip.String(), coordinatorPort)
	}
	return a.discover.Get()
}
func (a *Agent) run(ctx context.Context) {
	// Http timeout ensures coordinator failure cannot hang the streaming supervisor.
	httpClient := &http.Client{Timeout: 2500 * time.Millisecond}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		c := a.cfg.Get()
		if c.Mode != "master" && c.AutoStart {
			a.poll(ctx, httpClient, c)
		} else {
			a.media.Apply(Plan{}, c)
		}
		select {
		case <-ctx.Done():
			a.media.Close()
			return
		case <-ticker.C:
		}
	}
}
func (a *Agent) poll(ctx context.Context, hc *http.Client, c Configuration) {
	master := a.selectedMaster()
	if master == "" {
		a.disconnect("", "Ожидание обнаружения OBS", c)
		return
	}
	hb := heartbeatRequest{NodeID: c.NodeID, Name: c.Name, Mode: c.Mode, SendScreen: c.SendScreen, SendCamera: c.SendCamera,
		WatchPresenter: c.WatchPresenter, MediaStatus: a.media.Summary(), Version: appVersion, RTTMS: a.rttMS}
	body, _ := json.Marshal(hb)
	req, err := http.NewRequestWithContext(ctx, "POST", master+"/v1/heartbeat", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("X-LB-Key", c.Secret)
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		a.disconnect(master, err.Error(), c)
		return
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		a.disconnect(master, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data))), c)
		return
	}
	rtt := int(time.Since(started).Milliseconds())
	if rtt < 0 {
		rtt = 0
	}
	if rtt > 3000 {
		rtt = 3000
	}
	if a.rttMS == 0 {
		a.rttMS = rtt
	} else {
		a.rttMS = (a.rttMS*3 + rtt) / 4
	}
	req2, err := http.NewRequestWithContext(ctx, "GET", master+"/v1/plan?id="+c.NodeID, nil)
	if err != nil {
		return
	}
	req2.Header.Set("X-LB-Key", c.Secret)
	resp2, err := hc.Do(req2)
	if err != nil {
		a.disconnect(master, err.Error(), c)
		return
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(resp2.Body, 1024))
		a.disconnect(master, string(data), c)
		return
	}
	var p Plan
	if err := json.NewDecoder(io.LimitReader(resp2.Body, 128*1024)).Decode(&p); err != nil {
		a.disconnect(master, err.Error(), c)
		return
	}
	a.setStatus(true, master, "", p)
	a.media.Apply(p, c)
}

// Brief Wi-Fi or coordinator hiccups must not tear down working video every 2 seconds.
// Hold the last routing plan for 7 seconds before stopping old UDP streams.
func (a *Agent) disconnect(master, reason string, c Configuration) {
	a.mu.RLock()
	good := a.lastGood
	a.mu.RUnlock()
	a.setStatus(false, master, reason, Plan{})
	if good.IsZero() || time.Since(good) > 7*time.Second {
		a.media.Apply(Plan{}, c)
	}
}
