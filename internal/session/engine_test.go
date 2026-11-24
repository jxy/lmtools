package session

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/contextcfg"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/workingcontext"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func handoffEngine(write workingcontext.Writer) *workingcontext.Engine {
	return &workingcontext.Engine{
		Binding: contextcfg.Binding{Settings: contextcfg.Settings{Policy: contextcfg.Handoff, Window: 8192, Trigger: 1200}},
		History: &workingcontext.History{},
		Write:   write,
	}
}

// The conversation layer drives the real context engine and a provider
// request builder. An edit resumed at a developer instruction carries the
// instruction and the edited question; the engine's checkpoint is saved
// beside the message its boundary follows; the reply records the window's
// receipt, and the next request finds the checkpoint through it without a
// writer. History refs name the session's identity, and a ref to a message
// on another branch is refused.
func TestConversationThroughTheEngineAndARenderer(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	opts := plainOpts()
	sess := commit(t, prepare(t, s, Ref{}, Change{System: custom("sys")}, Turn{Prompt: "q1 " + strings.Repeat("x", 9000)}))
	reply(t, sess, "a1 "+strings.Repeat("y", 9000))
	asked := commit(t, prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "original q2", Developer: "cite sources"}))
	reply(t, asked, "a2")
	lineage, err := s.Lineage(asked.Ref())
	if err != nil {
		t.Fatal(err)
	}
	instruction, original := lineage[len(lineage)-3], lineage[len(lineage)-2]
	answer := lineage[2]

	plan := prepare(t, s, asked.Ref().At(instruction.ID), Change{}, Turn{Prompt: "edited q2"})
	engine := handoffEngine(func(context.Context, string, string) (string, error) { return "HANDOFF", nil })
	view, err := engine.Project(ctx, plan.Messages, plan.Refs, plan.Transcript, false)
	if err != nil {
		t.Fatal(err)
	}
	receipt := engine.Receipt()
	if receipt.Count != 3 || receipt.Prefix != sess.Identity+"/"+answer.ID {
		t.Fatalf("receipt %+v, want a checkpoint after %s", receipt, answer.ID)
	}
	saved := filepath.Join(s.Dir(), sess.Number, contextDirectory, answer.ID, receipt.Tag+".json")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("checkpoint file: %v", err)
	}
	_, body, err := core.BuildChatRequest(opts, view, core.ChatBuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sent := string(body)
	for _, want := range []string{`"sys"`, `"role":"developer"`, "cite sources", "edited q2", "HANDOFF"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the request lacks %s", want)
		}
	}
	for _, unwanted := range []string{"original q2", "xxxxxxxxxx"} {
		if strings.Contains(sent, unwanted) {
			t.Errorf("the request carries %s", unwanted)
		}
	}

	edited := commit(t, plan)
	reply(t, edited, "a2 edited")
	next := prepare(t, s, edited.Ref(), Change{}, Turn{Prompt: "q3"})
	later := handoffEngine(func(context.Context, string, string) (string, error) {
		t.Error("the next request wrote another handoff")
		return "", fmt.Errorf("refused")
	})
	if _, err := later.Project(ctx, next.Messages, next.Refs, next.Transcript, false); err != nil {
		t.Fatal(err)
	}
	if got := later.Receipt(); got.Tag != receipt.Tag || got.Count != receipt.Count {
		t.Fatalf("the next request used %+v", got)
	}

	if !strings.HasPrefix(next.Refs[1].Source, identityPrefix) {
		t.Fatalf("history ref %q", next.Refs[1].Source)
	}
	args, _ := json.Marshal(map[string]string{"ref": sess.Identity + "/" + original.ID + ".0"})
	if _, err := later.History.CallTool(ctx, "read", args); err == nil || !strings.Contains(err.Error(), "outside the available branch") {
		t.Fatalf("reading a message on another branch: %v", err)
	}
}

