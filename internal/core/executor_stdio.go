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
	// info is the descriptor identity used for alias checks and safe cleanup.
	info os.FileInfo
	// truncate is true only for regular files opened for writing.
	truncate bool
	// created is the O_CREATE|O_EXCL result, not a pre-open path observation.
	created bool
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
	started := cmd.Process != nil
	releaseErr := releaseCommandFiles(commandFiles, started)
	if releaseErr != nil {
		switch {
		case runErr == nil:
			runErr = releaseErr
		case !started:
			runErr = stdErrors.Join(runErr, fmt.Errorf("cleanup failed: %w", releaseErr))
		}
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
		if cleanupErr := releaseCommandFiles(opened, false); cleanupErr != nil {
			err = stdErrors.Join(err, fmt.Errorf("cleanup failed: %w", cleanupErr))
		}
		return nil, err
	}

	// Open input first so its failure cannot create an output that then needs
	// race-prone cleanup.
	open := func(stream, name string, flag int, perm os.FileMode) (commandFile, error) {
		if name == "" {
			return commandFile{}, nil
		}
		path := resolveCommandFilePath(name, args.Workdir)
		opening, err := openCommandFile(ctx, path, flag, perm)
		if err != nil {
			return commandFile{}, fmt.Errorf("open %s file %q: %w", stream, path, err)
		}
		opening.stream = stream
		opened = append(opened, opening)
		return opening, nil
	}

	stdinFile, err := open("stdin", args.StdinFile, os.O_RDONLY, 0)
	if err != nil {
		return fail(err)
	}
	stdoutFile, err := open("stdout", args.StdoutFile, os.O_CREATE|os.O_WRONLY, constants.FilePerm)
	if err != nil {
		return fail(err)
	}
	stderrFile, err := open("stderr", args.StderrFile, os.O_CREATE|os.O_WRONLY, constants.FilePerm)
	if err != nil {
		return fail(err)
	}

	// Compare the verified descriptors. Permitted devices are shareable because
	// their input and output streams cannot overwrite one another.
	if stdinFile.file != nil && !isPermittedCommandDevice(stdinFile.info.Mode(), stdinFile.path) {
		for _, output := range []commandFile{stdoutFile, stderrFile} {
			if output.file != nil && os.SameFile(stdinFile.info, output.info) {
				return fail(errStdinAliasesOutput(output.stream))
			}
		}
	}

	// The duplicate open cannot be the creator: stdout already held this inode.
	if stdoutFile.file != nil && stderrFile.file != nil && os.SameFile(stdoutFile.info, stderrFile.info) {
		if err := stderrFile.file.Close(); err != nil {
			return fail(fmt.Errorf("close duplicate stderr file: %w", err))
		}
		opened = opened[:len(opened)-1]
		stderrFile = stdoutFile
	}

	if err := truncateCommandOutputs(ctx, opened); err != nil {
		return fail(err)
	}

	if stdinFile.file != nil {
		cmd.Stdin = stdinFile.file
	}
	if stdoutFile.file != nil {
		cmd.Stdout = stdoutFile.file
	}
	if stderrFile.file != nil {
		cmd.Stderr = stderrFile.file
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

// discardCreatedCommandFiles unlinks files created by this setup when their
// paths still identify the opened files. Entries must have been verified and
// therefore carry info. Unlink is deliberately file-only; os.Remove could
// remove an empty directory swapped into the path after the identity check.
func discardCreatedCommandFiles(files []commandFile) error {
	var cleanupErr error
	for _, output := range files {
		if !output.created {
			continue
		}
		if output.info == nil {
			cleanupErr = stdErrors.Join(cleanupErr, fmt.Errorf(
				"remove created %s file %q: missing opened-file identity", output.stream, output.path))
			continue
		}
		current, err := os.Lstat(output.path)
		if stdErrors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			cleanupErr = stdErrors.Join(cleanupErr,
				fmt.Errorf("inspect created %s file %q: %w", output.stream, output.path, err))
			continue
		}
		if !os.SameFile(current, output.info) {
			continue
		}
		if err := unlinkCreatedCommandFile(output.path); err != nil {
			cleanupErr = stdErrors.Join(cleanupErr,
				fmt.Errorf("remove created %s file %q: %w", output.stream, output.path, err))
		}
	}
	return cleanupErr
}

