package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	// maxMessageBytes bounds one JSON-RPC message on either transport. A
	// tool result carrying an image is tens of megabytes of base64; a line
	// past this is a server that has stopped speaking the protocol.
	maxMessageBytes = 64 << 20
	// maxStderrLineBytes bounds one line of a stdio server's stderr before
	// it is logged.
	maxStderrLineBytes = 64 << 10
	// requestWriteTimeout bounds writing one message to a stdio server
	// whose caller set no deadline of its own, so a server that stops
	// reading cannot hold a write forever.
	requestWriteTimeout = 30 * time.Second
	// noticeWriteTimeout bounds taking the write slot and writing a
	// cancellation notice. The notice is best effort: the caller already
	// has the cancellation to report.
	noticeWriteTimeout = 500 * time.Millisecond
	// groupPollInterval is how often shutdown checks whether the server's
	// process group has emptied.
	groupPollInterval = 20 * time.Millisecond
)

// Shutdown timing, variables so tests can shorten them. After the stdio
// shutdown closes stdin, it waits up to shutdownGrace for the server to
// exit, sends SIGTERM, waits the whole shutdownGrace, and sends SIGKILL,
// which waits for whatever is left of shutdownDeadline. The deadline bounds
// the whole sequence, the wait for the process group to empty after the
// server is reaped included.
var (
	shutdownGrace    = 2 * time.Second
	shutdownDeadline = 6 * time.Second
)

// signalGroup sends sig to the process group pgid; signal 0 checks whether
// the group has members. Tests replace it to play a group this process may
// not signal.
var signalGroup = func(pgid int, sig syscall.Signal) error {
	return syscall.Kill(-pgid, sig)
}

// beforeReapForTest runs in waitLoop before it reaps the server, so a test
// can hold the reaping past the shutdown deadline.
var beforeReapForTest func()

// afterSlotForTest runs when a write has taken the write slot, before it
// writes, so a test can act while a write is in progress.
var afterSlotForTest func()

// transportSettings are the shutdown timing and the test seams a transport
// copies when it starts, so its goroutines never read the variables a test
// changes.
type transportSettings struct {
	grace, deadline time.Duration
	signal          func(pgid int, sig syscall.Signal) error
	beforeReap      func()
	afterSlot       func()
}

// transport carries one JSON-RPC exchange at a time for a client. roundTrip
// returns the response to req, having handed interim notifications and any
// server request to the peer handler on the way.
type transport interface {
	roundTrip(ctx context.Context, req *message, opts requestOptions) (*message, error)
	notify(ctx context.Context, note *message, opts requestOptions) error
	close() error
}

// requestOptions is what the HTTP binding mirrors into headers. The stdio
// binding ignores it: there, everything rides in the message.
type requestOptions struct {
	// protocolVersion is the MCP-Protocol-Version header; empty omits it.
	protocolVersion string
	// modern adds the routing headers of the 2026-07-28 revision.
	modern bool
	// name is the Mcp-Name value for tools/call.
	name string
	// params are the Mcp-Param-* headers a tool's schema asked for.
	params []headerValue
}

// peerHandler receives what the server sends besides responses. onRequest
// returns the reply to write, for the legacy servers that still ask. onExit,
// on stdio, hears once of a server exit the client did not cause.
type peerHandler struct {
	onNotification func(*message)
	onRequest      func(*message) *message
	onExit         func(error)
}

