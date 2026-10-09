package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"lmtools/internal/ui"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestREPLCommandsAndLiteralSlash(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, notes := newTestLoop(t, te)
	typeTurns(t, w, "/help\n/history\n/unknown argument\n/again 1\n/exit arg\nfirst\\\nsecond\n/history\n//help\n/exit\nnever sent\n", true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want the prompt and a literal slash prompt", len(requests))
	}
	if !strings.Contains(requests[0].Body, `first\nsecond`) || !strings.Contains(requests[1].Body, `"/help"`) {
		t.Fatalf("requests = %v, want original continuation text and literal /help", requests)
	}
	want := "user:first\nsecond|assistant:answer 1|user:/help|assistant:answer 2"
	if got := strings.Join(sessionLines(t, loop.from), "|"); !strings.HasSuffix(got, want) {
		t.Fatalf("session = %q, want suffix %q", got, want)
	}
	for _, text := range []string{"REPL commands:", "No prompt text", "Unknown REPL command", "Use /ed N", "/exit takes no arguments", `1: "first\nsecond"`} {
		if notes.count(text) != 1 {
			t.Fatalf("notes = %q, want %q once", notes.all(), text)
		}
	}
	if len(loop.history.entries) != 2 || loop.history.entries[1].text != "/help" {
		t.Fatalf("history = %#v, want only the two submitted prompts", loop.history)
	}
	if got := te.stdout.String(); got != "answer 1answer 2" {
		t.Fatalf("stdout = %q, want answer bytes alone", got)
	}
}

func TestREPLHistoryBoundsAndStableNumbers(t *testing.T) {
	var history replHistory
	for i := 0; i < maxREPLHistoryEntries+3; i++ {
		history.add(fmt.Sprintf("prompt %d", i+1))
	}
	if len(history.entries) != maxREPLHistoryEntries || history.entries[0].id != 4 {
		t.Fatalf("history = %#v, want the most recent 100 with stable IDs", history)
	}
	if _, ok := history.get(1); ok {
		t.Fatal("evicted prompt remains available")
	}
	if got, ok := history.get(4); !ok || got != "prompt 4" {
		t.Fatalf("get(4) = %q, %v", got, ok)
	}
	history = replHistory{}
	large := strings.Repeat("x", maxREPLHistoryBytes/2+1)
	history.add(large)
	history.add(large)
	if len(history.entries) != 1 || history.entries[0].id != 2 || history.bytes > maxREPLHistoryBytes {
		t.Fatalf("history retains %d entries and %d bytes, want the last large prompt only", len(history.entries), history.bytes)
	}
	history.add("")
	if history.nextID != 2 {
		t.Fatal("an image-only turn entered text history")
	}
}

func TestREPLPromptPreviewKeepsUnicodeAndRecallText(t *testing.T) {
	prompt := strings.Repeat("界", 121) + "\nsecond line"
	if got, want := replPromptPreview(prompt), strings.Repeat("界", 120)+"..."; got != want || !utf8.ValidString(got) {
		t.Fatalf("preview = %q, want %q", got, want)
	}
	var history replHistory
	history.add(prompt)
	if got, _ := history.get(1); got != prompt {
		t.Fatal("history lost text beyond the display preview")
	}
}

func TestREPLImageCommandsRetainUntilCommit(t *testing.T) {
	attempt := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		attempt++
		if attempt == 1 {
			return map[string]interface{}{"error": "try again"}, http.StatusBadRequest, nil
		}
		return openAITextResponse("saw the picture"), http.StatusOK, nil
	}))
	defer server.Close()
	dir := t.TempDir()
	image := filepath.Join(dir, "picture with spaces.png")
	invalid := filepath.Join(dir, "invalid.png")
	for path, data := range map[string][]byte{image: replPNG, invalid: []byte("not an image")} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	te := newTurnTestEnv(t, server)
	te.env.cfg.ImageDetail = "high"
	loop, w, notes := newTestLoop(t, te)
	input := fmt.Sprintf("/image %s\n/image %s\n/image %s\n\n\nfollow-up\n/exit\n", image, filepath.Join(dir, "missing.png"), invalid)
	typeTurns(t, w, input, true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	requests := server.GetRequests()
	if len(requests) != 3 {
		t.Fatalf("%d requests, want the failed image-only turn, retry, and follow-up", len(requests))
	}
	for i, request := range requests {
		if strings.Count(request.Body, "data:image/png") != 1 || !strings.Contains(request.Body, `"detail":"high"`) {
			t.Fatalf("request %d = %s, want exactly one image with high detail", i+1, request.Body)
		}
	}
	if loop.images != nil {
		t.Fatal("committed image is still pending")
	}
	if notes.count("1 image(s) pending") != 1 || notes.count("read image") != 2 {
		t.Fatalf("notes = %q, want attachment success and two image errors", notes.all())
	}
	if got := strings.Join(sessionLines(t, loop.from), "|"); strings.Count(got, "assistant:saw the picture") != 2 || !strings.Contains(got, "user:follow-up") {
		t.Fatalf("session = %q, want only the two successful turns", got)
	}
}

