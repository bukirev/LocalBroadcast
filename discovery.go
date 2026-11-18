package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

type Discover struct {
	mu   sync.RWMutex
	addr string
	seen time.Time
}

func (d *Discover) Set(addr string) { d.mu.Lock(); d.addr = addr; d.seen = time.Now(); d.mu.Unlock() }
func (d *Discover) Get() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.addr
}
func advertiseMaster(ctx context.Context, logs *LogBuffer) {
	msg, _ := json.Marshal(map[string]any{"app": "LocalBroadcast", "v": 1, "tcp": coordinatorPort})
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	send := func() {
		dest := &net.UDPAddr{IP: net.IPv4bcast, Port: discoveryPort}
		c, err := net.DialUDP("udp", nil, dest)
		if err != nil {
			return
		}
		_ = c.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = c.Write(msg)
		_ = c.Close()
		// Local subnet directed broadcast is more reliable on some routers.
		interfaces, _ := net.Interfaces()
		for _, iface := range interfaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				n, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				ipv4 := n.IP.To4()
				if ipv4 == nil || len(n.Mask) != 4 {
					continue
				}
				b := make(net.IP, 4)
				for i := 0; i < 4; i++ {
					b[i] = ipv4[i] | ^n.Mask[i]
				}
				if b.Equal(net.IPv4bcast) {
					continue
				}
				cc, er := net.DialUDP("udp", nil, &net.UDPAddr{IP: b, Port: discoveryPort})
				if er == nil {
					_, _ = cc.Write(msg)
					_ = cc.Close()
				}
			}
		}
	}
	send()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			send()
		}
	}
}
func listenDiscovery(ctx context.Context, d *Discover, logs *LogBuffer) {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: discoveryPort}
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		logs.Add("Обнаружение недоступно: " + err.Error())
		return
	}
	defer c.Close()
	b := make([]byte, 1024)
	for {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		n, remote, err := c.ReadFromUDP(b)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			continue
		}
		var info struct {
			App string `json:"app"`
			V   int    `json:"v"`
			TCP int    `json:"tcp"`
		}
		if json.Unmarshal(b[:n], &info) == nil && strings.EqualFold(info.App, "LocalBroadcast") && info.V == 1 && info.TCP == coordinatorPort {
			d.Set(fmt.Sprintf("http://%s:%d", remote.IP.String(), coordinatorPort))
		}
	}
}