// openCommandFile accepts regular files and explicitly permitted devices, never
// a final-component symlink. Lstat gives an early rejection, O_NOFOLLOW closes
// the check/open race, Fstat verifies the descriptor, and O_NONBLOCK prevents a
// raced-in FIFO from stalling setup.
func openCommandFile(ctx context.Context, path string, flag int, perm os.FileMode) (commandFile, error) {
	if err := ctx.Err(); err != nil {
		return commandFile{}, err
	}

	if info, err := os.Lstat(path); err == nil {
		if err := verifyCommandFileMode(info.Mode(), path); err != nil {
			return commandFile{}, err
		}
	} else if !os.IsNotExist(err) {
		return commandFile{}, err
	}

	file, created, err := createOrOpenCommandFile(path, flag|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return commandFile{}, err
	}
	return finishCommandFileOpen(commandFile{path: path, file: file, created: created}, flag)
}

// finishCommandFileOpen verifies the opened descriptor and completes its
// lifecycle record. It is separate so the post-open failure path is testable.
func finishCommandFileOpen(opening commandFile, flag int) (commandFile, error) {
	info, err := verifyOpenedCommandFile(opening.file, opening.path)
	if err != nil {
		closeErr := opening.file.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close command file %q: %w", opening.path, closeErr)
		}
		var removeErr error
		if opening.created {
			if unlinkErr := unlinkCreatedCommandFile(opening.path); unlinkErr != nil {
				removeErr = fmt.Errorf("remove created command file %q: %w", opening.path, unlinkErr)
			}
		}
		return commandFile{}, stdErrors.Join(err, closeErr, removeErr)
	}
	opening.info = info
	opening.truncate = flag&os.O_WRONLY != 0 && info.Mode().IsRegular()
	return opening, nil
}

// unlinkCreatedCommandFile removes a file without os.Remove's directory
// fallback. A path already removed by another actor needs no cleanup.
func unlinkCreatedCommandFile(path string) error {
	err := syscall.Unlink(path)
	if stdErrors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// commandFileOpenAttempts bounds retries against a concurrently changing path.
const commandFileOpenAttempts = 3

// createOrOpenCommandFile reports whether O_CREATE created the path. Existing
// outputs use a second open; a path removed between those calls is retried.
func createOrOpenCommandFile(path string, flag int, perm os.FileMode) (*os.File, bool, error) {
	if flag&os.O_CREATE == 0 {
		file, err := os.OpenFile(path, flag, perm)
		return file, false, err
	}

	for attempt := 0; ; attempt++ {
		file, created, retry, err := createOrOpenCommandFileOnce(path, flag, perm)
		if !retry || attempt == commandFileOpenAttempts-1 {
			return file, created, err
		}
	}
}

// createOrOpenCommandFileOnce isolates the retry decision for testing. EEXIST
// followed by ENOENT means the path disappeared between the two opens.
func createOrOpenCommandFileOnce(path string, flag int, perm os.FileMode) (file *os.File, created, retry bool, err error) {
	if file, err = os.OpenFile(path, flag|os.O_EXCL, perm); err == nil {
		return file, true, false, nil
	}
	if !stdErrors.Is(err, fs.ErrExist) {
		return nil, false, false, err
	}
	if file, err = os.OpenFile(path, flag&^os.O_CREATE, perm); err == nil {
		return file, false, false, nil
	}
	return nil, false, stdErrors.Is(err, fs.ErrNotExist), err
}

// verifyCommandFileMode is shared by the pre-open and post-open checks. A device
// needs both an allowed name and character-device mode.
func verifyCommandFileMode(mode os.FileMode, path string) error {
	if mode&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is a symbolic link", path)
	}
	if mode.IsRegular() || isPermittedCommandDevice(mode, path) {
		return nil
	}
	return fmt.Errorf("%q is neither a regular file nor one of %s",
		path, constants.PermittedCommandDevicesText)
}

// isPermittedCommandDevice is the complete name-and-mode decision.
func isPermittedCommandDevice(mode os.FileMode, path string) bool {
	return mode&os.ModeCharDevice != 0 && constants.IsPermittedCommandDeviceName(path)
}

// verifyOpenedCommandFile validates the descriptor obtained after the path
// check and returns its identity for alias checks and cleanup.
func verifyOpenedCommandFile(file *os.File, path string) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if err := verifyCommandFileMode(info.Mode(), path); err != nil {
		return nil, err
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

// releaseCommandFiles closes every descriptor and, if no process started,
// removes the regular files this setup created.
func releaseCommandFiles(files []commandFile, started bool) error {
	closeErr := closeCommandFiles(files)
	if !started {
		return stdErrors.Join(closeErr, discardCreatedCommandFiles(files))
	}
	return closeErr
}
