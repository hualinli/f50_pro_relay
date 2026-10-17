package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const iface = "relay_sta0"
const legacyIface = "ufi_sta0" // Recovery of ownership records written before v0.4.1.
const lan = "192.168.0.0/24"
const table = "30555"

type Lease struct {
	IP         string    `json:"ip"`
	Subnet     string    `json:"subnet"`
	Gateway    string    `json:"gateway"`
	DNS        []string  `json:"dns"`
	Seconds    int       `json:"seconds"`
	Expires    time.Time `json:"expires"`
	Generation int       `json:"generation"`
}

func (l Lease) Valid(now time.Time) bool { return validateLease(l) == nil && now.Before(l.Expires) }
func validateLease(l Lease) error {
	ip := net.ParseIP(l.IP).To4()
	maskIP := net.ParseIP(l.Subnet).To4()
	gw := net.ParseIP(l.Gateway).To4()
	if ip == nil || maskIP == nil || gw == nil || l.Seconds < 10 || len(l.DNS) == 0 {
		return errors.New("invalid DHCP lease")
	}
	ones, bits := net.IPMask(maskIP).Size()
	if bits != 32 || ones < 1 || ones > 30 {
		return errors.New("invalid DHCP subnet")
	}
	subnet := &net.IPNet{IP: ip.Mask(net.IPMask(maskIP)), Mask: net.IPMask(maskIP)}
	if !subnet.Contains(gw) || ip.Equal(gw) || ip.Equal(subnet.IP) {
		return errors.New("invalid DHCP gateway or host address")
	}
	_, local, _ := net.ParseCIDR(lan)
	if local.Contains(ip) || subnet.Contains(local.IP) {
		return errors.New("upstream overlaps hotspot LAN")
	}
	for _, d := range l.DNS {
		if net.ParseIP(d).To4() == nil {
			return errors.New("invalid DHCP DNS")
		}
	}
	return nil
}
func (l Lease) Prefix() string {
	n, _ := net.IPMask(net.ParseIP(l.Subnet).To4()).Size()
	return strconv.Itoa(n)
}
func (l Lease) Network() string {
	return net.ParseIP(l.IP).To4().Mask(net.IPMask(net.ParseIP(l.Subnet).To4())).String() + "/" + l.Prefix()
}
func (n *networkState) command(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	path := name
	if !strings.Contains(name, "/") {
		path = n.tools.resolve(name)
	}
	b, e := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if e != nil {
		return string(b), fmt.Errorf("%s failed: %w: %s", filepath.Base(name), e, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

type networkState struct {
	Base          string
	Interface     string `json:"interface,omitempty"`
	tools         Tools  `json:"-"`
	Owned, Active bool
	NeedsCleanup  bool
	Lease         Lease
}

func (n *networkState) persist() error {
	return atomicJSON(filepath.Join(n.Base, "network.json"), n, 0600)
}
func (n *networkState) init() error {
	for _, r := range []string{"10950:", "10960:"} {
		b, e := n.command("ip", "rule", "show")
		if e != nil {
			return e
		}
		if strings.Contains(b, r) {
			return errors.New("reserved policy rule is already in use")
		}
	}
	if b, e := n.command("ip", "route", "show", "table", table); e != nil || strings.TrimSpace(b) != "" {
		return errors.New("reserved route table is already in use")
	}
	if _, e := os.Stat("/sys/class/net/" + iface); e == nil {
		return errors.New("reserved wireless interface is already in use")
	}
	for _, c := range []struct {
		cmd  string
		args []string
	}{{"iptables", []string{"-S", "UFI_RELAY_FWD"}}, {"iptables", []string{"-t", "nat", "-S", "UFI_RELAY_NAT"}}, {"iptables", []string{"-t", "nat", "-S", "UFI_RELAY_DNS"}}, {"ip6tables", []string{"-S", "UFI_RELAY6_FWD"}}} {
		if _, e := n.command(c.cmd, c.args...); e == nil {
			return errors.New("reserved firewall chain is already in use")
		}
	}
	n.Interface = iface
	n.Owned = true
	if e := n.persist(); e != nil {
		return e
	}
	if _, e := n.command(n.tools.IW, "phy", "phy0", "interface", "add", iface, "type", "managed"); e != nil {
		return e
	}
	_, e := n.command("ip", "link", "set", "dev", iface, "up")
	return e
}
func (n *networkState) Prepare(l Lease) error {
	if e := validateLease(l); e != nil {
		return e
	}
	if n.Lease.IP != "" && n.Lease.IP != l.IP {
		_, _ = n.command("ip", "addr", "del", n.Lease.IP+"/"+n.Lease.Prefix(), "dev", iface)
	}
	if _, e := n.command("ip", "addr", "replace", l.IP+"/"+l.Prefix(), "dev", iface, "noprefixroute"); e != nil {
		return e
	}
	if _, e := n.command("ip", "route", "flush", "table", table); e != nil {
		return e
	}
	for _, a := range [][]string{{"route", "add", "table", table, l.Network(), "dev", iface, "src", l.IP}, {"route", "add", "table", table, lan, "dev", "br0", "src", "192.168.0.1"}, {"route", "add", "table", table, "default", "via", l.Gateway, "dev", iface, "src", l.IP}} {
		if _, e := n.command("ip", a...); e != nil {
			return e
		}
	}
	b, e := n.command("ip", "rule", "show")
	if e != nil {
		return e
	}
	if !strings.Contains(b, "10950:") {
		if _, e = n.command("ip", "rule", "add", "pref", "10950", "iif", "lo", "oif", iface, "lookup", table); e != nil {
			return e
		}
	}
	n.Lease = l
	return n.persist()
}
func (n *networkState) ipt(name string, args ...string) error {
	_, e := n.command(name, append([]string{"-w", "3"}, args...)...)
	return e
}
func (n *networkState) removeRule(name string, args ...string) {
	check := append([]string(nil), args...)
	for i, v := range check {
		if v == "-D" {
			check[i] = "-C"
			break
		}
	}
	if n.ipt(name, check...) == nil {
		_ = n.ipt(name, args...)
	}
}
func (n *networkState) Enable() error {
	if !n.Owned || !n.Lease.Valid(time.Now()) {
		return errors.New("no valid managed Wi-Fi lease")
	}
	if n.Active && n.HealthyRules() {
		return nil
	}
	if e := n.Disable(); e != nil {
		return e
	}
	rules := []struct {
		cmd string
		a   []string
	}{
		{"iptables", []string{"-N", "UFI_RELAY_FWD"}},
		{"iptables", []string{"-A", "UFI_RELAY_FWD", "-i", "br0", "-o", iface, "-s", lan, "-m", "state", "--state", "NEW,ESTABLISHED,RELATED", "-j", "ACCEPT"}},
		{"iptables", []string{"-A", "UFI_RELAY_FWD", "-i", iface, "-o", "br0", "-d", lan, "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"}},
		{"iptables", []string{"-I", "FORWARD", "1", "-j", "UFI_RELAY_FWD"}},
		{"iptables", []string{"-t", "nat", "-N", "UFI_RELAY_NAT"}},
		{"iptables", []string{"-t", "nat", "-A", "UFI_RELAY_NAT", "-j", "MASQUERADE"}},
		{"iptables", []string{"-t", "nat", "-I", "POSTROUTING", "1", "-s", lan, "-o", iface, "-j", "UFI_RELAY_NAT"}},
		{"iptables", []string{"-t", "nat", "-N", "UFI_RELAY_DNS"}},
		{"iptables", []string{"-t", "nat", "-A", "UFI_RELAY_DNS", "-p", "udp", "--dport", "53", "-j", "DNAT", "--to-destination", n.Lease.DNS[0] + ":53"}},
		{"iptables", []string{"-t", "nat", "-A", "UFI_RELAY_DNS", "-p", "tcp", "--dport", "53", "-j", "DNAT", "--to-destination", n.Lease.DNS[0] + ":53"}},
		{"iptables", []string{"-t", "nat", "-I", "PREROUTING", "1", "-i", "br0", "-s", lan, "-j", "UFI_RELAY_DNS"}},
		{"ip6tables", []string{"-N", "UFI_RELAY6_FWD"}},
		{"ip6tables", []string{"-A", "UFI_RELAY6_FWD", "-i", "br0", "-j", "REJECT", "--reject-with", "icmp6-adm-prohibited"}},
		{"ip6tables", []string{"-I", "FORWARD", "1", "-j", "UFI_RELAY6_FWD"}},
	}
	for _, r := range rules {
		if e := n.ipt(r.cmd, r.a...); e != nil {
			_ = n.Disable()
			return e
		}
	}
	if _, e := n.command("ip", "rule", "add", "pref", "10960", "from", lan, "iif", "br0", "lookup", table); e != nil {
		_ = n.Disable()
		return e
	}
	n.Active = true
	if e := n.persist(); e != nil {
		_ = n.Disable()
		return e
	}
	_, e := purgeClientConnections("")
	return e
}

// Ownership records preserve the interface name across upgrades. Legacy records
// did not store it; they exclusively owned ufi_sta0. Never infer ownership of APs.
func (n *networkState) ownedInterface() (string, error) {
	if n.Interface == "" {
		return legacyIface, nil
	}
	if n.Interface != iface && n.Interface != legacyIface {
		return "", errors.New("invalid owned relay interface")
	}
	return n.Interface, nil
}

func (n *networkState) Disable() error {
	if !n.Owned {
		return nil
	}
	iface, interfaceErr := n.ownedInterface()
	if interfaceErr != nil {
		return interfaceErr
	}
	var first error
	b, e := n.command("ip", "rule", "show")
	if e != nil {
		first = e
	} else if strings.Contains(b, "10960:") {
		_, e = n.command("ip", "rule", "del", "pref", "10960", "from", lan, "iif", "br0", "lookup", table)
		if e != nil {
			first = e
		}
	}
	n.removeRule("iptables", "-t", "nat", "-D", "PREROUTING", "-i", "br0", "-s", lan, "-j", "UFI_RELAY_DNS")
	n.removeRule("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", lan, "-o", iface, "-j", "UFI_RELAY_NAT")
	n.removeRule("iptables", "-D", "FORWARD", "-j", "UFI_RELAY_FWD")
	n.removeRule("ip6tables", "-D", "FORWARD", "-j", "UFI_RELAY6_FWD")
	for _, r := range []struct{ cmd, table, chain string }{{"iptables", "nat", "UFI_RELAY_NAT"}, {"iptables", "nat", "UFI_RELAY_DNS"}, {"iptables", "filter", "UFI_RELAY_FWD"}, {"ip6tables", "filter", "UFI_RELAY6_FWD"}} {
		if n.ipt(r.cmd, "-t", r.table, "-S", r.chain) == nil {
			if e := n.ipt(r.cmd, "-t", r.table, "-F", r.chain); e != nil && first == nil {
				first = e
			}
			if e := n.ipt(r.cmd, "-t", r.table, "-X", r.chain); e != nil && first == nil {
				first = e
			}
		}
	}
	if n.Active {
		if _, e := purgeClientConnections(n.Lease.IP); e != nil && first == nil {
			first = e
		}
	}
	// Do not claim cellular recovery while a failed route deletion still points at Wi-Fi.
	b, e = n.command("ip", "rule", "show")
	if e != nil && first == nil {
		first = e
	}
	n.Active = e != nil || strings.Contains(b, "10960:")
	n.NeedsCleanup = first != nil
	if e := n.persist(); e != nil && first == nil {
		first = e
		n.NeedsCleanup = true
	}
	return first
}
func (n *networkState) HealthyRules() bool {
	b, e := n.command("ip", "rule", "show")
	if e != nil || !strings.Contains(b, "10960:") {
		return false
	}
	return n.ipt("iptables", "-C", "FORWARD", "-j", "UFI_RELAY_FWD") == nil && n.ipt("iptables", "-t", "nat", "-C", "PREROUTING", "-i", "br0", "-s", lan, "-j", "UFI_RELAY_DNS") == nil && n.ipt("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", lan, "-o", iface, "-j", "UFI_RELAY_NAT") == nil && n.ipt("ip6tables", "-C", "FORWARD", "-j", "UFI_RELAY6_FWD") == nil
}
func (n *networkState) Cleanup() error {
	if !n.Owned {
		return nil
	}
	iface, interfaceErr := n.ownedInterface()
	if interfaceErr != nil {
		return interfaceErr
	}
	e := n.Disable()
	if b, _ := n.command("ip", "rule", "show"); strings.Contains(b, "10950:") {
		_, ce := n.command("ip", "rule", "del", "pref", "10950", "iif", "lo", "oif", iface, "lookup", table)
		if e == nil {
			e = ce
		}
	}
	_, ce := n.command("ip", "route", "flush", "table", table)
	if e == nil {
		e = ce
	}
	if _, ce = os.Stat("/sys/class/net/" + iface); ce == nil {
		_, ce = n.command(n.tools.IW, "dev", iface, "del")
		if e == nil {
			e = ce
		}
	}
	if e == nil {
		n.Owned = false
		n.Lease = Lease{}
		_ = os.Remove(filepath.Join(n.Base, "network.json"))
	}
	return e
}
