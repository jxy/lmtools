package workingcontext

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"reflect"
	"strings"
	"testing"
)

func TestCheckpointAndSizingAdvanceOverRawCommandBytes(t *testing.T) {
	ctx := context.Background()
	store := core.NewMemorySessionStore("", "read the tool output")
	content := strings.Repeat("\x80", 10000) + "end"
	if _, _, err := store.SaveToolResults(ctx, []core.ToolResult{{ID: "read", Output: content}}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Messages("")
	if err != nil {
		t.Fatal(err)
	}
	refs := MemoryRefs(raw)
	e := testEngine(contextcfg.Handoff)
	e.Binding.Window = 2048
	var calls, seen int
	e.Write = func(_ context.Context, _, source string) (string, error) {
		calls++
		if calls > 20 {
			t.Fatal("writer repeatedly received an empty portion of the record")
		}
		seen += strings.Count(source, "\x80")
		return "progress", nil
	}
	if _, err := e.summarize(ctx, "", raw, refs, 0, len(raw)); err != nil || seen != 10000 || calls < 2 {
		t.Fatalf("writer consumed %d bytes in %d calls: %v", seen, calls, err)
	}
	e.CountText = func(_ context.Context, part string) (int, error) {
		if part == "" {
			t.Fatal("sizing sent an empty piece")
		}
		return 1, nil
	}
	if err := e.admit(ctx, raw, false); err != nil {
		t.Fatal(err)
	}
	for cut := 2; cut < 5; cut++ {
		if got := mcp.TextBoundary("a🌱z", cut); got != 1 {
			t.Fatalf("split a valid rune at byte %d (cut %d)", got, cut)
		}
	}
}

func TestCheckpointSplitsAnOversizedRecordHeader(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	e.Binding.Window = 2048
	name := strings.Repeat("long-tool-name", 150)
	msg := core.TypedMessage{Role: "user", Blocks: []core.Block{core.ToolResultBlock{Name: name, Content: "last bytes"}}}
	raw := []core.TypedMessage{msg}
	refs := MemoryRefs(raw)
	var joined strings.Builder
	e.Write = func(_ context.Context, _, source string) (string, error) {
		_, data, ok := strings.Cut(source, "\n\nOriginal records:\n")
		if !ok || len(data) > e.Binding.Window/2 {
			t.Fatal("invalid source chunk")
		}
		part, ok := strings.CutPrefix(data, "\n[continued record] ")
		if !ok || part == "" {
			t.Fatal("chunk omitted record content")
		}
		joined.WriteString(part)
		return "saved", nil
	}
	_, err := e.summarize(context.Background(), "", raw, refs, 0, 1)
	want := "\n[" + refs[0].Source + ".0 result " + name + "] last bytes"
	if err != nil || joined.String() != want {
		t.Fatalf("oversized tool label or its result lost bytes: %v", err)
	}
}

func TestLargePNGUsesSameVisualBudgetInAttachmentsAndToolResults(t *testing.T) {
	var data bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 400, 400))); err != nil {
		t.Fatal(err)
	}
	if data.Len() < 400000 {
		t.Fatal("fixture must exceed the old byte-based estimate")
	}
	im := core.ImageBlock{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes())}
	e := testEngine(contextcfg.Full)
	e.Binding.Window = 200000
	for _, block := range []core.Block{im, core.ToolResultBlock{ToolUseID: "image", Images: []core.ImageBlock{im}}} {
		raw := []core.TypedMessage{{Role: "user", Blocks: []core.Block{block}}, core.NewTextMessage("user", "follow up")}
		view, err := e.Project(context.Background(), raw, nil, MemoryTranscript(nil), false)
		if err != nil || !reflect.DeepEqual(view, raw) {
			t.Fatalf("image projection changed or failed: %v", err)
		}
	}
	small := core.ImageBlock{URL: "https://example.invalid/same-pixels"}
	if size(core.TypedMessage{Blocks: []core.Block{im}}) != size(core.TypedMessage{Blocks: []core.Block{small}}) {
		t.Fatal("image budget depends on transport encoding")
	}
	result := core.ToolResultBlock{Images: []core.ImageBlock{im, im}}
	if got := size(core.TypedMessage{Blocks: []core.Block{result}}) - size(core.TypedMessage{Blocks: []core.Block{core.ToolResultBlock{}}}); got != 2*estimatedImageTokens {
		t.Fatalf("two tool images charged %d tokens", got)
	}
}

func testEngine(policy string) *Engine {
	settings := contextcfg.Settings{Policy: policy, Window: 8192, Trigger: 1200}
	return &Engine{Binding: contextcfg.Binding{Settings: settings}, History: &History{phaseHints: settings.PhaseHints()}}
}

