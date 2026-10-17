package relay

import (
	"testing"
	"time"
)

func TestLeaseRejectsUnsafeRoutes(t *testing.T) {
	good := Lease{IP: "192.168.10.106", Subnet: "255.255.255.0", Gateway: "192.168.10.1", DNS: []string{"119.29.29.29"}, Seconds: 7200, Expires: time.Now().Add(time.Hour)}
	if !good.Valid(time.Now()) {
		t.Fatal("valid lease rejected")
	}
	for _, mutate := range []func(*Lease){
		func(l *Lease) { l.Subnet = "255.0.255.0" },
		func(l *Lease) { l.Gateway = "10.0.0.1" },
		func(l *Lease) { l.DNS = []string{"1.1.1.1;reboot"} },
		func(l *Lease) { l.Expires = time.Now().Add(-time.Second) },
	} {
		l := good
		mutate(&l)
		if l.Valid(time.Now()) {
			t.Fatal("unsafe or expired lease accepted")
		}
	}
}
