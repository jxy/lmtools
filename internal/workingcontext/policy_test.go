package workingcontext

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"lmtools/internal/session"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHandoffRecoversEarlierWriterAndMissedRecords(t *testing.T) {
	for _, mode := range []string{"memory", "disk"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			e := testEngine(contextcfg.Handoff)
			e.CanReadHistory = true
			var s *session.Session
			var raw []core.TypedMessage
			var refs []session.ContextRef
			if mode != "memory" {
				old := session.GetSessionsDir()
				session.SetSessionsDir(t.TempDir())
				t.Cleanup(func() { session.SetSessionsDir(old) })
				var err error
				s, err = session.CreateSession("", core.NewTestLogger(false))
				if err != nil {
					t.Fatal(err)
				}
			}
			exchange := func(question, answer string, receipt *session.WindowReceipt) {
				t.Helper()
				if s == nil {
					raw = append(raw, core.NewTextMessage("user", question), core.NewTextMessage("assistant", answer))
					refs = MemoryRefs(raw)
					return
				}
				if _, err := session.AppendMessageWithToolInteraction(ctx, s, session.Message{Role: core.RoleUser, Content: question}, nil, nil); err != nil {
					t.Fatal(err)
				}
				s.WindowUsed = receipt
				if _, err := session.SaveAssistantResponse(ctx, s, core.Response{Text: answer}, "model"); err != nil {
					t.Fatal(err)
				}
				var err error
				s, err = session.OpenSession(ctx, s.Path)
				if err != nil {
					t.Fatal(err)
				}
				read, err := session.CreateContextBuilder(ctx, s)
				if err != nil {
					t.Fatal(err)
				}
				raw, refs, err = read()
				if err != nil {
					t.Fatal(err)
				}
			}
			projectNext := func(engine *Engine) {
				t.Helper()
				_, err := engine.Project(ctx, append(slices.Clone(raw), core.NewTextMessage("user", "continue")), refs, s, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			exchange("FIRST-RAW "+strings.Repeat("a", 3000), "finished", nil)
			e.Write = func(context.Context, string, string) (string, error) { return "FIRST-HANDOFF", nil }
			projectNext(e)
			if e.current.Count != 2 {
				t.Fatal(e.current)
			}
			exchange("continue", "MISSED-EVIDENCE "+strings.Repeat("b", 18000), e.Receipt())
			e.Write = func(context.Context, string, string) (string, error) {
				return "", errors.New("temporary writer failure")
			}
			projectNext(e)
			if !e.current.Fallback || e.current.Count != 4 {
				t.Fatal(e.current)
			}
			used := e.Receipt()
			if s != nil {
				ref := refs[used.Count-1]
				if _, ok, err := session.CandidateCheckpoint(ctx, ref, e.tag(), used.Prefix); err != nil || ok {
					t.Fatal("fallback occupied the writer slot", ok, err)
				}
				retry := testEngine(contextcfg.Handoff)
				retry.CanReadHistory = true
				writes := 0
				retry.Write = func(context.Context, string, string) (string, error) { writes++; return "recovered on retry", nil }
				projectNext(retry)
				if writes == 0 || retry.current.Fallback || retry.current.Handoff != "recovered on retry" {
					t.Fatal("cached a failure", retry.current, writes)
				}
				preserved, ok, err := session.CandidateCheckpoint(ctx, ref, used.Tag, used.Prefix)
				if err != nil || !ok || !preserved.Fallback || preserved.Handoff != e.current.Handoff {
					t.Fatal("retry changed the active fallback", preserved, err)
				}
				other := testEngine(contextcfg.Handoff)
				other.CanReadHistory = true
				projectNext(other)
				if other.current.Tag != retry.current.Tag {
					t.Fatal("lost the recovered candidate")
				}
			}
			exchange("continue", "LATEST-EVIDENCE "+strings.Repeat("c", 3500), used)
			if s != nil {
				e = testEngine(contextcfg.Handoff)
				e.CanReadHistory = true
			}
			var sources strings.Builder
			e.Write = func(_ context.Context, _, source string) (string, error) {
				sources.WriteString(source)
				return "recovered handoff", nil
			}
			projectNext(e)
			for _, want := range []string{"FIRST-HANDOFF", "MISSED-EVIDENCE", "LATEST-EVIDENCE"} {
				if !strings.Contains(sources.String(), want) {
					t.Fatal("writer lacked", want)
				}
			}
			if e.current.Fallback || strings.Contains(sources.String(), "FIRST-RAW") {
				t.Fatal("writer restarted at the beginning")
			}
		})
	}
}

