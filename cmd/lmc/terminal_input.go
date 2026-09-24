//go:build darwin || freebsd || linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"lmtools/internal/limitio"
	"os"
	"syscall"
	"unsafe"
)

// terminalReadSize is how many bytes one read of the terminal asks for. A
// canonical read returns one line at most, so a longer line arrives in
// pieces. Tests shrink it to split a line across reads.
var terminalReadSize = 4096

// afterTerminalReadyForTest runs between the terminal reporting that input
// is ready and the read, so a test can take the input away in between.
var afterTerminalReadyForTest func()

// beforeTerminalWakeForTest runs when a request's context has ended, before
// the write to the wake pipe, so a test can hold the write.
var beforeTerminalWakeForTest func()

// reopenTerminal opens the terminal named name for the owner, with
// O_NONBLOCK on this open alone. Tests replace it to play a name that opens
// another device.
var reopenTerminal = func(name string) (int, error) {
	return syscall.Open(name, syscall.O_RDONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
}

// fdSetSize is how many descriptors a select set holds.
const fdSetSize = 1024

// terminalReader reads the terminal on standard input through an open of
// its own. O_NONBLOCK belongs to an open file description, which standard
// output, standard error, and the shell commonly share with standard input,
// so it is set on this open alone, reopened by the terminal's name. The
// reader reads only while a request asks, once select reports the terminal
// ready or the wake pipe ends the wait, so no read is ever left pending for
// a request that has ended. Each read after readiness is nonblocking, since
// the readiness can be withdrawn before the read: an interrupt or a flush
// empties the queue.
type terminalReader struct {
	fd   int
	name string
	// wakeR and wakeW are a pipe. A request whose context ends writes to
	// wakeW, which ends a wait on wakeR.
	wakeR, wakeW int
	buf          []byte
	// held is what a read returned past the line it delivered, kept for the
	// next request. Only a terminal outside canonical mode leaves any: a
	// canonical read returns one line at most.
	held []byte
	// afterReady and beforeWake are the test hooks, copied when the reader
	// opens.
	afterReady, beforeWake func()
}

// openTerminalReader names the terminal on standard input, opens it again
// by that name, and checks that the name opened the same device. It fails
// when any step does, with the terminal's name and the reason; there is no
// fallback to reading standard input itself, whose blocking reads cannot
// be ended.
func openTerminalReader(stdin *os.File) (terminalInput, error) {
	conn, err := stdin.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("reach the descriptor of standard input: %w", err)
	}
	var reader *terminalReader
	var openErr error
	if err := conn.Control(func(stdinFd uintptr) {
		reader, openErr = openTerminalReaderFor(int(stdinFd))
	}); err != nil {
		return nil, fmt.Errorf("reach the descriptor of standard input: %w", err)
	}
	if openErr != nil {
		return nil, openErr
	}
	return reader, nil
}

func openTerminalReaderFor(stdinFd int) (*terminalReader, error) {
	name, err := terminalName(stdinFd)
	if err != nil {
		return nil, fmt.Errorf("name the terminal on standard input: %w", err)
	}
	fd, err := reopenTerminal(name)
	if err != nil {
		return nil, fmt.Errorf("reopen %s: %w", name, err)
	}
	if err := sameTerminal(stdinFd, fd); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("reopen %s: %w", name, err)
	}
	wakeR, wakeW, err := wakePipe()
	if err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("make the pipe that ends a wait for %s: %w", name, err)
	}
	reader := &terminalReader{
		fd:         fd,
		name:       name,
		wakeR:      wakeR,
		wakeW:      wakeW,
		buf:        make([]byte, terminalReadSize),
		afterReady: afterTerminalReadyForTest,
		beforeWake: beforeTerminalWakeForTest,
	}
	for _, d := range []int{fd, wakeR} {
		if d >= fdSetSize {
			_ = reader.close()
			return nil, fmt.Errorf("wait for %s: descriptor %d is past what select can wait on", name, d)
		}
	}
	return reader, nil
}

// sameTerminal checks that fd, opened by name, is the terminal standard
// input is: the same device node, and a terminal.
func sameTerminal(stdinFd, fd int) error {
	var want, got syscall.Stat_t
	if err := syscall.Fstat(stdinFd, &want); err != nil {
		return fmt.Errorf("stat standard input: %w", err)
	}
	if err := syscall.Fstat(fd, &got); err != nil {
		return fmt.Errorf("stat the open: %w", err)
	}
	if got.Dev != want.Dev || got.Ino != want.Ino || got.Rdev != want.Rdev {
		return errors.New("the name opens another device than standard input")
	}
	if !isTerminalFd(uintptr(fd)) {
		return errors.New("the name does not open a terminal")
	}
	return nil
}

