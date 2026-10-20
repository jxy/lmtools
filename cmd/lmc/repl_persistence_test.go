package main

import (
	"bytes"
	"errors"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"lmtools/internal/ui"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestREPLUnsavedAnswerSeparatesFatalDiagnostic(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		for _, ending := range []string{"", "\n", "\n\n"} {
			for _, terminal := range []bool{false, true} {
				var stdout, diagnostics, transcript bytes.Buffer
				screen := &replScreen{}
				answer := screen.writer(io.MultiWriter(&stdout, &transcript), true)
				diag := screen.writer(io.MultiWriter(&diagnostics, &transcript), false)
				loop := &replLoop{diag: diag}
				if terminal {
					loop.screen = screen
				}
				p := newResponsePresenter(answer, diag, false)
				p.separate = terminal
				text := "answer" + ending
				response := core.Response{Text: text, Streamed: streamed}
				if streamed {
					p.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: text})
				}
				p.HandleResponseComplete(response)
				p.Close()
				saveErr := errors.New("disk full")
				err := loop.finishTurn(turnOutcome{UnsavedAnswer: saveErr}, saveErr)
				if !errors.Is(err, saveErr) {
					t.Fatalf("fatal error lost its cause: %v", err)
				}
				ui.NewNotifierWithWriter(diag).Errorf("%v", err)
				if terminal && !strings.HasPrefix(transcript.String(), "answer\n\nError: ") {
					t.Errorf("stream=%v ending=%q transcript=%q", streamed, ending, transcript.String())
				}
				if !terminal && !strings.HasPrefix(diagnostics.String(), "Error: ") {
					t.Errorf("redirected diagnostics gained spacing: %q", diagnostics.String())
				}
				if stdout.String() != text || strings.Count(diagnostics.String(), "Error:") != 1 {
					t.Errorf("stdout=%q diagnostics=%q", stdout.String(), diagnostics.String())
				}
			}
		}
	}
}

func TestREPLUnsavedAnswerAfterDiagnosticKeepsExistingSpacing(t *testing.T) {
	for _, kind := range []string{"usage", "thinking"} {
		t.Run(kind, func(t *testing.T) {
			var transcript bytes.Buffer
			screen := &replScreen{}
			diag := screen.writer(&transcript, false)
			p := newResponsePresenter(screen.writer(&transcript, true), diag, kind == "thinking")
			response := core.Response{Text: "answer"}
			if kind == "usage" {
				count := 1
				response.Usage = &core.Usage{OutputTokens: &count}
			}
			p.HandleResponseComplete(response)
			p.Close()
			if kind == "usage" {
				ui.NewREPLNotifier(diag).Infof("Tokens: %s", response.Usage.DisplaySummary())
			}
			before := transcript.String()
			loop := &replLoop{screen: screen, diag: diag}
			stop := loop.finishTurn(turnOutcome{UnsavedAnswer: errors.New("disk full")}, nil)
			ui.NewNotifierWithWriter(diag).Errorf("%v", stop)
			if !strings.HasPrefix(transcript.String(), before+"Error: ") {
				t.Fatalf("fatal error added redundant separation: %q", transcript.String())
			}
		})
	}
}

func TestREPLPersistenceFailureStopsWithReadableDiagnostic(t *testing.T) {
	for _, phase := range []string{"initial commit", "assistant save", "tool follow-up"} {
		t.Run(phase, func(t *testing.T) {
			var calls atomic.Int32
			var sessionsDir string
			server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
				n := calls.Add(1)
				if phase == "tool follow-up" {
					if n == 1 {
						return toolCallResponse("echo", "done"), http.StatusOK, nil
					}
					// The command results have been committed; make the final
					// answer's real persistence attempt fail.
					entries, err := os.ReadDir(sessionsDir)
					if err != nil {
						return nil, http.StatusInternalServerError, err
					}
					for _, entry := range entries {
						if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
							replaceWithFile(t, filepath.Join(sessionsDir, entry.Name()))
						}
					}
				}
				response := openAITextResponse("answer")
				delete(response, "usage") // A usage note would mask the missing boundary.
				return response, http.StatusOK, nil
			}))
			defer server.Close()
			te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve")
			sessionsDir = te.sessionsDir
			te.env.cfg.REPL = true
			switch phase {
			case "initial commit":
				blocked := filepath.Join(t.TempDir(), "file-instead-of-sessions")
				if err := os.WriteFile(blocked, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				session.SetSessionsDir(blocked)
			case "assistant save":
				afterTurnCommitForTest = func(sess *session.Session) { replaceWithFile(t, sess.Path) }
				t.Cleanup(func() { afterTurnCommitForTest = nil })
			}
			loop, input, _ := newTestLoop(t, te)
			var stdout, transcript bytes.Buffer
			screen := &replScreen{}
			diag := screen.writer(&transcript, false)
			loop.screen, loop.diag = screen, diag
			te.env.stdout = screen.writer(io.MultiWriter(&stdout, &transcript), true)
			te.env.stderr, te.env.answersOnScreen = diag, true
			te.env.notifier = ui.NewREPLNotifier(diag)
			typeTurns(t, input, "first\nsecond\n", true)
			stop := loop.run(false)
			if stop == nil || !strings.Contains(stop.Error(), "answer could not be saved") {
				t.Fatalf("REPL did not stop on failed persistence: %v", stop)
			}
			ui.NewNotifierWithWriter(diag).Errorf("%v", stop)
			if !strings.Contains(transcript.String(), "answer\n\nError: answer could not be saved") || strings.Count(transcript.String(), "Error:") != 1 {
				t.Fatalf("unreadable or repeated fatal diagnostic: %q", transcript.String())
			}
			wantCalls, wantAnswer := int32(1), "answer"
			if phase == "tool follow-up" {
				wantCalls, wantAnswer = 2, "running itanswer"
			}
			if calls.Load() != wantCalls || stdout.String() != wantAnswer {
				t.Fatalf("calls=%d answer=%q; want %d %q", calls.Load(), stdout.String(), wantCalls, wantAnswer)
			}
		})
	}
}