// stdioTransport is the subprocess binding: one line per message on the
// child's stdin and stdout, stderr to the log, responses matched by id.
//
// The server runs in a process group of its own, so the terminal interrupt
// that reaches lmc and its tool commands leaves it running, and shutdown
// signals the whole group, reaching descendants a launcher such as npx
// leaves behind.
type stdioTransport struct {
	cmd *exec.Cmd
	// pgid is the server's process group, whose leader is the server.
	pgid int
	// stdin is the write end of the server's stdin. A pipe from os.Pipe
	// takes write deadlines, and closing it ends a write in progress.
	stdin   *os.File
	stdout  *os.File
	stderr  *os.File
	handler peerHandler
	log     Logf
	// warn receives what the operator should see: cleanup shutdown could
	// not confirm.
	warn Logf

	transportSettings

	// slot is the write slot, taken by one writer at a time. A channel
	// rather than a mutex, so waiting for it can end with the caller's
	// context.
	slot chan struct{}
	// stopped is closed when the transport stops taking writes: shutdown
	// began, or a write was cut off partway.
	stopped  chan struct{}
	stopOnce sync.Once

	mu       sync.Mutex
	pending  map[string]chan *message
	closed   bool
	readErr  error
	writeErr error         // a write cut off partway, which failed the transport
	done     chan struct{} // closed when the stdout reader stops
	exited   chan struct{} // closed when the server's exit is known
	waitErr  error         // how the server exited, nil for status 0
	reaped   chan struct{} // closed when the server has been reaped
	// holdsGroup is whether the server stays unreaped after it exits,
	// until signalsSent is closed; see waitLoop. Where the platform cannot
	// wait without reaping, shutdown signals the server alone.
	holdsGroup bool
	// signalsSent is closed when shutdown has sent its last signal.
	signalsSent chan struct{}
	shutdown    bool
	// shutdownDone is closed when the shutdown sequence has finished.
	shutdownDone chan struct{}
}

// startStdio launches the server without a shell, in a process group of its
// own, with the inherited environment plus the configured additions, and
// starts reading it.
func startStdio(cfg ServerConfig, handler peerHandler, log, warn Logf) (*stdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Cwd
	cmd.Env = os.Environ()
	for name, value := range cfg.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	cmd.Stdin = stdinR
	// Plain pipes rather than StdoutPipe: exec's Wait would otherwise close
	// the read side the moment the child exits, which is wrong while the
	// reader may still be draining it, and a descendant that inherited the
	// write side would hold the reader open forever. With os.Pipe the
	// transport owns both ends and closes the read side itself, after Wait.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, fmt.Errorf("start %q: %w", cfg.Command, err)
	}
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()

	t := &stdioTransport{
		transportSettings: transportSettings{
			grace:      shutdownGrace,
			deadline:   shutdownDeadline,
			signal:     signalGroup,
			beforeReap: beforeReapForTest,
			afterSlot:  afterSlotForTest,
		},
		cmd:          cmd,
		pgid:         cmd.Process.Pid,
		stdin:        stdinW,
		stdout:       stdoutR,
		stderr:       stderrR,
		handler:      handler,
		log:          log,
		warn:         warn,
		slot:         make(chan struct{}, 1),
		stopped:      make(chan struct{}),
		pending:      make(map[string]chan *message),
		done:         make(chan struct{}),
		exited:       make(chan struct{}),
		reaped:       make(chan struct{}),
		holdsGroup:   waitsUnreaped,
		signalsSent:  make(chan struct{}),
		shutdownDone: make(chan struct{}),
	}
	go t.readLoop()
	go t.stderrLoop()
	go t.waitLoop()
	return t, nil
}

// readLoop dispatches every line the server writes: responses to their
// waiters, notifications and requests to the peer handler.
func (t *stdioTransport) readLoop() {
	reader := bufio.NewReaderSize(t.stdout, 64<<10)
	var readErr error
	for {
		line, err := readLine(reader, maxMessageBytes)
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
		if len(line) == 0 {
			continue
		}
		msg, err := decodeMessage(line)
		if err != nil {
			t.log("ignoring stdout line that is not a message: %v", err)
			continue
		}
		t.dispatch(msg)
	}
	t.finish(readErr)
}

// dispatch routes one message. A response for an id nobody waits on is a
// late answer to a request that timed out or was cancelled, and is dropped.
func (t *stdioTransport) dispatch(msg *message) {
	switch msg.kind() {
	case kindResponse:
		t.mu.Lock()
		ch, ok := t.pending[idKey(msg.ID)]
		t.mu.Unlock()
		if ok {
			ch <- msg
		} else {
			t.log("dropping response for unknown request id %s", idKey(msg.ID))
		}
	case kindNotification:
		if t.handler.onNotification != nil {
			t.handler.onNotification(msg)
		}
	case kindRequest:
		if t.handler.onRequest == nil {
			return
		}
		if reply := t.handler.onRequest(msg); reply != nil {
			if err := t.write(context.Background(), reply, requestWriteTimeout); err != nil {
				t.log("reply to server request %s failed: %v", msg.Method, err)
			}
		}
	}
}

