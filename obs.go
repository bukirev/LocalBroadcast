package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// Minimal dependency-free RFC6455 client for OBS WebSocket 5.x.
// It is intentionally localhost-only: credentials are not sent over the LAN.
type rawSocket struct {
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
}

func dialWebsocket(ctx context.Context, raw string) (*rawSocket, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" || u.Host == "" {
		return nil, errors.New("OBS URL должен иметь вид ws://127.0.0.1:4455")
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return nil, errors.New("OBS WebSocket разрешён только через localhost")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	keyBytes := make([]byte, 16)
	if _, err = rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	if _, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, u.Host, key); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	res, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if res.StatusCode != 101 {
		_ = res.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("OBS WebSocket: HTTP %d", res.StatusCode)
	}
	expectedDigest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	expected := base64.StdEncoding.EncodeToString(expectedDigest[:])
	if res.Header.Get("Sec-WebSocket-Accept") != expected {
		_ = conn.Close()
		return nil, errors.New("OBS WebSocket handshake mismatch")
	}
	return &rawSocket{conn: conn, reader: reader}, nil
}
func (s *rawSocket) Close() { _ = s.conn.Close() }
func (s *rawSocket) sendFrame(op byte, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	header := []byte{0x80 | op, 0x80}
	n := len(payload)
	if n < 126 {
		header[1] |= byte(n)
	} else if n <= 65535 {
		header[1] |= 126
		header = binary.BigEndian.AppendUint16(header, uint16(n))
	} else {
		header[1] |= 127
		header = binary.BigEndian.AppendUint64(header, uint64(n))
	}
	header = append(header, mask...)
	data := append([]byte(nil), payload...)
	for i := range data {
		data[i] ^= mask[i%4]
	}
	_, err := s.conn.Write(append(header, data...))
	return err
}
func (s *rawSocket) recvFrame() (byte, []byte, error) {
	h := make([]byte, 2)
	if _, err := io.ReadFull(s.reader, h); err != nil {
		return 0, nil, err
	}
	if h[0]&0x80 == 0 {
		return 0, nil, errors.New("fragmented WebSocket frame unsupported")
	}
	op := h[0] & 0x0F
	l := uint64(h[1] & 0x7f)
	switch l {
	case 126:
		var v uint16
		if err := binary.Read(s.reader, binary.BigEndian, &v); err != nil {
			return 0, nil, err
		}
		l = uint64(v)
	case 127:
		if err := binary.Read(s.reader, binary.BigEndian, &l); err != nil {
			return 0, nil, err
		}
	}
	if l > 8*1024*1024 {
		return 0, nil, errors.New("oversized OBS response")
	}
	var mask [4]byte
	masked := h[1]&0x80 != 0
	if masked {
		if _, err := io.ReadFull(s.reader, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	p := make([]byte, int(l))
	if _, err := io.ReadFull(s.reader, p); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range p {
			p[i] ^= mask[i%4]
		}
	}
	return op, p, nil
}
func (s *rawSocket) sendJSON(payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.sendFrame(1, b)
}
func (s *rawSocket) recvJSON() (map[string]json.RawMessage, error) {
	for {
		op, p, err := s.recvFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case 1, 2:
			var m map[string]json.RawMessage
			if err := json.Unmarshal(p, &m); err != nil {
				return nil, err
			}
			return m, nil
		case 8:
			return nil, errors.New("OBS WebSocket закрыл соединение")
		case 9:
			if err := s.sendFrame(10, p); err != nil {
				return nil, err
			}
		}
	}
}
func obsAuth(password, salt, challenge string) string {
	secret := sha256.Sum256([]byte(password + salt))
	first := base64.StdEncoding.EncodeToString(secret[:])
	auth := sha256.Sum256([]byte(first + challenge))
	return base64.StdEncoding.EncodeToString(auth[:])
}

type obsClient struct {
	ws  *rawSocket
	seq int
}

