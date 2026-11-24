package workingcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	session "lmtools/internal/oldsession"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinsSurviveLossyHandoffAndKeepTheirUserRole(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	entry, err := session.PinEntry(session.PinChange{Action: "add"}, "Always use ALPHA.", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := []core.TypedMessage{{Role: "user", Blocks: entry.Blocks}, core.NewTextMessage("assistant", "understood"), core.NewTextMessage("user", strings.Repeat("older data ", 500)), core.NewTextMessage("assistant", "complete"), core.NewTextMessage("user", "answer exactly READY")}
	refs := MemoryRefs(raw[:4])
	refs[0].Pin = entry.Message.Pin
	e.Write = func(context.Context, string, string) (string, error) { return "work status", nil }
	got, err := e.Project(context.Background(), raw, refs, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	n := len(got)
	if n < 3 || got[n-2].Role != "user" || !strings.Contains(got[n-2].Blocks[0].(core.TextBlock).Text, "Always use ALPHA.") || got[n-1].Blocks[0].(core.TextBlock).Text != "answer exactly READY" {
		t.Fatal(got)
	}
	if e.Receipt().PinDigest == "" || e.Receipt().PinDigest != session.PinDigest(e.current.Pins) {
		t.Fatal("missing pin receipt")
	}
}

func TestPinPlacementOnProviderWires(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("test-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	pin := session.StandingPin{ID: "one", Source: "user-source.0", End: 14, Text: "EXACT-PIN-TEXT"}
	active := []session.StandingPin{pin}
	replace, err := session.PinEntry(session.PinChange{Action: "replace", ID: pin.ID}, "UPDATED-USER-RULE", active)
	if err != nil {
		t.Fatal(err)
	}
	remove, err := session.PinEntry(session.PinChange{Action: "remove", ID: pin.ID}, "", active)
	if err != nil {
		t.Fatal(err)
	}
	raw := []core.TypedMessage{
		core.NewTextMessage("system", "System policy"),
		core.NewTextMessage("user", "old request"),
		core.NewTextMessage("assistant", "done"),
		{Role: "user", Blocks: replace.Blocks},
		{Role: "user", Blocks: remove.Blocks},
		core.NewTextMessage("user", "CURRENT-LIVE-REQUEST"),
	}
	view := project(raw, session.Checkpoint{Count: 3, Handoff: "historical summary", Tail: "RECENT-EVIDENCE", Catalog: "FROZEN-CATALOG", Pins: []session.StandingPin{pin}}, "", true)
	for _, tc := range []struct {
		provider, model, field string
		responses              bool
	}{{"openai", "model", "input", true}, {"openai", "model", "messages", false}, {"anthropic", "claude-test", "messages", false}, {"google", "gemini-test", "contents", false}} {
		t.Run(tc.provider+tc.field, func(t *testing.T) {
			opts := core.RequestOptions{Provider: tc.provider, ProviderURL: "http://127.0.0.1:1/v1", APIKeyFile: key, Model: tc.model, OpenAIResponses: tc.responses}
			_, body, err := core.BuildChatRequest(opts, view, core.ChatBuildOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err = json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			var msgs []map[string]json.RawMessage
			if err = json.Unmarshal(payload[tc.field], &msgs); err != nil {
				t.Fatal(err)
			}
			seen := 0
			for _, m := range msgs {
				b, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(b, []byte(pin.Text)) {
					seen++
					if string(m["role"]) != `"user"` {
						t.Fatal("pin has wrong role", string(b))
					}
				}
				if (bytes.Contains(b, []byte("UPDATED-USER-RULE")) || bytes.Contains(b, []byte(remove.Message.Content))) && string(m["role"]) != `"user"` {
					t.Fatal("later pin change has wrong role", string(b))
				}
			}
			pinned := bytes.Index(body, []byte(pin.Text))
			replaced := bytes.Index(body, []byte("UPDATED-USER-RULE"))
			withdrawn := bytes.Index(body, []byte(remove.Message.Content))
			if seen != 1 || pinned < bytes.Index(body, []byte("RECENT-EVIDENCE")) || pinned < bytes.Index(body, []byte("FROZEN-CATALOG")) || replaced <= pinned || withdrawn <= replaced || bytes.Index(body, []byte("CURRENT-LIVE-REQUEST")) <= withdrawn {
				t.Fatal("pin order or multiplicity", string(body))
			}
		})
	}
}

func TestDeferredWriterAndPhaseRequestAtCompletedExchange(t *testing.T) {
	ctx := context.Background()
	e := testEngine(contextcfg.Handoff)
	e.Binding.Early = true
	e.Binding.Trigger = 3000
	raw := []core.TypedMessage{
		core.NewTextMessage("user", strings.Repeat("a", 3000)),
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "hint", Name: "mcp__history__request_compaction"}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "hint", Name: "mcp__history__request_compaction", Content: "Checkpoint requested"}}},
	}
	calls := 0
	e.Write = func(context.Context, string, string) (string, error) {
		calls++
		return "", errors.New("writer unavailable")
	}
	if _, err := e.Project(ctx, raw, MemoryRefs(raw), nil, false); err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
	raw = append(raw, core.NewTextMessage("assistant", "phase complete"), core.NewTextMessage("user", "next task"))
	refs := MemoryRefs(raw[:4])
	for i := 0; i < 2; i++ {
		if _, err := e.Project(ctx, raw, refs, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 || e.Receipt().Deferred == "" {
		t.Fatal("same eligible prefix retried", calls)
	}
}

func TestInterruptedCorrectionCarriesTheOriginalRequestAndLastObservation(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	raw := []core.TypedMessage{
		core.NewTextMessage("user", "Only use the archive for mirror reads above the historical cutoff."),
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "work", Name: "read"}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "work", Name: "read", Content: "observed cutoff 470 " + strings.Repeat("x", 3000)}}},
		core.NewTextMessage("assistant", "partial"),
		core.NewTextMessage("user", "Continue with the next release."),
	}
	refs := MemoryRefs(raw[:4])
	refs[3].Incomplete = true
	e.Write = func(context.Context, string, string) (string, error) { return "The release was discussed.", nil }
	view, err := e.Project(context.Background(), raw, refs, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	evidence := view[0].Blocks[0].(core.TextBlock).Text
	for _, want := range []string{refs[0].Source + ".0", refs[2].Source + ".0", "mirror reads", "observed cutoff 470"} {
		if !strings.Contains(e.current.Interrupted, want) || !strings.Contains(evidence, want) {
			t.Fatal("missing interrupted evidence", want)
		}
	}
	if view[len(view)-1].Blocks[0].(core.TextBlock).Text != "Continue with the next release." {
		t.Fatal(view)
	}
}

