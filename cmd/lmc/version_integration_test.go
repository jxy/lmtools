//go:build integration

package main

import (
	"strings"
	"testing"
)

// TestVersionFlagPrintsBuildInfo: -version prints one line on stdout naming
// the binary and the toolchain, sends nothing to stderr, exits zero, and asks
// for no provider, credentials, or input.
func TestVersionFlagPrintsBuildInfo(t *testing.T) {
	lmcBin := getLmcBinary(t)
	stdout, stderr, err := runLmcCommand(t, lmcBin, []string{"-version"}, "")
	if err != nil {
		t.Fatalf("lmc -version: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	line := strings.TrimSuffix(stdout, "\n")
	if strings.Contains(line, "\n") || !strings.HasPrefix(line, "lmc version ") || !strings.Contains(line, " built with go") {
		t.Fatalf("stdout = %q, want one line starting with %q and naming the toolchain", stdout, "lmc version ")
	}
}
