package workingcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"reflect"
	"strings"
	"testing"
)

func nativeBudgetFixture() []core.TypedMessage {
	messages := []core.TypedMessage{core.NewTextMessage("system", "original system")}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("call-%d", i)
		messages = append(messages,
			core.NewTextMessage("user", fmt.Sprintf("question %d", i)),
			core.TypedMessage{Role: "assistant", Blocks: []core.Block{
				core.ReasoningBlock{Provider: "openai", EncryptedContent: "signed-" + id},
				core.ToolUseBlock{ID: id, Name: "lookup", Input: json.RawMessage(`{"id":1}`)},
			}},
			core.TypedMessage{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: id, Name: "lookup", Content: strings.Repeat("observation ", 225)}}},
			core.NewTextMessage("assistant", "completed "+id),
		)
	}
	return append(messages, core.NewTextMessage("user", "continue"))
}

func TestNativeBudgetPreservesWholeExchanges(t *testing.T) {
	e := testEngine(contextcfg.Full)
	e.ExtraTokens = 150
	raw := nativeBudgetFixture()
	var segments int
	var seen []string
	e.CountMessages = func(_ context.Context, part []core.TypedMessage) (int, error) {
		segments++
		if !reflect.DeepEqual(part[0], raw[0]) || estimate(part, e.ExtraTokens) > e.Binding.Ceiling()/2 {
			t.Fatalf("invalid segment prefix or size: %d", estimate(part, e.ExtraTokens))
		}
		for i, m := range part {
			if !hasCall(m) {
				continue
			}
			if i == 0 || i+1 >= len(part) || !completedBatch(part, i+1) {
				t.Fatal("split a tool exchange")
			}
			call := m.Blocks[1].(core.ToolUseBlock)
			signature := m.Blocks[0].(core.ReasoningBlock)
			if signature.EncryptedContent != "signed-"+call.ID || part[i-1].Role != "user" {
				t.Fatal("lost native reasoning or the original user turn")
			}
			seen = append(seen, call.ID)
		}
		return estimate(part, e.ExtraTokens) / 2, nil
	}
	e.CountText = func(context.Context, string) (int, error) {
		t.Fatal("native blocks entered the text-only path")
		return 0, nil
	}
	if _, err := e.Project(context.Background(), raw, nil, nil, true); err == nil || segments != 0 {
		t.Fatal("preview measured or skipped the oversized projection")
	}
	view, err := e.Project(context.Background(), raw, nil, nil, false)
	if err != nil || !reflect.DeepEqual(view, raw) || segments < 2 || len(seen) != 8 {
		t.Fatalf("view %d segments %d calls %v: %v", len(view), segments, seen, err)
	}
	for i, id := range seen {
		if id != fmt.Sprintf("call-%d", i) {
			t.Fatal("reordered source", seen)
		}
	}
	previous := segments
	if err = e.admit(context.Background(), raw, false); err != nil || previous != segments {
		t.Fatal("native usage cache failed", err)
	}
}

// -developer puts a developer message before each new user request. It
// starts that exchange, so native sizing still finds the boundaries.
func TestNativeBudgetKeepsDeveloperMessagesWithTheirTurn(t *testing.T) {
	e := testEngine(contextcfg.Full)
	e.ExtraTokens = 150
	var raw []core.TypedMessage
	for _, m := range nativeBudgetFixture() {
		if text, ok := m.Blocks[0].(core.TextBlock); ok && m.Role == "user" && strings.HasPrefix(text.Text, "question ") {
			raw = append(raw, core.NewTextMessage("developer", "Answer in English."))
		}
		raw = append(raw, m)
	}
	segments := 0
	e.CountMessages = func(_ context.Context, part []core.TypedMessage) (int, error) {
		segments++
		// Every probe repeats the leading system and developer messages.
		for i := 2; i < len(part); i++ {
			if part[i].Role == "developer" && (i+1 >= len(part) || part[i+1].Role != "user" || !strings.HasPrefix(part[i+1].Blocks[0].(core.TextBlock).Text, "question ")) {
				t.Fatal("separated a developer message from its request")
			}
		}
		return estimate(part, e.ExtraTokens) / 2, nil
	}
	e.CountText = func(context.Context, string) (int, error) {
		t.Fatal("native blocks entered the text-only path")
		return 0, nil
	}
	view, err := e.Project(context.Background(), raw, nil, nil, false)
	if err != nil || !reflect.DeepEqual(view, raw) || segments < 2 {
		t.Fatalf("view %d segments %d: %v", len(view), segments, err)
	}
}

