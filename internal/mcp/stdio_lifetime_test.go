package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/mcp"
	"lmtools/internal/mcp/mcptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// relayEnv, when set, makes the test binary play lmc for the process group
// test. Its value is the scenario of the stdio server the relay starts.
const relayEnv = "LMC_MCP_TEST_RELAY"

func runRelayFromEnv() {
	scenario := os.Getenv(relayEnv)
	if scenario == "" {
		return
	}
	os.Exit(relay(scenario))
}

// relay traps the terminal interrupt, as the loop of lmc does, starts a
// stdio server playing scenario, and says so. Then it answers each line it
// reads with the result of an echo call, and shuts the server down when its
// input ends.
func relay(scenario string) int {
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	executable, err := os.Executable()
	if err != nil {
		fmt.Println("error", err)
		return 1
	}
	cfg := mcp.ServerConfig{
		Name:    "relayed",
		Type:    mcp.TransportStdio,
		Command: executable,
		Env:     map[string]string{mcptest.EnvScenario: scenario},
	}
	client, err := mcp.Dial(context.Background(), cfg, mcp.DialOptions{})
	if err != nil {
		fmt.Println("error", err)
		return 1
	}
	defer func() { _ = client.Close() }()
	fmt.Println("ready")
	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, err := client.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`), nil)
		cancel()
		if err != nil {
			fmt.Println("error", err)
			continue
		}
		var texts []string
		for _, item := range result.Content {
			texts = append(texts, item.Text)
		}
		fmt.Println("result", strings.Join(texts, " "))
	}
	return 0
}

// expectLine reads one line and checks it, within a bound.
func expectLine(t *testing.T, lines *bufio.Reader, want string) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		line, _ := lines.ReadString('\n')
		got <- strings.TrimSpace(line)
	}()
	select {
	case line := <-got:
		if line != want {
			t.Fatalf("relay said %q, want %q", line, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("relay said nothing, want %q", want)
	}
}

// readPID waits for a fake server to write a process ID to path.
func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no process ID appeared in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// groupGone reports whether no process remains in the process group pgid.
func groupGone(pgid int) error {
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal 0 to the group returned %v, want ESRCH", err)
	}
	return nil
}

// A terminal interrupt goes to the whole foreground process group. The test
// plays that group with a relay, a copy of this binary in a group of its
// own, which traps the interrupt as lmc does and starts a stdio server. The
// test signals the relay's group, never its own. The server runs in a third
// group, so the interrupt leaves it running, and a call relayed after the
// interrupt is answered.
func TestStdioServerSurvivesAnInterruptToItsClientsGroup(t *testing.T) {
	scenario, err := mcptest.ScenarioEnv(mcptest.Scenario{Era: mcptest.EraModern, Tools: []mcp.Tool{echoTool()}})
	if err != nil {
		t.Fatalf("ScenarioEnv() error = %v", err)
	}
	relay := exec.Command(os.Args[0], "-test.run=^$")
	relay.Env = append(os.Environ(), relayEnv+"="+scenario)
	relay.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := relay.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe() error = %v", err)
	}
	stdout, err := relay.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	if err := relay.Start(); err != nil {
		t.Fatalf("start relay: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		exited := make(chan struct{})
		go func() {
			_ = relay.Wait()
			close(exited)
		}()
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = syscall.Kill(-relay.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	})
	lines := bufio.NewReader(stdout)
	expectLine(t, lines, "ready")

	if err := syscall.Kill(-relay.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatalf("interrupt the relay's group: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := fmt.Fprintln(stdin, "call"); err != nil {
		t.Fatalf("ask the relay for a call: %v", err)
	}
	expectLine(t, lines, "result ok")
}

// stalledServer dials a server that stops reading its stdin once it has
// answered the discovery probe, and returns the client, the server's
// process ID, and a channel that receives a value when a write takes the
// write slot, drained of the writes dialing made.
func stalledServer(t *testing.T) (*mcp.Client, int, <-chan struct{}) {
	t.Helper()
	writes, stop := mcp.SignalWritesForTest()
	t.Cleanup(stop)
	pidPath := filepath.Join(t.TempDir(), "server.pid")
	client, _ := dialStdio(t, "stalled", mcptest.Scenario{
		Era:        mcptest.EraModern,
		Tools:      []mcp.Tool{echoTool()},
		StallAfter: "server/discover",
		PIDPath:    pidPath,
	}, mcp.DialOptions{})
	for len(writes) > 0 {
		<-writes
	}
	return client, readPID(t, pidPath), writes
}

// pastAnyPipe is call arguments larger than a pipe's buffer, so writing them
// to a server that has stopped reading blocks partway.
var pastAnyPipe = json.RawMessage(`{"text":"` + strings.Repeat("x", 4<<20) + `"}`)

// A server that stays alive and stops reading cannot hold a call past its
// cancellation. The request, larger than any pipe holds, is cut off when the
// call is cancelled during its write, and the call returns its cancellation
// within the bound. The truncated line leaves the stream unusable, so the
// transport shuts the server down on its own, and Close then finds the
// process group gone.
func TestStdioCancellationReturnsWhileTheServerStopsReading(t *testing.T) {
	client, pid, writes := stalledServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := client.CallTool(ctx, "echo", pastAnyPipe, nil)
		returned <- err
	}()
	select {
	case <-writes:
	case <-time.After(10 * time.Second):
		t.Fatal("the call never took the write slot")
	}
	cancelled := time.Now()
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CallTool() error = %v, want its cancellation", err)
		}
		if waited := time.Since(cancelled); waited > 2*time.Second {
			t.Fatalf("CallTool() returned %v after its cancellation", waited)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("CallTool() did not return after its cancellation")
	}

	deadline := time.Now().Add(10 * time.Second)
	for groupGone(pid) != nil {
		if time.Now().After(deadline) {
			t.Fatalf("the transport did not shut the server down after the cut off write: %v", groupGone(pid))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// A call whose context is done by the time its write takes the write slot
// sends nothing: it returns its cancellation, the server never sees the
// request or a cancellation notice, and the next call is answered. The
// context is done before the call, when taking the slot and the context's
// end are both ready to be chosen, or ends just as the write takes the slot.
// Each is tried twenty times, since a write that went ahead would get
// through most of them.
func TestStdioCallSendsNothingOnceItsContextIsDone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		atSlot bool
	}{
		{name: "done before the call"},
		{name: "done as the write takes the slot", atSlot: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var cancelAtSlot context.CancelFunc
			defer mcp.OnWriteSlotForTest(func() {
				mu.Lock()
				defer mu.Unlock()
				if cancelAtSlot != nil {
					cancelAtSlot()
				}
			})()
			client, record := dialStdio(t, "cancelled", mcptest.Scenario{
				Era:   mcptest.EraModern,
				Tools: []mcp.Tool{echoTool()},
			}, mcp.DialOptions{})

			for i := 0; i < 20; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				if tc.atSlot {
					mu.Lock()
					cancelAtSlot = cancel
					mu.Unlock()
				} else {
					cancel()
				}
				_, err := client.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`), nil)
				mu.Lock()
				cancelAtSlot = nil
				mu.Unlock()
				cancel()
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("call %d: CallTool() error = %v, want its cancellation", i+1, err)
				}
			}
			if got := callText(t, client, "echo", `{"text":"hi"}`); got != "ok" {
				t.Fatalf("call after the cancelled ones = %q, want ok", got)
			}
			calls := 0
			for _, method := range methods(readRecord(t, record)) {
				switch method {
				case "tools/call":
					calls++
				case "notifications/cancelled":
					t.Fatal("the server received a cancellation notice for a call that sent nothing")
				}
			}
			if calls != 1 {
				t.Fatalf("the server received %d calls, want only the one after the cancelled ones", calls)
			}
		})
	}
}

