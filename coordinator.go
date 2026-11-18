package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Coordinator struct {
	mu         sync.RWMutex
	peers      map[string]Peer
	cfg        *ConfigStore
	logs       *LogBuffer
	generation int64
}

func newCoordinator(cfg *ConfigStore, logs *LogBuffer) *Coordinator {
	return &Coordinator{cfg: cfg, logs: logs, peers: make(map[string]Peer)}
}

type heartbeatRequest struct {
	NodeID         string `json:"nodeId"`
	Name           string `json:"name"`
	Mode           string `json:"mode"`
	WatchPresenter bool   `json:"watchPresenter"`
	SendScreen     bool   `json:"sendScreen"`
	SendCamera     bool   `json:"sendCamera"`
	MediaStatus    string `json:"mediaStatus"`
	Version        string `json:"version"`
	RTTMS          int    `json:"rttMs"`
}

func ipFromRemote(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return ""
	}
	return host
}
func (c *Coordinator) authorized(r *http.Request) bool {
	// Password is required for every remotely exposed control endpoint.
	token := r.Header.Get("X-LB-Key")
	cfg := c.cfg.Get()
	return len(token) > 0 && constantTimeEqual(token, cfg.Secret)
}
func (c *Coordinator) register(h heartbeatRequest, remoteIP string) (Peer, error) {
	if len(h.NodeID) < 8 || len(h.NodeID) > 128 || len(h.Name) == 0 || len(h.Name) > 72 {
		return Peer{}, errors.New("invalid client identity")
	}
	if h.RTTMS < 0 || h.RTTMS > 3000 {
		return Peer{}, errors.New("invalid RTT")
	}
	if net.ParseIP(remoteIP) == nil {
		return Peer{}, errors.New("invalid sender address")
	}
	port, err := c.cfg.AssignCameraPort(h.NodeID)
	if err != nil {
		return Peer{}, err
	}
	p := Peer{ID: h.NodeID, Name: h.Name, Mode: h.Mode, IP: remoteIP, CameraPort: port,
		WatchPresenter: h.WatchPresenter, SendScreen: h.SendScreen, SendCamera: h.SendCamera,
		LastSeen: time.Now(), MediaStatus: h.MediaStatus, Version: h.Version, RTTMS: h.RTTMS}
	c.mu.Lock()
	old, exists := c.peers[h.NodeID]
	c.peers[h.NodeID] = p
	if !exists || old.IP != p.IP || old.Mode != p.Mode || old.WatchPresenter != p.WatchPresenter || old.SendScreen != p.SendScreen || old.SendCamera != p.SendCamera {
		c.generation++
	}
	c.mu.Unlock()
	if !exists {
		c.logs.Add("Устройство подключено: " + h.Name + " (" + remoteIP + ")")
	}
	return p, nil
}
func (c *Coordinator) peersSnapshot() []Peer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return sortedPeers(c.peers)
}
func (c *Coordinator) onlinePeers() []Peer {
	all := c.peersSnapshot()
	out := make([]Peer, 0, len(all))
	for _, p := range all {
		if p.Online() {
			out = append(out, p)
		}
	}
	return out
}
func (c *Coordinator) findPresenter(peers []Peer) *Peer {
	for i := range peers {
		if peers[i].SendScreen {
			return &peers[i]
		}
	}
	return nil
}
func routeAllowed(cfg Configuration, sourceID, targetID string, defaultValue bool) bool {
	if targets, ok := cfg.Routing[sourceID]; ok {
		if v, yes := targets[targetID]; yes {
			return v
		}
	}
	return defaultValue
}
func (c *Coordinator) computePlan(id, masterIP string) (Plan, error) {
	all := c.onlinePeers()
	var self *Peer
	for i := range all {
		if all[i].ID == id {
			self = &all[i]
			break
		}
	}
	if self == nil {
		return Plan{}, errors.New("компьютер не зарегистрирован или потерял соединение")
	}
	cfg := c.cfg.Get()
	c.mu.RLock()
	gen := c.generation
	c.mu.RUnlock()
	plan := Plan{Presenter: SourcePlan{Enabled: self.SendScreen, Targets: []Endpoint{}}, Camera: SourcePlan{Enabled: self.SendCamera, Targets: []Endpoint{}},
		PresenterPort: presenterPort, MasterIP: masterIP, Generation: gen, Transport: cfg.Transport, SRTLatencyMS: cfg.SRTLatencyMS, SRTAdaptive: cfg.SRTAdaptive}
	// Exactly one active presenter. A second presenter is blocked, to avoid corrupting UDP:5000.
	presenter := c.findPresenter(all)
	if presenter != nil && presenter.ID != self.ID && self.SendScreen {
		plan.Presenter.Enabled = false
	}
	if presenter != nil && self.ID != presenter.ID {
		plan.ReceivePresenter = routeAllowed(cfg, presenter.ID+":screen", self.ID, self.WatchPresenter)
	}
	if self.SendScreen && plan.Presenter.Enabled {
		if routeAllowed(cfg, self.ID+":screen", "obs", true) {
			plan.Presenter.Targets = append(plan.Presenter.Targets, Endpoint{IP: masterIP, Port: presenterPort, ID: "obs", Name: "OBS"})
		}
		for _, p := range all {
			if p.ID == self.ID {
				continue
			}
			if routeAllowed(cfg, self.ID+":screen", p.ID, p.WatchPresenter) {
				plan.Presenter.Targets = append(plan.Presenter.Targets, Endpoint{IP: p.IP, Port: presenterPort, ID: p.ID, Name: p.Name})
			}
		}
	}
	if self.SendCamera && routeAllowed(cfg, self.ID+":camera", "obs", true) {
		plan.Camera.Targets = append(plan.Camera.Targets, Endpoint{IP: masterIP, Port: self.CameraPort, ID: "obs", Name: "OBS"})
	}
	return plan, nil
}
func (c *Coordinator) routes() []DisplayRoute {
	peers := c.onlinePeers()
	cfg := c.cfg.Get()
	out := []DisplayRoute{}
	presenter := c.findPresenter(peers)
	if presenter != nil {
		sourceID := presenter.ID + ":screen"
		out = append(out, DisplayRoute{SourceID: sourceID, Source: presenter.Name + " · экран", TargetID: "obs", Target: "OBS", Enabled: routeAllowed(cfg, sourceID, "obs", true), Default: true})
		for _, p := range peers {
			if p.ID == presenter.ID {
				continue
			}
			out = append(out, DisplayRoute{SourceID: sourceID, Source: presenter.Name + " · экран", TargetID: p.ID, Target: p.Name, Enabled: routeAllowed(cfg, sourceID, p.ID, p.WatchPresenter), Default: p.WatchPresenter})
		}
	}
	for _, p := range peers {
		if p.SendCamera {
			sourceID := p.ID + ":camera"
			out = append(out, DisplayRoute{SourceID: sourceID, Source: p.Name + " · камера", TargetID: "obs", Target: "OBS", Enabled: routeAllowed(cfg, sourceID, "obs", true), Default: true})
		}
	}
	return out
}
func (c *Coordinator) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		if !c.authorized(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var h heartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		p, err := c.register(h, ipFromRemote(r.RemoteAddr))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		jsonWrite(w, p)
	})
	mux.HandleFunc("/v1/plan", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method", 405)
			return
		}
		if !c.authorized(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", 400)
			return
		}
		// The address on which the coordinator received this request is the correct
		// destination for this client's media, even when OBS has multiple NICs.
		localIP := ""
		if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			localIP = ipFromRemote(a.String())
		}
		if localIP == "" || localIP == "0.0.0.0" {
			http.Error(w, "unable to determine OBS LAN IP", 500)
			return
		}
		plan, err := c.computePlan(id, localIP)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		jsonWrite(w, plan)
	})
	return mux
}
func (c *Coordinator) run(ctx context.Context) error {
	srv := &http.Server{Addr: fmt.Sprintf(":%d", coordinatorPort), Handler: c.handler(), ReadHeaderTimeout: 4 * time.Second}
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	c.logs.Add(fmt.Sprintf("Координатор работает на TCP %d", coordinatorPort))
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err = srv.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
func constantTimeEqual(a, b string) bool {
	// Equalize length before comparing to avoid leaking matching prefixes.
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
func jsonWrite(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// Local IP determined by a UDP route lookup, with no packet sent.
func lanIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:53")
	if err == nil {
		defer conn.Close()
		if host := ipFromRemote(conn.LocalAddr().String()); host != "" {
			return host
		}
	}
	ifaces, _ := net.InterfaceAddrs()
	for _, a := range ifaces {
		if ipnet, ok := a.(*net.IPNet); ok {
			ip := ipnet.IP.To4()
			if ip != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}
func stripIPv6Brackets(ip string) string { return strings.Trim(ip, "[]") }