func TestNativeBudgetMeasuresLargeExchangeAlone(t *testing.T) {
	const ceiling = 7168
	const extra = 150
	raw := nativeBudgetFixture()
	raw[3].Blocks = []core.Block{core.ToolResultBlock{
		ToolUseID: "call-0",
		Name:      "lookup",
		Content:   strings.Repeat("x", 9000),
	}}
	parts := nativeSegments(raw, extra, ceiling)
	var reconstructed []core.TypedMessage
	large := 0
	for _, part := range parts {
		// Each probe repeats the leading system and ends with a counting prompt.
		exchanges := part[1 : len(part)-1]
		if len(exchanges) == 0 || estimate(part, extra) > ceiling {
			t.Fatal("sizing probe contains no exchange or exceeds the configured ceiling")
		}
		if estimate(part, extra) > ceiling/2 {
			large++
			if !reflect.DeepEqual(exchanges, raw[1:5]) {
				t.Fatal("large exchange was split or combined with another turn")
			}
		}
		reconstructed = append(reconstructed, exchanges...)
	}
	if large != 1 || !reflect.DeepEqual(reconstructed, raw[1:]) {
		t.Fatal("sizing probes did not preserve the full input and its one large exchange")
	}
}

func TestNativeBudgetRejectsUnboundedOrInvalidSegments(t *testing.T) {
	raw := nativeBudgetFixture()
	for _, last := range []core.TypedMessage{
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "pending", Name: "lookup"}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "orphan", Content: "x"}}},
		{Role: "user", Blocks: []core.Block{core.ImageBlock{URL: "https://example.invalid/image"}}},
		{Role: "user", Blocks: []core.Block{core.AudioBlock{ID: "provider-audio"}}},
		core.NewTextMessage("user", strings.Repeat("z", 20000)),
	} {
		extended := append(append([]core.TypedMessage{}, raw...), last)
		if parts := nativeSegments(extended, 150, 7168); parts != nil {
			t.Fatalf("unexpected native segments for %T", last.Blocks[0])
		}
	}
	e := testEngine(contextcfg.Full)
	e.CountMessages = func(context.Context, []core.TypedMessage) (int, error) { return e.Binding.Ceiling()/2 + 1, nil }
	var over budgetExceeded
	if err := e.admit(context.Background(), raw, false); !errors.As(err, &over) {
		t.Fatalf("accepted oversized native input: %v", err)
	}
	e = testEngine(contextcfg.Full)
	e.CountMessages = func(context.Context, []core.TypedMessage) (int, error) {
		return 0, errors.New("incompatible native measurement")
	}
	e.CountText = func(_ context.Context, text string) (int, error) { return len(text) / 5, nil }
	if err := e.admit(context.Background(), raw, false); err != nil {
		t.Fatalf("public text fallback failed: %v", err)
	}
}

func TestNativeMeasurementAllowsForTheAnswerSchemaOnce(t *testing.T) {
	e := testEngine(contextcfg.Full)
	e.SchemaTokens = 200
	e.CountMessages = func(context.Context, []core.TypedMessage) (int, error) { return 400, nil }
	parts := [][]core.TypedMessage{{core.NewTextMessage("user", "one")}, {core.NewTextMessage("user", "two")}}
	if err := e.measureNative(context.Background(), parts, 1000); err != nil {
		t.Fatal(err)
	}
	if err := e.measureNative(context.Background(), parts, 999); !exceedsBudget(err) {
		t.Fatalf("missing answer schema in measured budget: %v", err)
	}
}