// finish records why reading stopped and wakes every waiter.
func (t *stdioTransport) finish(readErr error) {
	t.mu.Lock()
	t.closed = true
	t.readErr = readErr
	t.mu.Unlock()
	close(t.done)
}

func (t *stdioTransport) stderrLoop() {
	reader := bufio.NewReaderSize(t.stderr, 8<<10)
	for {
		line, err := readLine(reader, maxStderrLineBytes)
		if err != nil {
			if err != io.EOF {
				t.log("stderr: %v", err)
				// Drain so the child never blocks on a full pipe.
				_, _ = io.Copy(io.Discard, reader)
			}
			return
		}
		if len(line) > 0 {
			t.log("stderr: %s", line)
		}
	}
}

// waitLoop waits for the server to exit and leaves it unreaped until
// shutdown has sent its last signal. An unreaped process keeps its process
// ID, and with it the ID of the group it leads, from passing to another
// process, so no signal of shutdown can reach a group that took the ID
// over, however long the client runs after the exit. Where the platform
// cannot wait without reaping, or the wait fails, it reaps at the exit, and
// shutdown signals the server alone.
func (t *stdioTransport) waitLoop() {
	if t.holdsGroup {
		status, err := waitUnreaped(t.cmd.Process.Pid)
		if err == nil {
			t.exitedWith(exitStatusError(status))
			<-t.signalsSent
			_ = t.reap()
			return
		}
		t.log("waiting for the server without reaping it failed: %v; shutdown signals the server alone", err)
		t.mu.Lock()
		t.holdsGroup = false
		t.mu.Unlock()
	}
	t.exitedWith(t.reap())
}

// exitedWith records how the server exited and releases the pipes, so a
// reader blocked by a descendant that inherited stdout is unblocked once the
// server itself is gone. It reports an exit the client did not cause.
func (t *stdioTransport) exitedWith(err error) {
	t.mu.Lock()
	t.waitErr = err
	ownShutdown := t.shutdown
	t.mu.Unlock()
	close(t.exited)
	t.stdout.Close()
	t.stderr.Close()
	if !ownShutdown && t.handler.onExit != nil {
		t.handler.onExit(err)
	}
}

// reap waits for the server through its command, which releases the
// server's process ID, and returns how it exited.
func (t *stdioTransport) reap() error {
	if t.beforeReap != nil {
		t.beforeReap()
	}
	err := t.cmd.Wait()
	close(t.reaped)
	return err
}

// exitStatusError describes an exit status read without reaping, the way
// the error of exec's Wait does: nil for status 0.
func exitStatusError(status syscall.WaitStatus) error {
	if status.Exited() && status.ExitStatus() == 0 {
		return nil
	}
	return exitStatus(status)
}

type exitStatus syscall.WaitStatus

func (s exitStatus) Error() string {
	status := syscall.WaitStatus(s)
	switch {
	case status.Exited():
		return fmt.Sprintf("exit status %d", status.ExitStatus())
	case status.CoreDump():
		return fmt.Sprintf("signal: %v (core dumped)", status.Signal())
	case status.Signaled():
		return fmt.Sprintf("signal: %v", status.Signal())
	}
	return fmt.Sprintf("wait status %#x", uint32(status))
}

// write sends one message. It takes the write slot within ctx, sends
// nothing when ctx is done by then, and the write itself ends at the
// timeout or when ctx is done, whichever comes first. A write that had not
// started leaves the stream as it was. A write cut off partway leaves a
// truncated line on a stream framed by newlines, so it fails the transport,
// which stops taking writes and shuts the server down; the caller gets
// ctx's error when ctx ended it.
func (t *stdioTransport) write(ctx context.Context, msg *message, timeout time.Duration) error {
	data, err := encodeMessage(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	select {
	case t.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-t.stopped:
		return t.unavailable()
	}
	defer func() { <-t.slot }()
	select {
	case <-t.stopped:
		return t.unavailable()
	default:
	}
	if t.afterSlot != nil {
		t.afterSlot()
	}
	// A context already done ends the write before any byte is sent. The
	// select above picks at random among the cases that are ready, and the
	// interruption below runs on a goroutine of its own, too late for a
	// write the pipe takes whole.
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := t.stdin.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("write to server: %w", err)
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = t.stdin.SetWriteDeadline(time.Unix(1, 0))
		close(interrupted)
	})
	n, err := t.stdin.Write(data)
	if !stop() {
		// The interruption ran; let it finish before the slot is released,
		// so it cannot move the next writer's deadline.
		<-interrupted
	}
	if err == nil {
		return nil
	}
	if n > 0 {
		t.fail(fmt.Errorf("write to server cut off after %d of %d bytes: %w", n, len(data), err))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("write to server: %w", err)
}

