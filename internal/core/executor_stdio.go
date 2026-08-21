package core

import (
	"bytes"
	"context"
	stdErrors "errors"
	"fmt"
	"io"
	"io/fs"
	"lmtools/internal/constants"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// cappedWriter captures output up to a maximum size.
type cappedWriter struct {
	buf       bytes.Buffer
	maxSize   int64
	truncated bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if int64(w.buf.Len()) >= w.maxSize {
		w.truncated = true
		return len(p), nil
	}

	remaining := w.maxSize - int64(w.buf.Len())
	if int64(len(p)) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}

	return w.buf.Write(p)
}

type commandFile struct {
	stream string
	path   string
	file   *os.File
	// truncate marks a file opened for writing. Truncation keys on this, set
	// from the open mode that created the entry, rather than on the stream
	// label — a relabeled or newly added input must never become a truncation
	// target.
	truncate bool
}

// runCommandWithIO runs a command, captures non-redirected output up to
// maxSize bytes, and owns all redirected file lifetimes.
func runCommandWithIO(ctx context.Context, cmd *exec.Cmd, args *UniversalCommandArgs, maxSize int64) (string, bool, error) {
	// Use one capped writer so non-redirected stdout and stderr retain the
	// existing combined-output behavior.
	cw := &cappedWriter{maxSize: maxSize}
	cmd.Stdout = cw
	cmd.Stderr = cw

	// Truncating an output file is destructive and irreversible, so the
	// failures os/exec would otherwise only surface from Start have to be
	// resolved before it happens.
	if args.StdoutFile != "" || args.StderrFile != "" {
		if err := verifyCommandCanStart(cmd); err != nil {
			return "", false, err
		}
	}

	commandFiles, err := configureCommandFiles(ctx, cmd, args)
	if err != nil {
		return "", false, err
	}

	runErr := runCommandWithLiteralStdin(cmd, args.Stdin)
	if stdErrors.Is(runErr, exec.ErrWaitDelay) {
		// Reached only when the command itself succeeded, which is precisely
		// the case that used to be reported as an unqualified success after an
		// arbitrarily long overrun.
		runErr = fmt.Errorf(
			"command exited but a descendant held its output stream open past %v; captured output may be incomplete: %w",
			CommandWaitDelay, runErr)
	}
	closeErr := closeCommandFiles(commandFiles)
	if runErr == nil && closeErr != nil {
		runErr = closeErr
	}

	return cw.buf.String(), cw.truncated, runErr
}

// runCommandWithLiteralStdin uses a caller-managed pipe instead of assigning a
// Reader to Cmd.Stdin. os/exec otherwise owns the copy goroutine and Cmd.Wait
// can remain blocked after the direct process exits when a descendant inherits
// the pipe without reading it. StdinPipe is closed by Cmd.Wait on process exit,
// and the wrapped command cancellation also releases a blocked write.
func runCommandWithLiteralStdin(cmd *exec.Cmd, stdin *string) error {
	if stdin == nil {
		return cmd.Run()
	}

	pipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	copyDone := make(chan struct{})
	go func(input string) {
		_, _ = io.WriteString(pipe, input)
		_ = pipe.Close()
		close(copyDone)
	}(*stdin)

	cancelCommand := cmd.Cancel
	if cancelCommand != nil {
		cmd.Cancel = func() error {
			cancelErr := cancelCommand()
			_ = pipe.Close()
			return cancelErr
		}
	}
	runErr := cmd.Run()
	_ = pipe.Close()
	<-copyDone
	return runErr
}

// verifyCommandCanStart resolves what Start resolves, before any truncation
// happens. Everything os/exec can refuse without ever creating a process — a
// name that does not name an executable, a workdir the process cannot enter, a
// NUL byte no C string can carry — it refuses from Start, and Start runs on the
// far side of the destructive step, so a typo in the command name would
// otherwise empty the file the command was going to write.
//
// The checks run in the order Start would reach them: os/exec rejects a NUL in
// the environment from Cmd.environ, the runtime rejects the rest converting
// argv, and chdir precedes exec inside the child.
//
// It cannot be exhaustive, and does not claim to be. Every check is a TOCTOU
// against a filesystem anything else may be writing to, an interpreter named in
// a `#!` line is resolved by the kernel at exec time and is invisible from
// here, and permission is approximated from the mode bits rather than asked of
// the kernel. What survives is a preflight that catches the classes an operator
// can actually produce by mistake; the residual cases are races, not typos.
//
// None of this extends to a command that starts and then fails. Once the child
// is running the file is legitimately its output, and emptying it first is what
// every shell does with `>`.
func verifyCommandCanStart(cmd *exec.Cmd) error {
	// exec.Command records a failed PATH lookup here — but only for a bare
	// name, because it calls LookPath only when filepath.Base(name) == name.
	// Anything carrying a separator arrives with cmd.Err nil and is resolved by
	// verifyCommandExecutable below.
	if cmd.Err != nil {
		return cmd.Err
	}
	if err := verifyCommandStringsExecSafe(cmd); err != nil {
		return err
	}
	if err := verifyCommandWorkdir(cmd.Dir); err != nil {
		return err
	}
	return verifyCommandExecutable(cmd)
}

