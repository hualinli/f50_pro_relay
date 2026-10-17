package relay

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Event struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}
type Status struct {
	Traffic  *TrafficSession `json:"traffic,omitempty"`
	State    string          `json:"state"`
	Mode     Mode            `json:"mode"`
	Exit     string          `json:"exit"`
	SSID     string          `json:"ssid,omitempty"`
	Signal   string          `json:"signal,omitempty"`
	Lease    *Lease          `json:"lease,omitempty"`
	Health   Health          `json:"health"`
	Profiles []PublicProfile `json:"profiles"`
	Events   []Event         `json:"events"`
	Since    time.Time       `json:"since"`
	Version  string          `json:"version"`
}
type request struct {
	action     string
	profile    *Profile
	id, mode   string
	lease      *Lease
	event      string
	generation int
}
type response struct {
	data any
	err  error
}
type operation struct {
	request request
	ctx     context.Context
	reply   chan response
}
type childExit struct {
	Name string
	PID  int
	Err  error
}
type childRecord struct {
	Name string `json:"name"`
	PID  int    `json:"pid"`
}
type engine struct {
	traffic                          *TrafficSession
	trafficKey                       string
	options                          Options
	ctx                              context.Context
	leaseStamp                       string
	leaseEvent                       string
	base, runtime                    string
	config                           Config
	network                          networkState
	exits                            chan childExit
	probes                           chan Health
	certs                            *x509.CertPool
	children                         map[string]*exec.Cmd
	generation, epoch                int
	ids                              []string
	link                             map[string]string
	lease                            Lease
	policy                           switchPolicy
	health                           Health
	inFlight                         bool
	lastProbe, lastAttempt, lastScan time.Time
	scanPending                      bool
	scanRequested                    time.Time
	scan                             []AccessPoint
	preferred                        string
	events                           []Event
	since                            time.Time
	holdUntil                        time.Time
	dhcpRetry                        time.Time
	controlFailures                  int
	signal                           string
}