// A checkpoint adjusted to fit keeps its writing budget. A new notebook view
// with the same budget also keeps the previous handoff as the writer's
// starting point. Neither rereads the transcript from the first message.
func TestAdjustedCheckpointKeepsItsWritingBudget(t *testing.T) {
	for _, view := range []string{"", "tools"} {
		e := testEngine(contextcfg.Handoff)
		raw := []core.TypedMessage{core.NewTextMessage("user", "VERY-FIRST"), core.NewTextMessage("assistant", "done"), core.NewTextMessage("user", "next"), core.NewTextMessage("assistant", "UNSUMMARIZED "+strings.Repeat("a", 16000)), core.NewTextMessage("user", "continue")}
		refs := MemoryRefs(raw[:4])
		cp := session.Checkpoint{Window: e.Binding.Window, Threshold: e.Binding.Threshold(), Count: 2, Prefix: session.PrefixKeys(refs)[1], Handoff: "PRIOR-HANDOFF"}
		cp.Tag = derivedCheckpointTag(e.tag(), cp)
		if view != "" {
			e.Binding.Notebook, e.Binding.View = "notes", view
		}
		e.current, e.loaded = cp, true
		var sources strings.Builder
		e.Write = func(_ context.Context, _, src string) (string, error) { sources.WriteString(src); return "short", nil }
		_, err := e.Project(context.Background(), raw, refs, nil, false)
		if err != nil || strings.Contains(sources.String(), "VERY-FIRST") || !strings.Contains(sources.String(), "PRIOR-HANDOFF") || !strings.Contains(sources.String(), "UNSUMMARIZED") {
			t.Fatalf("view %q restarted or lost a record: %v", view, err)
		}
	}
}

// The first window has nothing to cut. A catalog the input ceiling cannot
// admit stays out of it, with one notice; a catalog that fits stays in.
func TestFirstWindowLeavesOutACatalogThatCannotFit(t *testing.T) {
	raw := []core.TypedMessage{core.NewTextMessage("system", "sys"), core.NewTextMessage("user", "short question")}
	large := "Project notebook at window start.\n" + strings.Repeat("note @rev — Title. A summary of the note.\n", 200)
	for _, catalog := range []string{"Project notebook at window start: empty.", large} {
		e := testEngine(contextcfg.Handoff)
		e.Binding.Window, e.Binding.Initial = 4096, catalog
		var notices []string
		e.Notice = func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) }
		for i := 0; i < 2; i++ {
			view, err := e.Project(context.Background(), raw, MemoryRefs(raw), nil, false)
			b, _ := json.Marshal(view)
			if err != nil || strings.Contains(string(b), "window start") == (catalog == large) || (len(notices) == 1) != (catalog == large) {
				t.Fatalf("catalog of %d bytes: %v %q", len(catalog), err, notices)
			}
		}
	}
	// A request that cannot fit even without the catalog reports only its refusal.
	e := testEngine(contextcfg.Handoff)
	e.Binding.Window, e.Binding.Initial = 4096, large
	var notices []string
	e.Notice = func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) }
	huge := []core.TypedMessage{core.NewTextMessage("system", "sys"), core.NewTextMessage("user", strings.Repeat("q", 10000))}
	if _, err := e.Project(context.Background(), huge, MemoryRefs(huge), nil, false); !exceedsBudget(err) || len(notices) != 0 {
		t.Fatal(err, notices)
	}
}