// verifyCommandStringsExecSafe rejects the NUL bytes that cannot survive the
// conversion to C strings. os/exec raises the environment one itself and the
// runtime raises argv and the directory as EINVAL out of the fork; both land
// after the truncation.
func verifyCommandStringsExecSafe(cmd *exec.Cmd) error {
	for _, entry := range cmd.Env {
		if strings.IndexByte(entry, 0) >= 0 {
			return stdErrors.New("exec: environment variable contains NUL")
		}
	}
	if strings.IndexByte(cmd.Path, 0) >= 0 || strings.IndexByte(cmd.Dir, 0) >= 0 {
		return commandStartError(cmd.Path, syscall.EINVAL)
	}
	for _, arg := range cmd.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return commandStartError(cmd.Path, syscall.EINVAL)
		}
	}
	return nil
}

// verifyCommandWorkdir answers the question chdir asks, which statting the
// directory does not: entering a directory needs search permission, and a
// directory stats perfectly well without granting it — mode 0600 is the short
// example, and it reaches Start as an unexplained `fork/exec: permission
// denied` naming the executable rather than the directory. Statting a path
// through the directory asks for exactly the missing permission, using the
// effective credentials the exec would use; access(2) would answer for the real
// ones instead.
func verifyCommandWorkdir(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("chdir %s: %w", dir, unwrapPathError(err))
	}
	if !info.IsDir() {
		return fmt.Errorf("chdir %s: not a directory", dir)
	}
	// resolveCommandFilePath appends without cleaning, for the same reason it
	// does so for redirections: a cleaned path can name a different directory
	// than the one the OS would walk into.
	if _, err := os.Stat(resolveCommandFilePath(".", dir)); err != nil {
		return fmt.Errorf("chdir %s: %w", dir, unwrapPathError(err))
	}
	return nil
}

// verifyCommandExecutable resolves argv[0] the way Start will. A bare name was
// already looked up by exec.Command, which rewrote Path to what it found; a
// name carrying a separator is never looked up at all, and a relative one is
// resolved by the child after it has chdir'd into cmd.Dir, so that is where
// this looks for it too.
func verifyCommandExecutable(cmd *exec.Cmd) error {
	if cmd.Path == "" {
		return stdErrors.New("exec: no command")
	}
	if filepath.Base(cmd.Path) == cmd.Path {
		// Only reachable for a Cmd that did not come from exec.Command: a
		// successful LookPath rewrites Path to the location it resolved, and a
		// failed one sets cmd.Err, which the caller already returned.
		_, err := exec.LookPath(cmd.Path)
		return err
	}

	info, err := os.Stat(resolveCommandFilePath(cmd.Path, cmd.Dir))
	if err != nil {
		return commandStartError(cmd.Path, unwrapPathError(err))
	}
	// execve refuses a directory, a device, and a regular file with no
	// executable bit alike, and spells all three EACCES. Reading the mode bits
	// approximates the kernel's permission check — an ACL or a noexec mount can
	// still refuse a file that looks executable here — but it catches the case
	// that motivates the preflight, which is a data file named as a command.
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return commandStartError(cmd.Path, syscall.EACCES)
	}
	return nil
}

// commandStartError spells a preflight rejection the way os/exec spells the
// same condition out of Start, so which side of the truncation raised it is not
// something the operator or the model has to learn to read.
func commandStartError(path string, err error) error {
	return &fs.PathError{Op: "fork/exec", Path: path, Err: err}
}

