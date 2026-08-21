package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	executorStdioHelperEnv   = "LMTOOLS_EXECUTOR_STDIO_HELPER"
	executorStdioHelperMode  = "LMTOOLS_EXECUTOR_STDIO_MODE"
	executorStdioReadyFile   = "LMTOOLS_EXECUTOR_STDIO_READY_FILE"
	executorStdioCleanupFile = "LMTOOLS_EXECUTOR_STDIO_CLEANUP_FILE"
)

const executorStdioDescendantLifetime = 10 * time.Second

// stdioStreamSetters names each redirectable stream and how a call selects it.
// Output-only tests range over stdioStreamSetters[1:].
var stdioStreamSetters = []struct {
	name string
	set  func(*UniversalCommandArgs, string)
}{
	{name: "stdin", set: func(args *UniversalCommandArgs, path string) { args.StdinFile = path }},
	{name: "stdout", set: func(args *UniversalCommandArgs, path string) { args.StdoutFile = path }},
	{name: "stderr", set: func(args *UniversalCommandArgs, path string) { args.StderrFile = path }},
}

// stdioApprover adapts a bare approval func to Approver, declining round-limit
// resets through the shared stub.
type stdioApprover struct {
	DeclineToolRoundLimitReset
	approve func(context.Context, UniversalCommandArgs) (bool, error)
}

func (a stdioApprover) Approve(ctx context.Context, args UniversalCommandArgs) (bool, error) {
	return a.approve(ctx, args)
}

func TestUniversalCommandStdioHelperProcess(t *testing.T) {
	if os.Getenv(executorStdioHelperEnv) != "1" {
		return
	}

	switch os.Getenv(executorStdioHelperMode) {
	case "copy-stdin":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		_, _ = os.Stdout.Write(input)
	case "both":
		fmt.Fprint(os.Stdout, "stdout")
		fmt.Fprint(os.Stderr, "stderr")
	case "interleaved":
		fmt.Fprint(os.Stdout, "stdout-1\n")
		fmt.Fprint(os.Stderr, "stderr-1\n")
		fmt.Fprint(os.Stdout, "stdout-2\n")
	case "large-stdout":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 4096))
	case "spawn-stdin-holder":
		startExecutorStdinHolder(false)
	case "spawn-stdin-holder-and-wait":
		startExecutorStdinHolder(true)
	case "hold-stdin":
		waitForExecutorStdinHolderCleanup()
	default:
		fmt.Fprint(os.Stderr, "unknown helper mode")
		os.Exit(2)
	}
	os.Exit(0)
}

func startExecutorStdinHolder(wait bool) {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(2)
	}

	cmd := exec.Command(executable, "-test.run=^TestUniversalCommandStdioHelperProcess$")
	cmd.Env = append(os.Environ(),
		executorStdioHelperEnv+"=1",
		executorStdioHelperMode+"=hold-stdin",
	)
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(2)
	}
	if err := cmd.Process.Release(); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(2)
	}

	if readyFile := os.Getenv(executorStdioReadyFile); readyFile != "" {
		if err := os.WriteFile(readyFile, []byte("ready"), 0o600); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
	}
	if wait {
		time.Sleep(10 * time.Second)
	}
}