// A checkpoint and a receipt count the stored messages they cover, so a
// request that sends no system prompt reuses the checkpoint a request with
// one wrote at the same message, found through a receipt or at a cut, and
// the other way around.
func TestCheckpointsServeRequestsWithAndWithoutASystemPrompt(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := commit(t, prepare(t, s, Ref{}, Change{System: custom("sys")}, Turn{Prompt: "q1 " + strings.Repeat("x", 9000)}))
	answer := reply(t, sess, "a1 "+strings.Repeat("y", 9000))
	first := prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "q2"})
	writer := handoffEngine(func(context.Context, string, string) (string, error) { return "HANDOFF", nil })
	if _, err := writer.Project(ctx, first.Messages, first.Refs, first.Transcript, false); err != nil {
		t.Fatal(err)
	}
	written := writer.Receipt()
	if written.Count != 3 || written.Prefix != sess.Identity+"/"+answer.ID {
		t.Fatalf("receipt %+v", written)
	}
	asked := commit(t, first)
	reply(t, asked, "a2")

	for _, tc := range []struct {
		name   string
		start  Ref
		system *string
		count  int
	}{
		{"a cut without a system prompt", sess.Ref(), custom(""), 2},
		{"a receipt without a system prompt", asked.Ref(), custom(""), 2},
		{"a receipt with a system prompt", asked.Ref(), nil, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := prepare(t, s, tc.start, Change{System: tc.system}, Turn{Prompt: "q3"})
			reader := handoffEngine(func(context.Context, string, string) (string, error) {
				t.Error("the request wrote another handoff")
				return "", fmt.Errorf("refused")
			})
			view, err := reader.Project(ctx, p.Messages, p.Refs, p.Transcript, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := reader.Receipt(); got.Tag != written.Tag || got.Count != tc.count {
				t.Fatalf("receipt %+v, want the checkpoint at %d", got, tc.count)
			}
			if lines := strings.Join(lines(view), "\n"); !strings.Contains(lines, "HANDOFF") || strings.Contains(lines, "xxxxxxxxxx") {
				t.Fatalf("request:\n%s", lines)
			}
		})
	}
}

// Checkpoints are saved beside the message their boundary follows. The first
// publication wins, a damaged tag is refused, and a boundary whose message
// was deleted takes no checkpoint; the history tools stop serving a history
// whose message was deleted.
func TestCheckpointStorage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := conversation(t, s)
	plan := prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "q2"})
	tr := plan.Transcript
	if tr.Stored(1) || !tr.Stored(3) {
		t.Fatalf("stored: system %t, answer %t", tr.Stored(1), tr.Stored(3))
	}
	first, err := tr.Publish(ctx, 3, "ab12", stubCheckpoint(tr, "FIRST"))
	if err != nil || first.Handoff != "FIRST" {
		t.Fatalf("first publication %+v: %v", first, err)
	}
	if second, err := tr.Publish(ctx, 3, "ab12", stubCheckpoint(tr, "SECOND")); err != nil || second.Handoff != "FIRST" {
		t.Fatalf("second publication %+v: %v", second, err)
	}
	if cp, ok, err := tr.Checkpoint(ctx, 3, "ab12"); err != nil || !ok || cp.Handoff != "FIRST" || cp.Tag != "ab12" {
		t.Fatalf("lookup %+v %t: %v", cp, ok, err)
	}
	if _, err := tr.Publish(ctx, 3, "../escape", stubCheckpoint(tr, "X")); err == nil {
		t.Fatal("a damaged tag was accepted")
	}
	if err := tr.Current(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, sess.Ref()); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Publish(ctx, 3, "cd34", stubCheckpoint(tr, "LATE")); err == nil {
		t.Fatal("a checkpoint was saved after its boundary was deleted")
	}
	if err := tr.Current(ctx); err == nil || !strings.Contains(err.Error(), "was deleted") {
		t.Fatalf("the history tools on a deleted message: %v", err)
	}
}

func stubCheckpoint(tr *Transcript, handoff string) contextstate.Checkpoint {
	return contextstate.Checkpoint{Count: 3, Prefix: tr.Key(3), Handoff: handoff}
}