// A launcher such as npx can exit when its stdin closes and leave the
// process that serves running. Close waits for the server's process group
// to empty, signalling it, so that process is gone when Close returns.
func TestStdioCloseEndsWhatTheServerLeftRunning(t *testing.T) {
	descendantPath := filepath.Join(t.TempDir(), "descendant.pid")
	client, _ := dialStdio(t, "launcher", mcptest.Scenario{
		Era:             mcptest.EraModern,
		Tools:           []mcp.Tool{echoTool()},
		SpawnDescendant: descendantPath,
	}, mcp.DialOptions{})
	descendant := readPID(t, descendantPath)

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := syscall.Kill(descendant, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the launcher's descendant %d after Close: signal 0 returned %v, want ESRCH", descendant, err)
	}
}

// A launcher that exits when its stdin closes can leave a process behind
// that ignores SIGTERM. The launcher stays unreaped through the grace after
// SIGTERM, which keeps its group's ID reserved, so SIGKILL still reaches
// the group, and the process is gone when Close returns.
func TestStdioCloseKillsWhatTheServerLeftThatIgnoresSIGTERM(t *testing.T) {
	descendantPath := filepath.Join(t.TempDir(), "descendant.pid")
	warn := &testLog{}
	client, _ := dialStdio(t, "launcher", mcptest.Scenario{
		Era:             mcptest.EraModern,
		Tools:           []mcp.Tool{echoTool()},
		IgnoreSIGTERM:   true,
		SpawnDescendant: descendantPath,
	}, mcp.DialOptions{Warn: warn.logf})
	descendant := readPID(t, descendantPath)
	if err := syscall.Kill(descendant, syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to the descendant: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(descendant, 0); err != nil {
		t.Fatalf("the descendant did not outlive SIGTERM: signal 0 returned %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := syscall.Kill(descendant, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the descendant %d after Close: signal 0 returned %v, want ESRCH", descendant, err)
	}
	if warn.contains("could not confirm") {
		t.Fatalf("Close reported cleanup it could not confirm: %q", warn.all())
	}
}

// A server that ignores the end of its stdin and SIGTERM is ended by
// SIGKILL to its group, within the shutdown deadline. An exit the client
// caused is not reported as the server's.
func TestStdioCloseKillsAStubbornServersGroup(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "server.pid")
	warn := &testLog{}
	client, _ := dialStdio(t, "stubborn", mcptest.Scenario{
		Era:              mcptest.EraModern,
		Tools:            []mcp.Tool{echoTool()},
		IgnoreStdinClose: true,
		IgnoreSIGTERM:    true,
		PIDPath:          pidPath,
	}, mcp.DialOptions{Warn: warn.logf})
	pid := readPID(t, pidPath)

	started := time.Now()
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 7*time.Second {
		t.Fatalf("Close() took %v, past the shutdown deadline", elapsed)
	}
	if err := groupGone(pid); err != nil {
		t.Fatalf("after Close: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if warn.contains("server exited") {
		t.Fatalf("the shutdown was reported as an exit: %q", warn.all())
	}
}

// When Close cannot confirm the shutdown, because the server is not reaped
// in time, or once it is, because the check of the group finds a member
// this process may not signal or fails some other way, Close still returns
// at its deadline and reports what it could not confirm.
func TestStdioCloseReportsCleanupItCannotConfirm(t *testing.T) {
	for _, tc := range []struct {
		name string
		// check is what signal 0 to the group returns, nil for the real one.
		check  error
		holdOn bool
		want   string
	}{
		{name: "server not reaped", holdOn: true, want: "not been reaped"},
		{name: "member it may not signal", check: syscall.EPERM, want: syscall.EPERM.Error()},
		{name: "another error", check: syscall.EINVAL, want: syscall.EINVAL.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer mcp.SetShutdownTimingForTest(50*time.Millisecond, 300*time.Millisecond)()
			if tc.holdOn {
				release := mcp.HoldReapingForTest()
				defer release()
			}
			defer mcp.SetSignalGroupForTest(func(pgid int, sig syscall.Signal) error {
				if sig == 0 && tc.check != nil {
					return tc.check
				}
				return syscall.Kill(-pgid, sig)
			})()
			warn := &testLog{}
			client, _ := dialStdio(t, "unconfirmed", mcptest.Scenario{
				Era:   mcptest.EraModern,
				Tools: []mcp.Tool{echoTool()},
			}, mcp.DialOptions{Warn: warn.logf})

			started := time.Now()
			if err := client.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("Close() took %v, past its 300ms deadline", elapsed)
			}
			for _, want := range []string{"could not confirm", tc.want} {
				if !warn.contains(want) {
					t.Fatalf("warnings %q do not say %q", warn.all(), want)
				}
			}
		})
	}
}