func waitForExecutorStdinHolderCleanup() {
	cleanupFile := os.Getenv(executorStdioCleanupFile)
	if cleanupFile == "" {
		time.Sleep(executorStdioDescendantLifetime)
		return
	}

	deadline := time.Now().Add(executorStdioDescendantLifetime)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(cleanupFile); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecutorSuppliesStdin(t *testing.T) {
	args := newExecutorStdioCommand("copy-stdin")
	args.Stdin = stringPtr("first line\nsecond line\n")

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if result.Output != *args.Stdin {
		t.Fatalf("captured output = %q, want stdin %q", result.Output, *args.Stdin)
	}
}

func TestExecutorBoundsLiteralStdinWhenDescendantInheritsPipe(t *testing.T) {
	cleanupFile := filepath.Join(t.TempDir(), "cleanup")
	args := newExecutorStdioCommand("spawn-stdin-holder")
	args.Environ[executorStdioCleanupFile] = cleanupFile
	args.Stdin = stringPtr(strings.Repeat("x", 2<<20))
	args.Timeout = 3

	result := waitForExecutorStdioResult(
		t,
		startExecutorStdioCommandWithContext(t, context.Background(), args),
		4*time.Second,
		"literal stdin remained blocked after the command deadline",
		func() { signalExecutorStdioCleanup(t, cleanupFile) },
	)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
}

func TestExecutorCancelsLiteralStdinWhenDescendantInheritsPipe(t *testing.T) {
	readyFile := filepath.Join(t.TempDir(), "ready")
	cleanupFile := filepath.Join(filepath.Dir(readyFile), "cleanup")
	args := newExecutorStdioCommand("spawn-stdin-holder-and-wait")
	args.Environ[executorStdioReadyFile] = readyFile
	args.Environ[executorStdioCleanupFile] = cleanupFile
	args.Stdin = stringPtr(strings.Repeat("x", 2<<20))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := startExecutorStdioCommandWithContext(t, ctx, args)
	waitForExecutorStdioReady(t, readyFile, resultCh, func() {
		signalExecutorStdioCleanup(t, cleanupFile)
	})

	cancel()
	result := waitForExecutorStdioResult(
		t,
		resultCh,
		2*time.Second,
		"literal stdin remained blocked after caller cancellation",
		func() { signalExecutorStdioCleanup(t, cleanupFile) },
	)
	if result.Error == "" {
		t.Fatal("execution succeeded after caller cancellation")
	}
}

func TestExecutorStreamsStdinFileRelativeToWorkdir(t *testing.T) {
	workdir := t.TempDir()
	inputPath := filepath.Join(workdir, "input.txt")
	if err := os.WriteFile(inputPath, []byte("first line\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := newExecutorStdioCommand("copy-stdin")
	args.Workdir = workdir
	args.StdinFile = "input.txt"

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if want := "first line\nsecond line\n"; result.Output != want {
		t.Fatalf("captured output = %q, want %q", result.Output, want)
	}
}

func TestExecutorPreservesSymlinkedWorkdirForStdinParentTraversal(t *testing.T) {
	root := t.TempDir()
	actualParent := filepath.Join(root, "actual")
	actualWorkdir := filepath.Join(actualParent, "workdir")
	lexicalParent := filepath.Join(root, "lexical")
	if err := os.MkdirAll(actualWorkdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lexicalParent, 0o700); err != nil {
		t.Fatal(err)
	}

	workdir := filepath.Join(lexicalParent, "workdir-link")
	if err := os.Symlink(actualWorkdir, workdir); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(actualParent, "input.txt"), []byte("actual input"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lexicalParent, "input.txt"), []byte("lexical input"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := newExecutorStdioCommand("copy-stdin")
	args.Workdir = workdir
	args.StdinFile = "../input.txt"

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if want := "actual input"; result.Output != want {
		t.Fatalf("captured output = %q, want %q", result.Output, want)
	}
}

func TestExecutorRedirectsSingleOutputStream(t *testing.T) {
	t.Run("stdout", func(t *testing.T) {
		workdir := t.TempDir()
		path := filepath.Join(workdir, "stdout.txt")
		if err := os.WriteFile(path, []byte("old contents"), 0o600); err != nil {
			t.Fatal(err)
		}

		args := newExecutorStdioCommand("both")
		args.Workdir = workdir
		args.StdoutFile = "stdout.txt"
		result := runExecutorStdioCommand(t, args)

		if result.Error != "" {
			t.Fatalf("execution error = %q", result.Error)
		}
		if result.Output != "stderr" {
			t.Fatalf("captured output = %q, want stderr only", result.Output)
		}
		assertFileContents(t, path, "stdout")
	})

	t.Run("stderr", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "stderr.txt")
		args := newExecutorStdioCommand("both")
		args.StderrFile = path
		result := runExecutorStdioCommand(t, args)

		if result.Error != "" {
			t.Fatalf("execution error = %q", result.Error)
		}
		if result.Output != "stdout" {
			t.Fatalf("captured output = %q, want stdout only", result.Output)
		}
		assertFileContents(t, path, "stderr")
	})
}

func TestExecutorWhitelistRequiresExactFileRedirectionRule(t *testing.T) {
	workdir := t.TempDir()
	outputPath := filepath.Join(workdir, "output.txt")
	whitelistPath := filepath.Join(workdir, "whitelist.txt")
	args := newExecutorStdioCommand("both")
	args.StdoutFile = outputPath
	rawArgs := marshalCommandArgs(t, args)

	plainRule, err := json.Marshal(args.Command)
	if err != nil {
		t.Fatal(err)
	}
	// environ is part of the shape a grant has to name, so the exact rule the
	// denial suggests carries the helper's environment alongside the redirection.
	exactRule := marshalCommandArgs(t, UniversalCommandArgs{
		Command:    args.Command,
		Environ:    args.Environ,
		StdoutFile: outputPath,
	})
	if err := os.WriteFile(whitelistPath, append(plainRule, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("must remain intact"), 0o600); err != nil {
		t.Fatal(err)
	}

	executor, err := NewExecutor(RequestOptions{
		ToolWhitelist:      whitelistPath,
		ToolNonInteractive: true,
		ToolTimeout:        5 * time.Second,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID: "plain-rule", Name: "universal_command", Args: rawArgs,
	}}, nil)[0]
	if result.Code != errors.ErrCodeDeniedNotWhitelisted {
		t.Fatalf("plain-rule result = %#v, want whitelist denial", result)
	}
	if !strings.Contains(result.Error, string(exactRule)) {
		t.Fatalf("plain-rule denial = %q, want exact redirected rule %s", result.Error, exactRule)
	}
	assertFileContents(t, outputPath, "must remain intact")

	if err := os.WriteFile(whitelistPath, append(exactRule, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	executor, err = NewExecutor(RequestOptions{
		ToolWhitelist:      whitelistPath,
		ToolNonInteractive: true,
		ToolTimeout:        5 * time.Second,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result = executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID: "exact-rule", Name: "universal_command", Args: rawArgs,
	}}, nil)[0]
	if result.Error != "" {
		t.Fatalf("exact-rule execution error = %q", result.Error)
	}
	assertFileContents(t, outputPath, "stdout")
}

func TestExecutorRedirectsOutputStreamsToDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stderrPath := filepath.Join(dir, "stderr.txt")
	args := newExecutorStdioCommand("both")
	args.StdoutFile = stdoutPath
	args.StderrFile = stderrPath

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if result.Output != "" {
		t.Fatalf("captured output = %q, want none", result.Output)
	}
	assertFileContents(t, stdoutPath, "stdout")
	assertFileContents(t, stderrPath, "stderr")
}

func TestExecutorKeepsSymlinkParentTraversalOutputsSeparate(t *testing.T) {
	root := t.TempDir()
	actualParent := filepath.Join(root, "actual")
	actualDir := filepath.Join(actualParent, "dir")
	lexicalParent := filepath.Join(root, "lexical")
	if err := os.MkdirAll(actualDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lexicalParent, 0o700); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(lexicalParent, "dir-link")
	if err := os.Symlink(actualDir, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	separator := string(filepath.Separator)
	stdoutPath := link + separator + ".." + separator + "output.txt"
	actualOutputPath := filepath.Join(actualParent, "output.txt")
	lexicalOutputPath := filepath.Join(lexicalParent, "output.txt")
	if err := os.WriteFile(actualOutputPath, []byte("old actual output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lexicalOutputPath, []byte("old lexical output"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := newExecutorStdioCommand("both")
	args.StdoutFile = stdoutPath
	args.StderrFile = lexicalOutputPath

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	assertFileContents(t, actualOutputPath, "stdout")
	assertFileContents(t, lexicalOutputPath, "stderr")
}

func TestExecutorSharesMatchingOutputFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "combined.txt")
	if err := os.WriteFile(path, []byte("old contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := newExecutorStdioCommand("interleaved")
	args.StdoutFile = path
	args.StderrFile = path

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if result.Output != "" {
		t.Fatalf("captured output = %q, want none", result.Output)
	}
	assertFileContents(t, path, "stdout-1\nstderr-1\nstdout-2\n")
}

func TestExecutorSharesAliasedOutputFile(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.log")
	stderrPath := filepath.Join(dir, "stderr.log")
	if err := os.WriteFile(stdoutPath, []byte("old contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(stdoutPath, stderrPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	args := newExecutorStdioCommand("interleaved")
	args.StdoutFile = stdoutPath
	args.StderrFile = stderrPath

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if result.Output != "" {
		t.Fatalf("captured output = %q, want none", result.Output)
	}
	assertFileContents(t, stdoutPath, "stdout-1\nstderr-1\nstdout-2\n")
	assertFileContents(t, stderrPath, "stdout-1\nstderr-1\nstdout-2\n")
}

func TestExecutorPipesStdinToRedirectedStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "copy.txt")
	args := newExecutorStdioCommand("copy-stdin")
	args.Stdin = stringPtr("copied verbatim\n")
	args.StdoutFile = path

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if result.Output != "" {
		t.Fatalf("captured output = %q, want none", result.Output)
	}
	assertFileContents(t, path, *args.Stdin)
}

func TestExecutorPipesStdinFileToRedirectedStdout(t *testing.T) {
	workdir := t.TempDir()
	inputPath := filepath.Join(workdir, "input.txt")
	outputPath := filepath.Join(workdir, "output.txt")
	if err := os.WriteFile(inputPath, []byte("copied from file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := newExecutorStdioCommand("copy-stdin")
	args.Workdir = workdir
	args.StdinFile = "input.txt"
	args.StdoutFile = "output.txt"

	result := runExecutorStdioCommand(t, args)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if result.Output != "" {
		t.Fatalf("captured output = %q, want none", result.Output)
	}
	assertFileContents(t, outputPath, "copied from file\n")
}

func TestExecutorDoesNotCapRedirectedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	args := newExecutorStdioCommand("large-stdout")
	args.StdoutFile = path

	result := runExecutorStdioCommandWithMaxOutput(t, args, 32)
	if result.Error != "" {
		t.Fatalf("execution error = %q", result.Error)
	}
	if result.Output != "" || result.Truncated {
		t.Fatalf("captured result = %#v, want empty and untruncated", result)
	}
	assertFileContents(t, path, strings.Repeat("x", 4096))
}

func TestExecutorReportsOutputFileOpenError(t *testing.T) {
	args := newExecutorStdioCommand("both")
	args.StdoutFile = filepath.Join(t.TempDir(), "missing", "stdout.txt")

	result := runExecutorStdioCommand(t, args)
	if result.Code != errors.ErrCodeExecError {
		t.Fatalf("error code = %q, want EXEC_ERROR", result.Code)
	}
	if !strings.Contains(result.Error, "open stdout file") {
		t.Fatalf("error = %q, want stdout file context", result.Error)
	}
}

func TestExecutorRejectsLiteralAndFileStdinBeforeApproval(t *testing.T) {
	for _, literal := range []string{"content", ""} {
		t.Run(fmt.Sprintf("literal-%q", literal), func(t *testing.T) {
			args := newExecutorStdioCommand("copy-stdin")
			args.Stdin = stringPtr(literal)
			args.StdinFile = "input.txt"

			result, approvalCalls := runExecutorStdioCommandRequiringApproval(t, args)
			if result.Code != errors.ErrCodeInvalidInput {
				t.Fatalf("error code = %q, want INVALID_INPUT", result.Code)
			}
			if want := "stdin and stdin_file cannot be used together"; result.Error != want {
				t.Fatalf("error = %q, want %q", result.Error, want)
			}
			if approvalCalls != 0 {
				t.Fatalf("approval calls = %d, want 0", approvalCalls)
			}
		})
	}
}

func TestExecutorRejectsStdinFileMatchingOutputBeforeTruncation(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*UniversalCommandArgs, string)
		wantError string
	}{
		{
			name: "stdout",
			configure: func(args *UniversalCommandArgs, _ string) {
				args.StdoutFile = "./input.txt"
			},
			wantError: "stdin_file cannot refer to the same file as stdout_file",
		},
		{
			name: "stderr",
			configure: func(args *UniversalCommandArgs, absolutePath string) {
				args.StderrFile = absolutePath
			},
			wantError: "stdin_file cannot refer to the same file as stderr_file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workdir := t.TempDir()
			inputPath := filepath.Join(workdir, "input.txt")
			if err := os.WriteFile(inputPath, []byte("must remain intact"), 0o600); err != nil {
				t.Fatal(err)
			}

			args := newExecutorStdioCommand("copy-stdin")
			args.Workdir = workdir
			args.StdinFile = "input.txt"
			tt.configure(&args, inputPath)

			result, approvalCalls := runExecutorStdioCommandRequiringApproval(t, args)
			if result.Code != errors.ErrCodeInvalidInput {
				t.Fatalf("error code = %q, want INVALID_INPUT", result.Code)
			}
			if result.Error != tt.wantError {
				t.Fatalf("error = %q, want %q", result.Error, tt.wantError)
			}
			if approvalCalls != 0 {
				t.Fatalf("approval calls = %d, want 0", approvalCalls)
			}
			assertFileContents(t, inputPath, "must remain intact")
		})
	}
}

func TestExecutorRejectsStdinFileAliasingOutput(t *testing.T) {
	workdir := t.TempDir()
	inputPath := filepath.Join(workdir, "input.txt")
	outputPath := filepath.Join(workdir, "output.txt")
	if err := os.WriteFile(inputPath, []byte("must remain intact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(inputPath, outputPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	args := newExecutorStdioCommand("copy-stdin")
	args.Workdir = workdir
	args.StdinFile = "input.txt"
	args.StdoutFile = "output.txt"

	result, approvalCalls := runExecutorStdioCommandRequiringApproval(t, args)
	if result.Code != errors.ErrCodeInvalidInput {
		t.Fatalf("error code = %q, want INVALID_INPUT", result.Code)
	}
	if want := "stdin_file cannot refer to the same file as stdout_file"; result.Error != want {
		t.Fatalf("error = %q, want %q", result.Error, want)
	}
	if approvalCalls != 0 {
		t.Fatalf("approval calls = %d, want 0", approvalCalls)
	}
	assertFileContents(t, inputPath, "must remain intact")
	assertFileContents(t, outputPath, "must remain intact")
}

func TestExecutorRejectsStdinOutputAliasThroughSymlinkedWorkdir(t *testing.T) {
	for _, stream := range stdioStreamSetters[1:] {
		t.Run(stream.name, func(t *testing.T) {
			root := t.TempDir()
			actualParent := filepath.Join(root, "actual")
			actualWorkdir := filepath.Join(actualParent, "workdir")
			lexicalParent := filepath.Join(root, "lexical")
			if err := os.MkdirAll(actualWorkdir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(lexicalParent, 0o700); err != nil {
				t.Fatal(err)
			}

			workdir := filepath.Join(lexicalParent, "workdir-link")
			if err := os.Symlink(actualWorkdir, workdir); err != nil {
				t.Skipf("directory symlinks unavailable: %v", err)
			}
			inputPath := filepath.Join(actualParent, "input.txt")
			lexicalPath := filepath.Join(lexicalParent, "input.txt")
			if err := os.WriteFile(inputPath, []byte("must remain intact"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(lexicalPath, []byte("unrelated file"), 0o600); err != nil {
				t.Fatal(err)
			}

			args := newExecutorStdioCommand("copy-stdin")
			args.Workdir = workdir
			args.StdinFile = "../input.txt"
			stream.set(&args, inputPath)

			result, approvalCalls := runExecutorStdioCommandRequiringApproval(t, args)
			if result.Code != errors.ErrCodeInvalidInput {
				t.Fatalf("error code = %q, want INVALID_INPUT (result: %#v)", result.Code, result)
			}
			wantError := fmt.Sprintf("stdin_file cannot refer to the same file as %s_file", stream.name)
			if result.Error != wantError {
				t.Fatalf("error = %q, want %q", result.Error, wantError)
			}
			if approvalCalls != 0 {
				t.Fatalf("approval calls = %d, want 0", approvalCalls)
			}
			assertFileContents(t, inputPath, "must remain intact")
			assertFileContents(t, lexicalPath, "unrelated file")
		})
	}
}

func TestExecutorRechecksStdinOutputAliasesAfterApproval(t *testing.T) {
	// A hard link is indistinguishable from the original by path, so only the
	// post-open inode comparison catches it. A symlink is caught earlier and
	// more bluntly: redirections may not name one at all, because the path the
	// operator approved would not be the file written.
	linkTypes := []struct {
		name      string
		link      func(string, string) error
		wantError func(stream, path string) string
	}{
		{
			name: "hard-link",
			link: os.Link,
			wantError: func(stream, _ string) string {
				return fmt.Sprintf("stdin_file cannot refer to the same file as %s_file", stream)
			},
		},
		{
			name: "symlink",
			link: os.Symlink,
			wantError: func(stream, path string) string {
				return fmt.Sprintf("open %s file %q: %q is a symbolic link", stream, path, path)
			},
		},
	}
	for _, linkType := range linkTypes {
		for _, stream := range stdioStreamSetters[1:] {
			t.Run(linkType.name+"-"+stream.name, func(t *testing.T) {
				workdir := t.TempDir()
				inputPath := filepath.Join(workdir, "input.txt")
				outputPath := filepath.Join(workdir, "output.txt")
				if err := os.WriteFile(inputPath, []byte("must remain intact"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(outputPath, []byte("old output"), 0o600); err != nil {
					t.Fatal(err)
				}

				args := newExecutorStdioCommand("copy-stdin")
				args.StdinFile = inputPath
				stream.set(&args, outputPath)
				rawArgs := marshalCommandArgs(t, args)

				var approvalCalls int
				var replacementErr error
				approver := stdioApprover{approve: func(context.Context, UniversalCommandArgs) (bool, error) {
					approvalCalls++
					if err := os.Remove(outputPath); err != nil {
						replacementErr = err
						return false, err
					}
					replacementErr = linkType.link(inputPath, outputPath)
					return replacementErr == nil, replacementErr
				}}

				executor := newStdioTestExecutor(t, 0, approver)

				result := executor.ExecuteParallel(context.Background(), []ToolCall{{
					ID:   "stdio-alias-race-test",
					Name: "universal_command",
					Args: rawArgs,
				}}, TestToolUI{})[0]
				if replacementErr != nil {
					t.Skipf("%s unavailable: %v", linkType.name, replacementErr)
				}

				if approvalCalls != 1 {
					t.Fatalf("approval calls = %d, want 1", approvalCalls)
				}
				if result.Code != errors.ErrCodeExecError {
					t.Fatalf("error code = %q, want EXEC_ERROR (result: %#v)", result.Code, result)
				}
				wantError := linkType.wantError(stream.name, outputPath)
				if result.Error != wantError {
					t.Fatalf("error = %q, want %q", result.Error, wantError)
				}
				assertFileContents(t, inputPath, "must remain intact")
				assertFileContents(t, outputPath, "must remain intact")
			})
		}
	}
}

func TestExecutorOpensStdinFileBeforeOutputFiles(t *testing.T) {
	workdir := t.TempDir()
	outputPath := filepath.Join(workdir, "output.txt")
	if err := os.WriteFile(outputPath, []byte("must remain intact"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := newExecutorStdioCommand("copy-stdin")
	args.Workdir = workdir
	args.StdinFile = "missing.txt"
	args.StdoutFile = "output.txt"

	result := runExecutorStdioCommand(t, args)
	if result.Code != errors.ErrCodeExecError {
		t.Fatalf("error code = %q, want EXEC_ERROR", result.Code)
	}
	if !strings.Contains(result.Error, "open stdin file") {
		t.Fatalf("error = %q, want stdin file context", result.Error)
	}
	assertFileContents(t, outputPath, "must remain intact")
}

// commandStartFailure is one way a command cannot start. os/exec raises every
// one of these from Start, which runs after the output file has been emptied,
// so each is also a way to destroy a file on behalf of a command that never
// ran.
type commandStartFailure struct {
	name string
	// mutate turns a working call into an unstartable one, using root as a
	// scratch directory it may populate.
	mutate func(t *testing.T, args *UniversalCommandArgs, root string)
	// wantErr is a substring of the failure, and is deliberately the wording
	// Start itself uses: the point of the preflight is to reach the same
	// verdict earlier, not a different one.
	wantErr string
}

// commandStartFailureCases enumerates what verifyCommandCanStart has to catch.
// Only the first case is caught by exec.Command's own PATH lookup, and only
// because a bare name is the one shape it looks up; everything below it reached
// Start with the file already truncated.
func commandStartFailureCases() []commandStartFailure {
	return []commandStartFailure{
		{
			name: "bare name not on PATH",
			mutate: func(_ *testing.T, args *UniversalCommandArgs, _ string) {
				args.Command = []string{"lmc-no-such-command-xyz"}
			},
			wantErr: "executable file not found",
		},
		{
			name: "relative name carrying a separator",
			mutate: func(_ *testing.T, args *UniversalCommandArgs, _ string) {
				args.Command = []string{"." + string(filepath.Separator) + "lmc-no-such-command-xyz"}
			},
			wantErr: "no such file or directory",
		},
		{
			name: "absolute name that does not exist",
			mutate: func(_ *testing.T, args *UniversalCommandArgs, root string) {
				args.Command = []string{filepath.Join(root, "definitely-absent")}
			},
			wantErr: "no such file or directory",
		},
		{
			name: "relative name resolved against workdir",
			mutate: func(_ *testing.T, args *UniversalCommandArgs, root string) {
				args.Workdir = root
				args.Command = []string{"." + string(filepath.Separator) + "definitely-absent"}
			},
			wantErr: "no such file or directory",
		},
		{
			name: "file without an executable bit",
			mutate: func(t *testing.T, args *UniversalCommandArgs, root string) {
				path := filepath.Join(root, "data.txt")
				if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				args.Command = []string{path}
			},
			wantErr: "permission denied",
		},
		{
			name: "name that is a directory",
			mutate: func(_ *testing.T, args *UniversalCommandArgs, root string) {
				args.Command = []string{root}
			},
			wantErr: "permission denied",
		},
		{
			name:   "workdir that cannot be entered",
			mutate: lockUnenterableWorkdir,
			// Start blames the executable for this one ("fork/exec <argv0>:
			// permission denied"); the preflight names the directory, which is
			// the thing the operator can fix.
			wantErr: "permission denied",
		},
		{
			name: "workdir that does not exist",
			mutate: func(_ *testing.T, args *UniversalCommandArgs, root string) {
				args.Workdir = filepath.Join(root, "definitely-absent")
			},
			// Start blames the executable here too; the preflight names the
			// missing directory.
			wantErr: "no such file or directory",
		},
		{
			name: "NUL byte in argv",
			mutate: func(_ *testing.T, args *UniversalCommandArgs, _ string) {
				args.Command = append(args.Command, "trailing\x00argument")
			},
			wantErr: "invalid argument",
		},
		{
			name: "NUL byte in environ",
			mutate: func(_ *testing.T, args *UniversalCommandArgs, _ string) {
				args.Environ["LMTOOLS_NUL\x00KEY"] = "value"
			},
			wantErr: "environment variable contains NUL",
		},
	}
}

// lockUnenterableWorkdir builds a directory that stats as a directory and
// refuses to be entered, which is the gap between "is a directory" and "chdir
// will work". Root ignores the missing search bit, so there is nothing to test
// as root.
func lockUnenterableWorkdir(t *testing.T, args *UniversalCommandArgs, root string) {
	if os.Getuid() == 0 {
		t.Skip("running as root: a missing search bit does not deny chdir")
	}
	workdir := filepath.Join(root, "locked")
	if err := os.Mkdir(workdir, 0o700); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	if err := os.Chmod(workdir, 0o600); err != nil {
		t.Fatal(err)
	}
	// t.TempDir's cleanup has to walk back in.
	t.Cleanup(func() { _ = os.Chmod(workdir, 0o700) })
	args.Workdir = workdir
}

// A command that cannot start must not empty the file it was going to write.
func TestOutputFileSurvivesEveryPreflightedStartFailure(t *testing.T) {
	for _, tt := range commandStartFailureCases() {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "important.txt")
			if err := os.WriteFile(output, []byte("PRECIOUS DATA"), 0o600); err != nil {
				t.Fatal(err)
			}

			args := newExecutorStdioCommand("both")
			args.Timeout = 5
			args.StdoutFile = output
			tt.mutate(t, &args, root)

			result := runExecutorStdioCommand(t, args)
			if result.Code != errors.ErrCodeExecError {
				t.Fatalf("result = %#v, want EXEC_ERROR", result)
			}
			if !strings.Contains(result.Error, tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", result.Error, tt.wantErr)
			}
			assertFileContents(t, output, "PRECIOUS DATA")
		})
	}
}

// The preflight is only worth anything if it agrees with Start, so every case
// is asserted twice: verifyCommandCanStart refuses the command, and starting
// the same command refuses it too. The second half is what keeps the table
// honest as os/exec changes.
func TestVerifyCommandCanStartAgreesWithStart(t *testing.T) {
	for _, tt := range commandStartFailureCases() {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			args := newExecutorStdioCommand("both")
			tt.mutate(t, &args, root)

			err := verifyCommandCanStart(commandForStdioArgs(args))
			if err == nil {
				t.Fatal("verifyCommandCanStart() = nil, want a rejection")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("verifyCommandCanStart() error = %q, want it to contain %q", err, tt.wantErr)
			}

			started := commandForStdioArgs(args)
			if err := started.Start(); err == nil {
				_ = started.Process.Kill()
				_ = started.Wait()
				t.Fatal("Start() = nil; this case no longer describes a command that cannot start")
			}
		})
	}
}

// The other half of the preflight's contract: it may only refuse what Start
// would refuse. Resolving argv[0] against the workdir rather than the process
// directory is the part that is easy to get wrong in the rejecting direction,
// and nothing else here would notice.
func TestVerifyCommandCanStartAcceptsCommandsThatStart(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(nested, "runnable.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	relative := "." + string(filepath.Separator) + "runnable.sh"

	tests := []struct {
		name string
		args UniversalCommandArgs
	}{
		{name: "absolute executable", args: UniversalCommandArgs{Command: []string{script}}},
		{name: "bare name resolved on PATH", args: UniversalCommandArgs{Command: []string{"true"}}},
		{
			name: "relative executable resolved against workdir",
			args: UniversalCommandArgs{Command: []string{relative}, Workdir: nested},
		},
	}

	link := filepath.Join(root, "nested-link")
	if err := os.Symlink(nested, link); err == nil {
		tests = append(tests, struct {
			name string
			args UniversalCommandArgs
		}{
			name: "workdir naming a directory symlink",
			args: UniversalCommandArgs{Command: []string{relative}, Workdir: link},
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := verifyCommandCanStart(commandForStdioArgs(tt.args)); err != nil {
				t.Fatalf("verifyCommandCanStart() = %v, want nil", err)
			}
			if err := commandForStdioArgs(tt.args).Run(); err != nil {
				t.Fatalf("Run() = %v; the case does not describe a command that starts", err)
			}
		})
	}
}

// commandForStdioArgs builds the exec.Cmd the executor builds for these
// arguments. It mirrors executeCommand rather than calling it, because
// executeCommand runs the command; keeping the two in one place would mean
// exporting a seam from a file this change does not own.
func commandForStdioArgs(args UniversalCommandArgs) *exec.Cmd {
	cmd := exec.Command(args.Command[0], args.Command[1:]...)
	cmd.Dir = args.Workdir
	cmd.Env = os.Environ()
	for key, value := range args.Environ {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
	}
	return cmd
}

func newExecutorStdioCommand(mode string) UniversalCommandArgs {
	executable, err := os.Executable()
	if err != nil {
		executable = os.Args[0]
	}
	return UniversalCommandArgs{
		Command: []string{executable, "-test.run=^TestUniversalCommandStdioHelperProcess$"},
		Environ: map[string]string{
			executorStdioHelperEnv:  "1",
			executorStdioHelperMode: mode,
		},
	}
}

func marshalCommandArgs(t *testing.T, args UniversalCommandArgs) json.RawMessage {
	t.Helper()
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal command args: %v", err)
	}
	return rawArgs
}

// newStdioTestExecutor builds the one-command executor these tests share. A
// nil approver runs auto-approved and non-interactive; providing one routes
// every call through it instead.
func newStdioTestExecutor(t *testing.T, maxOutput int, approver Approver) *Executor {
	t.Helper()
	executor, err := NewExecutor(RequestOptions{
		ToolTimeout:        5 * time.Second,
		ToolAutoApprove:    approver == nil,
		ToolNonInteractive: approver == nil,
		ToolMaxOutputBytes: maxOutput,
	}, nil, approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func runExecutorStdioCommand(t *testing.T, args UniversalCommandArgs) ToolResult {
	t.Helper()
	return runExecutorStdioCommandWithMaxOutput(t, args, 1024*1024)
}

func runExecutorStdioCommandWithMaxOutput(t *testing.T, args UniversalCommandArgs, maxOutput int) ToolResult {
	t.Helper()
	executor := newStdioTestExecutor(t, maxOutput, nil)

	results := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "stdio-test",
		Name: "universal_command",
		Args: marshalCommandArgs(t, args),
	}}, nil)
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	return results[0]
}

func startExecutorStdioCommandWithContext(t *testing.T, ctx context.Context, args UniversalCommandArgs) <-chan ToolResult {
	t.Helper()
	rawArgs := marshalCommandArgs(t, args)
	executor := newStdioTestExecutor(t, 1024*1024, nil)

	resultCh := make(chan ToolResult, 1)
	go func() {
		results := executor.ExecuteParallel(ctx, []ToolCall{{
			ID:   "stdio-context-test",
			Name: "universal_command",
			Args: rawArgs,
		}}, nil)
		resultCh <- results[0]
	}()
	return resultCh
}

func waitForExecutorStdioReady(t *testing.T, path string, resultCh <-chan ToolResult, cleanup func()) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case result := <-resultCh:
			cleanup()
			t.Fatalf("command returned before spawning its descendant: %#v", result)
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return
			} else if !os.IsNotExist(err) {
				t.Fatalf("stat ready file: %v", err)
			}
		case <-timer.C:
			cleanup()
			t.Fatal("command did not report that its descendant was ready")
		}
	}
}

func waitForExecutorStdioResult(
	t *testing.T,
	resultCh <-chan ToolResult,
	bound time.Duration,
	failure string,
	cleanup func(),
) ToolResult {
	t.Helper()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		cleanup()
		return result
	case <-timer.C:
	}

	// Release the helper descendant and reap the executor result before failing
	// so a regression does not leave a blocked worker in the test process.
	cleanup()
	cleanupTimer := time.NewTimer(2 * time.Second)
	defer cleanupTimer.Stop()
	select {
	case <-resultCh:
		t.Fatal(failure)
	case <-cleanupTimer.C:
		t.Fatalf("%s; worker was still blocked after cleanup deadline", failure)
	}
	return ToolResult{}
}

func signalExecutorStdioCleanup(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("cleanup"), 0o600); err != nil {
		t.Fatalf("signal descendant cleanup: %v", err)
	}
}

func runExecutorStdioCommandRequiringApproval(t *testing.T, args UniversalCommandArgs) (ToolResult, int) {
	t.Helper()
	approver := NewTestApprover(true)
	executor := newStdioTestExecutor(t, 0, approver)

	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "stdio-validation-test",
		Name: "universal_command",
		Args: marshalCommandArgs(t, args),
	}}, TestToolUI{})[0]
	return result, len(approver.ApprovalCalls)
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(contents) != want {
		t.Fatalf("%s contents = %q, want %q", path, contents, want)
	}
}
