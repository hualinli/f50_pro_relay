package relay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Assert which real command arguments recovery selects, including old records
// that predate the stored interface field, without touching host networking.
func TestRecoveryUsesRecordedInterface(t *testing.T) {
	for _, record := range []string{`{"Owned":true}`, `{"Owned":true,"interface":"ufi_sta0"}`, `{"Owned":true,"interface":"relay_sta0"}`} {
		t.Run(record, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			tool := filepath.Join(dir, "tool")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \"$*\" in\n'rule show') echo '10950: from all iif lo lookup 30555';;\nesac\nexit 0\n"
			if e := os.WriteFile(tool, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			var n networkState
			if e := json.Unmarshal([]byte(record), &n); e != nil {
				t.Fatal(e)
			}
			n.Base = dir
			n.tools = Tools{IP: tool, IPTables: tool, IP6Tables: tool, IW: tool}
			want := n.Interface
			if want == "" {
				want = legacyIface
			}
			if e := n.Cleanup(); e != nil {
				t.Fatal(e)
			}
			b, _ := os.ReadFile(log)
			calls := string(b)
			if !strings.Contains(calls, "-o "+want+" -j UFI_RELAY_NAT") || !strings.Contains(calls, "oif "+want+" lookup 30555") {
				t.Fatal("cleanup used wrong interface:", calls)
			}
			if n.Owned {
				t.Fatal("ownership not released")
			}
		})
	}
	n := networkState{Owned: true, Interface: "wlan0"}
	if n.Cleanup() == nil {
		t.Fatal("must not clean up an AP interface")
	}
}
