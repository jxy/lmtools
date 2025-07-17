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

// buildArgoBinary builds the argo binary for testing
func buildArgoBinary(t *testing.T) string {
	t.Helper()
	
	tmpDir := t.TempDir()
	argoBin := filepath.Join(tmpDir, "argo.test")
	
	cmd := exec.Command("go", "build", "-o", argoBin, ".")
	cmd.Dir = "." // Run in cmd/argo directory
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build argo: %v\nOutput: %s", err, output)
	}
	
	return argoBin
}

// runArgoCommand runs argo with the given arguments and input
func runArgoCommand(t *testing.T, argoBin string, args []string, input string) (stdout, stderr string, err error) {
	t.Helper()
	
	cmd := exec.Command(argoBin, args...)
	cmd.Stdin = strings.NewReader(input)
	
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}