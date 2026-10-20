package main

import (
	"bytes"
	"encoding/json"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"lmtools/internal/providers"
	"lmtools/internal/retry"
	"lmtools/internal/ui"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestInterleavedResponseHasReadableBoundaries(t *testing.T) {
	var answer, screen bytes.Buffer
	p := newResponsePresenter(io.MultiWriter(&answer, &screen), &screen, true)
	p.answerLabel = "Assistant"
	for _, event := range []core.ResponseStreamEvent{
		{Type: core.ResponseStreamTextDelta, Text: "First"},
		{Type: core.ResponseStreamReasoningStart},
		{Type: core.ResponseStreamReasoningDelta, Text: "Checking"},
		{Type: core.ResponseStreamReasoningEnd},
		{Type: core.ResponseStreamTextDelta, Text: "Second"},
	} {
		p.HandleStreamEvent(event)
	}
	p.HandleResponseComplete(core.Response{Streamed: true})
	p.Close()
	if answer.String() != "FirstSecond" {
		t.Fatalf("answer changed: %q", answer.String())
	}
	want := "Assistant:\nFirst\n\n--- thinking ---\nChecking\n--- end thinking ---\n\nAssistant:\nSecond"
	if !strings.HasPrefix(screen.String(), want) {
		t.Fatalf("transcript=%q", screen.String())
	}
}

func TestBufferedResponsePreservesThinkingAnswerOrder(t *testing.T) {
	blocks := []core.Block{
		core.TextBlock{Text: "First"},
		core.ReasoningBlock{Type: "thinking", Text: "Checking"},
		core.TextBlock{Text: "Second"},
	}
	const thinking = "--- thinking ---\nChecking\n--- end thinking ---\n\n"
	for _, tt := range []struct {
		name, text, want string
		showThinking     bool
	}{
		{"ordered blocks", "FirstSecond", "Assistant:\nFirst\n\n" + thinking + "Assistant:\nSecond", true},
		{"different projection", "Canonical", thinking + "Assistant:\nCanonical", true},
		{"extra block text", "First", thinking + "Assistant:\nFirst", true},
		{"missing block text", "FirstSecondThird", thinking + "Assistant:\nFirstSecondThird", true},
		{"thinking hidden", "FirstSecond", "Assistant:\nFirstSecond", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var answer, screen bytes.Buffer
			p := newResponsePresenter(io.MultiWriter(&answer, &screen), &screen, tt.showThinking)
			p.answerLabel = "Assistant"
			p.HandleResponseComplete(core.Response{Text: tt.text, Blocks: blocks})
			if screen.String() != tt.want || answer.String() != tt.text {
				t.Fatalf("screen=%q answer=%q; want screen=%q answer=%q", screen.String(), answer.String(), tt.want, tt.text)
			}
		})
	}
}

func TestREPLInspectionAndDisplayCommandsDoNotSendRequests(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, _, notes := newTestLoop(t, te)
	var diagnostics bytes.Buffer
	loop.diag = &diagnostics
	loop.base.ProviderURL = "https://user:secret@example.test/v1?key=secret"
	loop.history.add("line one\nline two\\")
	loop.images = []core.ImageBlock{{Name: "one.png"}, {Name: "two.png"}}
	for _, command := range []string{"/info", "/help input", "/help tools", "/help history", "/history 1", "/images", "/remove-image 1", "/thinking on", "/usage off", "/show"} {
		if action := loop.interpretInput(command); action.send || action.exit {
			t.Fatalf("%s triggered a turn", command)
		}
	}
	if len(server.GetRequests()) != 0 {
		t.Fatal("local commands contacted the provider")
	}
	if len(loop.images) != 1 || loop.images[0].Name != "two.png" || !loop.env.cfg.ShowThinking || !loop.hideUsage {
		t.Fatal("commands did not update local state")
	}
	all := strings.Join(notes.all(), "\n")
	for _, want := range []string{"Endpoint: https://example.test/v1/chat/completions", "line one\n", "line two\\\n", "No saved conversation yet"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in %s", want, all)
		}
	}
	if strings.Contains(all, "secret") {
		t.Fatal("connection inspection exposed URL credentials")
	}
	if loop.base.Effort != "" {
		t.Fatal("thinking display changed reasoning effort")
	}
}

func TestREPLUnsentAndMultilinePreview(t *testing.T) {
	var out bytes.Buffer
	n := ui.NewREPLNotifier(&out)
	loop := &replLoop{env: &turnEnv{notifier: n, cfg: &config.Config{}}, draft: "draft", images: []core.ImageBlock{{Name: "a.png"}}, base: core.RequestOptions{Developer: "pending"}}
	loop.reportUnsent()
	showTextPreview(n, "Draft", "first\n\nsecond\x1b[31m", 100)
	want := "Not sent: draft, 1 image(s), developer instruction.\nDraft (18 bytes):\n  first\n  \n  second\\u001b[31m\nEnd draft.\n"
	if out.String() != want {
		t.Fatalf("preview=%q", out.String())
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("preview emitted terminal styling")
	}
}

