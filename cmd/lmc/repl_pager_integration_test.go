//go:build integration && (darwin || freebsd || linux)

package main

import (
	"lmtools/internal/mockserver"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The note under a shortened output names a command that works at the next
// question, while the model waits: the whole output opens in the pager, and
// the question is asked again. After the turn the same command works at the
// prompt.
func TestTerminalREPLShowsAShortenedOutputAtTheNextQuestion(t *testing.T) {
	setTestPager(t)
	var mu sync.Mutex
	requests := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		switch n {
		case 1:
			return toolCallResponse("seq", "1", "40"), http.StatusOK, nil
		case 2:
			response := toolCallResponse("echo", "next")
			call := response["choices"].([]map[string]interface{})[0]["message"].(map[string]interface{})["tool_calls"].([]map[string]interface{})[0]
			call["id"] = "call_2"
			return response, http.StatusOK, nil
		}
		return openAITextResponse("done"), http.StatusOK, nil
	}))
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil, "-tool")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "count to forty\n")
	r.expect(t, "Allow seq?")
	r.expect(t, "  > ")
	r.typeKeys(t, "y\n")
	r.expect(t, "[20 lines not shown; /show output 1 prints them]")
	r.expect(t, "Allow echo?")
	r.expect(t, "  > ")
	r.typeKeys(t, "/show output 1\n")
	r.expect(t, "PAGER_OPENED")
	r.expect(t, "Output 1, from [1/1] seq (stdout+stderr, 40 lines):")
	r.expect(t, "  | 20\r\n")
	r.expect(t, "Allow echo?")
	r.expect(t, "  > ")
	r.typeKeys(t, "y\n")
	r.expect(t, "done")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/show output\n")
	r.expect(t, "PAGER_OPENED")
	r.expect(t, "  | 40\r\n")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d", code)
	}
}

// The pager opens by the rows text takes on the terminal: an answer of one
// line that the terminal wraps over several screens is paged too.
func TestTerminalREPLPagesALongAnswerTheTerminalWraps(t *testing.T) {
	setTestPager(t)
	answer := strings.Repeat("word ", 1100)
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		return openAITextResponse(answer), http.StatusOK, nil
	}))
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "write at length\n")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/show last\n")
	r.expect(t, "PAGER_OPENED")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d", code)
	}
}

// setTestPager makes PAGER a script that says it opened, then prints the
// file.
func setTestPager(t *testing.T) {
	t.Helper()
	pager := filepath.Join(t.TempDir(), "pager.sh")
	if err := os.WriteFile(pager, []byte("#!/bin/sh\nprintf 'PAGER_OPENED\\n'\ncat \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAGER", pager)
}
