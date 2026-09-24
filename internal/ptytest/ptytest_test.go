//go:build darwin || freebsd || linux

package ptytest

import (
	"errors"
	"io"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func openPTY(t *testing.T) *PTY {
	t.Helper()
	p, err := Open()
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// A line typed at the master reaches a reader of the slave once Enter ends
// it, and the terminal echoes it, turning the newline into a carriage return
// and a newline.
func TestTypedLineReachesTheSlave(t *testing.T) {
	p := openPTY(t)
	screen := p.Screen()
	if err := p.Type("hello\n"); err != nil {
		t.Fatalf("Type() error = %v", err)
	}
	buf := make([]byte, 64)
	n, err := p.Slave.Read(buf)
	if err != nil || string(buf[:n]) != "hello\n" {
		t.Fatalf("slave read %q, %v; want the line", buf[:n], err)
	}
	if err := screen.Expect("hello\r\n", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

// Ctrl-D at the start of a line is the end of input: a read of the slave
// returns zero bytes.
func TestEndOfInputAtTheStartOfALine(t *testing.T) {
	p := openPTY(t)
	p.Screen()
	if err := p.Type(EndOfInput); err != nil {
		t.Fatalf("Type() error = %v", err)
	}
	n, err := p.Slave.Read(make([]byte, 64))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("slave read %d bytes, %v; want the end of input", n, err)
	}
}

// The slave is blocking, as a terminal a process inherits normally is.
func TestTheSlaveIsBlocking(t *testing.T) {
	p := openPTY(t)
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, p.Slave.Fd(), syscall.F_GETFL, 0)
	if errno != 0 {
		t.Fatalf("F_GETFL: %v", errno)
	}
	if flags&syscall.O_NONBLOCK != 0 {
		t.Fatal("the slave's description has O_NONBLOCK")
	}
}

// Start gives a subprocess the slave as its controlling terminal and makes
// its group the terminal's foreground group: it finds the terminal by name,
// and Ctrl-C typed at the master reaches its trap.
func TestStartMakesTheSlaveTheControllingTerminal(t *testing.T) {
	p := openPTY(t)
	screen := p.Screen()
	cmd := exec.Command("sh", "-c", `tty; trap 'echo interrupted; exit 3' INT; echo ready; while :; do sleep 1; done`)
	if err := p.Start(cmd); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			<-exited
		}
	})

	for _, want := range []string{p.Name, "ready"} {
		if err := screen.Expect(want, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Type(Interrupt); err != nil {
		t.Fatalf("Type() error = %v", err)
	}
	if err := screen.Expect("interrupted", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		waited = true
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			t.Fatalf("the subprocess ended with %v, want the trap's exit status 3", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the subprocess did not exit after its trap ran")
	}
}