func TestProseCheckpointKeyUsesEffectiveDefaults(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	implicit := e.tag()
	e.Binding.View, e.Binding.Format = "catalog", "prose"
	if e.tag() != implicit {
		t.Fatal("spelling out the default changed the checkpoint key")
	}
	e.Binding.Early = true
	if e.tag() == implicit {
		t.Fatal("enabling phase requests must distinguish checkpoint candidates")
	}
}

func TestCheckpointPreservesInstructionsAndReplaysStableSuffix(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	calls := 0
	e.Write = func(_ context.Context, in, source string) (string, error) {
		calls++
		if strings.Contains(source, "opaque-secret") {
			t.Fatal("reasoning entered writer")
		}
		if !strings.HasPrefix(in, handoffPrompt) {
			t.Fatal("default did not use the prose writer")
		}
		return "User selected amber; earlier test passed.", nil
	}
	raw := []core.TypedMessage{
		core.NewTextMessage("system", "system"), core.NewTextMessage("developer", "D1"), core.NewTextMessage("user", strings.Repeat("prior ", 510)),
		{Role: "assistant", Blocks: []core.Block{core.ReasoningBlock{EncryptedContent: "opaque-secret"}, core.TextBlock{Text: "complete"}}},
		core.NewTextMessage("developer", "D2"), core.NewTextMessage("user", "new instruction"),
	}
	refs := MemoryRefs(raw[:4])
	if _, err := e.Project(context.Background(), raw, refs, MemoryTranscript(refs), true); err == nil || calls != 0 {
		t.Fatalf("preview %v calls %d", err, calls)
	}
	v, err := e.Project(context.Background(), raw, refs, MemoryTranscript(refs), false)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || v[0].Role != "system" || v[1].Role != "developer" || v[len(v)-2].Role != "developer" || !reflect.DeepEqual(v[len(v)-1], raw[len(raw)-1]) {
		t.Fatalf("%d calls, view %#v", calls, v)
	}
	raw = append(raw, core.NewTextMessage("assistant", "done"), core.NewTextMessage("user", "next"))
	e.Binding.Trigger = 7000
	w, err := e.Project(context.Background(), raw, MemoryRefs(raw[:len(raw)-1]), MemoryTranscript(MemoryRefs(raw[:len(raw)-1])), false)
	if err != nil || !reflect.DeepEqual(w[:len(v)], v) || calls != 1 {
		t.Fatalf("prefix changed: %v", err)
	}
}

func TestCompactionAfterCompletedBatch(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	e.Write = func(context.Context, string, string) (string, error) { return "Receipt confirmed; continue.", nil }
	raw := []core.TypedMessage{
		core.NewTextMessage("user", "actual goal"),
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "id", Name: "read", Input: json.RawMessage("{}")}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "id", Name: "read", Content: strings.Repeat("x", 3200)}}},
	}
	v, err := e.Project(context.Background(), raw, MemoryRefs(raw), MemoryTranscript(MemoryRefs(raw)), false)
	if err != nil {
		t.Fatal(err)
	}
	if e.current.Count != 0 || !reflect.DeepEqual(v, raw) {
		t.Fatal("soft threshold hid the newest tool observation")
	}
	e.Binding.Early = true
	raw[1].Blocks = append(raw[1].Blocks, core.ToolUseBlock{ID: "phase", Name: "mcp__history__request_compaction"})
	raw[2].Blocks = append(raw[2].Blocks, core.ToolResultBlock{ToolUseID: "phase", Content: "Checkpoint requested"})
	v, err = e.Project(context.Background(), raw, MemoryRefs(raw), MemoryTranscript(MemoryRefs(raw)), false)
	if err != nil || !reflect.DeepEqual(v, raw) {
		t.Fatal("phase hint hid a fresh observation", err)
	}
	goal := raw[0]
	raw = append([]core.TypedMessage{core.NewTextMessage("user", strings.Repeat("earlier work ", 1700)), core.NewTextMessage("assistant", "done")}, raw...)
	v, err = e.Project(context.Background(), raw, MemoryRefs(raw), MemoryTranscript(MemoryRefs(raw)), false)
	if err != nil {
		t.Fatal(err)
	}
	if !e.current.Continue || e.current.Count != len(raw) || !reflect.DeepEqual(v[len(v)-2], goal) {
		t.Fatalf("bad continuation: %#v", v)
	}
	data, _ := json.Marshal(v[len(v)-1])
	if !strings.Contains(string(data), strings.Repeat("x", 3200)) {
		t.Fatal("cut lost new tool output")
	}
	for _, m := range v {
		if hasCall(m) || resultMessage(m) {
			t.Fatal("orphaned tool state")
		}
	}
	raw = raw[2:]
	raw[2].Blocks = []core.Block{core.ToolResultBlock{ToolUseID: "different", Name: "read", Content: "bad"}}
	if n, _ := cutPoint(raw, 0, len(raw)); n != 0 {
		t.Fatal("cut malformed batch")
	}
}

