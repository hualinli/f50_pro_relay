package relay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

type Mode string

const (
	ModeAuto     Mode = "auto"
	ModeCellular Mode = "cellular"
	ModeWiFi     Mode = "wifi"
)

var (
	ErrNotRunning = errors.New("relay manager is not running")
	ErrAlreadyRun = errors.New("relay manager Run may only be called once")
	ErrInUse      = errors.New("relay installation is already in use")
)

// ScanSnapshot reports cached scan results; Pending means a fresh scan is in progress.
type ScanSnapshot struct {
	Pending  bool          `json:"pending"`
	Networks []AccessPoint `json:"networks"`
}

// Manager serializes network changes. Its methods may be called concurrently.
// Construct one per device, call Run once, and cancel its context to stop.
// The package installs no process signal handlers and opens no HTTP/RPC listeners.
// Context cancellation skips queued operations; an operation already executing may
// finish after its caller stops waiting. Cancel Run and wait for its return to stop safely.
type Manager struct {
	options     Options
	requests    chan operation
	ready, done chan struct{}
	state       atomic.Uint32
}

func New(options Options) (*Manager, error) {
	o, e := options.normalized()
	if e != nil {
		return nil, e
	}
	return &Manager{options: o, requests: make(chan operation, 64), ready: make(chan struct{}), done: make(chan struct{})}, nil
}

