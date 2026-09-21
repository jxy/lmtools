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
	// shutdownGrace is how long each step of the stdio shutdown sequence
	// waits before the next: closing stdin, then SIGTERM, then SIGKILL.
	shutdownGrace = 2 * time.Second
)

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
// returns the reply to write, for the legacy servers that still ask.
type peerHandler struct {
	onNotification func(*message)
	onRequest      func(*message) *message
}

// stdioTransport is the subprocess binding: one line per message on the
// child's stdin and stdout, stderr to the log, responses matched by id.
type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *os.File
	stderr  *os.File
	handler peerHandler
	log     Logf

	writeMu sync.Mutex

	mu       sync.Mutex
	pending  map[string]chan *message
	closed   bool
	readErr  error
	done     chan struct{} // closed when the stdout reader stops
	exited   chan struct{} // closed when the process has been waited for
	waitErr  error
	shutdown bool
}

// startStdio launches the server without a shell, with the inherited
// environment plus the configured additions, and starts reading it.
func startStdio(cfg ServerConfig, handler peerHandler, log Logf) (*stdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Cwd
	cmd.Env = os.Environ()
	for name, value := range cfg.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	// Plain pipes rather than StdoutPipe: exec's Wait would otherwise close
	// the read side the moment the child exits, which is wrong while the
	// reader may still be draining it, and a descendant that inherited the
	// write side would hold the reader open forever. With os.Pipe the
	// transport owns both ends and closes the read side itself, after Wait.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, fmt.Errorf("start %q: %w", cfg.Command, err)
	}
	stdoutW.Close()
	stderrW.Close()

	t := &stdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdoutR,
		stderr:  stderrR,
		handler: handler,
		log:     log,
		pending: make(map[string]chan *message),
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
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
			if err := t.write(reply); err != nil {
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

// waitLoop reaps the process and then releases the pipes, so a reader
// blocked by a descendant that inherited stdout is unblocked once the
// server itself is gone.
func (t *stdioTransport) waitLoop() {
	err := t.cmd.Wait()
	t.mu.Lock()
	t.waitErr = err
	t.mu.Unlock()
	close(t.exited)
	t.stdout.Close()
	t.stderr.Close()
}

func (t *stdioTransport) write(msg *message) error {
	data, err := encodeMessage(msg)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write to server: %w", err)
	}
	return nil
}

// exitError names why the server is unavailable: the exit status once the
// process is gone, the read failure otherwise. The reader sees the closed
// pipe a moment before Wait reaps the process, so the status is given that
// moment to arrive.
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

	if err := t.write(req); err != nil {
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
// signal; a failure to send it is logged, since the caller already has the
// cancellation to report.
func (t *stdioTransport) cancel(req *message, cause error) {
	params := map[string]interface{}{"requestId": req.ID}
	if cause != nil {
		params["reason"] = cause.Error()
	}
	note, err := newNotification("notifications/cancelled", params)
	if err == nil {
		err = t.write(note)
	}
	if err != nil {
		t.log("cancel notification for request %s failed: %v", idKey(req.ID), err)
	}
}

func (t *stdioTransport) notify(_ context.Context, note *message, _ requestOptions) error {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return t.exitError()
	}
	return t.write(note)
}

// close shuts the server down the way the specification asks: close its
// stdin, wait, SIGTERM, wait, SIGKILL. Each wait is bounded by
// shutdownGrace so a server that ignores every signal but the last still
// costs a bounded time.
func (t *stdioTransport) close() error {
	t.mu.Lock()
	if t.shutdown {
		t.mu.Unlock()
		return nil
	}
	t.shutdown = true
	t.mu.Unlock()

	t.writeMu.Lock()
	_ = t.stdin.Close()
	t.writeMu.Unlock()
	if t.waitExit(shutdownGrace) {
		return nil
	}
	_ = t.cmd.Process.Signal(syscall.SIGTERM)
	if t.waitExit(shutdownGrace) {
		return nil
	}
	_ = t.cmd.Process.Kill()
	<-t.exited
	return nil
}

func (t *stdioTransport) waitExit(grace time.Duration) bool {
	select {
	case <-t.exited:
		return true
	case <-time.After(grace):
		return false
	}
}
