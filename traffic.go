package relay

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TrafficSession records one baseline when an association is observed. Current
// counters are read by the consumer on demand; no periodic traffic sampler runs.
// Since is detection time, not a claim about the exact 802.11 association time.
type TrafficSession struct {
	BSSID       string    `json:"bssid"`
	Since       time.Time `json:"since"`
	InterfaceID string    `json:"interface_id"`
	RXBase      uint64    `json:"rx_base"`
	TXBase      uint64    `json:"tx_base"`
}

func trafficBaseline(base string, now time.Time) *TrafficSession {
	id, e1 := os.ReadFile(filepath.Join(base, "ifindex"))
	rx, e2 := os.ReadFile(filepath.Join(base, "statistics/rx_bytes"))
	tx, e3 := os.ReadFile(filepath.Join(base, "statistics/tx_bytes"))
	r, e4 := strconv.ParseUint(strings.TrimSpace(string(rx)), 10, 64)
	t, e5 := strconv.ParseUint(strings.TrimSpace(string(tx)), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return nil
	}
	return &TrafficSession{Since: now, InterfaceID: strings.TrimSpace(string(id)), RXBase: r, TXBase: t}
}

func (d *engine) trackAssociation(completed bool, key string) {
	if !completed {
		d.trafficKey = ""
		d.traffic = nil
		return
	}
	if d.trafficKey == key {
		return
	}
	d.trafficKey = key
	d.traffic = trafficBaseline("/sys/class/net/"+iface, time.Now())
	if d.traffic != nil {
		d.traffic.BSSID = d.link["bssid"]
	}
}