func TestREPLClearImagesAndImageCommandValidation(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	image := filepath.Join(t.TempDir(), "picture.png")
	if err := os.WriteFile(image, replPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	te := newTurnTestEnv(t, server)
	loop, w, notes := newTestLoop(t, te)
	typeTurns(t, w, fmt.Sprintf("/image\n/image %s\n/clear-images\n\nquestion\n/exit\n", image), true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	requests := server.GetRequests()
	if len(requests) != 1 || strings.Contains(requests[0].Body, "data:image") {
		t.Fatalf("requests = %v, want one text-only turn after clearing images", requests)
	}
	if notes.count("Use /image PATH") != 1 || notes.count("Cleared pending images") != 1 {
		t.Fatalf("notes = %q, want usage and clear confirmation", notes.all())
	}
	loop.base.ArgoLegacy = true
	action := loop.interpretInput("/image " + image)
	if action.send || action.exit || len(loop.images) != 0 || notes.count("not supported with -argo-legacy") != 1 {
		t.Fatalf("legacy image action = %#v, notes = %q", action, notes.all())
	}
}

func TestREPLPresentationStaysOnDiagnostics(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, notes := newTestLoop(t, te)
	loop.terminal = true
	typeTurns(t, w, "one\ntwo\n/exit\n", true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`model: "gpt-test"`, "API: Chat Completions (buffered)", "/help for commands", `User 1: "one"`, `User 2: "two"`, "lmc: Working on turn 1...", "lmc: Working on turn 2...", "Session: "} {
		if notes.count(text) != 1 {
			t.Fatalf("notes = %q, want %q once", notes.all(), text)
		}
	}
	if got := te.stdout.String(); got != "answer 1answer 2" {
		t.Fatalf("stdout = %q, want answer bytes alone", got)
	}
}

func TestREPLSeparatesFailureFromPartialAnswer(t *testing.T) {
	var output bytes.Buffer
	screen := &replScreen{}
	diag := screen.writer(&output, false)
	loop := &replLoop{
		env:     &turnEnv{notifier: ui.NewNotifierWithWriter(diag)},
		diag:    diag,
		screen:  screen,
		signals: newREPLSignals(),
	}
	_, _ = io.WriteString(screen.writer(&output, true), "partial answer")
	if err := loop.finishTurn(turnOutcome{}, errors.New("provider failed")); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "partial answer\n\nError: provider failed\n"; got != want {
		t.Fatalf("terminal = %q, want %q", got, want)
	}
}

func TestREPLAPINameFollowsWireRouting(t *testing.T) {
	for _, tc := range []struct {
		opts core.RequestOptions
		want string
	}{
		{core.RequestOptions{Provider: constants.ProviderOpenAI}, "Chat Completions"},
		{core.RequestOptions{Provider: constants.ProviderOpenAI, OpenAIResponses: true}, "Responses"},
		{core.RequestOptions{Provider: constants.ProviderAnthropic}, "Messages"},
		{core.RequestOptions{Provider: constants.ProviderGoogle}, "GenerateContent"},
		{core.RequestOptions{Provider: constants.ProviderArgo, Model: "gpt-test", OpenAIResponses: true}, "Responses"},
		{core.RequestOptions{Provider: constants.ProviderArgo, Model: "claude-test", OpenAIResponses: true}, "Messages"},
		{core.RequestOptions{Provider: constants.ProviderArgo, Model: "gemma-test", OpenAIResponses: true}, "Chat Completions"},
		{core.RequestOptions{Provider: constants.ProviderArgo, ArgoLegacy: true}, "Argo chat"},
	} {
		if got := replAPIName(tc.opts); got != tc.want {
			t.Fatalf("replAPIName(%#v) = %q, want %q", tc.opts, got, tc.want)
		}
	}
}

func TestREPLDeveloperInstructionIsCommittedOnce(t *testing.T) {
	attempt := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		attempt++
		if attempt == 1 {
			return map[string]interface{}{"error": "try again"}, http.StatusBadRequest, nil
		}
		return openAITextResponse("answer"), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-developer", "Use short answers.")
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "failed\nfirst\nsecond\n/exit\n", true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	requests := server.GetRequests()
	if len(requests) != 3 {
		t.Fatalf("%d requests, want failure followed by two committed turns", len(requests))
	}
	for i, request := range requests {
		if strings.Count(request.Body, `"role":"developer"`) != 1 || strings.Count(request.Body, "Use short answers.") != 1 {
			t.Fatalf("request %d = %s, want the developer instruction once", i+1, request.Body)
		}
	}
	if loop.base.Developer != "" {
		t.Fatal("developer instruction is still pending after the first commit")
	}
	if got := strings.Join(sessionLines(t, loop.from), "|"); strings.Count(got, "developer:Use short answers.") != 1 || strings.Contains(got, "user:failed") {
		t.Fatalf("session = %q, want the instruction once without the failed turn", got)
	}
}

func TestREPLPendingDeveloperDefersStartupRecovery(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server, "-developer", "Use short answers.", "-tool", "-tool-auto-approve")
	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatal(err)
	}
	appendTestSessionMessage(t, context.Background(), sess, core.RoleUser, "Earlier question")
	if _, err := session.SaveAssistantResponseWithTools(context.Background(), sess, "", []core.ToolCall{{
		ID: "call_deferred", Name: "universal_command", Args: json.RawMessage(`{"command":["printf","recovered"]}`),
	}}, "gpt-test"); err != nil {
		t.Fatal(err)
	}
	te.opts.Resume = session.GetSessionID(sess.Path)
	loop, _, notes := newTestLoop(t, te)
	if err := loop.recoverPendingTools(); err != nil {
		t.Fatal(err)
	}
	if len(server.GetRequests()) != 0 || len(notes.all()) != 0 {
		t.Fatalf("startup recovered before a new user turn: requests=%v, notes=%q", server.GetRequests(), notes.all())
	}
	if loop.base.Developer == "" {
		t.Fatal("startup consumed the pending developer instruction")
	}
}

