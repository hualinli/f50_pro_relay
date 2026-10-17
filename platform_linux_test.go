//go:build linux

package relay

import (
	"encoding/binary"
	"testing"
)

func TestConnectionCleanupPreservesManagement(t *testing.T) {
	cases := []struct {
		line string
		ok   bool
	}{
		{"ipv4 2 tcp 6 100 ESTABLISHED src=192.168.0.95 dst=192.168.0.1 sport=55556 dport=5555 src=192.168.0.1 dst=192.168.0.95 sport=5555 dport=55556", false},
		{"ipv4 2 udp 17 100 src=192.168.0.95 dst=192.168.0.1 sport=55111 dport=53 src=119.29.29.29 dst=192.168.10.106 sport=53 dport=55111", true},
		{"ipv4 2 tcp 6 100 ESTABLISHED src=192.168.0.95 dst=1.1.1.1 sport=55556 dport=443 src=1.1.1.1 dst=192.168.10.106 sport=443 dport=55556", true},
		{"ipv4 2 tcp 6 100 src=192.168.10.106 dst=1.1.1.1 sport=55556 dport=443 src=1.1.1.1 dst=192.168.10.106 sport=443 dport=55556", false},
	}
	for _, c := range cases {
		f, ok := parseFlow(c.line)
		if ok != c.ok {
			t.Fatal("incorrect flow ownership", c.line)
		}
		if ok {
			b := deleteMessage(f, 9)
			if int(binary.LittleEndian.Uint32(b)) != len(b) || binary.LittleEndian.Uint16(b[4:]) != 0x102 || binary.LittleEndian.Uint32(b[8:]) != 9 {
				t.Fatal("invalid conntrack delete encoding")
			}
		}
	}
}
