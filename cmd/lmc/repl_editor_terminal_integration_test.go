//go:build integration && (darwin || freebsd || linux)

package main

import (
	"errors"
	"lmtools/internal/constants"
	"lmtools/internal/mockserver"
	"lmtools/internal/ptytest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

type testEditor struct{ control, log string }

func shellEditorWord(text string) string { return "'" + strings.ReplaceAll(text, "'", "'\"'\"'") + "'" }

func newTestEditor(t *testing.T) testEditor {
	t.Helper()
	dir := t.TempDir()
	h := testEditor{filepath.Join(dir, "control"), filepath.Join(dir, "editor log")}
	script := filepath.Join(dir, "editor with spaces.sh")
	body := `#!/bin/sh
[ "$1" = 'argument with spaces' ] || exit 9
printf '%s\n' "$4" > "$3"
cp "$4" "$3.seed"
: > "$4~"
: > "$(dirname "$4")/.swap"
mode=$(cat "$2")
case "$mode" in
  success) printf draft > "$4" ;;
  append) printf '[reopened]' >> "$4" ;;
  identity) ;;
  fail) stty raw -echo; printf changed > "$4"; exit 7 ;;
  symlink) rm "$4"; ln -s "$2" "$4" ;;
  oversized) dd if=/dev/zero of="$4" bs=__OVERSIZE__ count=1 2>/dev/null ;;
  hold) printf 'EDITOR_HOLD\n'; sleep 1; printf held > "$4" ;;
  wait) trap 'printf "INT_HANDLED\n"; exit 130' INT
        printf 'EDITOR_WAIT\n'; while ! read answer; do :; done; printf '%s' "$answer" > "$4" ;;
  stop) printf 'EDITOR_STOP\n'; kill -STOP $$; printf 'EDITOR_RESUMED\n'; while ! read answer; do :; done; printf '%s' "$answer" > "$4" ;;
  empty) : > "$4" ;;
  *) exit 8 ;;
esac
`
	body = strings.ReplaceAll(body, "__OVERSIZE__", strconv.Itoa(constants.MaxCLIInputSize+1))
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", shellEditorWord(script)+" 'argument with spaces' "+shellEditorWord(h.control)+" "+shellEditorWord(h.log))
	h.mode(t, "success")
	return h
}

func (h testEditor) mode(t *testing.T, mode string) {
	t.Helper()
	if err := os.WriteFile(h.control, []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h testEditor) seed(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.log + ".seed")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (h testEditor) cleaned(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(h.log)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(strings.TrimSpace(string(data)))
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("editor temporary directory remains: %s (err %v)", dir, err)
	}
}

func editorReviewPrompt(t *testing.T, r *replUnderTerminal) {
	t.Helper()
	r.expect(t, "Enter sends the draft;")
	r.expect(t, freshPrompt)
}

func TestTerminalREPLEditorDraftReopenAppendAndCleanup(t *testing.T) {
	_ = getLmcBinary(t)
	temp := filepath.Join(t.TempDir(), "temporary files with spaces")
	if err := os.Mkdir(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temp)
	h := newTestEditor(t)
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/ed\n")
	r.expect(t, `Draft (5 bytes): "draft"`)
	editorReviewPrompt(t, r)
	h.cleaned(t)
	if len(server.GetRequests()) != 0 {
		t.Fatal("editor success caused inference before confirmation")
	}
	h.mode(t, "append")
	r.typeKeys(t, "/ed\n")
	r.expect(t, `"draft[reopened]"`)
	editorReviewPrompt(t, r)
	if h.seed(t) != "draft" || r.nonblocking(t) {
		t.Fatal("editor did not reopen the draft or changed stdin's blocking description")
	}
	r.typeKeys(t, " plus\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit %d: %s", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 1 || !strings.Contains(requests[0].Body, `"draft[reopened] plus"`) {
		t.Fatalf("requests=%v", requests)
	}
	h.cleaned(t)
}

func TestTerminalREPLEditorHistoryRegexRequiresFreshEnter(t *testing.T) {
	h := newTestEditor(t)
	h.mode(t, "identity")
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "match first\nmatch second\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/ed ^match \n\nqueued prompt\n")
	r.expect(t, `"match first\n\nmatch second"`)
	editorReviewPrompt(t, r)
	if len(server.GetRequests()) != 2 || h.seed(t) != "match first\n\nmatch second" {
		t.Fatalf("queued input bypassed review or wrong seed: requests=%v, seed=%q", server.GetRequests(), h.seed(t))
	}
	h.mode(t, "hold")
	r.typeKeys(t, "/ed 1\n")
	r.expect(t, "EDITOR_HOLD")
	r.typeKeys(t, "\nqueued during editor\n")
	r.expect(t, `Draft (4 bytes): "held"`)
	editorReviewPrompt(t, r)
	if len(server.GetRequests()) != 2 {
		t.Fatal("input typed during the editor caused inference")
	}
	r.typeKeys(t, "/ed [\n/ed 999\n/ed no-match\n")
	r.expect(t, "invalid history regular expression")
	r.expect(t, "prompt 999 is unavailable")
	r.expect(t, "no retained history prompts match")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\n")
	r.expect(t, "answer 3")
	r.expect(t, freshPrompt)
	h.mode(t, "identity")
	r.typeKeys(t, "unfinished\\\n/ed\n")
	r.expect(t, `"unfinished"`)
	editorReviewPrompt(t, r)
	if h.seed(t) != "unfinished" {
		t.Fatalf("continuation editor seed=%q", h.seed(t))
	}
	r.typeKeys(t, ptytest.Interrupt)
	r.expect(t, "Discarded the draft.")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d, screen=%q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 3 || !strings.Contains(requests[2].Body, `"held"`) {
		t.Fatalf("invalid selection changed draft: %v", requests)
	}
	h.cleaned(t)
}

func TestTerminalREPLEditorFailuresKeepDraftAndRestoreTerminal(t *testing.T) {
	for _, failure := range []string{"fail", "symlink", "oversized"} {
		t.Run(failure, func(t *testing.T) {
			h := newTestEditor(t)
			server := mockserver.NewMockServer(numberedAnswers())
			defer server.Close()
			r := startREPLUnderTerminal(t, server.URL(), nil)
			r.expect(t, freshPrompt)
			r.typeKeys(t, "/ed\n")
			editorReviewPrompt(t, r)
			h.mode(t, failure)
			r.typeKeys(t, "/ed\n")
			r.expect(t, "kept the existing draft")
			r.expect(t, freshPrompt)
			h.cleaned(t)
			r.typeKeys(t, "x\x7f\n") // Erase and echo require restored canonical mode.
			r.expect(t, "answer 1")
			r.expect(t, freshPrompt)
			r.typeKeys(t, "/exit\n")
			if code := r.wait(t); code != 0 {
				t.Fatalf("exit=%d, screen=%q", code, r.screen.String())
			}
			requests := server.GetRequests()
			if len(requests) != 1 || !strings.Contains(requests[0].Body, `"draft"`) {
				t.Fatalf("failed edit replaced original draft: %v", requests)
			}
		})
	}
}

func TestTerminalREPLEditorInterruptSuspendAndDraftDiscard(t *testing.T) {
	h := newTestEditor(t)
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/ed\n")
	editorReviewPrompt(t, r)
	h.mode(t, "wait")
	r.typeKeys(t, "/ed\n")
	r.expect(t, "EDITOR_WAIT")
	r.typeKeys(t, ptytest.Interrupt)
	r.expect(t, "INT_HANDLED")
	r.expect(t, "kept the existing draft")
	r.expect(t, freshPrompt)
	h.mode(t, "stop")
	r.typeKeys(t, "/ed\n")
	r.expect(t, "EDITOR_RESUMED")
	r.typeKeys(t, "\x1a") // Native Ctrl-Z also resumes the editor, rather than stranding it.
	r.typeKeys(t, "stopped and resumed\n")
	editorReviewPrompt(t, r)
	r.typeKeys(t, ptytest.Interrupt)
	r.expect(t, "Discarded the draft.")
	r.expect(t, freshPrompt)
	if len(server.GetRequests()) != 0 {
		t.Fatal("editor or discarded draft caused inference")
	}
	r.typeKeys(t, "fresh\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d, screen=%q", code, r.screen.String())
	}
	h.cleaned(t)
}