func TestCheckpointFailureAndHardCeiling(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	e.Write = func(context.Context, string, string) (string, error) { return "", errors.New("unavailable") }
	raw := []core.TypedMessage{core.NewTextMessage("user", strings.Repeat("a", 3000)), core.NewTextMessage("assistant", "ok"), core.NewTextMessage("user", "next")}
	v, err := e.Project(context.Background(), raw, MemoryRefs(raw[:2]), MemoryTranscript(MemoryRefs(raw[:2])), false)
	if err != nil || e.current.Count != 0 || !reflect.DeepEqual(v, raw) {
		t.Fatal(err)
	}
	raw[0] = core.NewTextMessage("user", strings.Repeat("a", 16000))
	if _, err = e.Project(context.Background(), raw, MemoryRefs(raw[:2]), MemoryTranscript(MemoryRefs(raw[:2])), false); err == nil {
		t.Fatal("over-limit fallback")
	}
}

func TestHistoryOffsetsAndScope(t *testing.T) {
	body := strings.Repeat("明", 800) + "PRECISE" + strings.Repeat("z", 300)
	msg := []core.TypedMessage{core.NewTextMessage("user", body), {Role: "assistant", Blocks: []core.Block{core.ReasoningBlock{Signature: "private"}}}, core.NewTextMessage("user", "recent")}
	refs := MemoryRefs(msg)
	h := &History{}
	h.Bind(msg, refs, 2, MemoryTranscript(refs))
	p, err := h.search(context.Background(), query{Query: "precise"})
	if err != nil || len(p.Hits) != 1 || p.Hits[0].Offset == 0 || !strings.Contains(p.Hits[0].Snippet, "PRECISE") {
		t.Fatal(p, err)
	}
	v, err := h.read(context.Background(), p.Hits[0].Ref, p.Hits[0].Offset, 400)
	b, _ := json.Marshal(v)
	if err != nil || !strings.Contains(string(b), "PRECISE") {
		t.Fatal(err)
	}
	for _, test := range []query{{Query: "recent", Scope: "archived"}, {Query: "private"}} {
		p, err = h.search(context.Background(), test)
		if err != nil || len(p.Hits) != 0 || !p.Exhausted {
			t.Fatal(p, err)
		}
	}
	if _, err := h.read(context.Background(), "not-in-this-branch.0", 0, 0); err == nil {
		t.Fatal("read unknown ref")
	}
}

func TestOversizedObservationCanBePagedOnlyWithOriginalHistory(t *testing.T) {
	ctx := context.Background()
	payload := strings.Repeat("界", 5000) + "ROW-EXACT" + strings.Repeat("λ", 7500)
	raw := []core.TypedMessage{
		core.NewTextMessage("user", "Find the specified row."),
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "large", Name: "events"}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "large", Name: "events", Content: payload}}},
	}
	refs := MemoryRefs(raw)
	e := testEngine(contextcfg.Handoff)
	e.Write = func(context.Context, string, string) (string, error) { return "Read the saved observation.", nil }
	if _, err := e.Project(ctx, raw, refs, MemoryTranscript(refs), false); err == nil {
		t.Fatal("paged without a history reader")
	}
	e.CanReadHistory = true
	view, err := e.Project(ctx, raw, refs, MemoryTranscript(refs), false)
	if err != nil || e.current.PagedResult != refs[2].Source {
		t.Fatalf("missing source for oversized observation: %v", err)
	}
	b, _ := json.Marshal(view)
	if strings.Contains(string(b), "ROW-EXACT") || !strings.Contains(string(b), refs[2].Source+".0") {
		t.Fatal("expected an attributed excerpt")
	}
	hits, err := e.History.search(ctx, query{Query: "ROW-EXACT", Scope: "archived"})
	if err != nil || len(hits.Hits) != 1 {
		t.Fatal(hits, err)
	}
	v, err := e.History.read(context.Background(), hits.Hits[0].Ref, hits.Hits[0].Offset, 300)
	b, _ = json.Marshal(v)
	if err != nil || !strings.Contains(string(b), "ROW-EXACT") {
		t.Fatalf("cannot recover the original from its source: %s %v", b, err)
	}
	// A later run with history reads off keeps the excerpts and says so once.
	off := testEngine(contextcfg.Handoff)
	off.current, off.loaded = e.current, true
	var notices []string
	off.Notice = func(format string, _ ...any) { notices = append(notices, format) }
	for i := 0; i < 2; i++ {
		view, err = off.Project(ctx, raw, refs, MemoryTranscript(refs), false)
		b, _ = json.Marshal(view)
		if err != nil || strings.Contains(string(b), "Read by byte offset") || !strings.Contains(string(b), "only these excerpts") || len(notices) != 1 {
			t.Fatalf("history reads off: %v %q", err, notices)
		}
	}
}
