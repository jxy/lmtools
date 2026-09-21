package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionFlagPrintsBuildInfo builds the proxy and runs -version: one line
// on stdout naming the binary and the toolchain, nothing on stderr, exit
// status zero, and no provider or credentials asked for.
func TestVersionFlagPrintsBuildInfo(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "apiproxy.test")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build apiproxy: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "-version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("apiproxy -version: %v\nstderr: %s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	line := strings.TrimSuffix(stdout.String(), "\n")
	if strings.Contains(line, "\n") || !strings.HasPrefix(line, "apiproxy version ") || !strings.Contains(line, " built with go") {
		t.Fatalf("stdout = %q, want one line starting with %q and naming the toolchain", stdout.String(), "apiproxy version ")
	}
}
