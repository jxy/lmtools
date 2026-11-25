package workingcontext

import (
	"context"
	"errors"
	"lmtools/internal/contextcfg"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/notebook"
	"lmtools/internal/session"
	"reflect"
	"strings"
	"testing"
)

func TestManualCompactKeepsPinsInstructionsAndUnfinishedToolTurn(t *testing.T) {
	c := newStoredConversation(t)
	pin, err := session.PinEntry(contextstate.PinChange{Action: "add"}, "Report times in UTC.", nil)
	if err != nil {
		t.Fatal(err)
	}
	instruction := core.NewTextMessage("developer", "Use the release checklist.")
	if _, err = c.s.Append(c.ctx, pin, session.Entry{Role: core.RoleDeveloper, Content: session.Content{Blocks: instruction.Blocks}}); err != nil {
		t.Fatal(err)
	}
	c.exchange(strings.Repeat("earlier data ", 100), "ready", nil)
	end := len(c.raw)
	c.toolTurn("active question", "inspect", "just observed")
	e := testEngine(contextcfg.Handoff)
	e.Write = func(_ context.Context, _, source string) (string, error) {
		if strings.Contains(source, "active question") || !strings.Contains(source, "earlier data") {
			t.Fatalf("unexpected writer source %q", source)
		}
		return "Previous phase complete.", nil
	}
	cut, err := e.Compact(c.ctx, c.raw, c.refs, c.tr)
	if err != nil || cut.Checkpoint == nil || cut.Checkpoint.Prefix != c.refs[end-1].Source || cut.After >= cut.Before {
		t.Fatalf("manual cut %+v: %v", cut, err)
	}
	next := testEngine(contextcfg.Full)
	next.Binding.ManualCheckpoint = cut.Checkpoint
	projected, err := c.request(next)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 3+len(c.raw[end:]) || !reflect.DeepEqual(projected[0], instruction) ||
		!strings.Contains(projected[1].Blocks[0].(core.TextBlock).Text, "Previous phase complete.") ||
		!strings.Contains(projected[2].Blocks[0].(core.TextBlock).Text, "Report times in UTC.") ||
		!reflect.DeepEqual(projected[3:], c.raw[end:]) {
		t.Fatalf("compacted context lost instructions or the active tool exchange: %+v", projected)
	}
}

func TestManualCompactCanRetryAnIneffectiveOrFailedWriter(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange(strings.Repeat("original ", 90), "done", nil)
	e := testEngine(contextcfg.Full)
	e.Write = func(context.Context, string, string) (string, error) { return strings.Repeat("long ", 200), nil }
	cut, err := e.Compact(c.ctx, c.raw, c.refs, c.tr)
	if err != nil || cut.Checkpoint != nil || cut.After <= cut.Before {
		t.Fatalf("longer summary: %+v, %v", cut, err)
	}
	refusal := errors.New("writer unavailable")
	e.Write = func(context.Context, string, string) (string, error) { return "", refusal }
	if cut, err = e.Compact(c.ctx, c.raw, c.refs, c.tr); !errors.Is(err, refusal) || cut.Checkpoint != nil {
		t.Fatalf("failed writer: %+v, %v", cut, err)
	}
	e.Write = func(context.Context, string, string) (string, error) { return "done", nil }
	if cut, err = e.Compact(c.ctx, c.raw, c.refs, c.tr); err != nil || cut.Checkpoint == nil {
		t.Fatalf("retry: %+v, %v", cut, err)
	}
}

func TestManualCompactCanReplaceACandidateThatOnlyShortensFullReplay(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange(strings.Repeat("first phase ", 170), "done", nil)
	write := func(start *contextcfg.CheckpointRef, summary string) *contextcfg.CheckpointRef {
		t.Helper()
		e := testEngine(contextcfg.Full)
		e.Binding.Trigger = 0
		e.Binding.ManualCheckpoint = start
		e.Write = func(context.Context, string, string) (string, error) { return summary, nil }
		cut, err := e.Compact(c.ctx, c.raw, c.refs, c.tr)
		if err != nil || cut.Checkpoint == nil || cut.After >= cut.Before {
			t.Fatalf("cut from %+v: %+v, %v", start, cut, err)
		}
		return cut.Checkpoint
	}
	first := write(nil, "First phase complete.")
	c.exchange(strings.Repeat("second phase ", 35), "done", nil)
	large := strings.Repeat("detail ", 160)
	full := write(nil, large)
	const short = "Both phases complete."
	incremental := write(first, short)
	for _, want := range []struct {
		ref  *contextcfg.CheckpointRef
		text string
	}{{full, strings.TrimSpace(large)}, {incremental, short}} {
		cp, ok, err := c.tr.Checkpoint(c.ctx, len(c.refs), want.ref.Tag)
		if err != nil || !ok || cp.Handoff != want.text {
			t.Fatalf("checkpoint %s: found=%t, %v, handoff %q", want.ref.Tag, ok, err, cp.Handoff)
		}
	}
}

