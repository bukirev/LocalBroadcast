//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestUpdateSafetyBlocksOBSAndRequiresMediaConsent(t *testing.T) {
	store, err := newConfigStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	media := newMediaManager(&LogBuffer{})
	media.pipelines["screen"] = &Pipeline{running: true}
	a := &Application{store: store, media: media}
	if err = a.updateSafety(false); err == nil {
		t.Fatal("active media without consent accepted")
	}
	if err = a.updateSafety(true); err != nil {
		t.Fatal(err)
	}
	for _, active := range []string{"GetStreamStatus", "GetRecordStatus"} {
		t.Run(active, func(t *testing.T) {
			ln, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				br := bufio.NewReader(conn)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				key := req.Header.Get("Sec-WebSocket-Key")
				// Reuse the exact handshake algorithm, no authentication in this fake OBS.
				h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(h[:]))
				ws := &rawSocket{conn: conn, reader: br}
				writeMockWS(conn, map[string]any{"op": 0, "d": map[string]any{"rpcVersion": 1}})
				if _, err = ws.recvJSON(); err != nil {
					return
				}
				writeMockWS(conn, map[string]any{"op": 2, "d": map[string]any{}})
				for {
					msg, err := ws.recvJSON()
					if err != nil {
						return
					}
					var d struct {
						Type string `json:"requestType"`
						ID   string `json:"requestId"`
					}
					json.Unmarshal(msg["d"], &d)
					writeMockWS(conn, map[string]any{"op": 7, "d": map[string]any{"requestId": d.ID, "requestStatus": map[string]any{"result": true, "code": 100}, "responseData": map[string]any{"outputActive": d.Type == active}}})
				}
			}()
			cfg := store.Get()
			cfg.ObsURL = "ws://" + ln.Addr().String()
			cfg.ObsPassword = ""
			if err = store.Update(cfg); err != nil {
				t.Fatal(err)
			}
			a.coordinator = &Coordinator{}
			if err = a.updateSafety(true); err == nil {
				t.Fatal("OBS output active but update accepted")
			}
		})
	}
}

