package relay

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func wpa(runtime, cmd string) (string, error) {
	tmp := filepath.Join(runtime, fmt.Sprintf("q-%d-%d", os.Getpid(), time.Now().UnixNano()))
	c, e := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: tmp, Net: "unixgram"})
	if e != nil {
		return "", e
	}
	defer func() { c.Close(); os.Remove(tmp) }()
	// The directory is private to root/wifi; wifi must be able to reply.
	if e = os.Chmod(tmp, 0666); e != nil {
		return "", e
	}
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, e = c.WriteToUnix([]byte(cmd), &net.UnixAddr{Name: filepath.Join(runtime, "ctrl", iface), Net: "unixgram"}); e != nil {
		return "", e
	}
	b := make([]byte, 65536)
	n, _, e := c.ReadFromUnix(b)
	if e != nil {
		return "", e
	}
	out := strings.TrimSpace(string(b[:n]))
	if out == "FAIL" || strings.HasPrefix(out, "UNKNOWN COMMAND") {
		return out, errors.New("supplicant rejected control request")
	}
	return out, nil
}
func fields(s string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(l, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

type AccessPoint struct {
	SSID      string `json:"ssid"`
	BSSID     string `json:"bssid"`
	Frequency int    `json:"frequency"`
	Signal    int    `json:"signal"`
	Flags     string `json:"flags"`
}

func decodeSSID(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if p, e := hex.DecodeString(s[i+2 : i+4]); e == nil {
				b.Write(p)
				i += 3
				continue
			}
		}
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
func parseScan(s string) []AccessPoint {
	out := []AccessPoint{}
	for _, l := range strings.Split(s, "\n") {
		f := strings.SplitN(l, "\t", 5)
		if len(f) != 5 {
			continue
		}
		freq, e := strconv.Atoi(f[1])
		if e != nil {
			continue
		}
		sig, _ := strconv.Atoi(f[2])
		out = append(out, AccessPoint{SSID: decodeSSID(f[4]), BSSID: f[0], Frequency: freq, Signal: sig, Flags: f[3]})
	}
	return out
}
