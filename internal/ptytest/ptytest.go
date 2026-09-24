//go:build darwin || freebsd || linux

// Package ptytest opens pseudo terminals for tests. The code under test
// gets the slave as its terminal, in the test's own process or in a
// subprocess whose controlling terminal it becomes; the test types into the
// master and reads from it what the terminal shows.
//
// It uses the standard library alone: /dev/ptmx on Linux and macOS, and
// posix_openpt on FreeBSD.
package ptytest

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Control characters of a terminal's default settings.
const (
	// Interrupt is Ctrl-C, which sends SIGINT to the terminal's foreground
	// process group and discards its pending input.
	Interrupt = "\x03"
	// EndOfInput is Ctrl-D. At the start of a line a read returns zero
	// bytes; after text it delivers the text without a newline.
	EndOfInput = "\x04"
)

// PTY is a pseudo terminal pair.
type PTY struct {
	// Master is the side a test types into and reads the screen from. It
	// stays blocking, since the runtime poller cannot serve a terminal on
	// every platform.
	Master *os.File
	// Slave is the terminal the code under test uses. It stays blocking,
	// so a subprocess given it starts with the descriptions a terminal
	// normally has.
	Slave *os.File
	// Name is the slave's device path.
	Name string

	screenOnce sync.Once
	screen     *Screen
}

// Open opens a pseudo terminal pair with the platform's default settings:
// canonical input, echo, and the signal characters.
func Open() (*PTY, error) {
	masterFd, name, err := openMaster()
	if err != nil {
		return nil, err
	}
	slaveFd, err := syscall.Open(name, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = syscall.Close(masterFd)
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	return &PTY{
		Master: os.NewFile(uintptr(masterFd), "ptmx"),
		Slave:  os.NewFile(uintptr(slaveFd), name),
		Name:   name,
	}, nil
}

// Close closes both sides, the slave first, so a reader of the master sees
// the terminal close once no subprocess holds the slave.
func (p *PTY) Close() error {
	slaveErr := p.Slave.Close()
	masterErr := p.Master.Close()
	if slaveErr != nil {
		return slaveErr
	}
	return masterErr
}

// Type writes s to the master, as keys typed at the terminal.
func (p *PTY) Type(s string) error {
	_, err := io.WriteString(p.Master, s)
	return err
}

// Start starts cmd in a session of its own whose controlling terminal is the
// slave, which becomes its standard input, output, and error where cmd
// leaves them unset. The terminal's foreground process group is then cmd's,
// so an interrupt typed at the master reaches it.
func (p *PTY) Start(cmd *exec.Cmd) error {
	if cmd.Stdin == nil {
		cmd.Stdin = p.Slave
	}
	if cmd.Stdout == nil {
		cmd.Stdout = p.Slave
	}
	if cmd.Stderr == nil {
		cmd.Stderr = p.Slave
	}
	// Ctty is a descriptor number in the child.
	ctty := 0
	if stdin, ok := cmd.Stdin.(*os.File); !ok || stdin != p.Slave {
		cmd.ExtraFiles = append(cmd.ExtraFiles, p.Slave)
		ctty = 2 + len(cmd.ExtraFiles)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = ctty
	return cmd.Start()
}

// Screen returns what the terminal shows, read from the master by a
// goroutine that starts on the first call and keeps the master drained, so
// output to the slave never blocks on a full buffer.
func (p *PTY) Screen() *Screen {
	p.screenOnce.Do(func() {
		p.screen = &Screen{update: make(chan struct{})}
		go p.screen.read(p.Master)
	})
	return p.screen
}

// Screen is what a terminal has shown: every byte read from its master,
// echoes of what was typed included.
type Screen struct {
	mu  sync.Mutex
	buf []byte
	// pos is where the next Expect starts looking.
	pos int
	// update is closed and replaced whenever bytes arrive or reading stops.
	update chan struct{}
	// err is why reading stopped, once it has.
	err error
}

func (s *Screen) read(master io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := master.Read(buf)
		s.mu.Lock()
		s.buf = append(s.buf, buf[:n]...)
		if err != nil {
			s.err = err
		}
		close(s.update)
		s.update = make(chan struct{})
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// Expect waits up to timeout for want to appear after the text the previous
// Expect matched, and moves past it.
func (s *Screen) Expect(want string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		if i := bytes.Index(s.buf[s.pos:], []byte(want)); i >= 0 {
			s.pos += i + len(want)
			s.mu.Unlock()
			return nil
		}
		update, err := s.update, s.err
		s.mu.Unlock()
		if err != nil {
			return fmt.Errorf("the terminal closed (%v) before %q appeared; it showed %q", err, want, s.String())
		}
		select {
		case <-update:
		case <-deadline.C:
			return fmt.Errorf("%q did not appear within %v; the terminal showed %q", want, timeout, s.String())
		}
	}
}

// Since returns what the terminal has shown after the text the last Expect
// matched.
func (s *Screen) Since() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.buf[s.pos:])
}

// String returns everything the terminal has shown.
func (s *Screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.buf)
}

// ioctl calls ioctl with a pointer argument, converted in the call itself,
// where the conversion keeps the pointer valid.
func ioctl(fd int, request uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}