// fail records a write cut off partway and shuts the server down without
// holding up the caller, whose call returns its own error.
func (t *stdioTransport) fail(err error) {
	t.mu.Lock()
	if t.writeErr == nil {
		t.writeErr = err
	}
	t.closed = true
	t.mu.Unlock()
	t.log("%v; shutting the server down", err)
	t.stopWrites()
	go func() { _ = t.close() }()
}

func (t *stdioTransport) stopWrites() {
	t.stopOnce.Do(func() { close(t.stopped) })
}

// unavailable names why the transport takes no more writes.
func (t *stdioTransport) unavailable() error {
	t.mu.Lock()
	writeErr := t.writeErr
	t.mu.Unlock()
	if writeErr != nil {
		return fmt.Errorf("server connection failed: %w", writeErr)
	}
	return errors.New("server is shutting down")
}

// exitError names why the server is unavailable: the exit status once the
// process is gone, the read failure otherwise. The reader sees the closed
// pipe a moment before waitLoop learns of the exit, so the status is given
// that moment to arrive.
func (t *stdioTransport) exitError() error {
	select {
	case <-t.exited:
	case <-time.After(time.Second):
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.exited:
		if t.waitErr != nil {
			return fmt.Errorf("server exited: %w", t.waitErr)
		}
		return errors.New("server exited")
	default:
	}
	if t.readErr != nil {
		return fmt.Errorf("server connection lost: %w", t.readErr)
	}
	return errors.New("server closed its output")
}

func (t *stdioTransport) roundTrip(ctx context.Context, req *message, _ requestOptions) (*message, error) {
	key := idKey(req.ID)
	ch := make(chan *message, 1)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, t.exitError()
	}
	t.pending[key] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, key)
		t.mu.Unlock()
	}()

	if err := t.write(ctx, req, requestWriteTimeout); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-t.done:
		// The reader may have delivered the response just before it stopped.
		select {
		case resp := <-ch:
			return resp, nil
		default:
		}
		return nil, t.exitError()
	case <-ctx.Done():
		t.cancel(req, ctx.Err())
		return nil, ctx.Err()
	}
}

// cancel tells the server to stop work on a request the client has given up
// on. On stdio there is no stream to close, so the notification is the only
// signal, and it only asks: it cannot stop work already done. Sending it is
// best effort under noticeWriteTimeout, covering the wait for the write slot
// too, and a notice that cannot be sent in time is skipped with a log line,
// since the caller already has the cancellation to report.
func (t *stdioTransport) cancel(req *message, cause error) {
	params := map[string]interface{}{"requestId": req.ID}
	if cause != nil {
		params["reason"] = cause.Error()
	}
	note, err := newNotification("notifications/cancelled", params)
	if err == nil {
		ctx, stop := context.WithTimeout(context.Background(), noticeWriteTimeout)
		err = t.write(ctx, note, noticeWriteTimeout)
		stop()
	}
	if err != nil {
		t.log("cancel notification for request %s skipped: %v", idKey(req.ID), err)
	}
}

func (t *stdioTransport) notify(ctx context.Context, note *message, _ requestOptions) error {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return t.exitError()
	}
	return t.write(ctx, note, requestWriteTimeout)
}

