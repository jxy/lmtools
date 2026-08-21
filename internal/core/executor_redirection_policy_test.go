package core

import (
	"context"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symlink in an approved workdir must not become a write to whatever it
// points at. The approval line shows the path the model asked for, so a final
// component that resolves elsewhere writes somewhere nobody reviewed.
func TestExecutorRefusesSymlinkedRedirectionTargets(t *testing.T) {
	for _, stream := range stdioStreamSetters {
		for _, dangling := range []bool{false, true} {
			name := stream.name
			if dangling {
				name += "-dangling"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				secret := filepath.Join(root, "protected.txt")
				link := filepath.Join(root, "innocuous.txt")

				if !dangling {
					if err := os.WriteFile(secret, []byte("ORIGINAL"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(secret, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}

				args := newExecutorStdioCommand("both")
				args.Timeout = 5
				stream.set(&args, link)

				result := runExecutorStdioCommand(t, args)
				if result.Code != errors.ErrCodeExecError {
					t.Fatalf("code = %q, want %s (result %#v)", result.Code, errors.ErrCodeExecError, result)
				}
				if !strings.Contains(result.Error, "is a symbolic link") {
					t.Fatalf("error = %q, want a symlink rejection", result.Error)
				}

				// A dangling link must not be followed into existence either;
				// O_CREATE through one creates the target, not the link.
				if _, err := os.Lstat(secret); dangling && err == nil {
					t.Fatal("dangling symlink target was created")
				}
				if !dangling {
					assertFileContents(t, secret, "ORIGINAL")
				}
			})
		}
	}
}

// A rule that names no stdin does not grant stdin. Without this, every
// interpreter already sitting in an operator's whitelist became a way to run
// arbitrary code the moment the stdin channel was added.
func TestWhitelistDoesNotGrantLiteralStdinImplicitly(t *testing.T) {
	whitelist, err := parseCommandRule(`["python3"]`, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{whitelist: []commandRule{whitelist}, canPrompt: false}

	payload := "import os;os.system('id')"
	got := policy.decide(UniversalCommandArgs{Command: []string{"python3"}, Stdin: &payload})
	if got != decisionDenyNotWhitelisted {
		t.Fatalf("decide() with literal stdin = %v, want %v", got, decisionDenyNotWhitelisted)
	}
	if got := policy.decide(UniversalCommandArgs{Command: []string{"python3"}}); got != decisionAllow {
		t.Fatalf("decide() without stdin = %v, want allow", got)
	}
}

// An operator who does want it can say so, and the denial tells them how.
func TestObjectRuleCanGrantLiteralStdin(t *testing.T) {
	granted, err := parseCommandRule(`{"command":["python3"],"stdin":true}`, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{whitelist: []commandRule{granted}, canPrompt: false}

	payload := "print(1)"
	if got := policy.decide(UniversalCommandArgs{Command: []string{"python3"}, Stdin: &payload}); got != decisionAllow {
		t.Fatalf("decide() = %v, want allow", got)
	}
	// The grant is for the stdin channel only; a redirection it never named is
	// still outside it.
	if got := policy.decide(UniversalCommandArgs{
		Command:    []string{"python3"},
		Stdin:      &payload,
		StdoutFile: "out.txt",
	}); got != decisionDenyNotWhitelisted {
		t.Fatalf("decide() with extra redirection = %v, want denial", got)
	}

	suggestion := suggestedCommandRuleJSON(&UniversalCommandArgs{
		Command: []string{"python3"},
		Stdin:   &payload,
	})
	if suggestion != `{"command":["python3"],"stdin":true}` {
		t.Fatalf("suggested rule = %s, want a stdin grant that parses", suggestion)
	}
	if _, err := parseCommandRule(suggestion, matchBareCommandOnly); err != nil {
		t.Fatalf("suggested rule does not parse: %v", err)
	}
}

// The suggested rule sits directly beneath an approval line that printed the
// command as written, so it must not HTML-escape what that line did not.
func TestSuggestedCommandRuleIsNotHTMLEscaped(t *testing.T) {
	suggestion := suggestedCommandRuleJSON(&UniversalCommandArgs{
		Command: []string{"sh", "-c", "make build && ./run > out 2>&1"},
	})
	if want := `["sh","-c","make build && ./run > out 2>&1"]`; suggestion != want {
		t.Fatalf("suggested rule = %s, want %s", suggestion, want)
	}
	if _, err := parseCommandRule(suggestion, matchBareCommandOnly); err != nil {
		t.Fatalf("suggested rule does not parse: %v", err)
	}
}

// A denial must not be escapable by adding a field it never mentioned. An exact
// match here meant {"command":["tee"],"stdout_file":"/etc/hosts"} stopped that
// call and nothing else.
func TestObjectBlacklistRuleDeniesSupersetCalls(t *testing.T) {
	denial, err := parseCommandRule(`{"command":["tee"],"stdout_file":"/etc/hosts"}`, matchAnyCall)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{blacklist: []commandRule{denial}, autoApprove: true}

	denied := []UniversalCommandArgs{
		{Command: []string{"tee"}, StdoutFile: "/etc/hosts"},
		{Command: []string{"tee"}, StdoutFile: "/etc/hosts", StderrFile: "/tmp/e"},
		{Command: []string{"tee", "-a"}, StdoutFile: "/etc/hosts", Workdir: "/tmp"},
	}
	for i, args := range denied {
		if got := policy.decide(args); got != decisionDenyBlacklist {
			t.Fatalf("denied[%d] decision = %v, want blacklist denial", i, got)
		}
	}

	// Subset matching narrows on the fields the rule names; a different target
	// is a different call and is not what this rule denies.
	if got := policy.decide(UniversalCommandArgs{
		Command:    []string{"tee"},
		StdoutFile: "/tmp/other",
	}); got != decisionAllow {
		t.Fatalf("unrelated target decision = %v, want allow", got)
	}
}

// A grant stays exact in the other direction: a field the rule left out must be
// absent from the call, or the rule would authorize a redirection nobody wrote.
func TestObjectWhitelistRuleStaysExact(t *testing.T) {
	grant, err := parseCommandRule(`{"command":["tee"],"stdout_file":"out.txt"}`, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{whitelist: []commandRule{grant}, canPrompt: false}

	if got := policy.decide(UniversalCommandArgs{Command: []string{"tee"}, StdoutFile: "out.txt"}); got != decisionAllow {
		t.Fatalf("exact call decision = %v, want allow", got)
	}
	if got := policy.decide(UniversalCommandArgs{
		Command:    []string{"tee"},
		StdoutFile: "out.txt",
		StderrFile: "err.txt",
	}); got != decisionDenyNotWhitelisted {
		t.Fatalf("superset call decision = %v, want denial", got)
	}
}

// Two commands writing one file have independent offsets and each truncates the
// other. There is no ordering that makes it safe, so the round refuses both.
func TestBatchRejectsCommandsSharingAnOutputFile(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "out.txt")

	call := func(id, text, stdout string) ToolCall {
		args := UniversalCommandArgs{
			Command:    []string{"/bin/echo", text},
			StdoutFile: stdout,
			Timeout:    5,
		}
		return ToolCall{ID: id, Name: "universal_command", Args: marshalCommandArgs(t, args)}
	}

	executor := newStdioTestExecutor(t, 0, nil)

	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		call("first", "alpha", shared),
		call("second", "beta", shared),
		call("third", "gamma", filepath.Join(root, "other.txt")),
	}, TestToolUI{})

	for i := 0; i < 2; i++ {
		if results[i].Code != errors.ErrCodeInvalidInput {
			t.Fatalf("results[%d] = %#v, want %s", i, results[i], errors.ErrCodeInvalidInput)
		}
		if !strings.Contains(results[i].Error, "also writes to") {
			t.Fatalf("results[%d] error = %q, want a shared-output rejection", i, results[i].Error)
		}
	}
	// The unrelated call in the same batch is untouched by the collision.
	if results[2].Error != "" {
		t.Fatalf("results[2] = %#v, want success", results[2])
	}
	assertFileContents(t, filepath.Join(root, "other.txt"), "gamma\n")
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Fatalf("shared output was created despite rejection (err = %v)", err)
	}
}

// The same file reached by two spellings is still the same file.
func TestBatchDetectsSharedOutputThroughDifferentPaths(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "out.txt")
	if err := os.WriteFile(shared, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	prepared := []preparedExecution{
		{index: 0, id: "a", args: &UniversalCommandArgs{Command: []string{"echo"}, StdoutFile: shared}},
		{index: 1, id: "b", args: &UniversalCommandArgs{
			Command: []string{"echo"}, Workdir: root, StderrFile: "out.txt",
		}},
	}
	results := make([]ToolResult, 2)

	remaining := rejectSharedOutputFiles(prepared, results)
	if len(remaining) != 0 {
		t.Fatalf("remaining = %d, want both calls rejected", len(remaining))
	}
	for i, result := range results {
		if result.Code != errors.ErrCodeInvalidInput {
			t.Fatalf("results[%d] = %#v, want %s", i, result, errors.ErrCodeInvalidInput)
		}
	}
}

// os.SameFile can only answer for a file that already exists, and an output
// file is one this round is about to create — so for the case the check exists
// for, the resolved strings were the whole check. resolveCommandFilePath never
// cleans (the open must use the path the operator was shown), so "out.log" and
// "./out.log" under one workdir went through as two files, and each command
// truncated the other's output while both reported success.
func TestBatchDetectsSharedOutputThroughUncreatedAliases(t *testing.T) {
	root := t.TempDir()

	aliases := []struct {
		name         string
		first, other UniversalCommandArgs
	}{
		{
			name:  "dot slash prefix",
			first: UniversalCommandArgs{Workdir: root, StdoutFile: "out.log"},
			other: UniversalCommandArgs{Workdir: root, StdoutFile: "./out.log"},
		},
		{
			name:  "traversal through an ordinary directory",
			first: UniversalCommandArgs{Workdir: root, StdoutFile: "out.log"},
			other: UniversalCommandArgs{Workdir: root, StdoutFile: "sub/../out.log"},
		},
		{
			name:  "doubled separator",
			first: UniversalCommandArgs{Workdir: root, StdoutFile: "out.log"},
			other: UniversalCommandArgs{Workdir: root + "/", StdoutFile: "./out.log"},
		},
		{
			name:  "absolute against workdir-relative",
			first: UniversalCommandArgs{StdoutFile: filepath.Join(root, "out.log")},
			other: UniversalCommandArgs{Workdir: root, StderrFile: "out.log"},
		},
	}

	for _, tt := range aliases {
		t.Run(tt.name, func(t *testing.T) {
			first, other := tt.first, tt.other
			first.Command = []string{"echo"}
			other.Command = []string{"echo"}

			prepared := []preparedExecution{
				{index: 0, id: "a", args: &first},
				{index: 1, id: "b", args: &other},
			}
			results := make([]ToolResult, 2)

			remaining := rejectSharedOutputFiles(prepared, results)
			if len(remaining) != 0 {
				t.Fatalf("remaining = %d, want both calls rejected", len(remaining))
			}
			for i, result := range results {
				if result.Code != errors.ErrCodeInvalidInput {
					t.Fatalf("results[%d] = %#v, want %s", i, result, errors.ErrCodeInvalidInput)
				}
				if !strings.Contains(result.Error, "also writes to") {
					t.Fatalf("results[%d] error = %q, want a shared-output rejection", i, result.Error)
				}
			}
		})
	}
}

// End to end, because the aliased pair passed the preflight and both commands
// really did run: the file ended up holding an interleaving of the two, and
// both results said success.
func TestBatchRejectsAliasedOutputFilesBeforeAnythingRuns(t *testing.T) {
	root := t.TempDir()

	call := func(id, text, stdout string) ToolCall {
		return ToolCall{ID: id, Name: UniversalCommandToolName, Args: marshalCommandArgs(t, UniversalCommandArgs{
			Command:    []string{"/bin/echo", text},
			Workdir:    root,
			StdoutFile: stdout,
			Timeout:    5,
		})}
	}

	executor := newStdioTestExecutor(t, 0, nil)

	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		call("first", strings.Repeat("A", 20), "out.log"),
		call("second", strings.Repeat("B", 20), "./out.log"),
	}, TestToolUI{})

	for i, result := range results {
		if result.Code != errors.ErrCodeInvalidInput {
			t.Fatalf("results[%d] = %#v, want %s", i, result, errors.ErrCodeInvalidInput)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "out.log")); !os.IsNotExist(err) {
		t.Fatalf("the aliased output file was created despite the rejection (err = %v)", err)
	}
}

