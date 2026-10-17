package relay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// udhcpc requires an executable hook. This tiny script atomically writes only
// lease data to a private file; it does not execute this process's CLI or open RPC.
func (d *engine) writeDHCPHook() (string, error) {
	hook := filepath.Join(d.base, "dhcp-hook.sh")
	// A token prevents a delayed hook from a retired client being accepted by a new one.
	token := fmt.Sprintf("%d-%d-%d", os.Getpid(), d.generation, time.Now().UnixNano())
	d.leaseStamp = token
	d.leaseEvent = ""
	_ = os.Remove(filepath.Join(d.base, "dhcp-event"))
	script := "#!/system/bin/sh\nset -eu\numask 077\nbase=" + shellQuote(d.base) + "\nbb=" + shellQuote(d.options.Tools.BusyBox) + "\ntmp=\"$base/.dhcp-event-$$\"\ntrap 'rm -f \"$tmp\"' EXIT\nprintf '%s\\n' " + shellQuote(token) + " \"$(\"$bb\" date +%s)-$$\" \"$1\" \"${ip:-}\" \"${subnet:-}\" \"${router:-}\" \"${dns:-}\" \"${lease:-}\" >\"$tmp\"\n\"$bb\" mv -f \"$tmp\" \"$base/dhcp-event\"\n"
	return hook, atomicFile(hook, []byte(script), 0700)
}
func parseDHCPEvent(raw, token string, generation int) (request, error) {
	fields := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	if len(fields) != 8 || fields[0] != token || fields[1] == "" {
		return request{}, errors.New("invalid or retired DHCP event")
	}
	fields = append(fields[:1], fields[2:]...)
	event := fields[1]
	r := request{action: "lease", event: event, generation: generation}
	if event == "deconfig" || event == "nak" {
		return r, nil
	}
	if event != "bound" && event != "renew" {
		return request{}, errors.New("unknown DHCP event")
	}
	seconds, e := strconv.Atoi(fields[6])
	if e != nil {
		return request{}, errors.New("invalid DHCP duration")
	}
	routers := strings.Fields(fields[4])
	if len(routers) == 0 {
		return request{}, errors.New("missing DHCP router")
	}
	l := Lease{IP: fields[2], Subnet: fields[3], Gateway: routers[0], DNS: strings.Fields(fields[5]), Seconds: seconds, Generation: generation}
	if e = validateLease(l); e != nil {
		return request{}, e
	}
	r.lease = &l
	return r, nil
}
func (d *engine) readDHCPEvent() {
	b, e := os.ReadFile(filepath.Join(d.base, "dhcp-event"))
	if e != nil || len(b) > 4096 || string(b) == d.leaseEvent {
		return
	}
	d.leaseEvent = string(b)
	r, e := parseDHCPEvent(string(b), d.leaseStamp, d.generation)
	if e != nil {
		d.event("ignored invalid or retired DHCP event")
		return
	}
	if _, e = d.handle(r); e != nil {
		d.leaseEvent = "" // Retry transient route failures, e.g. br0 not yet ready during boot.
		d.event("DHCP event could not be applied")
	}
}
