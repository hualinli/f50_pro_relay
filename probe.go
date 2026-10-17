package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type Health struct {
	OK     bool      `json:"ok"`
	Portal bool      `json:"portal"`
	URL    string    `json:"portal_url,omitempty"`
	At     time.Time `json:"at"`
	epoch  int       `json:"-"`
}

func roots() *x509.CertPool {
	p, _ := x509.SystemCertPool()
	if p == nil {
		p = x509.NewCertPool()
	}
	for _, dir := range []string{"/apex/com.android.conscrypt/cacerts", "/system/etc/security/cacerts"} {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			if !e.IsDir() {
				b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
				p.AppendCertsFromPEM(b)
			}
		}
	}
	return p
}
func boundDialer(dns []string) *net.Dialer {
	control := func(network, address string, c syscall.RawConn) error {
		var e error
		if err := c.Control(func(fd uintptr) { e = bindInterface(fd) }); err != nil {
			return err
		}
		return e
	}
	dd := &net.Dialer{Timeout: 2 * time.Second, Control: control}
	var idx atomic.Uint32
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		i := int(idx.Add(1)-1) % len(dns)
		return dd.DialContext(ctx, "udp4", net.JoinHostPort(dns[i], "53"))
	}}
	return &net.Dialer{Timeout: 3 * time.Second, Control: control, Resolver: r}
}
func probe(l Lease, epoch int, certs *x509.CertPool) Health {
	result := Health{At: time.Now(), epoch: epoch}
	if len(l.DNS) == 0 {
		return result
	}
	dial := boundDialer(l.DNS)
	tr := &http.Transport{DialContext: func(c context.Context, n, a string) (net.Conn, error) { return dial.DialContext(c, "tcp4", a) }, TLSClientConfig: &tls.Config{RootCAs: certs, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	type target struct {
		url  string
		code int
	}
	targets := []target{{"http://connectivitycheck.gstatic.com/generate_204", 204}, {"https://www.baidu.com/", 200}}
	ch := make(chan Health, len(targets))
	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	for _, t := range targets {
		go func(t target) {
			h := Health{}
			req, e := http.NewRequestWithContext(ctx, "GET", t.url, nil)
			if e != nil {
				ch <- h
				return
			}
			req.Header.Set("User-Agent", "UFI-Relay/1.0")
			resp, e := client.Do(req)
			if e != nil {
				ch <- h
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			h.OK = resp.StatusCode == t.code
			if strings.HasPrefix(t.url, "http:") && !h.OK {
				if loc, e := resp.Location(); e == nil && (loc.Scheme == "http" || loc.Scheme == "https") {
					loc.User = nil
					h.Portal = true
					h.URL = loc.String()
				} else if resp.StatusCode == 200 {
					h.Portal = true
					h.URL = t.url
				}
			}
			ch <- h
		}(t)
	}
	for range targets {
		h := <-ch
		if h.OK {
			result.OK = true
			cancel()
		}
		if h.Portal {
			result.Portal = true
			result.URL = h.URL
		}
	}
	if result.OK {
		result.Portal = false
		result.URL = ""
	}
	return result
}
