package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Exercise the actual request loop without creating device interfaces or processes.
func testActor(t *testing.T) (*Manager, *engine) {
	t.Helper()
	m, e := New(Options{BaseDir: filepath.Join(t.TempDir(), "private"), RuntimeDir: filepath.Join(t.TempDir(), "wifi")})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &engine{options: m.options, ctx: ctx, config: Config{Mode: "auto", Profiles: []Profile{{ID: "saved", SSID: "Example", Security: "wpa2", Password: "private-password"}}}, link: map[string]string{"wpa_state": "COMPLETED"}, events: []Event{{Message: "original"}}, lease: Lease{IP: "192.168.10.2", Subnet: "255.255.255.0", Gateway: "192.168.10.1", DNS: []string{"1.1.1.1"}, Seconds: 7200, Expires: time.Now().Add(time.Hour)}, exits: make(chan childExit, 1), probes: make(chan Health, 1)}
	m.state.Store(2)
	close(m.ready)
	finished := make(chan struct{})
	go func() {
		_ = m.loop(ctx, d, time.NewTicker(time.Hour))
		m.state.Store(3)
		close(m.done)
		close(finished)
	}()
	t.Cleanup(func() { cancel(); <-finished })
	return m, d
}
func TestManagerConcurrentSnapshotsAreIsolated(t *testing.T) {
	m, _ := testActor(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				s, e := m.Status(context.Background())
				if e != nil {
					t.Error(e)
					return
				}
				s.Events[0].Message = "mutated"
				s.Profiles[0].SSID = "mutated"
				s.Lease.DNS[0] = "8.8.8.8"
				p, e := m.Profiles(context.Background())
				if e != nil {
					t.Error(e)
					return
				}
				p[0].SSID = "mutated"
			}
		}()
	}
	wg.Wait()
	s, e := m.Status(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.Events[0].Message != "original" || s.Profiles[0].SSID != "Example" || s.Lease.DNS[0] != "1.1.1.1" {
		t.Fatal("caller mutated manager state", s)
	}
}
func TestManagerRejectsCallsOutsideLifecycle(t *testing.T) {
	base := filepath.Join(t.TempDir(), "not-created")
	m, e := New(Options{BaseDir: base})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Status(context.Background()); !errors.Is(e, ErrNotRunning) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = m.Run(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = os.Stat(base); !os.IsNotExist(e) {
		t.Fatal("cancelled Run changed filesystem")
	}
	if e = m.Run(context.Background()); !errors.Is(e, ErrAlreadyRun) {
		t.Fatal(e)
	}
	if _, e = m.Status(context.Background()); !errors.Is(e, ErrNotRunning) {
		t.Fatal(e)
	}
}
func TestCancelledQueuedMutationIsNotApplied(t *testing.T) {
	m, e := New(Options{BaseDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	m.state.Store(2)
	close(m.ready)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = m.SetMode(ctx, ModeCellular); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if len(m.requests) != 0 {
		t.Fatal("pre-cancelled request was queued")
	}
	// A request cancelled after enqueue is checked again before execution.
	ctx, cancel = context.WithCancel(context.Background())
	reply := make(chan response, 1)
	m.requests <- operation{request: request{action: "mode", mode: "cellular"}, ctx: ctx, reply: reply}
	cancel()
	runCtx, stop := context.WithCancel(context.Background())
	d := &engine{config: Config{Mode: "auto"}}
	finished := make(chan error, 1)
	go func() { finished <- m.loop(runCtx, d, time.NewTicker(time.Hour)) }()
	if r := <-reply; !errors.Is(r.err, context.Canceled) {
		t.Fatal(r.err)
	}
	stop()
	<-finished
	if d.config.Mode != "auto" {
		t.Fatal("cancelled mutation was applied")
	}
}
func TestImportAndRecoveryCannotChangeLiveInstallation(t *testing.T) {
	options := Options{BaseDir: t.TempDir()}
	cfg := Config{Profiles: []Profile{{SSID: "Example", Password: "private-password", Security: "wpa2"}}}
	if e := ImportConfig(options, cfg); e != nil {
		t.Fatal(e)
	}
	lock, e := installationLock(options.BaseDir)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if e = ImportConfig(options, Config{Mode: "cellular"}); !errors.Is(e, ErrInUse) {
		t.Fatal(e)
	}
	if e = Recover(options); !errors.Is(e, ErrInUse) {
		t.Fatal(e)
	}
	got, e := loadConfig(options.BaseDir)
	if e != nil {
		t.Fatal(e)
	}
	if got.Mode != "auto" || len(got.Profiles) != 1 {
		t.Fatal("locked configuration changed")
	}
}
func TestFailedStartupPreservesUnownedRuntimeFile(t *testing.T) {
	base := t.TempDir()
	runtime := t.TempDir()
	path := filepath.Join(runtime, "wpa.conf")
	if e := os.WriteFile(path, []byte("unowned"), 0600); e != nil {
		t.Fatal(e)
	}
	m, e := New(Options{BaseDir: base, RuntimeDir: runtime, Tools: Tools{IP: "/nonexistent/ufi-test-ip"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Run(context.Background()); e == nil {
		t.Fatal("unexpected successful startup")
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "unowned" {
		t.Fatal("failed startup touched unowned configuration")
	}
}