func TestProgressStopsBeforeAnswerAndReportsRetry(t *testing.T) {
	var out bytes.Buffer
	screen := &replScreen{statusOut: &out, inlineStatus: true}
	p := &turnProgress{screen: screen}
	p.report(retry.Progress{Attempt: 2, Total: 3, Delay: time.Second})
	p.stop()
	_, _ = io.WriteString(screen.writer(&out, true), "answer")
	if !strings.Contains(out.String(), "Retrying in 1s (attempt 2/3)...\r\x1b[2Kanswer") {
		t.Fatalf("progress=%q", out.String())
	}
	p.report(retry.Progress{Attempt: 1, Total: 1})
	p.stop()
	time.Sleep(waitingStatusDelay + 20*time.Millisecond)
	if strings.Contains(out.String(), "Waiting for response") {
		t.Fatal("stale waiting status appeared after stop")
	}
}

func TestBasicTerminalProgressUsesCompletePlainLines(t *testing.T) {
	var out bytes.Buffer
	screen := &replScreen{statusOut: &out, columns: func() int { return 5 }}
	p := &turnProgress{screen: screen}
	p.report(retry.Progress{Attempt: 2, Total: 3, Delay: time.Second})
	p.stop()
	_, _ = io.WriteString(screen.writer(&out, true), "answer")
	screen.separateAnswer(screen.writer(&out, false))
	screen.setStatus("Waiting for response...")
	screen.separateAnswer(screen.writer(&out, false))
	want := "Retrying in 1s (attempt 2/3)...\nanswer\n\nWaiting for response...\n"
	if out.String() != want {
		t.Fatalf("basic status lost text or spacing: %q", out.String())
	}
	p.report(retry.Progress{Attempt: 1, Total: 1})
	p.stop()
	time.Sleep(waitingStatusDelay + 20*time.Millisecond)
	if out.String() != want {
		t.Fatalf("stopped timer added output: %q", out.String())
	}
}

func TestREPLDisplayTogglesLeaveRequestsAndStdoutUnchanged(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		response := openAITextResponse("answer")
		response["choices"].([]map[string]interface{})[0]["message"].(map[string]interface{})["reasoning_content"] = "visible reasoning"
		response["usage"] = map[string]int{"prompt_tokens": 10, "completion_tokens": 2}
		return response, http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, input, _ := newTestLoop(t, te)
	var diagnostics bytes.Buffer
	loop.env.notifier = ui.NewREPLNotifier(&diagnostics)
	loop.env.stderr, loop.diag = &diagnostics, &diagnostics
	typeTurns(t, input, "/thinking on\n/usage off\nfirst\n/thinking off\n/usage on\nsecond\n/show\n/exit\n", true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	if strings.Count(diagnostics.String(), "--- thinking ---") != 1 || strings.Count(diagnostics.String(), "Tokens:") != 1 {
		t.Fatalf("display toggles=%q", diagnostics.String())
	}
	if te.stdout.String() != "answeranswer" || strings.Contains(diagnostics.String(), "\x1b") {
		t.Fatalf("redirected output changed: stdout=%q diagnostics=%q", te.stdout.String(), diagnostics.String())
	}
	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	for _, request := range requests {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(request.Body), &payload); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"thinking", "reasoning", "reasoning_effort", "output_config"} {
			if _, exists := payload[field]; exists {
				t.Fatalf("display toggle introduced %s", field)
			}
		}
	}
}

func TestREPLInfoResolvesTheDefaultModelAndStreamingMode(t *testing.T) {
	var out bytes.Buffer
	opts := core.RequestOptions{Provider: constants.ProviderArgo, OpenAIResponses: true, StreamChat: true}
	loop := &replLoop{base: opts, env: &turnEnv{cfg: &config.Config{}, notifier: ui.NewREPLNotifier(&out)}}
	loop.showInfo()
	model := providers.DefaultChatModel(constants.ProviderArgo)
	if !strings.Contains(out.String(), "Configured model: "+model+" (default)") || !strings.Contains(out.String(), "API: Responses") || !strings.Contains(out.String(), "/responses") {
		t.Fatalf("info does not describe the request: %s", out.String())
	}
	if replStreaming(core.RequestOptions{StreamChat: true, ArgoLegacy: true, ToolEnabled: true}) {
		t.Fatal("legacy tool requests are buffered")
	}
}

func TestREPLIncompleteResponseReportsWhatWasSaved(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		response := openAITextResponse("partial answer")
		response["choices"].([]map[string]interface{})[0]["finish_reason"] = "length"
		return response, http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, _, _ := newTestLoop(t, te)
	out, err := loop.runTurn(turnInput{text: "question"}, nil)
	if err == nil || !out.PartialAnswer || out.Session == nil || te.stdout.String() != "partial answer" {
		t.Fatalf("incomplete outcome=%+v err=%v answer=%q", out, err, te.stdout.String())
	}
	if saved := describeSaved(out); !strings.HasPrefix(saved, "partial assistant response saved through message ") {
		t.Fatalf("saved status=%q", saved)
	}
}