// unwrapPathError trades a *fs.PathError for the errno inside it. The path it
// names is a probe this package invented; reporting it would describe the check
// rather than the command.
func unwrapPathError(err error) error {
	var pathErr *fs.PathError
	if stdErrors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func configureCommandFiles(ctx context.Context, cmd *exec.Cmd, args *UniversalCommandArgs) ([]commandFile, error) {
	opened := make([]commandFile, 0, 3)
	fail := func(err error) ([]commandFile, error) {
		_ = closeCommandFiles(opened)
		return nil, err
	}

	// The input is opened before either output so that an input-open failure
	// cannot leave an output file already created.
	open := func(stream, name string, flag int, perm os.FileMode) (*os.File, os.FileInfo, error) {
		if name == "" {
			return nil, nil, nil
		}
		path := resolveCommandFilePath(name, args.Workdir)
		file, info, err := openRegularCommandFile(ctx, path, flag, perm)
		if err != nil {
			return nil, nil, fmt.Errorf("open %s file %q: %w", stream, path, err)
		}
		opened = append(opened, commandFile{
			stream:   stream,
			path:     path,
			file:     file,
			truncate: flag&os.O_WRONLY != 0,
		})
		return file, info, nil
	}

	stdinFile, stdinInfo, err := open("stdin", args.StdinFile, os.O_RDONLY, 0)
	if err != nil {
		return fail(err)
	}
	stdoutFile, stdoutInfo, err := open("stdout", args.StdoutFile, os.O_CREATE|os.O_WRONLY, constants.FilePerm)
	if err != nil {
		return fail(err)
	}
	stderrFile, stderrInfo, err := open("stderr", args.StderrFile, os.O_CREATE|os.O_WRONLY, constants.FilePerm)
	if err != nil {
		return fail(err)
	}

	// Same-file checks compare the FileInfo each open already verified: the
	// identity of an open descriptor cannot change, so statting it again would
	// answer nothing new.
	if stdinFile != nil {
		for _, output := range []struct {
			stream string
			info   os.FileInfo
		}{
			{stream: "stdout", info: stdoutInfo},
			{stream: "stderr", info: stderrInfo},
		} {
			if output.info != nil && os.SameFile(stdinInfo, output.info) {
				return fail(errStdinAliasesOutput(output.stream))
			}
		}
	}

	if stdoutFile != nil && stderrFile != nil && os.SameFile(stdoutInfo, stderrInfo) {
		if err := stderrFile.Close(); err != nil {
			return fail(fmt.Errorf("close duplicate stderr file: %w", err))
		}
		opened = opened[:len(opened)-1]
		stderrFile = stdoutFile
	}

	if err := truncateCommandOutputs(ctx, opened); err != nil {
		return fail(err)
	}

	if stdinFile != nil {
		cmd.Stdin = stdinFile
	}
	if stdoutFile != nil {
		cmd.Stdout = stdoutFile
	}
	if stderrFile != nil {
		cmd.Stderr = stderrFile
	}
	return opened, nil
}

// truncateCommandOutputs empties the output files only after every open has
// succeeded, so a failure to open the second output cannot leave the first one
// destroyed. It rechecks cancellation between files because the caller may have
// spent real time in those opens.
func truncateCommandOutputs(ctx context.Context, opened []commandFile) error {
	for _, output := range opened {
		if !output.truncate {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := output.file.Truncate(0); err != nil {
			return fmt.Errorf("truncate %s file %q: %w", output.stream, output.path, err)
		}
	}
	return nil
}

// openRegularCommandFile restricts command redirections to regular files named
// directly, never through a symlink. The approval prompt shows the path the
// model asked for, so a final component that resolves elsewhere would write
// somewhere the operator never saw. Lstat rejects a link that is already there
// and O_NOFOLLOW rejects one planted between the check and the open; both cover
// the final component only, which is as far as an ordinary open can be
// constrained without openat2. Opens also carry O_NONBLOCK so a path replaced
// with a FIFO cannot stall setup beyond the command deadline.
//
// One cancellation check, at entry. It is what satisfies the between-opens
// requirement, since configureCommandFiles calls this once per redirection and
// the destructive step rechecks for itself in truncateCommandOutputs. Checks
// placed later in this function were reachable only by a context going done
// inside it: a poll cannot interrupt a syscall, O_NONBLOCK is what bounds the
// one open that could block, and nothing between the entry check and the return
// destroys anything. A guard that no test can fail is not a guard.
func openRegularCommandFile(ctx context.Context, path string, flag int, perm os.FileMode) (*os.File, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	if info, err := os.Lstat(path); err == nil {
		if err := verifyRegularCommandFileMode(info.Mode(), path); err != nil {
			return nil, nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}

	file, err := os.OpenFile(path, flag|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return nil, nil, err
	}

	info, err := verifyOpenedRegularCommandFile(file, path)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

// verifyRegularCommandFileMode is the single place that decides whether a mode
// is acceptable for a redirection, so the pre-open and post-open checks cannot
// drift apart.
func verifyRegularCommandFileMode(mode os.FileMode, path string) error {
	if mode&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is a symbolic link", path)
	}
	if !mode.IsRegular() {
		return fmt.Errorf("%q is not a regular file", path)
	}
	return nil
}

// verifyOpenedRegularCommandFile rechecks the descriptor actually obtained.
// O_NOFOLLOW and the Lstat above race with anything that can rename the path,
// and this is the check that answers for what was opened rather than for what
// was looked at. It returns the FileInfo it already paid for, which callers
// reuse for same-file comparisons.
func verifyOpenedRegularCommandFile(file *os.File, path string) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	// Fstat never reports a symlink: opening one either failed under O_NOFOLLOW
	// or resolved past it. Only the regular-file half is meaningful here.
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	return info, nil
}

func resolveCommandFilePath(path, workdir string) string {
	if workdir == "" || filepath.IsAbs(path) {
		return path
	}

	// filepath.Join and filepath.Clean cannot be used here. The OS resolves
	// pathname components in order, so cleaning "symlink/../file" before an
	// open can select a different file than opening the path as written.
	if os.IsPathSeparator(workdir[len(workdir)-1]) {
		return workdir + path
	}
	return workdir + string(filepath.Separator) + path
}

func closeCommandFiles(files []commandFile) error {
	var firstErr error
	for _, commandFile := range files {
		if err := commandFile.file.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close %s file %q: %w", commandFile.stream, commandFile.path, err)
		}
	}
	return firstErr
}
