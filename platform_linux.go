//go:build linux

package relay

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func bindInterface(fd uintptr) error {
	return syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
}
func chownWifi(path string) error { return os.Chown(path, 1010, 1010) }
func attr(kind uint16, data []byte) []byte {
	n := 4 + len(data)
	b := make([]byte, (n+3)&^3)
	binary.LittleEndian.PutUint16(b, uint16(n))
	binary.LittleEndian.PutUint16(b[2:], kind)
	copy(b[4:], data)
	return b
}

type flow struct {
	src, dst     net.IP
	sport, dport uint16
	proto        byte
	replyDst     string
}

func parseFlow(line string) (flow, bool) {
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
	_, local, _ := net.ParseCIDR(lan)
	// Never remove local management sessions. DNS is the sole local exception.
	return f, f.src != nil && f.dst != nil && local.Contains(f.src) && (f.proto == 6 || f.proto == 17) && (!local.Contains(f.dst) || f.dport == 53)
}
func deleteMessage(f flow, seq uint32) []byte {
	ip := append(attr(1, f.src), attr(2, f.dst)...)
	ports := make([]byte, 2)
	binary.BigEndian.PutUint16(ports, f.sport)
	proto := append(attr(1, []byte{f.proto}), attr(2, ports)...)
	binary.BigEndian.PutUint16(ports, f.dport)
	proto = append(proto, attr(3, ports)...)
	tuple := append(attr(0x8001, ip), attr(0x8002, proto)...)
	body := append([]byte{syscall.AF_INET, 0, 0, 0}, attr(0x8001, tuple)...)
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b, uint32(16+len(body)))
	binary.LittleEndian.PutUint16(b[4:], 0x102)
	binary.LittleEndian.PutUint16(b[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_ACK)
	binary.LittleEndian.PutUint32(b[8:], seq)
	return append(b, body...)
}
func purgeClientConnections(oldIP string) (int, error) {
	b, e := os.ReadFile("/proc/net/nf_conntrack")
	if e != nil {
		return 0, e
	}
	fd, e := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, 12)
	if e != nil {
		return 0, e
	}
	defer syscall.Close(fd)
	if e = syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); e != nil {
		return 0, e
	}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Sec: 1})
	count := 0
	seq := uint32(1)
	until := time.Now().Add(3 * time.Second)
	for _, line := range strings.Split(string(b), "\n") {
		f, ok := parseFlow(line)
		if !ok || (oldIP != "" && f.replyDst != oldIP) {
			continue
		}
		if time.Now().After(until) {
			return count, errors.New("connection cleanup exceeded deadline")
		}
		if e = syscall.Sendto(fd, deleteMessage(f, seq), 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); e != nil {
			return count, e
		}
		buf := make([]byte, 4096)
		n, _, e := syscall.Recvfrom(fd, buf, 0)
		if e != nil {
			return count, e
		}
		msgs, e := syscall.ParseNetlinkMessage(buf[:n])
		if e != nil {
			return count, e
		}
		for _, m := range msgs {
			if m.Header.Seq == seq && m.Header.Type == syscall.NLMSG_ERROR && len(m.Data) >= 4 {
				code := int32(binary.LittleEndian.Uint32(m.Data))
				if code != 0 && code != -2 {
					return count, syscall.Errno(-code)
				}
			}
		}
		seq++
		count++
	}
	return count, nil
}