func TestTerminalREPLEditorFallbackEdAndLiteralContinuation(t *testing.T) {
	if _, err := exec.LookPath("ed"); err != nil {
		t.Skip("ed unavailable")
	}
	t.Setenv("EDITOR", "")
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/ed\n")
	r.expect(t, "Opening editor;")
	r.typeKeys(t, "a\n/exit\n.\nw\nq\n")
	editorReviewPrompt(t, r)
	if len(server.GetRequests()) != 0 {
		t.Fatal("ed exit submitted automatically")
	}
	r.typeKeys(t, "\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "first\\\n//ed\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d, screen=%q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 2 || !strings.Contains(requests[0].Body, `"/exit\n"`) || !strings.Contains(requests[1].Body, `"first\n/ed"`) {
		t.Fatalf("literal draft/continuation requests=%v", requests)
	}
}

func TestTerminalREPLEditorRedirectedStreamsAndTermination(t *testing.T) {
	h := newTestEditor(t)
	h.mode(t, "wait")
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	stdoutPath, stderrPath := filepath.Join(t.TempDir(), "stdout"), filepath.Join(t.TempDir(), "stderr")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	r := startREPLUnderTerminalStreams(t, server.URL(), stdout, stderr)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/ed\n")
	r.expect(t, "EDITOR_WAIT")
	pathData, err := os.ReadFile(h.log)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(string(pathData))
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("draft mode=%v, err=%v", info, err)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("draft directory mode=%v, err=%v", dirInfo, err)
	}
	r.typeKeys(t, "redirected draft\n")
	editorReviewPrompt(t, r)
	r.typeKeys(t, "\n")
	r.expect(t, "Note: Session:")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/ed\n")
	r.expect(t, "EDITOR_WAIT")
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := r.wait(t); code == 0 {
		t.Fatal("SIGTERM ended with success")
	}
	h.cleaned(t)
	answer, err := os.ReadFile(stdoutPath)
	if err != nil || string(answer) != "answer 1\n" {
		t.Fatalf("stdout=%q, err=%v", answer, err)
	}
	diagnostics, err := os.ReadFile(stderrPath)
	if err != nil || strings.Contains(string(diagnostics), "EDITOR_WAIT") || strings.Contains(string(diagnostics), "Draft (") {
		t.Fatalf("editor wrote redirected stderr=%q, err=%v", diagnostics, err)
	}
	if len(server.GetRequests()) != 1 {
		t.Fatalf("unexpected extra inference: %v", server.GetRequests())
	}
}

func TestTerminalREPLEditorFailedExecRestoresForeground(t *testing.T) {
	_ = getLmcBinary(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ed"), []byte("#!/missing-editor-interpreter\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", dir)
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/ed\n")
	r.expect(t, "launch editor:")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "freshx\x7f\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d, screen=%q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 1 || !strings.Contains(requests[0].Body, `"fresh"`) || r.nonblocking(t) {
		t.Fatalf("terminal did not recover from failed exec: requests=%v", requests)
	}
}

func TestTerminalREPLEditorEmptyDraftDoesNotSend(t *testing.T) {
	h := newTestEditor(t)
	h.mode(t, "empty")
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/ed\n")
	r.expect(t, "Draft is empty; type text or use /ed to revise.")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\n")
	r.expect(t, freshPrompt)
	if len(server.GetRequests()) != 0 {
		t.Fatal("empty draft caused inference")
	}
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d, screen=%q", code, r.screen.String())
	}
	h.cleaned(t)
}
