package workingcontext

import (
	"context"
	"lmtools/internal/contextcfg"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fakeTranscript keys each boundary by its number and holds one checkpoint.
type fakeTranscript struct {
	saved contextstate.Checkpoint
	found bool
}

func (fakeTranscript) Stored(int) bool    { return true }
func (fakeTranscript) Key(end int) string { return "key" + string(rune('0'+end)) }
func (f fakeTranscript) Checkpoint(context.Context, int, string) (contextstate.Checkpoint, bool, error) {
	return f.saved, f.found, nil
}

func (fakeTranscript) Publish(_ context.Context, _ int, _ string, cp contextstate.Checkpoint) (contextstate.Checkpoint, error) {
	return cp, nil
}
func (fakeTranscript) Current(context.Context) error { return nil }
func (fakeTranscript) Used(contextstate.Receipt)     {}

// The checkpoint a window used is the one the newest reply's receipt names,
// and only while the history it covers is the history the receipt saw.
func TestActiveCheckpointFollowsTheNewestReceipt(t *testing.T) {
	good := contextstate.Checkpoint{Count: 2, Prefix: "key2", Handoff: "HANDOFF"}
	receipt := func(r contextstate.Receipt) []contextstate.Ref {
		return []contextstate.Ref{
			{Role: core.RoleUser},
			{Role: core.RoleAssistant},
			{Role: core.RoleUser},
			{Role: core.RoleAssistant, Receipt: &r},
			{Role: core.RoleUser},
		}
	}
	for _, tc := range []struct {
		name    string
		receipt contextstate.Receipt
		saved   contextstate.Checkpoint
		found   bool
		want    int
		err     string
	}{
		{"the receipt's checkpoint", contextstate.Receipt{Count: 2, Prefix: "key2", Tag: "ab12"}, good, true, 2, ""},
		{"another history", contextstate.Receipt{Count: 2, Prefix: "key9", Tag: "ab12"}, good, true, 0, ""},
		{"a boundary past the reply", contextstate.Receipt{Count: 4, Prefix: "key4", Tag: "ab12"}, good, true, 0, ""},
		{"a damaged tag", contextstate.Receipt{Count: 2, Prefix: "key2", Tag: "../x"}, good, true, 0, "invalid saved window identity"},
		{"a missing checkpoint", contextstate.Receipt{Count: 2, Prefix: "key2", Tag: "ab12"}, good, false, 0, "missing or inconsistent"},
		{"other pins", contextstate.Receipt{Count: 2, Prefix: "key2", Tag: "ab12", PinDigest: "other"}, good, true, 0, "missing or inconsistent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp, err := activeCheckpoint(context.Background(), receipt(tc.receipt), fakeTranscript{saved: tc.saved, found: tc.found}, 5)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || cp.Count != tc.want {
				t.Fatalf("checkpoint through %d: %v; want %d", cp.Count, err, tc.want)
			}
		})
	}
}

// A request on a saved session records its window's receipt there, so the
// reply saved next, a tool round's included, names the window it came from.
// A preview records nothing.
func TestProjectRecordsTheReceiptForTheNextReply(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange("q", "a", nil)
	e := testEngine(contextcfg.Full)
	reply := func() *contextstate.Receipt {
		t.Helper()
		m, err := c.s.SaveReply(c.ctx, core.Response{Text: "reply"}, "model")
		if err != nil {
			t.Fatal(err)
		}
		return m.Receipt
	}
	if _, err := e.Project(c.ctx, append(slices.Clone(c.raw), core.NewTextMessage("user", "next")), c.refs, c.tr, true); err != nil {
		t.Fatal(err)
	}
	if got := reply(); got != nil {
		t.Fatalf("a preview recorded %+v", got)
	}
	c.reload()
	if _, err := c.next(e); err != nil {
		t.Fatal(err)
	}
	if got := reply(); got == nil || !reflect.DeepEqual(*got, *e.Receipt()) {
		t.Fatalf("recorded %+v, want %+v", got, e.Receipt())
	}
}

// A receipt records the model's capacity the run found, with the model and
// endpoint it belongs to, so a later run with them can take it.
func TestReceiptRecordsTheCapacity(t *testing.T) {
	e := testEngine(contextcfg.Full)
	e.Binding.Capacity = contextcfg.Capacity{Tokens: 4096, Model: "small", Endpoint: "here"}
	raw := []core.TypedMessage{core.NewTextMessage("user", "q")}
	refs := MemoryRefs(raw)
	if _, err := e.Project(context.Background(), raw, refs, MemoryTranscript(refs), false); err != nil {
		t.Fatal(err)
	}
	if got := e.Receipt().Capacity; got == nil || *got != e.Binding.Capacity {
		t.Fatalf("receipt capacity %+v", got)
	}
	e.Binding.Capacity = contextcfg.Capacity{}
	if got := e.Receipt().Capacity; got != nil {
		t.Fatalf("an unknown capacity was recorded as %+v", got)
	}
}
