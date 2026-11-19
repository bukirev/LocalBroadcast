package main

// OBS cannot infer camera exposure / HDMI-capture latency from packet arrival
// times. We automatically align *estimated* network contributions and keep
// per-source calibration for capture/encoder delays. For frame-accurate sync,
// perform a flash/clap calibration on actual hardware and save the offsets.

type SyncAdjustment struct {
	SourceID          string `json:"sourceId"`
	Name              string `json:"name"`
	RTTMS             int    `json:"rttMs"`
	NetworkEstimateMS int    `json:"networkEstimateMs"`
	RenderDelayMS     int    `json:"renderDelayMs"`
	AudioOffsetMS     int    `json:"audioOffsetMs"`
}

func minInt(x, y int) int {
	if x < y {
		return x
	}
	return y
}
func clamp(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
func estimatedNetworkDelay(p Peer) int {
	// RTT is measured by the client's HTTP heartbeat exchange. RTT/2 is not
	// exact one-way latency, so it is only a rough baseline.
	return clamp(p.RTTMS/2, 0, 400)
}
func plannedSyncOffsets(c Configuration, peers []Peer) map[string]SyncAdjustment {
	type source struct {
		id, name string
		estimate int
	}
	sources := []source{}
	foundPresenter := false
	maxDelay := 0
	for _, p := range peers {
		if !p.Online() {
			continue
		}
		est := estimatedNetworkDelay(p)
		if p.SendScreen && !foundPresenter {
			sources = append(sources, source{p.ID + ":screen", "LB | Presenter", est})
			foundPresenter = true
		}
		if p.SendCamera {
			short := p.ID
			if len(short) > 6 {
				short = short[:6]
			}
			sources = append(sources, source{p.ID + ":camera", "LB | " + p.Name + " (" + short + ")", est})
		}
	}
	for _, s := range sources {
		if s.estimate > maxDelay {
			maxDelay = s.estimate
		}
	}
	results := map[string]SyncAdjustment{}
	for _, s := range sources {
		video := 0
		if c.SyncEnabled {
			video = maxDelay - s.estimate
		}
		tune := c.SyncOffsets[s.id]
		video = clamp(video+tune.VideoMS, 0, 10000)
		audio := clamp(video+tune.AudioMS, -950, 20000)
		results[s.id] = SyncAdjustment{SourceID: s.id, Name: s.name, RTTMS: s.estimate * 2, NetworkEstimateMS: s.estimate, RenderDelayMS: video, AudioOffsetMS: audio}
	}
	return results
}