func TestManualCompactBoundsThePriorSummaryWithoutAKnownWindow(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange("EARLIER-EVIDENCE "+strings.Repeat("work ", 5500), "done", nil)
	large := testEngine(contextcfg.Full)
	large.Binding.Window, large.Binding.Trigger = 100000, 0
	large.Write = func(context.Context, string, string) (string, error) {
		return "LARGE-CHECKPOINT " + strings.Repeat("h", 16000), nil
	}
	first, err := large.Compact(c.ctx, c.raw, c.refs, c.tr)
	if err != nil || first.Checkpoint == nil {
		t.Fatalf("initial cut: %+v, %v", first, err)
	}
	c.exchange("NEW-EVIDENCE", "done", nil)
	resumed := testEngine(contextcfg.Full)
	resumed.Binding.Window, resumed.Binding.Trigger = 0, 0
	resumed.Binding.ManualCheckpoint = first.Checkpoint
	var sources strings.Builder
	resumed.Write = func(_ context.Context, _, source string) (string, error) {
		if strings.Contains(source, "LARGE-CHECKPOINT") {
			return "", errors.New("oversized prior summary in writer request")
		}
		sources.WriteString(source)
		return "Updated handoff.", nil
	}
	cut, err := resumed.Compact(c.ctx, c.raw, c.refs, c.tr)
	if err != nil || cut.Checkpoint == nil || !strings.Contains(sources.String(), "EARLIER-EVIDENCE") || !strings.Contains(sources.String(), "NEW-EVIDENCE") {
		t.Fatalf("cut with unknown capacity: %+v, %v", cut, err)
	}
}

func TestManualCheckpointUsesTheUpdatedNotebookAtASharedBoundary(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange(strings.Repeat("old evidence ", 100), "done", nil)
	book := notebook.Store{Root: t.TempDir()}
	edit := notebook.Edit{ID: "project", Title: "Project", Summary: "First decision.", Body: "Evidence.", Sources: []string{"transcript"}, Expected: "new"}
	cut := func() contextstate.Checkpoint {
		t.Helper()
		e := testEngine(contextcfg.Full)
		e.Binding.Notebook = book.Root
		e.Write = func(context.Context, string, string) (string, error) { return "Project summary.", nil }
		r, err := e.Compact(c.ctx, c.raw, c.refs, c.tr)
		if err != nil || r.Checkpoint == nil {
			t.Fatalf("compact: %+v, %v", r, err)
		}
		cp, ok, err := c.tr.Checkpoint(c.ctx, len(c.refs), r.Checkpoint.Tag)
		if err != nil || !ok {
			t.Fatalf("read checkpoint: found=%t, %v", ok, err)
		}
		return cp
	}
	first, err := book.Put(edit)
	if err != nil {
		t.Fatal(err)
	}
	previous := cut()
	edit.Expected, edit.Summary = first.Revision, "Revised decision."
	if _, err := book.Put(edit); err != nil {
		t.Fatal(err)
	}
	updated := cut()
	if previous.Tag == updated.Tag || !strings.Contains(previous.Catalog, "First decision.") || !strings.Contains(updated.Catalog, "Revised decision.") {
		t.Fatalf("shared boundary lost a catalog: %q then %q", previous.Catalog, updated.Catalog)
	}
}

func TestAutomaticCutCanAdvancePastAManualCheckpoint(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange(strings.Repeat("first phase ", 40), "finished", nil)
	e := testEngine(contextcfg.Handoff)
	e.Write = func(context.Context, string, string) (string, error) { return "First phase summary.", nil }
	cut, err := e.Compact(c.ctx, c.raw, c.refs, c.tr)
	if err != nil || cut.Checkpoint == nil {
		t.Fatalf("initial manual cut: %+v, %v", cut, err)
	}
	auto := testEngine(contextcfg.Handoff)
	auto.Binding.ManualCheckpoint = cut.Checkpoint
	c.exchange(strings.Repeat("second phase ", 300), "finished too", nil)
	auto.Write = func(_ context.Context, _, source string) (string, error) {
		if !strings.Contains(source, "First phase summary.") || !strings.Contains(source, "second phase") {
			t.Fatal("automatic writer missed its manual checkpoint or the new exchange")
		}
		return "Both phases complete.", nil
	}
	if _, err := c.next(auto); err != nil {
		t.Fatal(err)
	}
	receipt := auto.Receipt()
	if receipt.Prefix != c.refs[len(c.refs)-1].Source {
		t.Fatalf("automatic checkpoint did not advance: %+v", receipt)
	}
	c.exchange("continue", "done", receipt)
	resumed := testEngine(contextcfg.Handoff)
	resumed.Binding.ManualCheckpoint = cut.Checkpoint
	if _, err := c.next(resumed); err != nil || resumed.Receipt().Tag != receipt.Tag {
		t.Fatalf("resume selected %+v, want %+v: %v", resumed.Receipt(), receipt, err)
	}
}

func TestReceiptAfterManualCutSelectsANewSummaryAtTheSameBoundary(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange(strings.Repeat("original detail ", 50), "done", nil)
	e := testEngine(contextcfg.Full)
	e.Write = func(context.Context, string, string) (string, error) { return "Manual summary.", nil }
	cut, err := e.Compact(c.ctx, c.raw, c.refs, c.tr)
	if err != nil || cut.Checkpoint == nil {
		t.Fatalf("manual cut: %+v, %v", cut, err)
	}
	end := len(c.refs)
	adjusted, ok, err := c.tr.Checkpoint(c.ctx, end, cut.Checkpoint.Tag)
	if err != nil || !ok {
		t.Fatalf("read checkpoint: found=%t, %v", ok, err)
	}
	adjusted.Handoff = "Summary fitted to a smaller window."
	adjusted, err = c.tr.Publish(c.ctx, end, derivedCheckpointTag(cut.Checkpoint.Tag, adjusted), adjusted)
	if err != nil {
		t.Fatal(err)
	}
	c.exchange("next", "answer", &contextstate.Receipt{Count: end, Prefix: adjusted.Prefix, Tag: adjusted.Tag, ManualCheckpoint: cut.Checkpoint})
	fresh := testEngine(contextcfg.Handoff)
	fresh.Binding.ManualCheckpoint = cut.Checkpoint
	if _, err := c.next(fresh); err != nil || fresh.Receipt().Tag != adjusted.Tag {
		t.Fatalf("resume did not select the window used by the reply: %+v, %v", fresh.Receipt(), err)
	}
}
