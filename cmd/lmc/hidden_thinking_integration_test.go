//go:build integration

package main

import (
	"strings"
	"testing"
)

// A provider that counts reasoning and returns none hides it on every
// response, so a run says so once, however many turns report it.
func TestCLINotesHiddenThinkingOncePerRun(t *testing.T) {
	answer := func(text string) []byte {
		return []byte(`data: {"choices":[{"index":0,"delta":{"content":"` + text + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":30,"completion_tokens_details":{"reasoning_tokens":25}}}` + "\n\ndata: [DONE]\n\n")
	}
	server, requests := completionServer(t, completionReply{answer("FIRST"), true}, completionReply{answer("SECOND"), true})
	stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), false, true, "-log-dir", t.TempDir(), "-repl", thinkingOn), "first\nsecond\n")
	if err != nil || len(requests()) != 2 || !strings.Contains(stdout, "FIRST") || !strings.Contains(stdout, "SECOND") {
		t.Fatalf("stdout=%q stderr=%s err=%v requests=%d", stdout, stderr, err, len(requests()))
	}
	if got := strings.Count(stderr, "No visible thinking was returned for 25 reasoning tokens."); got != 1 {
		t.Fatalf("notes = %d, want 1:\n%s", got, stderr)
	}
}