func connectOBS(ctx context.Context, c Configuration) (*obsClient, error) {
	conn, err := dialWebsocket(ctx, c.ObsURL)
	if err != nil {
		return nil, err
	}
	_ = conn.conn.SetDeadline(time.Now().Add(5 * time.Second))
	hello, err := conn.recvJSON()
	if err != nil {
		conn.Close()
		return nil, err
	}
	var op int
	_ = json.Unmarshal(hello["op"], &op)
	if op != 0 {
		conn.Close()
		return nil, errors.New("ожидался OBS Hello")
	}
	var data struct {
		RPCVersion     int `json:"rpcVersion"`
		Authentication *struct {
			Challenge string `json:"challenge"`
			Salt      string `json:"salt"`
		} `json:"authentication"`
	}
	if err := json.Unmarshal(hello["d"], &data); err != nil {
		conn.Close()
		return nil, err
	}
	ident := map[string]any{"rpcVersion": 1, "eventSubscriptions": 0}
	if data.Authentication != nil {
		if c.ObsPassword == "" {
			conn.Close()
			return nil, errors.New("для OBS необходим пароль WebSocket в настройках")
		}
		ident["authentication"] = obsAuth(c.ObsPassword, data.Authentication.Salt, data.Authentication.Challenge)
	}
	if err := conn.sendJSON(map[string]any{"op": 1, "d": ident}); err != nil {
		conn.Close()
		return nil, err
	}
	received, err := conn.recvJSON()
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = json.Unmarshal(received["op"], &op)
	if op != 2 {
		conn.Close()
		return nil, errors.New("OBS отклонил авторизацию")
	}
	return &obsClient{ws: conn}, nil
}
func (c *obsClient) request(kind string, data any) (json.RawMessage, error) {
	c.seq++
	id := fmt.Sprintf("lb-%d", c.seq)
	d := map[string]any{"requestType": kind, "requestId": id}
	if data != nil {
		d["requestData"] = data
	}
	if err := c.ws.sendJSON(map[string]any{"op": 6, "d": d}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.ws.recvJSON()
		if err != nil {
			return nil, err
		}
		var op int
		_ = json.Unmarshal(msg["op"], &op)
		if op != 7 {
			continue
		}
		var response struct {
			RequestID     string `json:"requestId"`
			RequestStatus struct {
				Result  bool   `json:"result"`
				Code    int    `json:"code"`
				Comment string `json:"comment"`
			} `json:"requestStatus"`
			ResponseData json.RawMessage `json:"responseData"`
		}
		if err := json.Unmarshal(msg["d"], &response); err != nil {
			return nil, err
		}
		if response.RequestID != id {
			continue
		}
		if !response.RequestStatus.Result {
			return nil, fmt.Errorf("OBS %s: %s (%d)", kind, response.RequestStatus.Comment, response.RequestStatus.Code)
		}
		return response.ResponseData, nil
	}
}

type obsSource struct {
	name string
	port int
}

