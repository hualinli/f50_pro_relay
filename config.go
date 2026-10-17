package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"f50_pro_relay/internal/fileutil"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type Profile struct {
	ID       string `json:"id"`
	SSID     string `json:"ssid"`
	Password string `json:"password"`
	Security string `json:"security"`
	Priority int    `json:"priority"`
	Hidden   bool   `json:"hidden,omitempty"`
}
type PublicProfile struct {
	ID       string `json:"id"`
	SSID     string `json:"ssid"`
	Security string `json:"security"`
	Priority int    `json:"priority"`
	Hidden   bool   `json:"hidden"`
}
type Config struct {
	Mode     string    `json:"mode"`
	Profiles []Profile `json:"profiles"`
}

func (c Config) Public() []PublicProfile {
	p := make([]PublicProfile, 0, len(c.Profiles))
	for _, v := range c.Profiles {
		p = append(p, PublicProfile{v.ID, v.SSID, v.Security, v.Priority, v.Hidden})
	}
	return p
}
func validProfile(p Profile) error {
	if !utf8.ValidString(p.SSID) || len(p.SSID) < 1 || len(p.SSID) > 32 {
		return errors.New("SSID must contain 1–32 UTF-8 bytes")
	}
	if p.Priority < 0 || p.Priority > 100 {
		return errors.New("priority must be between 0 and 100")
	}
	switch p.Security {
	case "open":
		if p.Password != "" {
			return errors.New("open network cannot have a password")
		}
	case "wpa2", "wpa3", "auto":
		if len(p.Password) < 8 || len(p.Password) > 63 || strings.ContainsRune(p.Password, 0) {
			return errors.New("password must contain 8–63 bytes without NUL")
		}
	default:
		return errors.New("security must be open, wpa2, wpa3 or auto")
	}
	return nil
}
func loadConfig(base string) (Config, error) {
	c := Config{Mode: "auto", Profiles: []Profile{}}
	b, e := os.ReadFile(filepath.Join(base, "config.json"))
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, errors.New("invalid config file")
	}
	if c.Mode != "auto" && c.Mode != "cellular" && c.Mode != "wifi" {
		return c, errors.New("invalid saved mode")
	}
	for _, p := range c.Profiles {
		if e = validProfile(p); e != nil {
			return c, fmt.Errorf("invalid saved profile %s", p.ID)
		}
	}
	return c, nil
}
func atomicJSON(path string, v any, mode os.FileMode) error {
	return fileutil.AtomicJSON(path, v, mode)
}
func atomicFile(path string, b []byte, mode os.FileMode) error {
	return fileutil.AtomicFile(path, b, mode)
}
func saveConfig(base string, c Config) error {
	return atomicJSON(filepath.Join(base, "config.json"), c, 0600)
}
func upsert(c *Config, p Profile) error {
	if e := validProfile(p); e != nil {
		return e
	}
	for i, old := range c.Profiles {
		if old.ID == p.ID && p.ID != "" || old.SSID == p.SSID {
			p.ID = old.ID
			c.Profiles[i] = p
			return nil
		}
	}
	b := make([]byte, 8)
	if _, e := rand.Read(b); e != nil {
		return e
	}
	p.ID = hex.EncodeToString(b)
	c.Profiles = append(c.Profiles, p)
	return nil
}

// wpa_supplicant uses byte escapes, not JSON's Unicode escape notation.
func wpaQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, v := range []byte(s) {
		switch {
		case v == '"' || v == '\\':
			b.WriteByte('\\')
			b.WriteByte(v)
		case v >= 32 && v < 127:
			b.WriteByte(v)
		default:
			fmt.Fprintf(&b, "\\x%02x", v)
		}
	}
	b.WriteByte('"')
	return b.String()
}
func renderWPA(c Config, runtime string) ([]byte, []string) {
	ps := append([]Profile(nil), c.Profiles...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Priority > ps[j].Priority })
	var b strings.Builder
	fmt.Fprintf(&b, "ctrl_interface=%s/ctrl\nupdate_config=0\np2p_disabled=1\ncountry=CN\nsae_pwe=2\n", runtime)
	ids := []string{}
	for _, p := range ps {
		ids = append(ids, p.ID)
		fmt.Fprintf(&b, "network={\n ssid=%x\n id_str=%s\n priority=%d\n", []byte(p.SSID), wpaQuote(p.ID), p.Priority)
		if p.Hidden {
			b.WriteString(" scan_ssid=1\n")
		}
		switch p.Security {
		case "open":
			b.WriteString(" key_mgmt=NONE\n")
		case "wpa2":
			fmt.Fprintf(&b, " key_mgmt=WPA-PSK\n psk=%s\n", wpaQuote(p.Password))
		case "wpa3":
			fmt.Fprintf(&b, " key_mgmt=SAE\n sae_password=%s\n ieee80211w=2\n", wpaQuote(p.Password))
		default:
			fmt.Fprintf(&b, " key_mgmt=WPA-PSK SAE\n psk=%s\n sae_password=%s\n ieee80211w=1\n", wpaQuote(p.Password), wpaQuote(p.Password))
		}
		b.WriteString("}\n")
	}
	return []byte(b.String()), ids
}
