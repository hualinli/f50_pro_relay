package relay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecoveryDebounceAndCooldown(t *testing.T) {
	n := time.Unix(100, 0)
	p := switchPolicy{Cooldown: 30 * time.Second}
	a, _ := p.Health(n, true)
	if a {
		t.Fatal("promoted after one success")
	}
	a, _ = p.Health(n.Add(time.Second), true)
	if !a {
		t.Fatal("not promoted")
	}
	for i := 0; i < 2; i++ {
		_, f := p.Health(n, false)
		if f {
			t.Fatal("fell back before threshold")
		}
	}
	_, f := p.Health(n.Add(3*time.Second), false)
	if !f || p.Active {
		t.Fatal("did not fall back")
	}
	p.Health(n.Add(5*time.Second), true)
	a, _ = p.Health(n.Add(6*time.Second), true)
	if a {
		t.Fatal("ignored cooldown")
	}
	a, _ = p.Health(n.Add(34*time.Second), true)
	if !a {
		t.Fatal("did not recover")
	}
	if !p.LinkLost(n.Add(35*time.Second)) || p.Active {
		t.Fatal("link loss must immediately demote")
	}
}
func TestPublicProfilesNeverExposePassword(t *testing.T) {
	c := Config{Profiles: []Profile{{ID: "a", SSID: "a", Security: "wpa3", Password: "private-secret", Priority: 1}}}
	b, _ := json.Marshal(c.Public())
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "password") {
		t.Fatal(string(b))
	}
}
func TestWPAInputIsDataNotConfiguration(t *testing.T) {
	c := Config{Profiles: []Profile{{ID: "a", SSID: "x\nnetwork={}", Password: "abc\"\\\n123456", Security: "wpa3"}}}
	b, _ := renderWPA(c, "/data/vendor/wifi/ufi-relay")
	if strings.Contains(string(b), "ssid=\"x") || strings.Contains(string(b), "abc\"\\\n") {
		t.Fatal(string(b))
	}
	if !strings.Contains(string(b), `\x0a`) {
		t.Fatal("missing byte escape")
	}
	if e := validProfile(Profile{SSID: strings.Repeat("中", 11), Security: "open"}); e == nil {
		t.Fatal("accepted SSID over 32 bytes")
	}
}
