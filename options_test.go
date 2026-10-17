package relay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingToolDoesNotCreateInstallationOrRunCommands(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "command-ran")
	executable := filepath.Join(dir, "tool")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "private")
	tools := Tools{IW: filepath.Join(dir, "missing-iw"), WPASupplicant: executable, BusyBox: executable, IP: executable, IPTables: executable, IP6Tables: executable}
	m, err := New(Options{BaseDir: base, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "required tool iw") {
		t.Fatalf("missing dependency not reported: %v", err)
	}
	for _, path := range []string{base, marker} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preflight changed %s: %v", path, err)
		}
	}
}

func TestToolPreflightRejectsDirectoryAndNonExecutable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, file} {
		tools := Tools{IW: path}
		if err := tools.validate(); err == nil || !strings.Contains(err.Error(), "not an executable file") {
			t.Fatalf("accepted %s: %v", path, err)
		}
	}
}