// A tools view leaves out a catalog that an earlier run saved with the
// active checkpoint. The catalog view keeps it.
func TestToolsViewLeavesOutASavedCatalog(t *testing.T) {
	raw := []core.TypedMessage{core.NewTextMessage("user", "first"), core.NewTextMessage("assistant", "done"), core.NewTextMessage("user", "next")}
	refs := MemoryRefs(raw)
	for _, view := range []string{"catalog", "tools"} {
		e := testEngine(contextcfg.Handoff)
		e.Binding.Notebook, e.Binding.View = "notes", view
		e.current = session.Checkpoint{Count: 2, Prefix: session.PrefixKeys(refs)[1], Handoff: "PRIOR-HANDOFF", Catalog: "OLD-CATALOG"}
		e.loaded = true
		visible, err := e.Project(context.Background(), raw, refs, nil, false)
		b, _ := json.Marshal(visible)
		if err != nil || !strings.Contains(string(b), "PRIOR-HANDOFF") || strings.Contains(string(b), "OLD-CATALOG") != (view == "catalog") {
			t.Fatal(view, err)
		}
	}
}

type neighboringServer struct{ core.MCPTools }

func (neighboringServer) Call(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return mcp.TextResult("remote", nil), nil
}

func TestLocalHistoryRoutingTimeoutAndRawPages(t *testing.T) {
	h := NewTools(neighboringServer{}, &mcp.ServerConfig{Name: "history"}, contextcfg.Settings{}, 25*time.Millisecond, nil)
	ctx := context.Background()
	remote := mcp.QualifiedName("history__db", "search")
	result, err := h.Call(ctx, remote, nil)
	if err != nil || !strings.Contains(result.Content[0].Text, "remote") {
		t.Fatal(result, err)
	}
	content := strings.Repeat("\x80", 600) + "NEEDLE"
	raw := []core.TypedMessage{{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{Name: remote, ID: "1"}}}, {Role: "user", Blocks: []core.Block{core.ToolResultBlock{Name: remote, ToolUseID: "1", Content: content}}}}
	refs := MemoryRefs(raw)
	h.History.Bind(raw, refs, 2, nil)
	res, err := h.History.search(ctx, query{Query: "NEEDLE"})
	if err != nil || len(res.Hits) != 1 || res.Hits[0].Tool != remote || res.Hits[0].Offset != 536 {
		t.Fatal(res, err)
	}
	ref := refs[1].Source + ".0"
	result, err = h.Call(ctx, "mcp__history__read", json.RawMessage(fmt.Sprintf(`{"ref":"%s","offset":128,"limit":9}`, ref)))
	if err != nil || result.IsError || !strings.Contains(result.Content[0].Text, `"next_offset":137`) || !strings.Contains(result.Content[0].Text, strings.Repeat("�", 9)) {
		t.Fatal(result, err)
	}
	if len(Records(raw[0], refs[0].Source, 0)) != 1 || IsHistoryTool(remote) || !IsHistoryTool("mcp__history__read") {
		t.Fatal(remote)
	}
	dir := t.TempDir()
	head := &session.Session{Path: dir, Head: &session.MessageRef{Path: dir, ID: "0001"}}
	h.History.Bind(raw, refs, 2, head)
	if err := session.WithSessionLock(dir, time.Second, func() error {
		parent, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err := h.Call(parent, "mcp__history__read", json.RawMessage(fmt.Sprintf(`{"ref":"%s"}`, ref)))
		if !errors.Is(err, context.DeadlineExceeded) || parent.Err() != nil {
			t.Fatalf("history missed its own deadline: %v, parent %v", err, parent.Err())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryMatchAcrossSearchChunkBoundary(t *testing.T) {
	h := &History{}
	for _, n := range []int{65532, 65535, 65536, 131071} {
		raw := []core.TypedMessage{core.NewTextMessage("user", strings.Repeat("a", n)+"K界")}
		h.Bind(raw, MemoryRefs(raw), 1, nil)
		got, err := h.search(context.Background(), query{Query: "k界", Scope: "all"})
		if err != nil || len(got.Hits) != 1 || !strings.Contains(got.Hits[0].Snippet, "K界") {
			t.Fatal(n, got, err)
		}
	}
}

func TestSavedAudioUsesDurationOrMediaEstimate(t *testing.T) {
	old := session.GetSessionsDir()
	session.SetSessionsDir(t.TempDir())
	t.Cleanup(func() { session.SetSessionsDir(old) })
	ctx := context.Background()
	s, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatal(err)
	}
	tone, err := os.ReadFile(filepath.Join("..", "..", "testdata", "api-fixtures", "cases", "openai-chat-audio-input", "fixtures", "tone.wav"))
	if err != nil {
		t.Fatal(err)
	}
	audio := core.AudioBlock{Data: base64.StdEncoding.EncodeToString(tone), Format: "wav"}
	if _, err := session.AppendMessageWithBlocks(ctx, s, session.Message{Role: core.RoleUser}, nil, nil, []core.Block{audio}); err != nil {
		t.Fatal(err)
	}
	s, err = session.OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	read, err := session.CreateContextBuilder(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	raw, refs, err := read()
	if err != nil || len(raw) != 1 || !reflect.DeepEqual(raw[0].Blocks, []core.Block{audio}) {
		t.Fatal("saved audio failed to round-trip", err)
	}
	e := testEngine(contextcfg.Full)
	e.Binding.Window = 4096
	view, err := e.Project(ctx, raw, refs, s, false)
	if err != nil || !reflect.DeepEqual(raw, view) {
		t.Fatal("audio without a duration failed admission or changed on resume", err)
	}
	for _, duration := range []int{1, 120} {
		audio.Duration = duration
		raw[0].Blocks = []core.Block{audio}
		ref := core.TypedMessage{Blocks: []core.Block{core.AudioBlock{ID: "recording", Duration: duration}}}
		if size(raw[0]) != size(ref) || size(ref) != 14+duration*estimatedAudioTokensPerSecond {
			t.Errorf("duration %d gave different budgets for the same recording", duration)
		}
		_, err := e.Project(ctx, raw, refs, s, false)
		if (err != nil) != (duration == 120) {
			t.Errorf("duration=%d, admission error=%v", duration, err)
		}
	}
	huge := core.TypedMessage{Blocks: []core.Block{core.AudioBlock{Duration: math.MaxInt}}}
	if got := estimate([]core.TypedMessage{huge, huge}, 100); got != math.MaxInt {
		t.Fatal("duration overflowed the context estimate", got)
	}
}

func TestHistoryDoesNotEchoPendingQuestionOrRevealPrivateBlocks(t *testing.T) {
	h := &History{}
	msgs := []core.TypedMessage{
		core.NewTextMessage("system", "private-system"), core.NewTextMessage("developer", "private-developer"),
		core.NewTextMessage("user", "source document says jade"), core.NewTextMessage("assistant", "done"),
		core.NewTextMessage("user", "Find missing-marker"),
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "q", Name: "mcp__history__search", Input: json.RawMessage(`{"query":"missing-marker"}`)}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "q", Name: "mcp__history__search", Content: "missing-marker"}}},
	}
	refs := MemoryRefs(msgs)
	h.Bind(msgs, refs, 4, nil)
	for _, word := range []string{"missing-marker", "private-system", "private-developer"} {
		p, err := h.search(context.Background(), query{Query: word})
		if err != nil || len(p.Hits) != 0 {
			t.Fatalf("leaked %q: %v %v", word, p, err)
		}
	}
	p, err := h.search(context.Background(), query{Query: "missing-marker", Scope: "all"})
	if err != nil || len(p.Hits) != 1 || p.Hits[0].Turn != 5 {
		t.Fatal(p, err)
	}
	for _, j := range []int{0, 1, 5, 6} {
		if _, err := h.read(context.Background(), refs[j].Source+".0", 0, 10); err == nil {
			t.Fatal("read hidden block", j)
		}
	}
}