func TestProtectedPinsTakePrecedenceOverCatalogAndRemainIdentifiable(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	e.Binding.Window = 4096
	raw := []core.TypedMessage{core.NewTextMessage("user", "start"), core.NewTextMessage("assistant", "done"), core.NewTextMessage("user", "next")}
	pins := []session.StandingPin{{ID: "p1", Source: "user.0", Text: "Preserve this entire instruction."}}
	cp := session.Checkpoint{Count: 2, Handoff: "handoff", Pins: pins, Catalog: strings.Repeat("b", 13000), Tail: strings.Repeat("x", 9000)}
	without := cp
	without.Pins = nil
	if err := e.fitCheckpoint(context.Background(), raw, MemoryRefs(raw[:2]), &without, false); err != nil || without.Catalog != "" || without.Tail != "" || without.Handoff != "handoff" {
		t.Fatalf("optional context blocked an unpinned turn: %v", err)
	}
	if err := e.fitCheckpoint(context.Background(), raw, MemoryRefs(raw[:2]), &cp, false); err != nil || cp.Catalog != "" || cp.Tail != "" || cp.Pins[0] != pins[0] {
		t.Fatal(err, cp.Count)
	}
	cp.Pins[0].Text = strings.Repeat("exact ", 2000)
	err := pinBudgetError(e.fitCheckpoint(context.Background(), raw, MemoryRefs(raw[:2]), &cp, false), cp.Pins)
	if !exceedsBudget(err) || !strings.Contains(err.Error(), "p1") || !strings.Contains(err.Error(), "ceiling") || len(cp.Pins[0].Text) != 12000 {
		t.Fatal("missing pinned-budget diagnostic", err)
	}
}

func TestNotebookTriggerAndCeilingInclusivity(t *testing.T) {
	ctx := context.Background()
	raw := []core.TypedMessage{core.NewTextMessage("user", "first"), core.NewTextMessage("assistant", "completed"), core.NewTextMessage("user", "continue")}
	refs := MemoryRefs(raw[:2])
	for _, offset := range []int{-1, 0, 1} {
		e := testEngine(contextcfg.Handoff)
		e.ExtraTokens = e.Binding.Threshold() + offset - estimate(raw, 0)
		writes := 0
		e.Write = func(context.Context, string, string) (string, error) { writes++; return "saved", nil }
		if _, err := e.Project(ctx, raw, refs, nil, false); err != nil || (writes > 0) != (offset >= 0) {
			t.Fatal(offset, writes, err)
		}
	}
	for _, offset := range []int{-1, 0, 1} {
		e := testEngine(contextcfg.Full)
		e.ExtraTokens = e.Binding.Ceiling() + offset - estimate(raw, 0)
		if err := e.admit(ctx, raw, false); (offset > 0) != exceedsBudget(err) {
			t.Fatal(offset, err)
		}
	}
}

func TestWriterBudgetRecovery(t *testing.T) {
	ctx := context.Background()
	raw := []core.TypedMessage{core.NewTextMessage("user", strings.Repeat("e", 18000)), core.NewTextMessage("assistant", "complete"), core.NewTextMessage("user", "next")}
	for _, mode := range []string{"oversize", "archive", "auth"} {
		e := testEngine(contextcfg.Handoff)
		e.CanReadHistory = true
		calls := 0
		e.Write = func(context.Context, string, string) (string, error) {
			calls++
			switch mode {
			case "oversize":
				return strings.Repeat("d", 1000), nil
			case "auth":
				return "", lmerrors.NewHTTPError(401, "renew")
			}
			return "", errors.New("writer unavailable")
		}
		if mode == "oversize" {
			raw[0] = core.NewTextMessage("user", strings.Repeat("e", 3000))
		} else {
			raw[0] = core.NewTextMessage("user", strings.Repeat("e", 18000))
		}
		_, err := e.Project(ctx, raw, MemoryRefs(raw[:2]), nil, false)
		switch mode {
		case "oversize":
			if err != nil || calls != 1 || len(e.current.Handoff) != 1000 {
				t.Fatal(mode, calls, err)
			}
		case "archive":
			if err != nil || !e.current.Fallback {
				t.Fatal(mode, err)
			}
		case "auth":
			if !credentialRefusal(err) || e.current.Count != 0 {
				t.Fatal(mode, err)
			}
		}
	}
}
