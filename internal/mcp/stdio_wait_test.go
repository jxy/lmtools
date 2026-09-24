package mcp

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// waitUnreaped reports how a child exited, in the words exec's Wait uses,
// and leaves the child unreaped: signal 0 still finds it, and Wait then
// reaps it and reads the same status. A child that stopped has not exited.
func TestWaitUnreapedReportsTheExitAndLeavesTheChild(t *testing.T) {
	if !waitsUnreaped {
		t.Skip("this platform has no wait that leaves the child unreaped")
	}
	for _, tc := range []struct {
		name   string
		script string
		stop   bool
		kill   bool
		want   string
	}{
		{name: "clean exit", script: "exit 0"},
		{name: "exit status", script: "exit 3", want: "exit status 3"},
		{name: "killed", script: "exec sleep 60", kill: true, want: "signal: killed"},
		{name: "stopped and then killed", script: "exec sleep 60", stop: true, kill: true, want: "signal: killed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", tc.script)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			pid := cmd.Process.Pid
			type result struct {
				status syscall.WaitStatus
				err    error
			}
			waited := make(chan result, 1)
			if tc.stop {
				if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
					t.Fatalf("stop the child: %v", err)
				}
			}
			go func() {
				status, err := waitUnreaped(pid)
				waited <- result{status, err}
			}()
			if tc.stop {
				select {
				case r := <-waited:
					t.Fatalf("waitUnreaped() returned %v, %v for a child that only stopped", r.status, r.err)
				case <-time.After(300 * time.Millisecond):
				}
			}
			if tc.kill {
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatalf("kill the child: %v", err)
				}
			}
			var r result
			select {
			case r = <-waited:
			case <-time.After(10 * time.Second):
				t.Fatal("waitUnreaped() did not return after the child exited")
			}
			if r.err != nil {
				t.Fatalf("waitUnreaped() error = %v", r.err)
			}
			got := ""
			if err := exitStatusError(r.status); err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Fatalf("waitUnreaped() status = %q, want %q", got, tc.want)
			}
			if err := syscall.Kill(pid, 0); err != nil {
				t.Fatalf("after waitUnreaped: signal 0 returned %v, want the child unreaped", err)
			}

			reaped := ""
			if err := cmd.Wait(); err != nil {
				reaped = err.Error()
			}
			if reaped != tc.want {
				t.Fatalf("Wait() = %q, want the status waitUnreaped read, %q", reaped, tc.want)
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("after Wait: signal 0 returned %v, want the child reaped", err)
			}
		})
	}
}
