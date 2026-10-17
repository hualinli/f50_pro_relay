package relay

import "time"

type switchPolicy struct {
	Active              bool
	Successes, Failures int
	LastFallback        time.Time
	Cooldown            time.Duration
}

// Link loss/lease expiry bypass the health-check debounce.
func (p *switchPolicy) LinkLost(now time.Time) bool {
	was := p.Active
	p.Active = false
	p.Successes = 0
	p.Failures = 0
	if was {
		p.LastFallback = now
	}
	return was
}
func (p *switchPolicy) Health(now time.Time, ok bool) (activate, fallback bool) {
	if ok {
		p.Failures = 0
		p.Successes++
		if !p.Active && p.Successes >= 2 && (p.LastFallback.IsZero() || now.Sub(p.LastFallback) >= p.Cooldown) {
			p.Active = true
			return true, false
		}
	} else {
		p.Successes = 0
		p.Failures++
		if p.Active && p.Failures >= 3 {
			p.LinkLost(now)
			return false, true
		}
	}
	return false, false
}