func (d *engine) event(s string) {
	// Messages are generated internally; never include request bodies/config data.
	d.events = append(d.events, Event{time.Now(), s})
	if len(d.events) > 80 {
		d.events = d.events[len(d.events)-80:]
	}
	d.options.Logger.Info(s)
}
func (d *engine) status() Status {
	s := Status{Mode: Mode(d.config.Mode), State: "CELLULAR", Exit: "cellular", Health: d.health, Profiles: d.config.Public(), Events: append([]Event(nil), d.events...), Since: d.since, Version: Version}
	s.SSID = d.link["ssid"]
	s.Signal = d.signal
	if d.traffic != nil {
		t := *d.traffic
		s.Traffic = &t
	}
	if d.lease.Valid(time.Now()) {
		l := d.lease
		l.DNS = append([]string(nil), l.DNS...)
		s.Lease = &l
	}
	if d.network.Active {
		s.State = "WIFI_ACTIVE"
		s.Exit = "wifi"
	} else if d.config.Mode != "cellular" {
		switch {
		case time.Now().Before(d.holdUntil):
			s.State = "BACKOFF"
		case d.link["wpa_state"] != "COMPLETED":
			s.State = "WIFI_SEARCHING"
		case !d.lease.Valid(time.Now()):
			s.State = "WIFI_CONNECTING"
		case d.health.Portal:
			s.State = "WIFI_PORTAL"
		default:
			s.State = "WIFI_CHECKING"
		}
	}
	return s
}
func (d *engine) saveChildren() {
	cs := []childRecord{}
	for name, c := range d.children {
		cs = append(cs, childRecord{name, c.Process.Pid})
	}
	_ = atomicJSON(filepath.Join(d.base, "children.json"), cs, 0600)
}
func (d *engine) child(name, bin string, args []string, env []string) error {
	if d.children[name] != nil {
		return nil
	}
	c := exec.Command(bin, args...)
	c.Env = append(os.Environ(), env...)
	f, e := os.OpenFile(filepath.Join(d.base, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	c.Stdout = f
	c.Stderr = f
	if e = c.Start(); e != nil {
		return fmt.Errorf("could not start %s", name)
	}
	d.children[name] = c
	d.saveChildren()
	go func() {
		e := c.Wait()
		select {
		case d.exits <- childExit{name, c.Process.Pid, e}:
		case <-d.ctx.Done():
		}
	}()
	return nil
}
func (d *engine) stopChild(name string) {
	c := d.children[name]
	if c == nil {
		return
	}
	delete(d.children, name)
	d.saveChildren()
	_ = c.Process.Signal(syscall.SIGTERM)
	go func() {
		time.Sleep(time.Second)
		if childOwned(d.base, d.runtime, childRecord{Name: name, PID: c.Process.Pid}) {
			_ = c.Process.Kill()
		}
	}()
}
func (d *engine) fallback(reason string) {
	active := d.network.Active
	if e := d.network.Disable(); e != nil {
		d.event("fallback cleanup needs retry: " + e.Error())
	}
	d.policy.LinkLost(time.Now())
	if active {
		d.event("cellular: " + reason)
	}
}
func (d *engine) clearLease(reason string) {
	d.fallback(reason)
	d.lease = Lease{}
	d.leaseStamp = ""
	d.leaseEvent = ""
	_ = os.Remove(filepath.Join(d.base, "dhcp-event"))
	d.health = Health{}
	d.epoch++
	d.inFlight = false
	d.stopChild("dhcp")
}
func (d *engine) restartWPA() error {
	d.clearLease("network configuration changed")
	d.stopChild("wpa")
	time.Sleep(1200 * time.Millisecond)
	d.link = map[string]string{}
	d.trackAssociation(false, "")
	d.generation++
	d.epoch++
	d.lastAttempt = time.Time{}
	d.lastScan = time.Time{}
	if e := os.MkdirAll(d.runtime, 0700); e != nil {
		return e
	}
	if e := chownWifi(d.runtime); e != nil {
		return e
	}
	if e := os.Chmod(d.runtime, 0700); e != nil {
		return e
	}
	b, ids := renderWPA(d.config, d.runtime)
	d.ids = ids
	path := filepath.Join(d.runtime, "wpa.conf")
	if e := atomicFile(path, b, 0600); e != nil {
		return e
	}
	if e := chownWifi(path); e != nil {
		return e
	}
	_ = os.Remove(filepath.Join(d.runtime, "ctrl", iface))
	if e := d.child("wpa", d.options.Tools.WPASupplicant, []string{"-Dnl80211", "-i" + iface, "-c" + path}, nil); e != nil {
		return e
	}
	return nil
}
func (d *engine) dhcp() error {
	hook, err := d.writeDHCPHook()
	if err != nil {
		return err
	}
	return d.child("dhcp", d.options.Tools.BusyBox, []string{"udhcpc", "-i", iface, "-f", "-n", "-t", "3", "-T", "3", "-s", hook}, []string{"UFI_RELAY_GENERATION=" + strconv.Itoa(d.generation)})
}
func (d *engine) selectProfile(id string) error {
	for i, v := range d.ids {
		if v == id {
			if _, e := wpa(d.runtime, "SELECT_NETWORK "+strconv.Itoa(i)); e != nil {
				return e
			}
			d.lastAttempt = time.Now()
			return nil
		}
	}
	return errors.New("saved network not found")
}
func (d *engine) chooseVisible() {
	if d.config.Mode == "cellular" || time.Now().Before(d.holdUntil) {
		return
	}
	ps := append([]Profile(nil), d.config.Profiles...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Priority > ps[j].Priority })
	if d.preferred != "" {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].ID == d.preferred && ps[j].ID != d.preferred })
	}
	for _, p := range ps {
		for _, s := range d.scan {
			if p.SSID == s.SSID {
				_ = d.selectProfile(p.ID)
				return
			}
		}
	}
	for _, p := range ps {
		if p.Hidden {
			_ = d.selectProfile(p.ID)
			return
		}
	}
}
func (d *engine) scanNow() error {
	if d.scanPending {
		return nil
	}
	_, e := wpa(d.runtime, "SCAN")
	if e != nil {
		return e
	}
	d.scanPending = true
	d.scanRequested = time.Now()
	d.lastScan = time.Now()
	return nil
}
func (d *engine) tick() {
	if !d.refreshLAN() {
		return
	}
	now := time.Now()
	d.readDHCPEvent()
	if d.network.NeedsCleanup {
		d.fallback("retrying network recovery")
		if d.network.NeedsCleanup {
			return
		}
	}
	if d.children["wpa"] == nil {
		if e := d.restartWPA(); e != nil {
			d.event("supplicant restart failed")
		}
		return
	}
	out, e := wpa(d.runtime, "STATUS")
	if e != nil {
		d.clearLease("supplicant control unavailable")
		d.controlFailures++
		if d.controlFailures >= 3 {
			_ = d.restartWPA()
			d.controlFailures = 0
		}
		return
	}
	d.controlFailures = 0
	oldCompleted := d.link["wpa_state"] == "COMPLETED"
	d.link = fields(out)
	completed := d.link["wpa_state"] == "COMPLETED"
	d.trackAssociation(completed, d.link["bssid"]+"|"+d.link["ssid"])
	if completed {
		if result, e := wpa(d.runtime, "SIGNAL_POLL"); e == nil {
			d.signal = fields(result)["RSSI"]
		}
	} else {
		d.signal = ""
	}
	if !completed {
		if oldCompleted || d.lease.IP != "" || d.network.Active {
			d.clearLease("Wi-Fi link disconnected")
		}
		if d.config.Mode != "cellular" && !now.Before(d.holdUntil) && now.Sub(d.lastScan) >= 20*time.Second {
			if e := d.scanNow(); e != nil && len(d.ids) > 0 && now.Sub(d.lastAttempt) >= 20*time.Second {
				_ = d.selectProfile(d.ids[0])
			}
		}
	} else if d.config.Mode != "cellular" {
		if d.children["dhcp"] == nil && !now.Before(d.dhcpRetry) {
			if e := d.dhcp(); e != nil {
				d.event("DHCP client could not start")
			}
			d.dhcpRetry = now.Add(10 * time.Second)
		}
		if d.lease.IP != "" && !d.lease.Valid(now) {
			d.clearLease("DHCP lease expired")
		}
		if d.lease.Valid(now) {
			if d.config.Mode == "wifi" && !d.network.Active {
				if e := d.network.Enable(); e == nil {
					d.policy.Active = true
					d.event("Wi-Fi: manual preference")
				} else {
					d.fallback("activation failed")
				}
			}
			if !d.inFlight && now.Sub(d.lastProbe) >= 5*time.Second {
				d.inFlight = true
				d.lastProbe = now
				l := d.lease
				epoch := d.epoch
				go func() {
					h := probe(l, epoch, d.certs)
					select {
					case d.probes <- h:
					case <-d.ctx.Done():
					}
				}()
			}
		}
	}
	if d.scanPending && now.Sub(d.scanRequested) >= 2*time.Second {
		if b, e := wpa(d.runtime, "SCAN_RESULTS"); e == nil {
			d.scan = parseScan(b)
		}
		d.scanPending = false
		if !completed {
			d.chooseVisible()
		}
	}
	if d.network.Active && !d.network.HealthyRules() {
		d.fallback("network rules were changed by the system")
	}
	_ = atomicJSON(filepath.Join(d.base, "status.json"), d.status(), 0600)
}
func (d *engine) onHealth(h Health) {
	if h.epoch != d.epoch {
		return
	}
	d.inFlight = false
	d.health = h
	if !d.lease.Valid(time.Now()) || d.link["wpa_state"] != "COMPLETED" || d.config.Mode != "auto" {
		return
	}
	activate, fall := d.policy.Health(time.Now(), h.OK)
	if fall {
		d.fallback("Wi-Fi internet checks failed repeatedly")
	}
	if activate {
		if e := d.network.Enable(); e != nil {
			d.fallback("Wi-Fi rule activation failed")
			d.event(e.Error())
		} else {
			d.event("Wi-Fi: internet checks passed")
		}
	}
}
func (d *engine) handle(r request) (any, error) {
	switch r.action {
	case "status":
		return d.status(), nil
	case "profiles":
		return d.config.Public(), nil
	case "scan":
		e := d.scanNow()
		return ScanSnapshot{Pending: d.scanPending, Networks: append([]AccessPoint{}, d.scan...)}, e
	case "save":
		if r.profile == nil {
			return nil, errors.New("profile is required")
		}
		c := d.config
		c.Profiles = append([]Profile(nil), c.Profiles...)
		if e := upsert(&c, *r.profile); e != nil {
			return nil, e
		}
		if e := saveConfig(d.base, c); e != nil {
			return nil, e
		}
		d.config = c
		if e := d.restartWPA(); e != nil {
			return nil, e
		}
		d.event("saved Wi-Fi profiles updated")
		return d.config.Public(), nil
	case "forget":
		c := d.config
		c.Profiles = nil
		found := false
		for _, p := range d.config.Profiles {
			if p.ID == r.id {
				found = true
			} else {
				c.Profiles = append(c.Profiles, p)
			}
		}
		if !found {
			return nil, errors.New("saved network not found")
		}
		if e := saveConfig(d.base, c); e != nil {
			return nil, e
		}
		d.config = c
		return d.config.Public(), d.restartWPA()
	case "mode":
		if r.mode != "auto" && r.mode != "cellular" && r.mode != "wifi" {
			return nil, errors.New("mode must be auto, cellular or wifi")
		}
		c := d.config
		c.Mode = r.mode
		if e := saveConfig(d.base, c); e != nil {
			return nil, e
		}
		d.config = c
		if r.mode == "cellular" {
			d.clearLease("cellular mode requested")
			_, _ = wpa(d.runtime, "DISCONNECT")
		} else {
			d.holdUntil = time.Time{}
			d.lastScan = time.Time{}
			if len(d.ids) > 0 {
				_ = d.selectProfile(d.ids[0])
			}
		}
		d.event("mode: " + r.mode)
		return d.status(), nil
	case "connect":
		d.preferred = r.id
		d.holdUntil = time.Time{}
		d.clearLease("manual network selection")
		return nil, d.selectProfile(r.id)
	case "reconnect":
		d.holdUntil = time.Time{}
		if len(d.ids) == 0 {
			return nil, errors.New("no saved networks")
		}
		return nil, d.selectProfile(d.ids[0])
	case "test-disconnect":
		d.holdUntil = time.Now().Add(15 * time.Second)
		_, e := wpa(d.runtime, "DISCONNECT")
		// Let the normal monitor detect the link loss and perform fallback.
		d.event("link-loss rehearsal: reconnect allowed after 15 seconds")
		return d.status(), e
	case "renew":
		c := d.children["dhcp"]
		if c == nil {
			return nil, errors.New("no running DHCP client")
		}
		return nil, c.Process.Signal(syscall.SIGUSR1)
	case "lease":
		if r.generation != d.generation {
			return nil, nil
		}
		if r.event == "deconfig" || r.event == "nak" {
			if d.lease.IP != "" {
				d.clearLease("DHCP address deconfigured")
			}
			return nil, nil
		}
		if r.event != "bound" && r.event != "renew" {
			return nil, nil
		}
		if r.lease == nil {
			return nil, errors.New("missing lease")
		}
		l := *r.lease
		if e := validateLease(l); e != nil {
			return nil, e
		}
		l.Expires = time.Now().Add(time.Duration(l.Seconds) * time.Second)
		changed := d.lease.IP != l.IP || d.lease.Subnet != l.Subnet || d.lease.Gateway != l.Gateway || strings.Join(d.lease.DNS, ",") != strings.Join(l.DNS, ",")
		if changed {
			d.fallback("DHCP addressing changed")
			d.epoch++
			d.inFlight = false
			d.policy.Successes = 0
			if e := d.network.Prepare(l); e != nil {
				return nil, e
			}
			d.event("DHCP lease applied")
		} else {
			d.network.Lease = l
			_ = d.network.persist()
		}
		d.lease = l
		return nil, nil
	default:
		return nil, errors.New("unknown action")
	}
}