// Ready waits for startup. Run must have been started by the caller.
func (m *Manager) Ready(ctx context.Context) error {
	select {
	case <-m.ready:
		if m.state.Load() == 2 {
			return nil
		}
		return ErrNotRunning
	case <-m.done:
		return ErrNotRunning
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run owns the relay until ctx is cancelled or a startup/cleanup error occurs.
// It returns only after children and owned network resources have been cleaned up.
func (m *Manager) Run(ctx context.Context) (err error) {
	if !m.state.CompareAndSwap(0, 1) {
		return ErrAlreadyRun
	}
	defer func() { m.state.Store(3); close(m.done) }()
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := m.options.Tools.validate(); e != nil {
		return e
	}
	lock, e := installationLock(m.options.BaseDir)
	if e != nil {
		return e
	}
	defer lock.Close()
	cfg, e := loadConfig(m.options.BaseDir)
	if e != nil {
		return e
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d := &engine{options: m.options, ctx: runCtx, base: m.options.BaseDir, runtime: m.options.RuntimeDir, config: cfg, network: networkState{Base: m.options.BaseDir, tools: m.options.Tools}, exits: make(chan childExit, 16), probes: make(chan Health, 16), children: map[string]*exec.Cmd{}, link: map[string]string{}, policy: switchPolicy{Cooldown: 30 * time.Second}, certs: roots(), since: time.Now(), scan: []AccessPoint{}}
	if e = d.network.init(); e != nil {
		return errors.Join(e, d.network.Cleanup())
	}
	defer func() {
		cancel()
		d.fallback("service stopped")
		d.stopChild("dhcp")
		d.stopChild("wpa")
		time.Sleep(1200 * time.Millisecond)
		err = errors.Join(err, d.network.Cleanup())
		_ = os.Remove(filepath.Join(d.runtime, "wpa.conf"))
		_ = os.Remove(filepath.Join(d.base, "dhcp-event"))
	}()
	if e = d.restartWPA(); e != nil {
		return e
	}
	m.state.Store(2)
	close(m.ready)
	d.event("automatic relay started")
	return m.loop(runCtx, d, time.NewTicker(2*time.Second))
}
func (m *Manager) loop(ctx context.Context, d *engine, ticker *time.Ticker) error {
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case msg := <-m.requests:
			if ctx.Err() != nil {
				return nil
			}
			if e := msg.ctx.Err(); e != nil {
				msg.reply <- response{err: e}
				continue
			}
			data, e := d.handle(msg.request)
			msg.reply <- response{data, e}
		case h := <-d.probes:
			d.onHealth(h)
		case ex := <-d.exits:
			if c := d.children[ex.Name]; c != nil && c.Process.Pid == ex.PID {
				delete(d.children, ex.Name)
				d.saveChildren()
				d.clearLease(ex.Name + " process exited")
				d.event(ex.Name + " process exited; recovery pending")
			}
		case <-ticker.C:
			d.tick()
		}
	}
}
func (m *Manager) do(ctx context.Context, r request) (any, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if m.state.Load() == 0 || m.state.Load() == 3 {
		return nil, ErrNotRunning
	}
	if e := m.Ready(ctx); e != nil {
		return nil, e
	}
	op := operation{request: r, ctx: ctx, reply: make(chan response, 1)}
	select {
	case m.requests <- op:
	case <-m.done:
		return nil, ErrNotRunning
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-op.reply:
		return r.data, r.err
	case <-m.done:
		return nil, ErrNotRunning
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	v, e := m.do(ctx, request{action: "status"})
	if e != nil {
		return Status{}, e
	}
	return v.(Status), nil
}
func (m *Manager) Profiles(ctx context.Context) ([]PublicProfile, error) {
	v, e := m.do(ctx, request{action: "profiles"})
	if e != nil {
		return nil, e
	}
	return v.([]PublicProfile), nil
}
func (m *Manager) Scan(ctx context.Context) (ScanSnapshot, error) {
	v, e := m.do(ctx, request{action: "scan"})
	if e != nil {
		return ScanSnapshot{}, e
	}
	return v.(ScanSnapshot), nil
}

// SaveProfile creates or replaces a network by ID or SSID. It never returns a password.
func (m *Manager) SaveProfile(ctx context.Context, p Profile) (PublicProfile, error) {
	v, e := m.do(ctx, request{action: "save", profile: &p})
	if e != nil {
		return PublicProfile{}, e
	}
	for _, saved := range v.([]PublicProfile) {
		if saved.SSID == p.SSID {
			return saved, nil
		}
	}
	return PublicProfile{}, errors.New("saved profile not found")
}
func (m *Manager) DeleteProfile(ctx context.Context, id string) error {
	_, e := m.do(ctx, request{action: "forget", id: id})
	return e
}
func (m *Manager) Connect(ctx context.Context, id string) error {
	_, e := m.do(ctx, request{action: "connect", id: id})
	return e
}
func (m *Manager) SetMode(ctx context.Context, mode Mode) error {
	_, e := m.do(ctx, request{action: "mode", mode: string(mode)})
	return e
}
func (m *Manager) Reconnect(ctx context.Context) error {
	_, e := m.do(ctx, request{action: "reconnect"})
	return e
}
func (m *Manager) RenewLease(ctx context.Context) error {
	_, e := m.do(ctx, request{action: "renew"})
	return e
}

// TestDisconnect exercises the normal link-loss monitor, holding reconnection for 15 seconds.
func (m *Manager) TestDisconnect(ctx context.Context) error {
	_, e := m.do(ctx, request{action: "test-disconnect"})
	return e
}
func installationLock(base string) (*os.File, error) {
	if e := os.MkdirAll(base, 0700); e != nil {
		return nil, e
	}
	if e := os.Chmod(base, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(base, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, ErrInUse
	}
	return f, nil
}

// Recover cleans up an interrupted instance. It refuses to touch a running Manager.
// A host program may call it before Run, or from an external process supervisor.
func Recover(options Options) error {
	o, e := options.normalized()
	if e != nil {
		return e
	}
	lock, e := installationLock(o.BaseDir)
	if e != nil {
		return e
	}
	defer lock.Close()
	return recoverOwned(o)
}

// ImportConfig writes a validated configuration only while the installation is idle.
func ImportConfig(options Options, input Config) error {
	o, e := options.normalized()
	if e != nil {
		return e
	}
	lock, e := installationLock(o.BaseDir)
	if e != nil {
		return e
	}
	defer lock.Close()
	c := Config{Mode: input.Mode, Profiles: []Profile{}}
	if c.Mode == "" {
		c.Mode = "auto"
	}
	if c.Mode != "auto" && c.Mode != "wifi" && c.Mode != "cellular" {
		return errors.New("invalid mode")
	}
	for _, p := range input.Profiles {
		if e := upsert(&c, p); e != nil {
			return e
		}
	}
	return saveConfig(o.BaseDir, c)
}