func desiredOBSSources(peers []Peer) []obsSource {
	out := []obsSource{}
	presenterAssigned := false
	for _, p := range peers {
		if !p.Online() {
			continue
		}
		if p.SendScreen && !presenterAssigned {
			out = append(out, obsSource{name: "LB | Presenter", port: presenterPort})
			presenterAssigned = true
		}
		if p.SendCamera {
			short := p.ID
			if len(short) > 6 {
				short = short[:6]
			}
			out = append(out, obsSource{name: "LB | " + p.Name + " (" + short + ")", port: p.CameraPort})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].port < out[j].port })
	return out
}
func syncOBS(ctx context.Context, c Configuration, peers []Peer) error {
	client, err := connectOBS(ctx, c)
	if err != nil {
		return err
	}
	defer client.ws.Close()
	scenesRaw, err := client.request("GetSceneList", nil)
	if err != nil {
		return err
	}
	var scenes struct {
		Scenes []struct {
			SceneName string `json:"sceneName"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(scenesRaw, &scenes); err != nil {
		return err
	}
	found := false
	for _, scene := range scenes.Scenes {
		if scene.SceneName == c.ObsScene {
			found = true
			break
		}
	}
	if !found {
		if _, err = client.request("CreateScene", map[string]any{"sceneName": c.ObsScene}); err != nil {
			return err
		}
	}
	inputsRaw, err := client.request("GetInputList", nil)
	if err != nil {
		return err
	}
	var inputs struct {
		Inputs []struct {
			InputName string `json:"inputName"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(inputsRaw, &inputs); err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, i := range inputs.Inputs {
		existing[i.InputName] = true
	}
	itemsRaw, err := client.request("GetSceneItemList", map[string]any{"sceneName": c.ObsScene})
	if err != nil {
		return err
	}
	var items struct {
		SceneItems []struct {
			SourceName string `json:"sourceName"`
		} `json:"sceneItems"`
	}
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return err
	}
	inScene := map[string]bool{}
	for _, i := range items.SceneItems {
		inScene[i.SourceName] = true
	}
	for _, source := range desiredOBSSources(peers) {
		// OBS's Media Source uses 'ffmpeg_source' and treats input as an URL.
		settings := map[string]any{"is_local_file": false, "input": fmt.Sprintf("udp://@:%d?fifo_size=100000&overrun_nonfatal=1", source.port),
			"input_format": "mpegts", "buffering_mb": 0, "reconnect_delay_sec": 1}
		if !existing[source.name] {
			_, err = client.request("CreateInput", map[string]any{"sceneName": c.ObsScene, "inputName": source.name,
				"inputKind": "ffmpeg_source", "inputSettings": settings, "sceneItemEnabled": true})
			if err != nil {
				return err
			}
		} else {
			info, er := client.request("GetInputSettings", map[string]any{"inputName": source.name})
			if er != nil {
				return er
			}
			var current struct {
				InputSettings struct {
					Input       string `json:"input"`
					IsLocalFile bool   `json:"is_local_file"`
					InputFormat string `json:"input_format"`
					BufferingMB int    `json:"buffering_mb"`
				} `json:"inputSettings"`
			}
			_ = json.Unmarshal(info, &current)
			if current.InputSettings.Input != settings["input"] || current.InputSettings.IsLocalFile || current.InputSettings.InputFormat != "mpegts" || current.InputSettings.BufferingMB != 0 {
				if _, err = client.request("SetInputSettings", map[string]any{"inputName": source.name, "inputSettings": settings, "overlay": true}); err != nil {
					return err
				}
			}
			if !inScene[source.name] {
				if _, err = client.request("CreateSceneItem", map[string]any{"sceneName": c.ObsScene, "sourceName": source.name, "sceneItemEnabled": true}); err != nil {
					return err
				}
			}
		}
	}
	// OBS filters adjust the video timeline; the input audio offset is set
	// separately to delay audio by the same amount, preserving lip sync.
	// The network-based component is approximate. Capture and USB camera
	// latency should be calibrated using the per-source offsets.
	adjustments := plannedSyncOffsets(c, peers)
	for _, source := range desiredOBSSources(peers) {
		var adjustment *SyncAdjustment
		for _, a := range adjustments {
			if a.Name == source.name {
				v := a
				adjustment = &v
				break
			}
		}
		if adjustment == nil {
			continue
		}
		if err := applyOBSSync(client, *adjustment); err != nil {
			return err
		}
	}
	return nil
}
func applyOBSSync(client *obsClient, a SyncAdjustment) error {
	const filterName = "LB | Sync"
	raw, err := client.request("GetSourceFilterList", map[string]any{"sourceName": a.Name})
	if err != nil {
		return err
	}
	var data struct {
		Filters []struct {
			FilterName     string         `json:"filterName"`
			FilterSettings map[string]any `json:"filterSettings"`
		} `json:"filters"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		return err
	}
	exists := false
	previous := -1
	for _, f := range data.Filters {
		if f.FilterName == filterName {
			exists = true
			if v, ok := f.FilterSettings["delay_ms"].(float64); ok {
				previous = int(v)
			}
			break
		}
	}
	if !exists && a.RenderDelayMS > 0 {
		_, err = client.request("CreateSourceFilter", map[string]any{"sourceName": a.Name, "filterName": filterName, "filterKind": "async_delay_filter", "filterSettings": map[string]any{"delay_ms": a.RenderDelayMS}})
	} else if exists && previous != a.RenderDelayMS {
		_, err = client.request("SetSourceFilterSettings", map[string]any{"sourceName": a.Name, "filterName": filterName, "filterSettings": map[string]any{"delay_ms": a.RenderDelayMS}, "overlay": true})
	}
	if err != nil {
		return err
	}
	// OBS accepts signed audio offsets; skip no-op calls whenever possible.
	ar, err := client.request("GetInputAudioSyncOffset", map[string]any{"inputName": a.Name})
	if err != nil {
		return err
	}
	var current struct {
		InputAudioSyncOffset int `json:"inputAudioSyncOffset"`
	}
	if err = json.Unmarshal(ar, &current); err != nil {
		return err
	}
	if current.InputAudioSyncOffset != a.AudioOffsetMS {
		_, err = client.request("SetInputAudioSyncOffset", map[string]any{"inputName": a.Name, "inputAudioSyncOffset": a.AudioOffsetMS})
	}
	return err
}
func noOpJSONEqual(a, b json.RawMessage) bool { return bytes.Equal(a, b) }
