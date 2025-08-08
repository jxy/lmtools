//go:build integration || e2e
// +build integration e2e

package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildLmcBinary builds the lmc binary for testing
func buildLmcBinary(t *testing.T) string {
	t.Helper()
	
	tmpDir := t.TempDir()
	lmcBin := filepath.Join(tmpDir, "lmc.test")
	
	cmd := exec.Command("go", "build", "-o", lmcBin, ".")
	cmd.Dir = "." // Run in cmd/lmc directory
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build lmc: %v\nOutput: %s", err, output)
	}
	
	return lmcBin
}

// runLmcCommand runs lmc with the given arguments and input
func runLmcCommand(t *testing.T, lmcBin string, args []string, input string) (stdout, stderr string, err error) {
	t.Helper()
	
	cmd := exec.Command(lmcBin, args...)
	cmd.Stdin = strings.NewReader(input)
	
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}