// A server that exits on its own stays unreaped until Close has sent its
// last signal, since an unreaped process keeps its process ID, and the ID of
// the group it leads, from passing to another process. The test plays such
// a reuse: once the server is reaped, the check of the group answers as if
// another group had taken the ID over. While the server is unreaped, Close
// sends the group SIGTERM and, after the grace, SIGKILL, for what the
// server may have left there; it sends nothing after the reap, and reports
// the group it cannot confirm gone.
func TestStdioCloseSignalsTheGroupOnlyWhileTheServerIsUnreaped(t *testing.T) {
	defer mcp.SetShutdownTimingForTest(50*time.Millisecond, 500*time.Millisecond)()
	var mu sync.Mutex
	var sent, late []syscall.Signal
	defer mcp.SetSignalGroupForTest(func(pgid int, sig syscall.Signal) error {
		// The server leads the group, so the group's ID is its process ID,
		// which signal 0 finds until the server is reaped.
		reaped := errors.Is(syscall.Kill(pgid, 0), syscall.ESRCH)
		if sig == 0 {
			if reaped {
				return nil
			}
			return syscall.Kill(-pgid, 0)
		}
		mu.Lock()
		defer mu.Unlock()
		if reaped {
			late = append(late, sig)
			return nil
		}
		sent = append(sent, sig)
		return syscall.Kill(-pgid, sig)
	})()
	pidPath := filepath.Join(t.TempDir(), "server.pid")
	warn := &testLog{}
	client, _ := dialStdio(t, "exits", mcptest.Scenario{
		Era:     mcptest.EraModern,
		Tools:   []mcp.Tool{echoTool()},
		PIDPath: pidPath,
	}, mcp.DialOptions{Warn: warn.logf})
	pid := readPID(t, pidPath)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the server: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !warn.contains("server exited") {
		if time.Now().After(deadline) {
			t.Fatalf("no exit reported; warnings %q", warn.all())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the server after its exit was reported: signal 0 returned %v, want it unreaped", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(late) > 0 {
		t.Fatalf("Close sent %v to the group after the server was reaped", late)
	}
	if len(sent) != 2 || sent[0] != syscall.SIGTERM || sent[1] != syscall.SIGKILL {
		t.Fatalf("Close sent %v to the group before the reap, want SIGTERM and SIGKILL", sent)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the server after Close: signal 0 returned %v, want it reaped", err)
	}
	if !warn.contains("could not confirm") {
		t.Fatalf("warnings %q do not report the group taken over", warn.all())
	}
}

// A cancelled call leaves the client usable: the next call on it is
// answered, over stdio and over HTTP.
func TestCallAfterACancelledCall(t *testing.T) {
	scenario := mcptest.Scenario{Era: mcptest.EraModern, Tools: []mcp.Tool{echoTool()}, CallDelayMs: 1000}
	for _, tc := range []struct {
		name string
		dial func(t *testing.T) *mcp.Client
	}{
		{name: "stdio", dial: func(t *testing.T) *mcp.Client {
			client, _ := dialStdio(t, "slow", scenario, mcp.DialOptions{})
			return client
		}},
		{name: "http", dial: func(t *testing.T) *mcp.Client {
			_, cfg := httpFake(t, scenario)
			return dialHTTP(t, cfg, mcp.DialOptions{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.dial(t)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, err := client.CallTool(ctx, "echo", json.RawMessage(`{}`), nil)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancelled CallTool() error = %v, want its deadline", err)
			}
			if got := callText(t, client, "echo", `{}`); got != "ok" {
				t.Fatalf("call after the cancelled one = %q, want ok", got)
			}
		})
	}
}

// A server that exits on its own is reported once on the diagnostic
// stream. Its tools stay advertised, and each call to one fails with the
// exit, which the executor turns into an error result for the model.
func TestHostReportsAServerExitOnceAndKeepsItsTools(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "server.pid")
	cfg, _ := stdioConfig(t, "fragile", mcptest.Scenario{
		Era:     mcptest.EraModern,
		Tools:   []mcp.Tool{echoTool()},
		PIDPath: pidPath,
	})
	warn := &testLog{}
	host, err := mcp.Connect(context.Background(), []mcp.ServerConfig{cfg}, mcp.HostOptions{Warn: warn.logf})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(host.Close)
	if err := syscall.Kill(readPID(t, pidPath), syscall.SIGKILL); err != nil {
		t.Fatalf("kill the server: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !warn.contains("server exited") {
		if time.Now().After(deadline) {
			t.Fatalf("no exit reported; warnings %q", warn.all())
		}
		time.Sleep(10 * time.Millisecond)
	}

	name := mcp.QualifiedName("fragile", "echo")
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := host.Call(ctx, name, json.RawMessage(`{"text":"hi"}`))
		cancel()
		if err == nil || !strings.Contains(err.Error(), "server exited") {
			t.Fatalf("call %d after the exit: error = %v, want the exit", i+1, err)
		}
	}
	if tools := host.Tools(); len(tools) != 1 || tools[0].Name != name {
		t.Fatalf("tools after the exit = %+v, want %s still advertised", tools, name)
	}
	reports := 0
	for _, line := range warn.all() {
		if strings.Contains(line, "server exited") {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("exit reported %d times, want once: %q", reports, warn.all())
	}
}

// Shutdown never waits for the write slot. A call without a deadline is
// writing to a server that stopped reading when Close begins: closing the
// server's stdin ends that write, the call returns its error, and Close
// finishes within the shutdown deadline rather than the write's own bound.
func TestStdioCloseEndsAWriteInProgress(t *testing.T) {
	client, pid, writes := stalledServer(t)
	returned := make(chan error, 1)
	go func() {
		_, err := client.CallTool(context.Background(), "echo", pastAnyPipe, nil)
		returned <- err
	}()
	select {
	case <-writes:
	case <-time.After(10 * time.Second):
		t.Fatal("the call never took the write slot")
	}

	started := time.Now()
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 7*time.Second {
		t.Fatalf("Close() took %v, past the shutdown deadline", elapsed)
	}
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("the call whose write Close ended returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call whose write Close ended did not return")
	}
	if err := groupGone(pid); err != nil {
		t.Fatalf("after Close: %v", err)
	}
}
