package main

import (
	"context"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotebookResolvesSelectedModelAndOperatorCap(t *testing.T) {
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
	opts := cfg.RequestOptions()
	resolve := func(model string) {
		t.Helper()
		opts.Model = model
		if err := resolveNotebookWindow(context.Background(), &cfg, &opts, core.NewTestNotifier(), ""); err != nil {
			t.Fatal(err)
		}
	}
	resolve("big")
	if opts.Context.Policy != "handoff" || opts.Context.Window != 1048576 || !opts.Context.Early {
		t.Fatal(opts.Context)
	}
	resolve("big")
	if calls != 1 {
		t.Fatal("catalog fetched again", calls)
	}
	cfg.PrintCurl = true
	resolve("big")
	opts.Model = "small"
	if err := resolveNotebookWindow(context.Background(), &cfg, &opts, core.NewTestNotifier(), ""); err == nil || !strings.Contains(err.Error(), "-context-window") {
		t.Fatal("offline preview silently selected a projection for another model", err)
	}
	if calls != 1 || opts.Context.WindowModel != "big" {
		t.Fatal("preview changed the saved selection", calls, opts.Context)
	}
	explicit := opts
	explicit.Context.WindowModel, explicit.Context.WindowProvider = "", ""
	explicit.Context.WindowCap, explicit.Context.Window = 4096, 4096
	if err := resolveNotebookWindow(context.Background(), &cfg, &explicit, core.NewTestNotifier(), ""); err != nil || explicit.Context.Window != 4096 || !explicit.Context.Compacts() || calls != 1 {
		t.Fatal(explicit.Context, calls, err)
	}
	cfg.PrintCurl = false
	resolve("small")
	if cfg.Context.Window != 4096 {
		t.Fatal(cfg.Context)
	}
	resolve("unknown")
	if cfg.Context.Policy != "full" || cfg.Context.Window != 0 {
		t.Fatal(cfg.Context)
	}
	opts.Context.WindowCap = 8192
	resolve("big")
	if cfg.Context.Policy != "handoff" || cfg.Context.Window != 8192 {
		t.Fatal(cfg.Context)
	}
	resolve("small")
	if cfg.Context.Window != 4096 {
		t.Fatal(cfg.Context)
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
	read := func() []session.StandingPin {
		t.Helper()
		reader, err := session.CreateContextBuilder(context.Background(), loop.from)
		if err != nil {
			t.Fatal(err)
		}
		raw, refs, err := reader()
		if err != nil {
			t.Fatal(err)
		}
		pins, err := session.ActivePins(raw, refs)
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
	old := *loop.from.Head
	loop.interpretInput("/pins")
	loop.interpretInput("/pin --from nowhere.0")
	if *loop.from.Head != old || loop.failedTurns != 1 {
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
