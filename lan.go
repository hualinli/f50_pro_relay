package relay

import (
	"errors"
	"net"
)

// LAN describes the effective downstream IPv4 network, not a factory default.
type LAN struct {
	Interface string `json:"interface"`
	Index     int    `json:"index,omitempty"`
	Address   string `json:"address"`
	CIDR      string `json:"cidr"`
}

func (l LAN) valid() error {
	ip := net.ParseIP(l.Address).To4()
	_, network, e := net.ParseCIDR(l.CIDR)
	if e != nil || ip == nil || l.Interface != "br0" || network.IP.To4() == nil || !network.Contains(ip) {
		return errors.New("invalid downstream LAN")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones < 1 || ones > 30 || ip.Equal(network.IP) {
		return errors.New("invalid downstream subnet")
	}
	return nil
}

// ReadLAN reads the effective br0 address. During a missing/ambiguous address
// transition, callers must wait rather than route through an assumed subnet.
func ReadLAN() (LAN, error) {
	device, e := net.InterfaceByName("br0")
	if e != nil {
		return LAN{}, e
	}
	addresses, e := device.Addrs()
	if e != nil {
		return LAN{}, e
	}
	result, e := lanFromAddresses(addresses)
	if e != nil {
		return LAN{}, e
	}
	result.Index = device.Index
	return result, nil
}

func lanFromAddresses(addresses []net.Addr) (LAN, error) {
	var result LAN
	count := 0
	for _, a := range addresses {
		ip, n, e := net.ParseCIDR(a.String())
		if e != nil || ip.To4() == nil {
			continue
		}
		n.IP = ip.Mask(n.Mask)
		candidate := LAN{Interface: "br0", Address: ip.String(), CIDR: n.String()}
		if candidate.valid() != nil {
			continue
		}
		result = candidate
		count++
	}
	if count != 1 {
		return LAN{}, errors.New("downstream IPv4 address missing or ambiguous")
	}
	return result, nil
}
func checkOverlap(l Lease, local LAN) error {
	if e := local.valid(); e != nil {
		return e
	}
	_, a, _ := net.ParseCIDR(local.CIDR)
	_, b, e := net.ParseCIDR(l.Network())
	if e != nil {
		return e
	}
	if a.Contains(b.IP) || b.Contains(a.IP) {
		return errors.New("upstream overlaps hotspot LAN")
	}
	return nil
}
func (n *networkState) detectLAN() (LAN, error) {
	if n.readLAN != nil {
		return n.readLAN()
	}
	return ReadLAN()
}
func (n *networkState) ownedLAN() (LAN, error) {
	// Records before v0.5.0 exclusively used this network. Use it only to undo
	// their rules, never to infer the current network for a new connection.
	local := LAN{Interface: "br0", Address: "192.168.0.1", CIDR: "192.168.0.0/24"}
	if n.LAN != nil {
		local = *n.LAN
	}
	return local, local.valid()
}
func (d *engine) refreshLAN() bool {
	current, e := d.network.detectLAN()
	if e != nil {
		if d.lease.IP != "" || d.network.Active {
			d.clearLease("hotspot LAN unavailable")
		}
		return false
	}
	if d.network.LAN != nil && *d.network.LAN == current {
		return true
	}
	d.clearLease("hotspot LAN changed")
	if d.network.NeedsCleanup {
		return false
	}
	previous := d.network.LAN
	d.network.LAN = &current
	if e := d.network.persist(); e != nil {
		d.network.LAN = previous
		d.event("hotspot LAN update could not be saved")
		return false
	}
	d.event("hotspot LAN updated")
	return true
}
