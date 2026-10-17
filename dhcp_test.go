package relay

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDHCPHookDoesNotDependOnHostExecutable(t *testing.T) {
	// Exercise the actual generated hook with shell metacharacters in its path.
	base := filepath.Join(t.TempDir(), "relay ' $data")
	if e := os.MkdirAll(base, 0700); e != nil {
		t.Fatal(e)
	}
	shell := "/bin/sh"
	if _, e := os.Stat("/system/bin/sh"); e == nil {
		shell = "/system/bin/sh"
	}
	bb := filepath.Join(base, "busybox")
	if e := os.WriteFile(bb, []byte("#!"+shell+"\nexec \"$@\"\n"), 0700); e != nil {
		t.Fatal(e)
	}
	opts, e := (Options{BaseDir: base, Tools: Tools{BusyBox: bb}}).normalized()
	if e != nil {
		t.Fatal(e)
	}
	d := &engine{base: base, options: opts, ctx: context.Background(), generation: 4, lease: Lease{IP: "192.168.10.2", Subnet: "255.255.255.0", Gateway: "192.168.10.1", DNS: []string{"1.1.1.1"}, Seconds: 7200, Expires: time.Now().Add(-time.Hour)}}
	d.network.Base = base
	d.network.Lease = d.lease // Identical addressing should only extend the lease, without touching routes.
	hook, e := d.writeDHCPHook()
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(hook)
	if strings.Contains(string(b), "relayd") || strings.Contains(string(b), " lease ") {
		t.Fatal("hook depends on host CLI")
	}
	invoke := func() {
		c := exec.Command(shell, hook, "renew")
		c.Env = append(os.Environ(), "ip=192.168.10.2", "subnet=255.255.255.0", "router=192.168.10.1", "dns=1.1.1.1", "lease=7200")
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("hook failed: %v %s", e, out)
		}
		d.readDHCPEvent()
	}
	invoke()
	first := d.lease.Expires
	if !d.lease.Valid(time.Now()) {
		t.Fatal("lease was not renewed")
	}
	time.Sleep(10 * time.Millisecond)
	invoke()
	if !d.lease.Expires.After(first) {
		t.Fatal("identical repeated renewal was lost")
	}
	info, e := os.Stat(filepath.Join(base, "dhcp-event"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("insecure or missing lease event", e)
	}
	oldStamp := d.leaseStamp
	if _, e = d.writeDHCPHook(); e != nil {
		t.Fatal(e)
	}
	raw := oldStamp + "\ncallback\nrenew\n192.168.10.2\n255.255.255.0\n192.168.10.1\n1.1.1.1\n7200\n"
	if _, e = parseDHCPEvent(raw, d.leaseStamp, 4); e == nil {
		t.Fatal("retired client lease accepted")
	}
}
func TestMalformedDHCPDataCannotBecomeRoutes(t *testing.T) {
	good := "current\ncallback\nbound\n192.168.10.2\n255.255.255.0\n192.168.10.1\n1.1.1.1\n7200\n"
	if _, e := parseDHCPEvent(good, "current", 1); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{strings.Replace(good, "1.1.1.1", "1.1.1.1;reboot", 1), strings.Replace(good, "192.168.10.1\n", "10.1.1.1\n", 1), good + "unexpected\n"} {
		if _, e := parseDHCPEvent(raw, "current", 1); e == nil {
			t.Fatal("unsafe DHCP input accepted")
		}
	}
}

func TestDHCPRouteFailureIsRetried(t *testing.T) {
	base := t.TempDir()
	shell := "/bin/sh"
	if _, e := os.Stat("/system/bin/sh"); e == nil {
		shell = "/system/bin/sh"
	}
	tool := filepath.Join(base, "failed-ip")
	marker := filepath.Join(base, "attempts")
	script := "#!" + shell + "\nprintf 'attempt\\n' >> " + shellQuote(marker) + "\nexit 1\n"
	if e := os.WriteFile(tool, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	opts, e := (Options{BaseDir: base, Tools: Tools{IP: tool}}).normalized()
	if e != nil {
		t.Fatal(e)
	}
	d := &engine{base: base, options: opts, generation: 1, leaseStamp: "current", network: networkState{Base: base, tools: opts.Tools, readLAN: func() (LAN, error) { return LAN{Interface: "br0", Address: "192.168.0.1", CIDR: "192.168.0.0/24"}, nil }}}
	raw := "current\ncallback\nbound\n192.168.10.2\n255.255.255.0\n192.168.10.1\n1.1.1.1\n7200\n"
	if e := os.WriteFile(filepath.Join(base, "dhcp-event"), []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	d.readDHCPEvent()
	d.readDHCPEvent()
	b, e := os.ReadFile(marker)
	if e != nil || strings.Count(string(b), "attempt") != 2 {
		t.Fatal("transient DHCP application failure was consumed", e)
	}
	if d.lease.IP != "" {
		t.Fatal("failed route setup produced a usable lease")
	}
}
