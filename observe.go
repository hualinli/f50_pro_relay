package relay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Observation is a read-only snapshot. Reading it never scans, changes routes,
// starts a timer or writes history. RX/TX are from the uplink interface's view.
type PHYRate struct {
	Width *int   `json:"width,omitempty"`
	MCS   *int   `json:"mcs,omitempty"`
	NSS   *int   `json:"nss,omitempty"`
	GI    *int   `json:"gi,omitempty"`
	DCM   *int   `json:"dcm,omitempty"`
	PHY   string `json:"phy,omitempty"`
}

func parsePHY(value string) PHYRate {
	var p PHYRate
	fields := strings.Fields(value)
	for i, f := range fields {
		if strings.HasSuffix(f, "MHz") {
			if n, e := strconv.Atoi(strings.TrimSuffix(f, "MHz")); e == nil {
				p.Width = &n
			}
		}
		if i+1 >= len(fields) {
			continue
		}
		n, e := strconv.Atoi(fields[i+1])
		if e != nil {
			continue
		}
		switch f {
		case "HE-MCS", "EHT-MCS", "VHT-MCS", "MCS":
			p.MCS = &n
			p.PHY = strings.TrimSuffix(f, "-MCS")
			if f == "MCS" {
				p.PHY = "HT"
			}
		case "HE-NSS", "EHT-NSS", "VHT-NSS":
			p.NSS = &n
		case "HE-GI", "EHT-GI":
			p.GI = &n
		case "HE-DCM":
			p.DCM = &n
		}
	}
	return p
}

type WirelessLink struct {
	BSSID     string   `json:"bssid,omitempty"`
	RX        PHYRate  `json:"rx"`
	TX        PHYRate  `json:"tx"`
	Connected bool     `json:"connected"`
	Frequency *int     `json:"frequency,omitempty"`
	Channel   *int     `json:"channel,omitempty"`
	Signal    *float64 `json:"signal,omitempty"`
	Width     *int     `json:"width,omitempty"`
	Standard  string   `json:"standard,omitempty"`
	RXMbps    *float64 `json:"rx_mbps,omitempty"`
	TXMbps    *float64 `json:"tx_mbps,omitempty"`
}
type LinkCounters struct {
	InterfaceID string `json:"interface_id"`
	RX          uint64 `json:"rx"`
	TX          uint64 `json:"tx"`
}
type Observation struct {
	At       time.Time     `json:"at"`
	Link     WirelessLink  `json:"link"`
	Counters *LinkCounters `json:"counters,omitempty"`
}

func parseWirelessLink(output string) WirelessLink {
	var link WirelessLink
	link.Connected = strings.HasPrefix(strings.TrimSpace(output), "Connected to ")
	if !link.Connected {
		return link
	}
	if f := strings.Fields(output); len(f) >= 3 {
		link.BSSID = f[2]
	}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		fields := strings.Fields(value)
		if !ok || len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		switch key {
		case "freq":
			freq := int(n)
			link.Frequency = &freq
			channel := 0
			if freq == 2484 {
				channel = 14
			} else if freq >= 2412 && freq <= 2472 {
				channel = (freq - 2407) / 5
			} else if freq >= 5000 && freq < 5900 {
				channel = (freq - 5000) / 5
			} else if freq >= 5955 && freq <= 7115 {
				channel = (freq - 5950) / 5
			}
			if channel > 0 {
				link.Channel = &channel
			}
		case "signal":
			link.Signal = &n
		case "rx bitrate", "tx bitrate":
			if key == "rx bitrate" {
				link.RXMbps = &n
				link.RX = parsePHY(value)
			} else {
				link.TXMbps = &n
				link.TX = parsePHY(value)
			}
			// Prefer the newest PHY reported by either direction.
			if strings.Contains(value, " EHT-") {
				link.Standard = "WiFi 7"
			} else if strings.Contains(value, " HE-") && link.Standard != "WiFi 7" {
				link.Standard = "WiFi 6"
			} else if strings.Contains(value, " VHT-") && link.Standard == "" {
				link.Standard = "WiFi 5"
			} else if strings.Contains(value, " MCS ") && link.Standard == "" {
				link.Standard = "WiFi 4"
			}
			for _, f := range fields {
				if strings.HasSuffix(f, "MHz") {
					if width, e := strconv.Atoi(strings.TrimSuffix(f, "MHz")); e == nil && (link.Width == nil || width > *link.Width) {
						link.Width = &width
					}
				}
			}
		}
	}
	return link
}

var (
	ErrLinkUnavailable     = errors.New("relay link observation unavailable")
	ErrCountersUnavailable = errors.New("relay traffic counters unavailable")
)

// Observe reads this running manager's uplink on demand, outside its mutation queue.
// A partial observation may accompany an error; callers should preserve known data.
func (m *Manager) Observe(ctx context.Context) (Observation, error) {
	if m.state.Load() == 0 || m.state.Load() == 3 {
		return Observation{}, ErrNotRunning
	}
	if e := m.Ready(ctx); e != nil {
		return Observation{}, e
	}
	return observe(ctx, m.options)
}

// ReadObservation is for read-only diagnostics against an existing installation.
// It does not require or start a Manager and does not claim ownership of the link.
func ReadObservation(ctx context.Context, options Options) (Observation, error) {
	o, e := options.normalized()
	if e != nil {
		return Observation{}, e
	}
	return observe(ctx, o)
}

func observe(ctx context.Context, options Options) (Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sample := Observation{}
	output, err := exec.CommandContext(ctx, options.Tools.IW, "dev", iface, "link").Output()
	sample.At = time.Now().UTC()
	if err != nil {
		return sample, errors.Join(ErrLinkUnavailable, err, ctx.Err())
	}
	sample.Link = parseWirelessLink(string(output))
	if !sample.Link.Connected {
		return sample, nil
	}
	base := "/sys/class/net/" + iface
	id, e1 := os.ReadFile(filepath.Join(base, "ifindex"))
	rx, e2 := os.ReadFile(filepath.Join(base, "statistics/rx_bytes"))
	tx, e3 := os.ReadFile(filepath.Join(base, "statistics/tx_bytes"))
	r, e4 := strconv.ParseUint(strings.TrimSpace(string(rx)), 10, 64)
	t, e5 := strconv.ParseUint(strings.TrimSpace(string(tx)), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return sample, ErrCountersUnavailable
	}
	sample.Counters = &LinkCounters{InterfaceID: strings.TrimSpace(string(id)), RX: r, TX: t}
	return sample, nil
}
