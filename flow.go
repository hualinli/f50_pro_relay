package relay

import (
	"net"
	"strconv"
	"strings"
)

type flow struct {
	src, dst     net.IP
	sport, dport uint16
	proto        byte
	replyDst     string
}

func parseFlow(line string, lan LAN) (flow, bool) {
	var f flow
	src, dst, sport, dport := 0, 0, 0, 0
	for _, v := range strings.Fields(line) {
		switch {
		case v == "tcp":
			f.proto = 6
		case v == "udp":
			f.proto = 17
		case strings.HasPrefix(v, "src="):
			if src == 0 {
				f.src = net.ParseIP(v[4:]).To4()
			}
			src++
		case strings.HasPrefix(v, "dst="):
			if dst == 0 {
				f.dst = net.ParseIP(v[4:]).To4()
			} else if dst == 1 {
				f.replyDst = v[4:]
			}
			dst++
		case strings.HasPrefix(v, "sport="):
			if sport == 0 {
				n, _ := strconv.Atoi(v[6:])
				f.sport = uint16(n)
			}
			sport++
		case strings.HasPrefix(v, "dport="):
			if dport == 0 {
				n, _ := strconv.Atoi(v[6:])
				f.dport = uint16(n)
			}
			dport++
		}
	}
	_, local, e := net.ParseCIDR(lan.CIDR)
	if e != nil {
		return f, false
	}
	// Never remove local management sessions. DNS is the sole local exception.
	return f, f.src != nil && f.dst != nil && local.Contains(f.src) && (f.proto == 6 || f.proto == 17) && (!local.Contains(f.dst) || f.dport == 53)
}