// wakePipe makes a pipe whose ends are nonblocking and closed on exec.
func wakePipe() (int, int, error) {
	var p [2]int
	syscall.ForkLock.RLock()
	err := syscall.Pipe(p[:])
	if err == nil {
		syscall.CloseOnExec(p[0])
		syscall.CloseOnExec(p[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return -1, -1, err
	}
	for _, fd := range p {
		if err := syscall.SetNonblock(fd, true); err != nil {
			_ = syscall.Close(p[0])
			_ = syscall.Close(p[1])
			return -1, -1, err
		}
	}
	return p[0], p[1], nil
}

// wait blocks until the terminal reports input ready or ctx is done. The
// end of ctx writes to the wake pipe, which ends a wait in progress; a byte
// left there by a request that had already finished makes the next wait
// look again, and nothing more. A write that has started is waited for
// before wait returns, since the owner may close the pipe afterwards, and
// another open could then take the descriptor the write goes to.
func (t *terminalReader) wait(ctx context.Context) error {
	woke := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(woke)
		if t.beforeWake != nil {
			t.beforeWake()
		}
		t.wake()
	})
	defer func() {
		if !stop() {
			<-woke
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, woken, err := waitReadable(t.fd, t.wakeR)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for %s: %w", t.name, err)
		}
		if woken {
			t.drainWake()
		}
		if ready {
			return ctx.Err()
		}
	}
}

func (t *terminalReader) wake() {
	_, _ = syscall.Write(t.wakeW, []byte{0})
}

func (t *terminalReader) drainWake() {
	var buf [16]byte
	for {
		if n, err := syscall.Read(t.wakeR, buf[:]); n <= 0 || err != nil {
			return
		}
	}
}

// read returns the next bytes of input: what an earlier read held back, or
// what one read returns once the terminal is ready. Zero bytes are the end
// of input: Ctrl-D at the start of a line, or a hangup. The slice is valid
// until the next read. A request whose context has ended gets nothing, held
// bytes included.
func (t *terminalReader) read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(t.held) > 0 {
		chunk := t.held
		t.held = nil
		return chunk, nil
	}
	for {
		if err := t.wait(ctx); err != nil {
			return nil, err
		}
		if t.afterReady != nil {
			t.afterReady()
		}
		n, err := syscall.Read(t.fd, t.buf)
		switch {
		case err == nil:
			return t.buf[:n], nil
		case errors.Is(err, syscall.EAGAIN):
			// The readiness was withdrawn: an interrupt or a flush emptied
			// the queue after select reported it. Wait again.
		case errors.Is(err, syscall.EINTR):
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		case errors.Is(err, syscall.EIO):
			// The terminal is gone, which is the end of input.
			return nil, nil
		default:
			return nil, fmt.Errorf("read %s: %w", t.name, err)
		}
	}
}

// readLine reads until a newline or the end of input. Bytes without a
// newline are a fragment of the line, whether Ctrl-D delivered them or the
// read's buffer filled, so they are kept until the line ends. A request
// that ends early drops its fragments.
func (t *terminalReader) readLine(ctx context.Context, bound int) (inputLine, error) {
	b := lineBuilder{bound: bound}
	for {
		chunk, err := t.read(ctx)
		if err != nil {
			return inputLine{}, err
		}
		if len(chunk) == 0 {
			return b.line(true), nil
		}
		end := bytes.IndexByte(chunk, '\n')
		if end < 0 {
			b.add(chunk)
			continue
		}
		b.add(chunk[:end])
		if rest := chunk[end+1:]; len(rest) > 0 {
			t.held = append([]byte(nil), rest...)
		}
		return b.line(false), nil
	}
}

func (t *terminalReader) readAll(ctx context.Context, limit int64) ([]byte, error) {
	var all []byte
	for {
		chunk, err := t.read(ctx)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			return all, nil
		}
		if int64(len(all)+len(chunk)) > limit {
			return nil, &limitio.SizeLimitError{Kind: "data", MaxSize: limit}
		}
		all = append(all, chunk...)
	}
}

// flush discards the input the terminal holds, and what the reader held
// back from an earlier read, and reports whether a complete line was among
// it. In canonical mode the terminal counts complete lines alone, so an
// unfinished line is discarded without a report.
func (t *terminalReader) flush() (bool, error) {
	pending, err := pendingInput(t.fd)
	if err != nil {
		return false, fmt.Errorf("count the input %s holds: %w", t.name, err)
	}
	discarded := pending > 0 || bytes.IndexByte(t.held, '\n') >= 0
	t.held = nil
	if err := flushInput(t.fd); err != nil {
		return false, fmt.Errorf("flush the input %s holds: %w", t.name, err)
	}
	return discarded, nil
}

func (t *terminalReader) close() error {
	err := syscall.Close(t.fd)
	_ = syscall.Close(t.wakeR)
	_ = syscall.Close(t.wakeW)
	return err
}

// waitReadable waits in select until fd or wake can be read, and reports
// which.
func waitReadable(fd, wake int) (ready, woken bool, err error) {
	var set syscall.FdSet
	fdSetAdd(&set, fd)
	fdSetAdd(&set, wake)
	if err := selectReadable(max(fd, wake)+1, &set); err != nil {
		return false, false, err
	}
	return fdSetHas(&set, fd), fdSetHas(&set, wake), nil
}

// pendingInput returns how many bytes the terminal holds for reading.
func pendingInput(fd int) (int, error) {
	var n int32
	if err := terminalIoctl(fd, terminalPendingRequest, unsafe.Pointer(&n)); err != nil {
		return 0, err
	}
	return int(n), nil
}

// terminalIoctl calls ioctl with a pointer argument, converted in the call
// itself, where the conversion keeps the pointer valid.
func terminalIoctl(fd int, request uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}
