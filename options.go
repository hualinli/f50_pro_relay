package relay

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
)

// Version identifies the standalone relay module.
const Version = "0.4.0"

// Options configures an Android relay instance. New does not start processes or change networking.
// Instances on one device share the reserved ufi_sta0 interface and table 30555;
// callers must use one Manager for that device.
type Options struct {
	BaseDir    string
	RuntimeDir string
	Tools      Tools
	Logger     *slog.Logger
}

// Tools contains absolute executable paths. Empty fields use the F50 Pro defaults.
type Tools struct {
	IW            string
	WPASupplicant string
	BusyBox       string
	IP            string
	IPTables      string
	IP6Tables     string
}

func (o Options) normalized() (Options, error) {
	if o.BaseDir == "" {
		o.BaseDir = "/data/adb/ufi-relay"
	}
	if o.RuntimeDir == "" {
		o.RuntimeDir = "/data/vendor/wifi/ufi-relay"
	}
	if !filepath.IsAbs(o.BaseDir) || !filepath.IsAbs(o.RuntimeDir) {
		return o, errors.New("relay directories must be absolute")
	}
	o.BaseDir = filepath.Clean(o.BaseDir)
	o.RuntimeDir = filepath.Clean(o.RuntimeDir)
	if o.BaseDir == "/" || o.RuntimeDir == "/" || o.BaseDir == o.RuntimeDir || strings.HasPrefix(o.RuntimeDir, o.BaseDir+"/") || strings.HasPrefix(o.BaseDir, o.RuntimeDir+"/") {
		return o, errors.New("relay requires distinct private directories")
	}
	defaults := Tools{IW: filepath.Join(o.BaseDir, "iw"), WPASupplicant: "/vendor/bin/hw/wpa_supplicant", BusyBox: "/data/adb/magisk/busybox", IP: "/system/bin/ip", IPTables: "/system/bin/iptables", IP6Tables: "/system/bin/ip6tables"}
	pairs := [][2]*string{{&o.Tools.IW, &defaults.IW}, {&o.Tools.WPASupplicant, &defaults.WPASupplicant}, {&o.Tools.BusyBox, &defaults.BusyBox}, {&o.Tools.IP, &defaults.IP}, {&o.Tools.IPTables, &defaults.IPTables}, {&o.Tools.IP6Tables, &defaults.IP6Tables}}
	for _, pair := range pairs {
		if *pair[0] == "" {
			*pair[0] = *pair[1]
		}
		if !filepath.IsAbs(*pair[0]) {
			return o, errors.New("tool paths must be absolute")
		}
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return o, nil
}
func (t Tools) resolve(name string) string {
	switch name {
	case "ip":
		return t.IP
	case "iptables":
		return t.IPTables
	case "ip6tables":
		return t.IP6Tables
	default:
		return "/system/bin/" + name
	}
}
