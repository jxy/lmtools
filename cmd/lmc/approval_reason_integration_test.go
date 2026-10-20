//go:build integration && (darwin || freebsd || linux)

package main

import (
	"encoding/json"
	"io"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type approvalDraftCase struct {
	submit                                    string
	name, input, reason                       string
	editorMode, editorInput, seed, appendText string
	approved                                  bool
}

func TestTerminalToolDenialReasonReachesModelAndSession(t *testing.T) {
	for _, test := range []approvalDraftCase{
		{name: "single line", input: "This is a read-only review; do not create files.\n", reason: "This is a read-only review; do not create files."},
		{name: "EOF discards typed yes", input: "yes\x04"},
		{name: "EOF discards pasted yes", input: "\x1b[200~yes\x1b[201~\x04"},
		{name: "continued", input: "Only inspect files\\\nDo not change anything.\n", reason: "Only inspect files\nDo not change anything."},
		{name: "continued paste preserves slashes", input: "Reason\\\n\x1b[200~// comment\nbody\\\x1b[201~\n", reason: "Reason\n// comment\nbody\\"},
		{name: "continued paste keeps editor command literal", input: "Reason\\\n\x1b[200~/ed\x1b[201~\n", reason: "Reason\n/ed"},
		{name: "edit continued draft", input: "Only inspect files\\\nDo not change anything.\\\n/ed\n", editorMode: "identity", seed: "Only inspect files\nDo not change anything.", reason: "Only inspect files\nDo not change anything."},
		{name: "edit history", input: "/ed 1\n", editorMode: "identity", seed: "Review the files.", reason: "Review the files."},
		{name: "edit regex", input: "/ed ^Review\n", editorMode: "identity", seed: "Review the files.", reason: "Review the files."},
		{name: "edited yes approves", input: "/ed\n", editorMode: "wait", editorInput: "yes\n", approved: true},
		{name: "EOF discards edited yes", input: "/ed\n", editorMode: "wait", editorInput: "yes\n", submit: "\x04"},
		{name: "append to edited yes", input: "/ed\n", editorMode: "wait", editorInput: "yes\n", appendText: " but do not create files.", reason: "yes but do not create files."},
		{name: "queued yes waits for review", input: "/ed\ny\n", editorMode: "success", reason: "draft"},
		{name: "editor failure preserves draft", input: "Only inspect files\\\n/ed\n", editorMode: "fail", seed: "Only inspect files", reason: "Only inspect files"},
	} {
		t.Run(test.name, func(t *testing.T) { testApprovalDraftRoundTrip(t, test) })
	}
}

func testApprovalDraftRoundTrip(t *testing.T, test approvalDraftCase) {
	t.Helper()
	for _, responses := range []bool{false, true} {
		name := "chat"
		if responses {
			name = "responses"
		}
		t.Run(name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "tool-marker")
			arguments, err := json.Marshal(map[string]interface{}{"command": []string{"touch", marker}})
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var bodies [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				mu.Lock()
				bodies = append(bodies, body)
				first := len(bodies) == 1
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if responses {
					if !first {
						writeResponsesFinalText(w, "DENIAL_ACK")
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"id": "resp-denied", "status": "completed",
						"output": []map[string]interface{}{{
							"type": "function_call", "id": "fc-denied", "call_id": "denied-1",
							"name": "universal_command", "arguments": string(arguments), "status": "completed",
						}},
					})
					return
				}
				message := map[string]interface{}{"role": "assistant", "content": "DENIAL_ACK"}
				finish := "stop"
				if first {
					message = map[string]interface{}{
						"role": "assistant", "content": nil,
						"tool_calls": []map[string]interface{}{{
							"id": "denied-1", "type": "function",
							"function": map[string]interface{}{"name": "universal_command", "arguments": string(arguments)},
						}},
					}
					finish = "tool_calls"
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"choices": []map[string]interface{}{{"index": 0, "message": message, "finish_reason": finish}},
				})
			}))
			defer server.Close()
			flags := []string{"-tool", "-tool-include", "universal_command", "-retries", "0"}
			if responses {
				flags = append(flags, "-openai-responses")
			}
			var editor testEditor
			if test.editorMode != "" {
				editor = newTestEditor(t)
				editor.mode(t, test.editorMode)
			}
			r := startREPLUnderTerminal(t, server.URL, nil, flags...)
			r.expect(t, freshPrompt)
			r.typeKeys(t, "Review the files.\n")
			r.expect(t, strings.TrimSpace("Allow execution? "+toolApprovalChoices)+"\r\n      > ")
			r.typeKeys(t, test.input)
			if test.editorMode != "" {
				if test.editorMode == "wait" {
					r.expect(t, "EDITOR_WAIT")
					r.typeKeys(t, test.editorInput)
				}
				r.expect(t, "Enter submits the answer;")
				r.expect(t, freshPrompt)
				if seed := editor.seed(t); seed != test.seed {
					t.Fatalf("editor seed=%q, want %q", seed, test.seed)
				}
				editor.cleaned(t)
				mu.Lock()
				count := len(bodies)
				mu.Unlock()
				if count != 1 {
					t.Fatal("editor exit or queued input submitted the answer")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("command ran before review: %v", err)
				}
				if test.submit != "" {
					r.typeKeys(t, test.submit)
				} else {
					r.typeKeys(t, test.appendText+"\n")
				}
			}
			r.expect(t, "DENIAL_ACK")
			r.expect(t, freshPrompt)
			r.typeKeys(t, "/exit\n")
			if code := r.wait(t); code != 0 {
				t.Fatalf("exit code=%d", code)
			}
			_, statErr := os.Stat(marker)
			if (test.approved && statErr != nil) || (!test.approved && !os.IsNotExist(statErr)) {
				t.Fatalf("approved=%v, command marker error=%v", test.approved, statErr)
			}

			mu.Lock()
			captured := append([][]byte(nil), bodies...)
			mu.Unlock()
			if len(captured) != 2 {
				t.Fatalf("requests=%d, want initial request and denial follow-up", len(captured))
			}
			var body map[string]interface{}
			if err := json.Unmarshal(captured[1], &body); err != nil {
				t.Fatal(err)
			}
			key := "messages"
			if responses {
				key = "input"
			}
			denial := "user denied permission; reason provided by the user: " + test.reason
			if test.reason == "" {
				denial = "user denied permission"
			}
			modelText := "Command was not executed.\n" + denial
			if test.approved {
				modelText = ""
			}
			found := false
			for _, raw := range body[key].([]interface{}) {
				item := raw.(map[string]interface{})
				if responses && item["type"] == "function_call_output" {
					found = item["call_id"] == "denied-1" && item["output"] == modelText
				}
				if !responses && item["role"] == "tool" {
					found = item["tool_call_id"] == "denied-1" && item["content"] == modelText
				}
			}
			if !found {
				t.Fatalf("provider did not receive explicit user denial and attribution: %s", captured[1])
			}

			files, err := filepath.Glob(filepath.Join(r.sessionsDir, "*", "*.tools.json"))
			if err != nil {
				t.Fatal(err)
			}
			found = false
			for _, path := range files {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var interaction core.ToolInteraction
				if err := json.Unmarshal(data, &interaction); err != nil {
					t.Fatal(err)
				}
				for _, result := range interaction.Results {
					if result.ID == "denied-1" {
						if test.approved {
							found = !result.NotRun && result.Error == "" && result.Reason == ""
						} else {
							found = result.NotRun && result.Code == lmerrors.ErrCodeNotApproved && result.Error == denial && result.Reason == denial
						}
					}
				}
			}
			if !found {
				t.Fatal("session did not preserve the attributed user denial")
			}
		})
	}
}
