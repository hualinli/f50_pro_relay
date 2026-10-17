package relay

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func address(s string) net.Addr { ip, n, _ := net.ParseCIDR(s); n.IP = ip; return n }
func TestLANAddressSelectionAndOverlap(t *testing.T) {
	actual, e := lanFromAddresses([]net.Addr{address("fe80::1/64"), address("172.27.83.1/24")})
	if e != nil || actual.Address != "172.27.83.1" || actual.CIDR != "172.27.83.0/24" {
		t.Fatal(actual, e)
	}
	for _, a := range [][]net.Addr{nil, {address("fe80::1/64")}, {address("192.168.0.1/24"), address("172.27.83.1/24")}} {
		if _, e := lanFromAddresses(a); e == nil {
			t.Fatal("ambiguous/missing LAN accepted")
		}
	}
	lease := Lease{IP: "192.168.0.2", Subnet: "255.255.255.0", Gateway: "192.168.0.1", DNS: []string{"1.1.1.1"}, Seconds: 7200}
	if e := checkOverlap(lease, actual); e != nil {
		t.Fatal("old default still treated as downstream", e)
	}
	lease.IP = "172.27.83.2"
	lease.Gateway = "172.27.83.254"
	if checkOverlap(lease, actual) == nil {
		t.Fatal("equal subnets accepted")
	}
	lease.IP = "172.27.0.2"
	lease.Gateway = "172.27.0.1"
	lease.Subnet = "255.255.0.0"
	if checkOverlap(lease, actual) == nil {
		t.Fatal("covering upstream accepted")
	}
	actual = LAN{Interface: "br0", Address: "172.27.0.1", CIDR: "172.27.0.0/16"}
	lease.IP = "172.27.83.2"
	lease.Gateway = "172.27.83.1"
	lease.Subnet = "255.255.255.0"
	if checkOverlap(lease, actual) == nil {
		t.Fatal("covering downstream accepted")
	}
}
func TestLANChangeRemovesOldRulesBeforePreparingNewRoutes(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	tool := filepath.Join(dir, "tool")
	if e := os.WriteFile(tool, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+log+"'\nexit 0\n"), 0700); e != nil {
		t.Fatal(e)
	}
	old := LAN{Interface: "br0", Address: "192.168.0.1", CIDR: "192.168.0.0/24"}
	next := LAN{Interface: "br0", Address: "172.27.83.1", CIDR: "172.27.83.0/24"}
	// Build a minimal engine, using real persistence and command dispatch.
	instance := &engine{base: dir, options: Options{Logger: slog.Default()}, network: networkState{Base: dir, Owned: true, Interface: iface, LAN: &old, readLAN: func() (LAN, error) { return next, nil }, tools: Tools{IP: tool, IPTables: tool, IP6Tables: tool, IW: tool}}, lease: Lease{IP: "192.168.10.2"}}
	if !instance.refreshLAN() {
		t.Fatal("LAN change failed")
	}
	lease := Lease{IP: "192.168.10.2", Subnet: "255.255.255.0", Gateway: "192.168.10.1", DNS: []string{"1.1.1.1"}, Seconds: 7200}
	if e := instance.network.Prepare(lease); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(log)
	calls := string(b)
	oldRule := strings.Index(calls, "-D POSTROUTING -s 192.168.0.0/24")
	newRoute := strings.Index(calls, "172.27.83.0/24 dev br0 src 172.27.83.1")
	if oldRule < 0 || newRoute <= oldRule {
		t.Fatal("LAN rules migrated out of order:", calls)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "network.json"))
	var owned networkState
	if e := json.Unmarshal(b, &owned); e != nil || owned.LAN == nil || *owned.LAN != next {
		t.Fatal("ownership record lost new LAN", e)
	}
	next.Index = 99
	instance.lease = lease
	if !instance.refreshLAN() || instance.network.LAN.Index != 99 || instance.lease.IP != "" {
		t.Fatal("bridge recreation did not invalidate routes/lease")
	}
	instance.network.readLAN = func() (LAN, error) { return LAN{}, errors.New("transition") }
	if instance.refreshLAN() {
		t.Fatal("missing address transition accepted")
	}
}
func TestConnectionCleanupUsesDynamicLAN(t *testing.T) {
	local := LAN{Interface: "br0", Address: "172.27.83.1", CIDR: "172.27.83.0/24"}
	for _, test := range []struct {
		line string
		want bool
	}{
		{"tcp src=172.27.83.20 dst=172.27.83.1 sport=40000 dport=2345 src=172.27.83.1 dst=172.27.83.20 sport=2345 dport=40000", false},
		{"tcp src=172.27.83.20 dst=1.1.1.1 sport=40000 dport=443 src=1.1.1.1 dst=192.168.10.2 sport=443 dport=40000", true},
		{"udp src=172.27.83.20 dst=172.27.83.1 sport=40000 dport=53 src=1.1.1.1 dst=192.168.10.2 sport=53 dport=40000", true},
		{"tcp src=192.168.0.20 dst=1.1.1.1 sport=40000 dport=443", false},
	} {
		if _, ok := parseFlow(test.line, local); ok != test.want {
			t.Fatal(test.line)
		}
	}
}