func TestUpdateParentIdentity(t *testing.T) {
	created, err := updateProcessCreated(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if p, err := openExactUpdateParent(os.Getpid(), created+1); err == nil {
		p.close()
		t.Fatal("reused PID accepted")
	}
	p, err := openExactUpdateParent(os.Getpid(), created)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if p.wait(time.Millisecond) == nil {
		t.Fatal("live parent reported exited")
	}
}

type updateSmokeState struct {
	Version  string `json:"version"`
	Ready    bool   `json:"readyForWork"`
	Instance string `json:"instanceId"`
	Config   struct {
		NodeID string `json:"nodeId"`
	} `json:"config"`
	Updates struct {
		Executable string `json:"executable"`
	} `json:"updates"`
}

func updateSmokeRead(base string) (updateSmokeState, error) {
	var s updateSmokeState
	c := &http.Client{Timeout: time.Second}
	r, err := c.Get(base + "/api/state")
	if err != nil {
		return s, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return s, fmt.Errorf("status %d", r.StatusCode)
	}
	err = json.NewDecoder(r.Body).Decode(&s)
	return s, err
}
func updateSmokeStop(base string) error {
	r, err := http.NewRequest("POST", base+"/api/action", bytes.NewBufferString(`{"action":"shutdown"}`))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-LB-UI", "1")
	c := &http.Client{Timeout: 2 * time.Second}
	response, err := c.Do(r)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

// Real old helper and real future candidate, isolated from the user's live app.
// No network-role threads run; window, localhost UI, config and FFmpeg checks do.
func TestUpdateRealProcesses(t *testing.T) {
	if os.Getenv("LB_TEST_UPDATES") != "1" {
		t.Skip("use LB_TEST_UPDATES=1, Go + FFmpeg/FFplay on PATH")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffplay, err := exec.LookPath("ffplay")
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	oldBuild := filepath.Join(build, "old.exe")
	newBuild := filepath.Join(build, "new.exe")
	legacy := os.Getenv("LB_UPDATE_LEGACY_EXE")
	for path, v := range map[string]string{oldBuild: "0.3.6-beta", newBuild: "0.3.7-beta"} {
		if path == oldBuild && legacy != "" {
			if err := copyUpdateFile(legacy, oldBuild); err != nil {
				t.Fatal(err)
			}
			continue
		}
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -H windowsgui -X main.appVersion="+v, "-o", path, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	for _, scenario := range []string{"success", "candidate-fails"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "Программа с пробелами")
			os.Mkdir(root, 0700)
			if err := copyUpdateFile(oldBuild, updateTarget(root)); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(root, "user-file.txt"), []byte("preserve"), 0600)
			config := filepath.Join(t.TempDir(), "настройки.json")
			store, err := newConfigStore(config)
			if err != nil {
				t.Fatal(err)
			}
			cfg := store.Get()
			cfg.FFmpegPath = ffmpeg
			cfg.FFplayPath = ffplay
			cfg.AutoLaunchOBS = false
			cfg.AutoStart = false
			if err = store.Update(cfg); err != nil {
				t.Fatal(err)
			}
			configHash, _, _ := fileDigest(config)
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			base := fmt.Sprintf("http://127.0.0.1:%d", port)
			env := append(os.Environ(), "LB_UPDATE_TEST_NO_UI=1", "LB_UPDATE_TEST_OFFLINE=1")
			parent := exec.Command(updateTarget(root), "-config", config, "-ui-port", strconv.Itoa(port), "-no-browser")
			parent.Dir = root
			parent.Env = env
			prepareHeadlessCommand(parent)
			if err = parent.Start(); err != nil {
				t.Fatal(err)
			}
			parentDone := make(chan error, 1)
			go func() { parentDone <- parent.Wait() }()
			t.Cleanup(func() { _ = updateSmokeStop(base); parent.Process.Kill() })
			deadline := time.Now().Add(10 * time.Second)
			var initial updateSmokeState
			for time.Now().Before(deadline) {
				initial, err = updateSmokeRead(base)
				if err == nil && initial.Version == "0.3.6-beta" && initial.Ready {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if err != nil || initial.Config.NodeID != cfg.NodeID {
				t.Fatalf("initial app: %v %+v", err, initial)
			}
			dir := filepath.Join(t.TempDir(), randomHex(16))
			os.Mkdir(dir, 0700)
			notes := filepath.Join(build, "notes.md")
			os.WriteFile(notes, []byte("synthetic transition"), 0600)
			candidate := newBuild
			if scenario == "candidate-fails" {
				candidate = oldBuild
			}
			pkg := filepath.Join(dir, "package.zip")
			if err = buildUpdatePackage(candidate, notes, "0.3.7-beta", pkg); err != nil {
				t.Fatal(err)
			}
			digest, size, _ := fileDigest(pkg)
			oldHash, _, _ := fileDigest(oldBuild)
			created, err := updateProcessCreated(parent.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			p := updatePlan{Protocol: 1, ID: filepath.Base(dir), Root: root, Config: config, UIPort: port, ParentPID: parent.Process.Pid, ParentCreated: created, OldHash: oldHash, Release: updateRelease{Version: "0.3.7-beta", Size: size, SHA256: digest}}
			plan := filepath.Join(dir, "plan.json")
			if err = atomicUpdateJSON(plan, p); err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(dir, "LocalBroadcast.Update.exe")
			if err = copyUpdateFile(oldBuild, helper); err != nil {
				t.Fatal(err)
			}
			h := exec.Command(helper, "-update-helper", plan)
			h.Dir = dir
			h.Env = env
			prepareHeadlessCommand(h)
			if err = h.Start(); err != nil {
				t.Fatal(err)
			}
			helperDone := make(chan error, 1)
			go func() { helperDone <- h.Wait() }()
			t.Cleanup(func() { h.Process.Kill() })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err = updateWaitReceipt(ctx, filepath.Join(dir, "helper-ready.json"), p.ID, "", h.Process.Pid); err != nil {
				t.Fatal(err)
			}
			if err = atomicUpdateJSON(filepath.Join(dir, "go.json"), updateReceipt{ID: p.ID}); err != nil {
				t.Fatal(err)
			}
			if err = updateSmokeStop(base); err != nil {
				t.Fatal(err)
			}
			select {
			case <-parentDone:
			case <-time.After(10 * time.Second):
				t.Fatal("parent did not stop")
			}
			select {
			case err = <-helperDone:
				if scenario == "success" && err != nil {
					t.Fatal(err)
				}
				if scenario == "candidate-fails" && err == nil {
					t.Fatal("failed candidate declared successful")
				}
			case <-time.After(40 * time.Second):
				t.Fatal("helper did not finish")
			}
			want := "0.3.7-beta"
			phase := "committed"
			if scenario == "candidate-fails" {
				want = "0.3.6-beta"
				phase = "rolled-back"
			}
			deadline = time.Now().Add(10 * time.Second)
			var final updateSmokeState
			for time.Now().Before(deadline) {
				final, err = updateSmokeRead(base)
				if err == nil && final.Ready && final.Version == want {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if err != nil || !final.Ready || final.Version != want || final.Instance == initial.Instance || final.Config.NodeID != cfg.NodeID || final.Updates.Executable != updateTarget(root) {
				t.Fatalf("wrong restart: %v %+v", err, final)
			}
			j, err := loadUpdateJournal(root)
			if err != nil || j.Phase != phase {
				t.Fatalf("journal %+v %v", j, err)
			}
			if err = checkUpdateHash(config, configHash); err != nil {
				t.Fatal("config changed", err)
			}
			b, _ := os.ReadFile(filepath.Join(root, "user-file.txt"))
			if string(b) != "preserve" {
				t.Fatal("user file changed")
			}
			if err = updateSmokeStop(base); err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond)
			t.Logf("real helper %s; %s; original path, config, node ID and user file preserved", scenario, phase)
		})
	}
}
