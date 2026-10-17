package relay

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficBaselineAndAssociationLifetime(t *testing.T) {
	base := t.TempDir()
	if e := os.Mkdir(filepath.Join(base, "statistics"), 0700); e != nil {
		t.Fatal(e)
	}
	write := func(name, value string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(base, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("ifindex", "42\n")
	write("statistics/rx_bytes", "123456\n")
	write("statistics/tx_bytes", "900\n")
	now := time.Now()
	baseline := trafficBaseline(base, now)
	if baseline == nil || baseline.RXBase != 123456 || baseline.TXBase != 900 || baseline.InterfaceID != "42" || !baseline.Since.Equal(now) {
		t.Fatal("invalid baseline", baseline)
	}
	write("statistics/rx_bytes", "999999\n")
	d := &engine{traffic: baseline, trafficKey: "same"}
	d.trackAssociation(true, "same")
	if d.traffic != baseline || d.traffic.RXBase != 123456 {
		t.Fatal("steady association changed its baseline")
	}
	s := d.status()
	s.Traffic.RXBase = 0
	if d.traffic.RXBase != 123456 {
		t.Fatal("status leaked mutable session")
	}
	d.trackAssociation(false, "")
	if d.traffic != nil || d.trafficKey != "" {
		t.Fatal("disconnect retained old session")
	}
	write("statistics/tx_bytes", "invalid")
	if trafficBaseline(base, now) != nil {
		t.Fatal("invalid counters accepted")
	}
}
