package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func childOwned(base, runtime string, c childRecord) bool {
	cmd, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", c.PID))
	if e != nil {
		return false
	}
	for _, arg := range strings.Split(string(cmd), "\x00") {
		if c.Name == "wpa" && arg == "-c"+filepath.Join(runtime, "wpa.conf") {
			return true
		}
		if c.Name == "dhcp" && arg == filepath.Join(base, "dhcp-hook.sh") {
			return true
		}
	}
	return false
}
func recoverOwned(options Options) error {
	base, runtime := options.BaseDir, options.RuntimeDir
	// Only terminate children whose live command line still identifies this installation.
	b, e := os.ReadFile(filepath.Join(base, "children.json"))
	if e == nil {
		var cs []childRecord
		if json.Unmarshal(b, &cs) != nil {
			return errors.New("invalid child ownership file")
		}
		for _, c := range cs {
			if childOwned(base, runtime, c) {
				_ = syscall.Kill(c.PID, syscall.SIGTERM)
			}
		}
		time.Sleep(1200 * time.Millisecond)
		for _, c := range cs {
			if childOwned(base, runtime, c) {
				_ = syscall.Kill(c.PID, syscall.SIGKILL)
			}
		}
	}
	b, e = os.ReadFile(filepath.Join(base, "network.json"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var n networkState
	if e = json.Unmarshal(b, &n); e != nil {
		return errors.New("invalid network ownership file")
	}
	n.Base = base
	n.tools = options.Tools
	if e = n.Cleanup(); e != nil {
		return e
	}
	_ = os.Remove(filepath.Join(base, "children.json"))
	_ = os.Remove(filepath.Join(base, "control.sock"))
	_ = os.Remove(filepath.Join(runtime, "wpa.conf"))
	return nil
}