// A reader loses this race even more quietly than a second writer does. The
// writer's O_CREATE|O_TRUNC empties the file whichever worker gets there first,
// so the reader is handed nothing — or the writer's bytes — and told it
// succeeded. validateCommandIOArgs already refuses the same aliasing inside one
// call; splitting the two halves across two calls used to escape it entirely,
// because only output files were claimed.
func TestBatchRejectsOneCallsInputBeingAnotherCallsOutput(t *testing.T) {
	const original = "ORIGINAL INPUT"

	for _, tt := range []struct {
		name         string
		writerStream func(*UniversalCommandArgs, string)
	}{
		{
			name:         "against stdout_file",
			writerStream: func(args *UniversalCommandArgs, path string) { args.StdoutFile = path },
		},
		{
			name:         "against stderr_file",
			writerStream: func(args *UniversalCommandArgs, path string) { args.StderrFile = path },
		},
		{
			name:         "through an alias of the same path",
			writerStream: func(args *UniversalCommandArgs, path string) { args.StdoutFile = "./" + filepath.Base(path) },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data.txt")
			if err := os.WriteFile(data, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}

			reader := UniversalCommandArgs{
				Command:   []string{"/bin/cat"},
				Workdir:   root,
				StdinFile: "data.txt",
				Timeout:   5,
			}
			writer := UniversalCommandArgs{
				Command: []string{"/bin/echo", "clobbered"},
				Workdir: root,
				Timeout: 5,
			}
			tt.writerStream(&writer, data)

			executor := newStdioTestExecutor(t, 0, nil)

			results := executor.ExecuteParallel(context.Background(), []ToolCall{
				{ID: "reader", Name: UniversalCommandToolName, Args: marshalCommandArgs(t, reader)},
				{ID: "writer", Name: UniversalCommandToolName, Args: marshalCommandArgs(t, writer)},
			}, TestToolUI{})

			for i, result := range results {
				if result.Code != errors.ErrCodeInvalidInput {
					t.Fatalf("results[%d] = %#v, want %s", i, result, errors.ErrCodeInvalidInput)
				}
				if !strings.Contains(result.Error, "read by one command in this round and written by another") {
					t.Fatalf("results[%d] error = %q, want it to name the reader/writer collision", i, result.Error)
				}
			}
			// Neither call ran, so the input is exactly as the round found it.
			assertFileContents(t, data, original)
		})
	}
}

// Two readers of one file do not interfere, and the preflight must not start
// rejecting rounds that were always safe.
func TestBatchAllowsTwoCallsReadingOneInputFile(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data.txt")
	if err := os.WriteFile(data, []byte("shared input\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	read := func(id, stdin string) ToolCall {
		return ToolCall{ID: id, Name: UniversalCommandToolName, Args: marshalCommandArgs(t, UniversalCommandArgs{
			Command:   []string{"/bin/cat"},
			Workdir:   root,
			StdinFile: stdin,
			Timeout:   5,
		})}
	}

	executor := newStdioTestExecutor(t, 0, nil)

	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		read("first", "data.txt"),
		read("second", "./data.txt"),
	}, TestToolUI{})

	for i, result := range results {
		if result.Error != "" {
			t.Fatalf("results[%d] = %#v, want success", i, result)
		}
		if result.Output != "shared input\n" {
			t.Fatalf("results[%d] output = %q, want the file contents", i, result.Output)
		}
	}
}
