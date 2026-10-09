//go:build darwin || freebsd || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"lmtools/internal/processwait"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

// runEditor lends the terminal to one child while the owner is idle. A
// separate foreground group receives native editor Ctrl-C. Restore even when
// exec fails after the child took foreground ownership.
func (o *inputOwner) runEditor(ctx context.Context, cmd *exec.Cmd, show func(bool)) (err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !o.terminal || o.term == nil {
		return errors.New("editor requires terminal input")
	}
	if !processwait.Supported {
		return errors.New("editor terminal handoff is not supported on this platform")
	}
	name, err := terminalName(int(o.stdin.Fd()))
	if err != nil {
		return err
	}
	tty, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	fd := int(tty.Fd())
	if err := sameTerminal(int(o.stdin.Fd()), fd); err != nil {
		return err
	}
	var settings syscall.Termios
	var foreground int32
	if err := terminalIoctl(fd, terminalReadTermios, unsafe.Pointer(&settings)); err != nil {
		return err
	}
	if err := terminalIoctl(fd, syscall.TIOCGPGRP, unsafe.Pointer(&foreground)); err != nil {
		return err
	}
	defer func() {
		ignored := signal.Ignored(syscall.SIGTTOU)
		signal.Ignore(syscall.SIGTTOU)
		defer func() {
			if !ignored {
				signal.Reset(syscall.SIGTTOU)
			}
		}()
		groupErr := terminalIoctl(fd, syscall.TIOCSPGRP, unsafe.Pointer(&foreground))
		modeErr := terminalIoctl(fd, terminalWriteTermios, unsafe.Pointer(&settings))
		_, flushErr := o.term.flush()
		if restoreErr := errors.Join(groupErr, modeErr, flushErr); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %w", errEditorTerminal, restoreErr))
		}
	}()
	discarded, err := o.term.flush()
	if err != nil {
		return err
	}
	show(discarded)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Foreground: true, Ctty: fd}
	changed := make(chan os.Signal, 4)
	signal.Notify(changed, syscall.SIGCHLD)
	defer signal.Stop(changed)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch editor: %w", err)
	}
	finished := make(chan error, 1)
	go func() { _, err := processwait.WaitUnreaped(cmd.Process.Pid); finished <- err }()
	cancelled := ctx.Done()
	for {
		select {
		case err := <-finished:
			// The unreaped leader reserves the group ID until signaling has
			// stopped. Reap once, after cleaning up any editor descendants.
			signal.Stop(changed)
			if err == nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			} else {
				_ = cmd.Process.Kill()
			}
			return errors.Join(err, cmd.Wait(), ctx.Err())
		case <-cancelled:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			cancelled = nil
		case <-changed:
			// /ed has no suspended job to return to. Resume Ctrl-Z's
			// stopped group, rather than strand the REPL in Wait.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGCONT)
		}
	}
}
