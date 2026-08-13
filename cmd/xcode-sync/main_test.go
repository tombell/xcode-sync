package main

import (
	"testing"
	"time"
)

func TestParseOptionsDryRunAndRemoteOverrides(t *testing.T) {
	args, opts, err := parseOptions([]string{
		"pull", "source-mac", "--dry-run", "--user", "alice",
		"--source-binary=/opt/homebrew/bin/xcode-sync", "--export-timeout", "2m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "pull" || args[1] != "source-mac" {
		t.Fatalf("unexpected args: %#v", args)
	}
	if !opts.DryRun || opts.SSHUser != "alice" || opts.SourceBinary != "/opt/homebrew/bin/xcode-sync" || opts.ExportTimeout != 2*time.Minute {
		t.Fatalf("unexpected options: %#v", opts)
	}
}

func TestParseOptionsRejectsUnsafeCombinations(t *testing.T) {
	tests := [][]string{
		{"status", "source", "--dry-run"},
		{"audit", "--source-binary", "remote-xcode-sync"},
		{"pull", "source", "--user-data", "relative"},
		{"pull", "source", "--user", "a", "-u", "b"},
		{"pull", "source", "--export-timeout", "500ms"},
	}
	for _, args := range tests {
		if _, _, err := parseOptions(args); err == nil {
			t.Errorf("expected %#v to fail", args)
		}
	}
}
