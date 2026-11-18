package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func coordinatorFixture(t *testing.T) *Coordinator {
	t.Helper()
	config, err := newConfigStore(filepath.Join(t.TempDir(), "cfg.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := config.Get()
	c.Mode = "master"
	c.Name = "OBS"
	if err = config.Update(c); err != nil {
		t.Fatal(err)
	}
	return newCoordinator(config, &LogBuffer{})
}
func registerPeer(t *testing.T, c *Coordinator, id, name, ip string, screen, camera, watch bool) Peer {
	t.Helper()
	p, err := c.register(heartbeatRequest{NodeID: id, Name: name, Mode: "custom", SendScreen: screen, SendCamera: camera, WatchPresenter: watch, Version: "test"}, ip)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestPresenterAndCamerasRouteToOBSAndSubscribers(t *testing.T) {
	c := coordinatorFixture(t)
	presenter := registerPeer(t, c, "presenter01", "Presenter", "192.168.1.101", true, false, false)
	cam := registerPeer(t, c, "camera0001", "Camera 1", "192.168.1.102", false, true, true)
	registerPeer(t, c, "viewer0001", "Viewer", "192.168.1.103", false, false, true)
	presenterPlan, err := c.computePlan(presenter.ID, "192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}
	if !presenterPlan.Presenter.Enabled || len(presenterPlan.Presenter.Targets) != 3 {
		b, _ := json.Marshal(presenterPlan)
		t.Fatalf("unexpected presenter plan %s", b)
	}
	ports := map[string]int{}
	for _, dst := range presenterPlan.Presenter.Targets {
		ports[dst.IP] = dst.Port
	}
	for _, ip := range []string{"192.168.1.100", "192.168.1.102", "192.168.1.103"} {
		if ports[ip] != presenterPort {
			t.Errorf("missing presenter target %s", ip)
		}
	}
	camPlan, err := c.computePlan(cam.ID, "192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}
	if !camPlan.Camera.Enabled || len(camPlan.Camera.Targets) != 1 || camPlan.Camera.Targets[0].Port != cam.CameraPort || !camPlan.ReceivePresenter {
		t.Fatalf("unexpected camera plan %+v", camPlan)
	}
}
func TestRouteOverridesAndSinglePresenter(t *testing.T) {
	c := coordinatorFixture(t)
	registerPeer(t, c, "presenter01", "A Presenter", "192.168.1.101", true, false, false)
	registerPeer(t, c, "presenter02", "B Presenter", "192.168.1.104", true, false, false)
	viewer := registerPeer(t, c, "viewer0001", "Viewer", "192.168.1.103", false, false, true)
	blocked, err := c.computePlan("presenter02", "192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Presenter.Enabled {
		t.Fatal("second presenter must not send to UDP:5000")
	}
	if err := c.cfg.SetRoute("presenter01:screen", viewer.ID, false); err != nil {
		t.Fatal(err)
	}
	plan, err := c.computePlan("presenter01", "192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range plan.Presenter.Targets {
		if target.ID == viewer.ID {
			t.Fatal("disabled route remains active")
		}
	}
	viewPlan, err := c.computePlan(viewer.ID, "192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}
	if viewPlan.ReceivePresenter {
		t.Fatal("viewer still playing denied route")
	}
}
func TestCoordinatorAuth(t *testing.T) {
	c := coordinatorFixture(t)
	cfg := c.cfg.Get()
	r := fakeHTTPRequest("wrong")
	if c.authorized(r) {
		t.Fatal("wrong key accepted")
	}
	r = fakeHTTPRequest(cfg.Secret)
	if !c.authorized(r) {
		t.Fatal("correct key rejected")
	}
	if net.ParseIP(lanIP()) == nil {
		t.Fatal("invalid local IP")
	}
}
func fakeHTTPRequest(secret string) *http.Request {
	r := httptest.NewRequest("GET", "http://127.0.0.1/", nil)
	r.Header.Set("X-LB-Key", secret)
	return r
}

func TestHTTPRegistrationAndPlan(t *testing.T) {
	c := coordinatorFixture(t)
	server := httptest.NewServer(c.handler())
	defer server.Close()
	cfg := c.cfg.Get()
	payload := []byte(`{"nodeId":"presenter01","name":"Presenter","mode":"presenter","sendScreen":true,"sendCamera":false,"watchPresenter":false,"version":"test"}`)
	req, err := http.NewRequest("POST", server.URL+"/v1/heartbeat", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-LB-Key", cfg.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("register status %d", resp.StatusCode)
	}
	req2, err := http.NewRequest("GET", server.URL+"/v1/plan?id=presenter01", nil)
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("X-LB-Key", cfg.Secret)
	response, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("plan status %d", response.StatusCode)
	}
	var p Plan
	if err = json.NewDecoder(response.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if !p.Presenter.Enabled || len(p.Presenter.Targets) != 1 || p.Presenter.Targets[0].Port != 5000 {
		t.Fatalf("unexpected HTTP plan: %+v", p)
	}
}