func replRequestDeveloperTexts(t *testing.T, body string) []string {
	t.Helper()
	var request struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	var instructions []string
	for _, message := range request.Messages {
		if message.Role == "developer" {
			var text string
			if err := json.Unmarshal(message.Content, &text); err != nil {
				t.Fatal(err)
			}
			instructions = append(instructions, text)
		}
	}
	return instructions
}

func TestREPLDeveloperCommandsQueueReplaceAndClearWithoutSending(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server, "-developer", "Startup instruction")
	loop, w, notes := newTestLoop(t, te)
	image := core.ImageBlock{URL: core.ImageDataURL("image/png", replPNG), Name: "pending.png"}
	loop.images = []core.ImageBlock{image}
	typeTurns(t, w, "/developer   \n/clear-developer extra\n/developer first\n/developer second\\\nline\n/history\n/help\n/exit\n", true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	if got, want := loop.base.Developer, "second\nline"; got != want {
		t.Fatalf("pending developer = %q, want replacement %q", got, want)
	}
	if len(server.GetRequests()) != 0 || sessionDirCount(t, te.sessionsDir) != 0 || len(loop.history.entries) != 0 || te.stdout.Len() != 0 {
		t.Fatalf("commands sent or saved a turn: requests=%v, history=%v, stdout=%q", server.GetRequests(), loop.history.entries, te.stdout.String())
	}
	for _, text := range []string{"Use /developer TEXT", "/clear-developer takes no arguments", "No prompt text", "/developer TEXT", "/clear-developer"} {
		if notes.count(text) == 0 {
			t.Fatalf("notes=%q, want %q", notes.all(), text)
		}
	}
	if action := loop.interpretInput("/clear-developer"); action.send || action.exit || loop.base.Developer != "" {
		t.Fatalf("clear action=%+v, pending=%q", action, loop.base.Developer)
	}
	if len(loop.images) != 1 || loop.images[0] != image {
		t.Fatal("clearing the developer instruction changed pending images")
	}
}

