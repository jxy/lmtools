//go:build darwin || freebsd || linux

package main

import (
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/ptytest"
	"lmtools/internal/session"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// -repl needs an open of its own of the terminal on standard input. When the
// terminal's name opens another device, lmc -repl stops at startup, before
// it connects anything, with an error naming the terminal and the reason.
func TestREPLRefusesATerminalItCannotReopen(t *testing.T) {
	pty, err := ptytest.Open()
	if err != nil {
		t.Fatalf("ptytest.Open() error = %v", err)
	}
	defer pty.Close()
	other, err := ptytest.Open()
	if err != nil {
		t.Fatalf("ptytest.Open() error = %v", err)
	}
	defer other.Close()
	defer func(reopen func(string) (int, error)) { reopenTerminal = reopen }(reopenTerminal)
	reopenTerminal = func(string) (int, error) {
		return syscall.Open(other.Name, syscall.O_RDONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	}
	defer func(stdin *os.File, args []string, sessionsDir string) {
		os.Stdin, os.Args = stdin, args
		session.SetSessionsDir(sessionsDir)
		logger.ResetForTesting()
	}(os.Stdin, os.Args, session.GetSessionsDir())
	os.Stdin = pty.Slave
	os.Args = []string{
		"lmc", "-repl",
		"-provider", "openai", "-provider-url", "http://127.0.0.1:9/v1", "-model", "gpt-test",
		"-log-dir", t.TempDir(), "-sessions-dir", t.TempDir(),
	}

	result := make(chan error, 1)
	go func() { result <- run(core.NewTestNotifier()) }()
	select {
	case err = <-result:
	case <-time.After(20 * time.Second):
		// Closing the terminal ends the loop that did not stop.
		t.Fatal("run() did not stop at startup")
	}
	if err == nil {
		t.Fatal("run() error = nil, want -repl refused")
	}
	for _, want := range []string{"-repl needs its own open of the terminal", pty.Name, "another device"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("run() error = %v, want it to say %q", err, want)
		}
	}
}
