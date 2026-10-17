package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWirelessLinkParsing(t *testing.T) {
	// Use the same iw layout as the device, including different rates per direction.
	s := "Connected to aa:bb:cc:dd:ee:ff (on ufi_sta0)\n\tSSID: private-network\n\tfreq: 5220\n\tsignal: -31 dBm\n\trx bitrate: 1200.9 MBit/s 80MHz HE-MCS 11 HE-NSS 2 HE-GI 0 HE-DCM 0\n\ttx bitrate: 1080.6 MBit/s 80MHz HE-MCS 10 HE-NSS 2 HE-GI 0 HE-DCM 0\n"
	l := parseWirelessLink(s)
	if !l.Connected || *l.Signal != -31 || *l.Frequency != 5220 || *l.Channel != 44 || *l.Width != 80 || l.Standard != "WiFi 6" || *l.RXMbps != 1200.9 || *l.TXMbps != 1080.6 {
		t.Fatalf("incorrect link: %+v", l)
	}
	for _, value := range []string{"Not connected.\n", "", "unexpected output"} {
		l = parseWirelessLink(value)
		if l.Connected || l.Signal != nil || l.RXMbps != nil {
			t.Fatal("absent fields must not become live or zero-valued data")
		}
	}
	l = parseWirelessLink(s)
	if l.RX.MCS == nil || *l.RX.MCS != 11 || l.TX.MCS == nil || *l.TX.MCS != 10 || l.RX.NSS == nil || *l.RX.NSS != 2 || l.RX.GI == nil || *l.RX.GI != 0 || l.RX.DCM == nil || *l.RX.DCM != 0 {
		t.Fatal("HE parameters missing, direction mixed, or zero treated as absent")
	}
	l = parseWirelessLink("Connected to aa\nfreq: 2484\nsignal: -81 dBm\nrx bitrate: 54.0 MBit/s\n")
	if *l.Channel != 14 || l.Width != nil || l.Standard != "" || l.TXMbps != nil {
		t.Fatal("legacy or missing fields incorrectly inferred")
	}
}

func TestMonitorTimeoutAndReadFailure(t *testing.T) {
	o := Options{Tools: Tools{IW: filepath.Join(t.TempDir(), "missing-iw")}}
	if sample, err := ReadObservation(context.Background(), o); !errors.Is(err, ErrLinkUnavailable) || sample.Link.Connected || sample.Counters != nil {
		t.Fatal("read failure reported as valid sample")
	}
	// Cancellation must terminate the command when the browser goes away.
	tool := filepath.Join(t.TempDir(), "iw")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	o.Tools.IW = tool
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ReadObservation(ctx, o); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("canceled probe accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("probe ignored request cancellation")
	}
}

func TestObserveLifecycleAndReadOnlyDiagnostic(t *testing.T) {
	base := filepath.Join(t.TempDir(), "not-created")
	options := Options{BaseDir: base, RuntimeDir: filepath.Join(t.TempDir(), "wifi"), Tools: Tools{IW: filepath.Join(t.TempDir(), "missing-iw")}}
	m, e := New(options)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Observe(context.Background()); !errors.Is(e, ErrNotRunning) {
		t.Fatal("observation before Run must fail promptly")
	}
	if _, e = ReadObservation(context.Background(), options); !errors.Is(e, ErrLinkUnavailable) {
		t.Fatal(e)
	}
	if _, e = os.Stat(base); !os.IsNotExist(e) {
		t.Fatal("read-only diagnostic created installation files")
	}
	m.state.Store(3)
	if _, e = m.Observe(context.Background()); !errors.Is(e, ErrNotRunning) {
		t.Fatal("observation after stop must fail")
	}
}
