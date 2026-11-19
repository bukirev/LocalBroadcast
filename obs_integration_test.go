package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

func writeMockWS(conn net.Conn, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	header := []byte{0x81, 0}
	if len(data) < 126 {
		header[1] = byte(len(data))
	} else if len(data) <= 65535 {
		header[1] = 126
		header = binary.BigEndian.AppendUint16(header, uint16(len(data)))
	} else {
		return fmt.Errorf("large response")
	}
	_, err = conn.Write(append(header, data...))
	return err
}
func TestOBSWireProtocolCreateSources(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	errCh := make(chan error, 1)
	var mu sync.Mutex
	created := map[string]bool{}
	filters := map[string]bool{}
	audioOffsets := map[string]int{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			errCh <- err
			return
		}
		key := req.Header.Get("Sec-WebSocket-Key")
		hash := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		if _, err = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(hash[:])); err != nil {
			errCh <- err
			return
		}
		socket := &rawSocket{conn: conn, reader: br}
		if err = writeMockWS(conn, map[string]any{"op": 0, "d": map[string]any{"rpcVersion": 1, "authentication": map[string]any{"salt": "salt", "challenge": "challenge"}}}); err != nil {
			errCh <- err
			return
		}
		identify, err := socket.recvJSON()
		if err != nil {
			errCh <- err
			return
		}
		var msg struct {
			Authentication string `json:"authentication"`
		}
		if err = json.Unmarshal(identify["d"], &msg); err != nil {
			errCh <- err
			return
		}
		if msg.Authentication != obsAuth("password", "salt", "challenge") {
			errCh <- fmt.Errorf("wrong OBS authentication")
			return
		}
		if err = writeMockWS(conn, map[string]any{"op": 2, "d": map[string]any{"negotiatedRpcVersion": 1}}); err != nil {
			errCh <- err
			return
		}
		for {
			request, er := socket.recvJSON()
			if er != nil {
				errCh <- nil
				return
			}
			var r struct {
				RequestType string         `json:"requestType"`
				RequestID   string         `json:"requestId"`
				RequestData map[string]any `json:"requestData"`
			}
			if er = json.Unmarshal(request["d"], &r); er != nil {
				errCh <- er
				return
			}
			data := map[string]any{}
			switch r.RequestType {
			case "GetSceneList":
				data["scenes"] = []any{}
			case "CreateScene":
			case "GetInputList":
				data["inputs"] = []any{}
			case "GetSceneItemList":
				data["sceneItems"] = []any{}
			case "GetSourceFilterList":
				data["filters"] = []any{}
			case "GetInputAudioSyncOffset":
				data["inputAudioSyncOffset"] = 0
			case "CreateSourceFilter", "SetSourceFilterSettings":
				mu.Lock()
				filters[r.RequestData["sourceName"].(string)] = true
				mu.Unlock()
			case "SetInputAudioSyncOffset":
				mu.Lock()
				audioOffsets[r.RequestData["inputName"].(string)] = int(r.RequestData["inputAudioSyncOffset"].(float64))
				mu.Unlock()
			case "CreateInput":
				name, _ := r.RequestData["inputName"].(string)
				settings, _ := r.RequestData["inputSettings"].(map[string]any)
				input, _ := settings["input"].(string)
				endpoint, parseErr := url.Parse(input)
				port := 5101
				if name == "LB | Presenter" {
					port = 5000
				}
				if parseErr != nil || endpoint.Scheme != "udp" || endpoint.Host != fmt.Sprintf("0.0.0.0:%d", port) {
					t.Errorf("OBS must listen on explicit IPv4 to receive LAN and loopback streams: %q", input)
				}
				mu.Lock()
				created[name] = true
				mu.Unlock()
			default:
				errCh <- fmt.Errorf("unexpected OBS request: %s", r.RequestType)
				return
			}
			resp := map[string]any{"op": 7, "d": map[string]any{"requestType": r.RequestType, "requestId": r.RequestID,
				"requestStatus": map[string]any{"result": true, "code": 100}, "responseData": data}}
			if er = writeMockWS(conn, resp); er != nil {
				errCh <- er
				return
			}
		}
	}()
	c := defaultConfig()
	c.ObsPassword = "password"
	c.ObsURL = "ws://" + ln.Addr().String()
	now := time.Now()
	peers := []Peer{{ID: "presenter123", Name: "Presenter", SendScreen: true, LastSeen: now, RTTMS: 80},
		{ID: "camera12345", Name: "Camera", SendCamera: true, CameraPort: 5101, LastSeen: now, RTTMS: 20}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncOBS(ctx, c, peers); err != nil {
		t.Fatal(err)
	}
	if er := <-errCh; er != nil {
		t.Fatal(er)
	}
	mu.Lock()
	defer mu.Unlock()
	if !created["LB | Presenter"] || !created["LB | Camera (camera)"] {
		t.Fatalf("OBS sources not created: %v", created)
	}
	if !filters["LB | Camera (camera)"] || audioOffsets["LB | Camera (camera)"] != 30 {
		t.Fatalf("automatic network sync not applied: filters=%v audio=%v", filters, audioOffsets)
	}
}