func TestREPLDeveloperCommandRejectsUnsupportedWireWithoutReplacingPending(t *testing.T) {
	for _, tc := range []struct {
		provider, model   string
		responses, legacy bool
	}{
		{constants.ProviderAnthropic, "claude-test", false, false},
		{constants.ProviderGoogle, "gemini-test", false, false},
		{constants.ProviderArgo, "gpt-test", false, false},
		{constants.ProviderArgo, "claude-test", true, false},
		{constants.ProviderArgo, "gpt-test", true, true},
	} {
		t.Run(fmt.Sprintf("%s/%s/responses=%t/legacy=%t", tc.provider, tc.model, tc.responses, tc.legacy), func(t *testing.T) {
			server := mockserver.NewMockServer(numberedAnswers())
			defer server.Close()
			te := newTurnTestEnv(t, server)
			loop, _, notes := newTestLoop(t, te)
			loop.base.Provider, loop.base.Model = tc.provider, tc.model
			loop.base.OpenAIResponses, loop.base.ArgoLegacy = tc.responses, tc.legacy
			loop.base.Developer = "Existing pending instruction"
			action := loop.interpretInput("/developer replacement")
			if action.send || action.exit || loop.base.Developer != "Existing pending instruction" || notes.count("cannot preserve the developer role") != 1 {
				t.Fatalf("action=%+v, pending=%q, notes=%q", action, loop.base.Developer, notes.all())
			}
			if len(server.GetRequests()) != 0 || sessionDirCount(t, te.sessionsDir) != 0 {
				t.Fatal("a rejected developer command sent or saved a turn")
			}
		})
	}
}

func TestREPLDeveloperCommandPersistsWithNextTurnAfterRetry(t *testing.T) {
	var mu sync.Mutex
	attempt := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		attempt++
		current := attempt
		mu.Unlock()
		if current == 2 {
			return map[string]interface{}{"error": "try again"}, http.StatusBadRequest, nil
		}
		return openAITextResponse(fmt.Sprintf("answer %d", current)), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "first\n/developer stale\n/developer First line\\\nSecond line\nfailed\nfailed\nthird\n/developer throwaway\n/clear-developer\n//developer literal\n/exit\n", true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	requests := server.GetRequests()
	if len(requests) != 5 {
		t.Fatalf("%d requests, want only the five submitted prompts", len(requests))
	}
	for i, request := range requests {
		instructions := replRequestDeveloperTexts(t, request.Body)
		if i == 0 {
			if len(instructions) != 0 {
				t.Fatalf("first request unexpectedly contains developer instructions: %v", instructions)
			}
		} else if len(instructions) != 1 || instructions[0] != "First line\nSecond line" {
			t.Fatalf("request %d instructions=%q, want the committed instruction once", i+1, instructions)
		}
	}
	want := "user:first|assistant:answer 1|developer:First line\nSecond line|user:failed|assistant:answer 3|user:third|assistant:answer 4|user:/developer literal|assistant:answer 5"
	if got := strings.Join(sessionLines(t, loop.from), "|"); !strings.HasSuffix(got, want) {
		t.Fatalf("session=%q, want suffix %q", got, want)
	}
	if loop.base.Developer != "" || len(loop.history.entries) != 5 || loop.history.entries[4].text != "/developer literal" {
		t.Fatalf("pending=%q, history=%v; want consumed instruction and only prompt history", loop.base.Developer, loop.history.entries)
	}
}

func TestREPLDeveloperCommandSupportsImageOnlyTurn(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	path := filepath.Join(t.TempDir(), "picture.png")
	if err := os.WriteFile(path, replPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	te := newTurnTestEnv(t, server)
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "/image "+path+"\n/developer cleared\n/clear-developer\n/developer Image instruction\n\nfollow-up\n/exit\n", true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want image-only turn and follow-up", len(requests))
	}
	for _, request := range requests {
		instructions := replRequestDeveloperTexts(t, request.Body)
		if len(instructions) != 1 || instructions[0] != "Image instruction" || strings.Count(request.Body, "data:image/png") != 1 {
			t.Fatalf("request=%s, want one instruction and one image", request.Body)
		}
	}
	if loop.base.Developer != "" || len(loop.images) != 0 || len(loop.history.entries) != 1 || loop.history.entries[0].text != "follow-up" {
		t.Fatalf("pending=%q, images=%d, history=%v", loop.base.Developer, len(loop.images), loop.history.entries)
	}
}
