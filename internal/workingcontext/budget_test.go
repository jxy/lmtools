package workingcontext

import (
	"context"
	"errors"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"strings"
	"testing"
)

func TestUsageMeasurementRescuesProtectedText(t *testing.T) {
	e := testEngine(contextcfg.Full)
	text := strings.Repeat("abc", 5500)
	raw := []core.TypedMessage{core.NewTextMessage("user", text)}
	calls := 0
	e.CountText = func(_ context.Context, s string) (int, error) {
		calls++
		if len(s) > e.Binding.Window/2 {
			t.Fatalf("unbounded piece: %d", len(s))
		}
		return len(s)/4 + 10, nil
	}
	if _, err := e.Project(context.Background(), raw, nil, nil, true); err == nil || calls != 0 {
		t.Fatal("preview performed or skipped measurement")
	}
	v, err := e.Project(context.Background(), raw, nil, nil, false)
	if err != nil || len(v) != 1 || calls == 0 {
		t.Fatal(err, calls)
	}
	first := calls
	if err = e.admit(context.Background(), raw, false); err != nil || calls != first {
		t.Fatal("remeasured identical text", calls, err)
	}
}

func TestUnavailableMeasurementAllowsCutDuringToolTurn(t *testing.T) {
	raw := []core.TypedMessage{
		core.NewTextMessage("user", "finish the task"),
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "call", Name: "lookup"}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "call", Name: "lookup", Content: strings.Repeat("x", 16000)}}},
	}
	for _, probeErr := range []error{nil, errors.New("provider omitted input usage"), errors.New("connection lost"), lmerrors.NewHTTPError(429, "retry"), lmerrors.NewHTTPError(503, "unavailable")} {
		e := testEngine(contextcfg.Handoff)
		e.CanReadHistory = true
		probes := 0
		e.CountText = func(context.Context, string) (int, error) { probes++; return 0, probeErr }
		e.Write = func(context.Context, string, string) (string, error) { return "finish the current task", nil }
		view, err := e.Project(context.Background(), raw, MemoryRefs(raw), nil, false)
		if err != nil || probes == 0 || !e.current.Continue || e.current.PagedResult == "" || e.current.Fallback || len(view) == 0 {
			t.Fatalf("%v: %d probes, checkpoint %+v, error %v", probeErr, probes, e.current, err)
		}
	}
	for _, cause := range []error{context.Canceled, lmerrors.NewHTTPError(401, "renew")} {
		e := testEngine(contextcfg.Handoff)
		e.CountText = func(context.Context, string) (int, error) { return 0, cause }
		_, err := e.Project(context.Background(), raw, MemoryRefs(raw), nil, false)
		if !errors.Is(err, cause) || exceedsBudget(err) {
			t.Fatal(cause, err)
		}
	}
}

func TestMeasurementFailureAndOpaqueBudget(t *testing.T) {
	e := testEngine(contextcfg.Full)
	e.CountText = func(context.Context, string) (int, error) { return 0, errors.New("missing count") }
	if _, err := e.Project(context.Background(), []core.TypedMessage{core.NewTextMessage("user", strings.Repeat("z", 20000))}, nil, nil, false); err == nil || !strings.Contains(err.Error(), "missing count") {
		t.Fatal(err)
	}
	e.CountText = func(context.Context, string) (int, error) {
		t.Fatal("opaque reasoning was put into a measurement prompt")
		return 0, nil
	}
	raw := []core.TypedMessage{{Role: "assistant", Blocks: []core.Block{core.ReasoningBlock{EncryptedContent: strings.Repeat("z", 20000)}}}}
	if err := e.admit(context.Background(), raw, false); err == nil {
		t.Fatal("unbounded opaque input")
	}
}