// close shuts the server down the way the specification asks, applied to
// its process group and under one deadline: close its stdin, wait, SIGTERM
// the group, wait, SIGKILL the group, and wait for what is left of
// shutdownDeadline. It never waits for the write slot: closing stdin ends a
// write in progress. The server stays unreaped through the last signal,
// which keeps the group's ID from passing to another group; see endServer
// for what that costs. Once the server is reaped, close waits for the group
// to empty, because a launcher can exit when its stdin closes and leave
// descendants running, and sends nothing more. Cleanup the deadline does
// not see finish is reported on the diagnostic stream, and close returns. A
// second call waits for the first to finish.
func (t *stdioTransport) close() error {
	t.mu.Lock()
	if t.shutdown {
		t.mu.Unlock()
		<-t.shutdownDone
		return nil
	}
	t.shutdown = true
	t.mu.Unlock()
	defer close(t.shutdownDone)

	t.stopWrites()
	deadline := time.Now().Add(t.deadline)
	_ = t.stdin.Close()
	t.endServer(deadline)
	close(t.signalsSent)
	if waitClosed(t.reaped, deadline) && t.waitGroupGone(deadline) {
		return nil
	}
	t.warnf("could not confirm the shutdown of process group %d within %v: %s", t.pgid, t.deadline, t.remains())
	return nil
}

// endServer sends the signals of shutdown, once stdin is closed. It waits
// up to the grace for the server to exit, SIGTERMs the group, lets the
// whole grace pass, SIGKILLs the group, and waits for the server to exit
// within the deadline. The group gets both signals even when the server has
// exited: what it left running there cannot be seen before the reap, since
// signal 0 counts the unreaped server as a member, and nothing is sent after
// the reap. So a process left behind that ignores SIGTERM still meets
// SIGKILL, and every shutdown takes the grace after SIGTERM. Where the
// server is signalled alone, its exit ends the sequence.
func (t *stdioTransport) endServer(deadline time.Time) {
	exited := waitClosed(t.exited, t.stageEnd(deadline))
	t.signalServer(syscall.SIGTERM)
	termEnd := t.stageEnd(deadline)
	if t.signalsGroup() {
		time.Sleep(time.Until(termEnd))
	} else if exited || waitClosed(t.exited, termEnd) {
		return
	}
	t.signalServer(syscall.SIGKILL)
	waitClosed(t.exited, deadline)
}

// signalsGroup reports whether signalServer reaches the server's group.
func (t *stdioTransport) signalsGroup() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holdsGroup
}

// signalServer sends sig to the server's process group, while waitLoop
// keeps the server unreaped, or to the server alone through its process
// handle when the platform could not wait without reaping. The lock orders
// it against waitLoop giving up the group.
func (t *stdioTransport) signalServer(sig syscall.Signal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.holdsGroup {
		_ = t.signal(t.pgid, sig)
		return
	}
	_ = t.cmd.Process.Signal(sig)
}

// waitClosed waits until ch is closed or until the given time, and reports
// whether ch was closed.
func waitClosed(ch <-chan struct{}, until time.Time) bool {
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// stageEnd is when a shutdown stage stops waiting: the grace from now, or
// the deadline if that comes first.
func (t *stdioTransport) stageEnd(deadline time.Time) time.Time {
	if end := time.Now().Add(t.grace); end.Before(deadline) {
		return end
	}
	return deadline
}

// waitGroupGone polls, once the server is reaped, until the process group
// has no member left or until the given time, and reports whether the group
// is gone. Signal 0 finds no member with ESRCH. EPERM means a member exists
// that this process may not signal, and any other error leaves the group's
// state unknown; neither confirms that the group is gone. Signal 0 has no
// effect, so polling is safe even if another group has taken the ID.
func (t *stdioTransport) waitGroupGone(until time.Time) bool {
	for {
		if errors.Is(t.signal(t.pgid, 0), syscall.ESRCH) {
			return true
		}
		wait := time.Until(until)
		if wait <= 0 {
			return false
		}
		time.Sleep(min(wait, groupPollInterval))
	}
}

// remains describes what shutdown could not confirm: that the server
// exited, that it was reaped, or else the group check's answer.
func (t *stdioTransport) remains() string {
	select {
	case <-t.reaped:
	default:
		select {
		case <-t.exited:
			return "the server exited but has not been reaped"
		default:
			return "the server has not exited"
		}
	}
	if err := t.signal(t.pgid, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Sprintf("checking it returned %v", err)
	}
	return "it still has members"
}

func (t *stdioTransport) warnf(format string, args ...interface{}) {
	t.warn(format, args...)
}