func TestHistoryDescribesToolImagesWithoutCopyingTheirBytes(t *testing.T) {
	msg := core.TypedMessage{Role: "user", Blocks: []core.Block{core.ToolResultBlock{
		Name: "graph", Content: "figure generated", Images: []core.ImageBlock{{URL: "data:image/png;base64,private-image"}},
	}}}
	records := Records(msg, "source", 0)
	if len(records) != 1 || records[0].Text != "figure generated" || !strings.Contains(records[0].Capture, "Attached images: 1") || strings.Contains(records[0].Capture, "private-image") {
		t.Fatalf("unexpected history record: %#v", records)
	}
}

func TestFrozenCatalogAndCheckpointRenderAcrossWires(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	e.Write = func(context.Context, string, string) (string, error) { return "Earlier work.", nil }
	e.Binding.Initial = "Catalog version 1."
	raw := []core.TypedMessage{core.NewTextMessage("system", "Stable instructions"), core.NewTextMessage("user", strings.Repeat("z", 4000)), core.NewTextMessage("assistant", "Done."), core.NewTextMessage("user", "Question.")}
	first, err := e.Project(context.Background(), raw, MemoryRefs(raw[:3]), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, core.NewTextMessage("assistant", "answer"), core.NewTextMessage("user", "follow-up"))
	e.Binding.Trigger = 7000
	next, err := e.Project(context.Background(), raw, MemoryRefs(raw[:len(raw)-1]), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, wire := range []struct {
		p, m, field string
		responses   bool
	}{{"openai", "gpt-test", "messages", false}, {"openai", "gpt-test", "input", true}, {"anthropic", "claude-test", "messages", false}, {"google", "gemini-test", "contents", false}} {
		t.Run(fmt.Sprintf("%s-%s", wire.p, wire.field), func(t *testing.T) {
			options := core.RequestOptions{Provider: wire.p, Model: wire.m, OpenAIResponses: wire.responses}
			render := func(msg []core.TypedMessage) []any {
				_, b, err := core.BuildChatRequest(options, msg, core.ChatBuildOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var obj map[string]any
				if err = json.Unmarshal(b, &obj); err != nil {
					t.Fatal(err)
				}
				arr, ok := obj[wire.field].([]any)
				if !ok {
					t.Fatal(string(b))
				}
				return arr
			}
			a, b := render(first), render(next)
			if len(a) == 0 || len(b) < len(a) || !reflect.DeepEqual(a, b[:len(a)]) {
				t.Fatal("different provider prefix")
			}
		})
	}
}

func TestFrozenHistoryRejectsDeletedHead(t *testing.T) {
	old := session.GetSessionsDir()
	session.SetSessionsDir(t.TempDir())
	t.Cleanup(func() { session.SetSessionsDir(old) })
	ctx := context.Background()
	s, err := session.CreateSession("initial", core.NewTestLogger(false))
	if err != nil {
		t.Fatal(err)
	}
	s, err = session.OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	h := &History{}
	msg := []core.TypedMessage{core.NewTextMessage("user", "source")}
	h.Bind(msg, MemoryRefs(msg), 0, s)
	if err = session.DeleteNode(s.Path); err != nil {
		t.Fatal(err)
	}
	if _, err = h.CallTool(ctx, "search", json.RawMessage(`{"query":"source"}`)); err == nil {
		t.Fatal("served deleted scope")
	}
}

func TestCheckpointCandidateAndReceiptAfterBudgetChange(t *testing.T) {
	old := session.GetSessionsDir()
	session.SetSessionsDir(t.TempDir())
	t.Cleanup(func() { session.SetSessionsDir(old) })
	ctx := context.Background()
	s, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatal(err)
	}
	s, err = session.OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	question := "original evidence " + strings.Repeat("x", 48000)
	if _, err := session.AppendMessageWithToolInteraction(ctx, s, session.Message{Role: core.RoleUser, Content: question}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SaveAssistantResponse(ctx, s, core.Response{Text: "finished"}, "model"); err != nil {
		t.Fatal(err)
	}
	read, err := session.CreateContextBuilder(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	raw, refs, err := read()
	if err != nil {
		t.Fatal(err)
	}
	pending := core.NewTextMessage("user", "continue")
	raw = append(raw, pending)
	large := testEngine(contextcfg.Handoff)
	large.Binding.Window, large.Binding.Trigger = 65536, 22000
	oldMemo := strings.Repeat("large draft ", 500)
	large.Write = func(context.Context, string, string) (string, error) { return oldMemo, nil }
	if _, err := large.Project(ctx, raw, refs, nil, false); err != nil || large.Receipt().Count == 0 {
		t.Fatal("initial checkpoint", err)
	}
	for _, tc := range []struct {
		window, trigger int
		rewrite         bool
	}{
		{65536, 22000, false},
		{60000, 22000, true},
		{65536, 1200, true},
		{4096, 1200, true},
	} {
		e := testEngine(contextcfg.Handoff)
		e.Binding.Window, e.Binding.Trigger = tc.window, tc.trigger
		wrote := false
		e.Write = func(context.Context, string, string) (string, error) { wrote = true; return "short memo", nil }
		if _, err := e.Project(ctx, raw, refs, nil, false); err != nil || wrote != tc.rewrite {
			t.Fatalf("window %d trigger %d wrote %t: %v", tc.window, tc.trigger, wrote, err)
		}
	}
	// Commit an actual reply under the old receipt, then resume with a budget
	// too small for even that receipt's handoff. Its original evidence still exists.
	if _, err := session.AppendMessageWithToolInteraction(ctx, s, session.Message{Role: core.RoleUser, Content: "continue"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	s.WindowUsed = large.Receipt()
	// A committed receipt loads its checkpoint by the tag it recorded, which
	// can differ from the current settings tag.
	s.WindowUsed.Tag = "a9c3"
	if _, err = session.PublishCheckpoint(ctx, refs[s.WindowUsed.Count-1], s.WindowUsed.Tag, large.current); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SaveAssistantResponse(ctx, s, core.Response{Text: "continued"}, "model"); err != nil {
		t.Fatal(err)
	}
	raw, refs, err = read()
	if err != nil {
		t.Fatal(err)
	}
	resumed := testEngine(contextcfg.Handoff)
	resumed.Binding.Window, resumed.Binding.Trigger = 65536, 60000
	continued := append(slices.Clone(raw), core.NewTextMessage("user", "a new task"))
	view, err := resumed.Project(ctx, continued, refs, s, true)
	if err != nil || resumed.Receipt().Tag != "a9c3" || len(view) == 0 || !strings.Contains(view[0].Blocks[0].(core.TextBlock).Text, strings.TrimSpace(oldMemo)) {
		t.Fatal("a fitting committed checkpoint must be usable in an offline preview", err)
	}
	small := testEngine(contextcfg.Handoff)
	small.Binding.Window = 4096
	sawOriginal := false
	small.Write = func(_ context.Context, _, source string) (string, error) {
		if strings.Contains(source, strings.TrimSpace(oldMemo)) {
			t.Fatal("old oversized handoff entered the writer")
		}
		sawOriginal = sawOriginal || strings.Contains(source, "original evidence")
		return "short memo", nil
	}
	if _, err := small.Project(ctx, append(raw, pending), refs, nil, false); err != nil || !sawOriginal {
		t.Fatal("failed to rebuild active handoff from original records", err)
	}
}
