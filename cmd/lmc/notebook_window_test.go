package main

import (
	"context"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/contextcfg"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The window is the smaller of the operator's cap and the model's capacity
// when both are known, and the one known otherwise. The capacity comes from
// the newest reply made with the same model and endpoint, else from the
// catalog; an offline preview reads only the replies.
func TestNotebookWindowFromCapAndCapacity(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `[{"id":"big","capabilities":{"context_window_tokens":1048576}},{"id":"small","capabilities":{"context_window_tokens":4096}}]`)
	}))
	defer server.Close()
	cfg, err := config.ParseFlags([]string{"-provider", "openai", "-api-key-file", writeTestAPIKeyFile(t, "fixture"), "-models-url", server.URL, "-model", "big", "-sessions-dir", t.TempDir(), "-notebook", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	base := cfg.RequestOptions()
	resolve := func(model string, limit int, lineage ...session.Message) (contextcfg.Binding, error) {
		t.Helper()
		opts := base
		opts.Model = model
		opts.Context.WindowCap = limit
		err := resolveWindow(context.Background(), &cfg, &opts, core.NewTestNotifier(), "", lineage)
		return opts.Context, err
	}
	b, err := resolve("big", 0)
	if err != nil || b.Policy != "handoff" || b.Window != 1048576 || !b.Early || b.Capacity.Tokens != 1048576 || calls != 1 {
		t.Fatal(b, calls, err)
	}
	found := b.Capacity
	reply := session.Message{Role: core.RoleAssistant, Receipt: &contextstate.Receipt{Capacity: &found}}
	if b, err = resolve("big", 0, reply); err != nil || b.Window != 1048576 || calls != 1 {
		t.Fatal("the recorded capacity was not used", b, calls, err)
	}
	other := reply
	other.Receipt = &contextstate.Receipt{Capacity: &contextcfg.Capacity{Tokens: 8192, Model: "big", Endpoint: "elsewhere"}}
	if b, err = resolve("big", 0, other); err != nil || b.Window != 1048576 || calls != 2 {
		t.Fatal("a capacity found at another endpoint was used", b, calls, err)
	}
	if b, err = resolve("small", 8192); err != nil || b.Window != 4096 || b.Policy != "handoff" {
		t.Fatal("the smaller capacity did not bound the window", b, err)
	}
	if b, err = resolve("big", 8192); err != nil || b.Window != 8192 {
		t.Fatal("the smaller cap did not bound the window", b, err)
	}
	if b, err = resolve("unknown", 0); err != nil || b.Policy != "full" || b.Window != 0 || b.Capacity.Tokens != 0 {
		t.Fatal("no window did not replay in full", b, err)
	}
	if b, err = resolve("unknown", 8192); err != nil || b.Policy != "handoff" || b.Window != 8192 {
		t.Fatal("the cap alone did not set the window", b, err)
	}
	// /model resolves again on the options it already resolved: a model
	// without a known capacity keeps none of the earlier model's.
	switched := base
	switched.Model = "small"
	if err := resolveWindow(context.Background(), &cfg, &switched, core.NewTestNotifier(), "", nil); err != nil || switched.Context.Capacity.Tokens != 4096 {
		t.Fatal(switched.Context, err)
	}
	switched.Model = "unknown"
	if err := resolveWindow(context.Background(), &cfg, &switched, core.NewTestNotifier(), "", nil); err != nil || switched.Context.Capacity.Tokens != 0 {
		t.Fatal("the earlier model's capacity stayed", switched.Context, err)
	}
	// The rule holds without a notebook: text-only compaction takes the
	// capacity, and full replay under a cap takes the smaller.
	textCfg, err := config.ParseFlags([]string{"-provider", "openai", "-api-key-file", writeTestAPIKeyFile(t, "fixture"), "-models-url", server.URL, "-model", "small", "-sessions-dir", t.TempDir(), "-context-policy", "handoff"})
	if err != nil {
		t.Fatal(err)
	}
	text := textCfg.RequestOptions()
	if err := resolveWindow(context.Background(), &textCfg, &text, core.NewTestNotifier(), "", nil); err != nil || text.Context.Window != 4096 || text.Context.Policy != "handoff" {
		t.Fatal("text-only compaction", text.Context, err)
	}
	replayCfg, err := config.ParseFlags([]string{"-provider", "openai", "-api-key-file", writeTestAPIKeyFile(t, "fixture"), "-models-url", server.URL, "-model", "small", "-sessions-dir", t.TempDir(), "-context-policy", "full", "-context-window", "200000"})
	if err != nil {
		t.Fatal(err)
	}
	replay := replayCfg.RequestOptions()
	if err := resolveWindow(context.Background(), &replayCfg, &replay, core.NewTestNotifier(), "", nil); err != nil || replay.Context.Window != 4096 {
		t.Fatal("full replay under a cap", replay.Context, err)
	}
	cfg.PrintCurl = true
	before := calls
	if b, err = resolve("big", 0, reply); err != nil || b.Window != 1048576 || calls != before {
		t.Fatal("an offline preview did not use the recorded capacity", b, calls, err)
	}
	if _, err = resolve("small", 0); err == nil || !strings.Contains(err.Error(), "-context-window") || calls != before {
		t.Fatal("an offline preview without a window", calls, err)
	}
}

func TestREPLHandoffWithoutNotebookWritesFailsTheTurn(t *testing.T) {
	test := newTurnTestEnvAt(t, "http://127.0.0.1:1", "-notebook", t.TempDir(), "-context-window", "8192", "-tool-include", "notebook/read")
	loop := &replLoop{env: test.env, base: test.opts}
	if action := loop.interpretInput("/handoff"); action.send || loop.failedTurns != 1 {
		t.Fatal(action, loop.failedTurns)
	}
}

func TestREPLPinsCommitLocallyAndPreserveLiteralWhitespace(t *testing.T) {
	test := newTurnTestEnvAt(t, "http://127.0.0.1:1", "-notebook", t.TempDir(), "-context-window", "8192")
	loop := &replLoop{env: test.env, base: test.opts}
	action := loop.interpretInput("/pin  Keep this indentation.  ")
	if action.send || loop.from == nil || loop.failedTurns != 0 {
		t.Fatal(action, loop.from, loop.failedTurns)
	}
	read := func() []contextstate.StandingPin {
		t.Helper()
		view, err := loop.from.View(loop.base)
		if err != nil {
			t.Fatal(err)
		}
		pins, err := contextstate.ActivePins(view.Messages, view.Refs)
		if err != nil {
			t.Fatal(err)
		}
		return pins
	}
	pins := read()
	if len(pins) != 1 || pins[0].Text != " Keep this indentation.  " {
		t.Fatal(pins)
	}
	id := pins[0].ID
	old := loop.from.Head
	loop.interpretInput("/pins")
	loop.interpretInput("/pin --from nowhere.0")
	if loop.from.Head != old || loop.failedTurns != 1 {
		t.Fatal("list or invalid operation changed history")
	}
	loop.interpretInput("/pin --replace " + id + "  New literal.  ")
	if p := read(); len(p) != 1 || p[0].ID != id || p[0].Text != " New literal.  " {
		t.Fatal(p)
	}
	loop.interpretInput("/unpin " + id)
	if p := read(); len(p) != 0 || p == nil {
		t.Fatal(p)
	}
}
