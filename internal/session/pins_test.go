package session

import (
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEditingAQuestionKeepsPinChangesBeforeIt(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "sys")
	appendPlanMessage(t, ctx, s, core.RoleUser, "Use meters.")
	appendPlanMessage(t, ctx, s, core.RoleAssistant, "ok")
	_, refs := snapshotForTest(t, s)
	add := func(change PinChange, text string) string {
		t.Helper()
		entry, err := PinEntry(change, text, pinsForTest(t, s))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AppendMessageWithBlocks(ctx, s, entry.Message, nil, nil, entry.Blocks); err != nil {
			t.Fatal(err)
		}
		return entry.Message.Pin.ID
	}
	add(PinChange{Action: "add", Source: refs[1].Source + ".0"}, "Use meters.")
	id := add(PinChange{Action: "add"}, "Withdraw this.")
	add(PinChange{ID: id, Action: "remove"}, "")
	id = add(PinChange{Action: "add"}, "First wording.")
	add(PinChange{ID: id, Action: "replace"}, "Use this wording.")
	q := appendPlanMessage(t, ctx, s, core.RoleUser, "second question")
	want := pinsForTest(t, s)
	if len(want) != 2 {
		t.Fatal(want)
	}
	for _, mode := range []string{"branch", "resume"} {
		cfg := newTestCoordinatorConfig()
		cfg.Context.Policy = contextcfg.Full
		ref := GetSessionID(s.Path) + "/" + q
		if mode == "branch" {
			cfg.Branch = ref
		} else {
			cfg.Resume = ref
		}
		plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "edited question", false, PendingToolSkip)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ActivePins(plan.Messages, plan.Refs)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s preview pins: %+v %v", mode, got, err)
		}
		branch, err := plan.Commit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := pinsForTest(t, branch); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s stored pins: %+v", mode, got)
		}
	}
}

// A failed turn can leave an unanswered question that a pin then quotes.
// Editing the next question keeps that question, so the pin keeps its
// source. A question whose pin was withdrawn goes with the replaced turn.
func TestEditingAQuestionKeepsTheQuestionAPinQuotes(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "revision", true: "legacy"}[legacy], func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			s := createPlanSession(t, "sys")
			appendPlanMessage(t, ctx, s, core.RoleUser, "first")
			appendPlanMessage(t, ctx, s, core.RoleAssistant, "ok")
			quoted := appendPlanMessage(t, ctx, s, core.RoleUser, "Use meters.")
			dropped := appendPlanMessage(t, ctx, s, core.RoleUser, "Use feet.")
			if legacy {
				path := buildMessageFilePaths(s.Path, quoted).JSONPath
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var meta MessageMetadata
				if err := json.Unmarshal(data, &meta); err != nil {
					t.Fatal(err)
				}
				meta.Revision = ""
				if data, err = json.Marshal(meta); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if s, err = OpenSession(ctx, s.Path); err != nil {
					t.Fatal(err)
				}
			}
			_, refs := snapshotForTest(t, s)
			add := func(change PinChange, text string) string {
				t.Helper()
				entry, err := PinEntry(change, text, pinsForTest(t, s))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := AppendMessageWithBlocks(ctx, s, entry.Message, nil, nil, entry.Blocks); err != nil {
					t.Fatal(err)
				}
				return entry.Message.Pin.ID
			}
			add(PinChange{Action: "add", Source: refs[3].Source + ".0"}, "Use meters.")
			id := add(PinChange{Action: "add", Source: refs[4].Source + ".0"}, "Use feet.")
			add(PinChange{ID: id, Action: "remove"}, "")
			q := appendPlanMessage(t, ctx, s, core.RoleUser, "second question")
			want := pinsForTest(t, s)
			if len(want) != 1 || want[0].Text != "Use meters." {
				t.Fatal(want)
			}
			// A new system prompt makes the resume fork, which must copy the
			// same messages the request carries.
			for _, mode := range []string{"branch", "resume", "fork"} {
				cfg := newTestCoordinatorConfig()
				cfg.Context.Policy = contextcfg.Full
				ref := GetSessionID(s.Path) + "/" + q
				switch mode {
				case "branch":
					cfg.Branch = ref
				case "resume":
					cfg.Resume = ref
				case "fork":
					cfg.Resume = ref
					cfg.System, cfg.EffectiveSystem, cfg.SystemExplicitlySet = "new system", "new system", true
				}
				plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "edited question", false, PendingToolSkip)
				if err != nil {
					t.Fatal(err)
				}
				kept := map[string]bool{}
				for _, r := range plan.Refs {
					kept[r.ID] = true
				}
				if !kept[quoted] || kept[dropped] {
					t.Fatalf("%s kept quoted %t, withdrawn %t", mode, kept[quoted], kept[dropped])
				}
				got, err := ActivePins(plan.Messages, plan.Refs)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s preview pins: %+v %v", mode, got, err)
				}
				branch, err := plan.Commit(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if got := pinsForTest(t, branch); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s stored pins: %+v", mode, got)
				}
				if stored, _ := snapshotForTest(t, branch); len(stored) != len(plan.Messages) {
					t.Fatalf("%s request carried %d messages, the session holds %d", mode, len(plan.Messages), len(stored))
				}
			}
		})
	}
}

// An edit carries the lineage before the question it replaces, less an
// unanswered question nothing quotes. When that question becomes an answer
// before the commit, the lineage carries the dropped question, so the
// commit fails for a new branch, a resumed branch and a fork alike.
// editPlan saves an answered question, an unanswered one, a pin and a last
// question, and prepares an edit of the last question by mode. The edit's
// request leaves the unanswered question out. The returned function replaces
// the edited question with a message of another role.
func editPlan(t *testing.T, mode string) (context.Context, *RequestPlan, func(core.Role)) {
	t.Helper()
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "sys")
	appendPlanMessage(t, ctx, s, core.RoleUser, "first")
	appendPlanMessage(t, ctx, s, core.RoleAssistant, "ok")
	dropped := appendPlanMessage(t, ctx, s, core.RoleUser, "Use feet.")
	entry, err := PinEntry(PinChange{Action: "add"}, "Use meters.", pinsForTest(t, s))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendMessageWithBlocks(ctx, s, entry.Message, nil, nil, entry.Blocks); err != nil {
		t.Fatal(err)
	}
	q := appendPlanMessage(t, ctx, s, core.RoleUser, "second question")
	cfg := newTestCoordinatorConfig()
	cfg.Context.Policy = contextcfg.Full
	ref := GetSessionID(s.Path) + "/" + q
	switch mode {
	case "branch":
		cfg.Branch = ref
	case "resume":
		cfg.Resume = ref
	case "fork":
		cfg.Resume = ref
		cfg.System, cfg.EffectiveSystem, cfg.SystemExplicitlySet = "new system", "new system", true
	}
	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "edited question", false, PendingToolSkip)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range plan.Refs {
		if r.ID == dropped {
			t.Fatal("the request carried the unanswered question")
		}
	}
	return ctx, plan, func(role core.Role) {
		t.Helper()
		msg, err := readMessage(s.Path, q)
		if err != nil {
			t.Fatal(err)
		}
		msg.Role, msg.Revision = role, newRevision()
		if err := writeMessage(s.Path, q, *msg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEditFailsWhenItsQuestionBecomesAnAnswer(t *testing.T) {
	for _, mode := range []string{"branch", "resume", "fork"} {
		t.Run(mode, func(t *testing.T) {
			ctx, plan, replace := editPlan(t, mode)
			replace(core.RoleAssistant)
			committed, err := plan.Commit(ctx)
			if !errors.Is(err, ErrHeadReplaced) {
				if committed != nil {
					stored, _ := snapshotForTest(t, committed)
					t.Fatalf("session holds %d messages for a request of %d: %v", len(stored), len(plan.Messages), err)
				}
				t.Fatal(err)
			}
		})
	}
}

// A branch made for an edit keeps the lineage the edit's request carried.
// An answer can replace the edited question while the branch is made or
// after it is saved; neither changes the history the branch continues.
func TestEditBranchKeepsTheLineageItsRequestCarried(t *testing.T) {
	for _, mode := range []string{"branch", "resume"} {
		for _, when := range []string{"before its first message", "afterwards"} {
			t.Run(mode+" "+when, func(t *testing.T) {
				ctx, plan, replace := editPlan(t, mode)
				if when != "afterwards" {
					beforeUserMessageSaveForTest = func(*Session) error {
						replace(core.RoleAssistant)
						return nil
					}
					t.Cleanup(func() { beforeUserMessageSaveForTest = nil })
				}
				committed, err := plan.Commit(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if when == "afterwards" {
					replace(core.RoleAssistant)
				}
				if stored, _ := snapshotForTest(t, committed); len(stored) != len(plan.Messages) {
					t.Fatalf("the branch holds %d messages for a request of %d", len(stored), len(plan.Messages))
				}
			})
		}
	}
}

// alternativeAnswer saves sys, first, ok, question and an answer, and an
// alternative in a sibling that replaces the message at replaced, 3 for the
// question or 4 for the answer. It returns the session, the replaced
// message's ID, the sibling and the alternative's ID.
func alternativeAnswer(t *testing.T, ctx context.Context, replaced int) (*Session, string, *Session, string) {
	t.Helper()
	s := createPlanSession(t, "sys")
	var ids []string
	for _, m := range []struct {
		role core.Role
		text string
	}{{core.RoleUser, "first"}, {core.RoleAssistant, "ok"}, {core.RoleUser, "question"}, {core.RoleAssistant, "answer A"}} {
		ids = append(ids, appendPlanMessage(t, ctx, s, m.role, m.text))
	}
	anchor := ids[replaced-1]
	siblingPath, err := CreateSibling(ctx, s.Path, anchor)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := LoadSession(siblingPath)
	if err != nil {
		t.Fatal(err)
	}
	if replaced == 3 {
		appendPlanMessage(t, ctx, sibling, core.RoleUser, "edited question")
	}
	return s, anchor, sibling, appendPlanMessage(t, ctx, sibling, core.RoleAssistant, "answer B")
}

// regenerate prepares and commits a regeneration of the message ref names.
func regenerate(t *testing.T, ctx context.Context, ref string) (*RequestPlan, *Session) {
	t.Helper()
	cfg := newTestCoordinatorConfig()
	cfg.Branch = ref
	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "", true, PendingToolSkip)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := plan.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return plan, committed
}

// An alternative answer keeps answering its question after the answer it
// replaced is deleted and a new question takes that answer's ID. Reading
// the alternative's branch and branching again from inside it both use the
// role the branch recorded.
func TestSiblingKeepsItsAnchorRoleAfterTheAnchorIsReplaced(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s, answer, sibling, alternative := alternativeAnswer(t, ctx, 4)
	if err := DeleteNode(filepath.Join(s.Path, answer)); err != nil {
		t.Fatal(err)
	}
	var err error
	if s, err = OpenSession(ctx, s.Path); err != nil {
		t.Fatal(err)
	}
	if id := appendPlanMessage(t, ctx, s, core.RoleUser, "new question"); id != answer {
		t.Fatalf("the new question took ID %s", id)
	}
	if stored, _ := snapshotForTest(t, sibling); len(stored) != 5 {
		t.Fatalf("the alternative's branch holds %d messages", len(stored))
	}
	plan, committed := regenerate(t, ctx, GetSessionID(sibling.Path)+"/"+alternative)
	if len(plan.Messages) != 4 {
		t.Fatalf("regenerating the alternative sends %d messages", len(plan.Messages))
	}
	if stored, _ := snapshotForTest(t, committed); len(stored) != len(plan.Messages) {
		t.Fatalf("the new branch holds %d messages for a request of %d", len(stored), len(plan.Messages))
	}
}

// Deleting a message keeps the branches that replace it. Each continues
// the messages before the deleted one, as the role it recorded says, and
// a branch taken from inside one does the same.
func TestBranchSurvivesDeletingTheMessageItReplaced(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replaced int
		kept     []string
	}{
		{"answer", 4, []string{"system:sys", "user:first", "assistant:ok", "user:question", "assistant:answer B"}},
		{"question", 3, []string{"system:sys", "user:first", "assistant:ok", "user:edited question", "assistant:answer B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			s, anchor, sibling, alternative := alternativeAnswer(t, ctx, tc.replaced)
			if err := DeleteNode(filepath.Join(s.Path, anchor)); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenSession(ctx, sibling.Path); err != nil {
				t.Fatal(err)
			}
			stored, _ := snapshotForTest(t, sibling)
			if texts := messageTexts(stored); !reflect.DeepEqual(texts, tc.kept) {
				t.Fatalf("the branch holds %q", texts)
			}
			plan, committed := regenerate(t, ctx, GetSessionID(sibling.Path)+"/"+alternative)
			if tc.replaced == 4 && len(plan.Messages) != len(tc.kept)-1 {
				t.Fatalf("regenerating the alternative sends %d messages", len(plan.Messages))
			}
			if stored, _ := snapshotForTest(t, committed); len(stored) != len(plan.Messages) {
				t.Fatalf("the new branch holds %d messages for a request of %d", len(stored), len(plan.Messages))
			}
		})
	}
}

// A branch record that exists but cannot be read stops every read of the
// branch. Reading it with the replaced message's current role would give
// the branch another history.
func TestUnreadableBranchRecordStopsTheRead(t *testing.T) {
	for _, record := range []string{"{", `{"anchor_role":""}`} {
		t.Run(record, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			_, _, sibling, alternative := alternativeAnswer(t, ctx, 4)
			if err := os.WriteFile(filepath.Join(sibling.Path, branchRecordFile), []byte(record), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenSession(ctx, sibling.Path); err == nil || !strings.Contains(err.Error(), "branch record") {
				t.Fatalf("reading the branch: %v", err)
			}
			cfg := newTestCoordinatorConfig()
			cfg.Branch = GetSessionID(sibling.Path) + "/" + alternative
			if _, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "", true, PendingToolSkip); err == nil || !strings.Contains(err.Error(), "branch record") {
				t.Fatalf("branching from inside it: %v", err)
			}
		})
	}
}

// A write that finds its message ID taken in a branch moves to a new
// branch of the message that branch replaced. The new branch continues the
// history the first one recorded, although a question has since taken the
// replaced answer's ID.
func TestConflictBranchUsesTheRecordedAnchorRole(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "sys")
	appendPlanMessage(t, ctx, s, core.RoleUser, "first")
	appendPlanMessage(t, ctx, s, core.RoleAssistant, "ok")
	appendPlanMessage(t, ctx, s, core.RoleUser, "question")
	answer := appendPlanMessage(t, ctx, s, core.RoleAssistant, "answer A")
	siblingPath, err := CreateSibling(ctx, s.Path, answer)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteNode(filepath.Join(s.Path, answer)); err != nil {
		t.Fatal(err)
	}
	if s, err = OpenSession(ctx, s.Path); err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, s, core.RoleUser, "new question")
	afterGetNextMessageIDForTest = func(path, id string) {
		if path != siblingPath {
			return
		}
		afterGetNextMessageIDForTest = nil
		if err := writeMessage(path, id, Message{ID: id, Role: core.RoleAssistant, Content: "another answer", Timestamp: time.Now()}); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterGetNextMessageIDForTest = nil })
	sibling, err := LoadSession(siblingPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := AppendMessageWithToolInteraction(ctx, sibling, Message{Role: core.RoleAssistant, Content: "answer B", Timestamp: time.Now()}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path == siblingPath {
		t.Fatal("the write did not meet the conflict")
	}
	moved, err := LoadSession(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := snapshotForTest(t, moved)
	if texts := messageTexts(stored); !reflect.DeepEqual(texts, []string{"system:sys", "user:first", "assistant:ok", "user:question", "assistant:answer B"}) {
		t.Fatalf("the answer was saved after %q", texts)
	}
}

// A branch taken from a message inside another branch keeps that branch's
// notebook binding, although the session it starts from is bound to
// another notebook.
func TestBranchFromInsideABranchKeepsItsNotebook(t *testing.T) {
	for _, mode := range []string{"branch", "resume", "fork"} {
		t.Run(mode, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			s, _, sibling, alternative := alternativeAnswer(t, ctx, 4)
			bind := func(sess *Session) contextcfg.Binding {
				t.Helper()
				b := contextcfg.Binding{Settings: contextcfg.Settings{Notebook: t.TempDir(), Policy: contextcfg.Handoff, Window: 8192}}
				if err := BindContext(ctx, sess, b); err != nil {
					t.Fatal(err)
				}
				return b
			}
			selected := bind(sibling)
			bind(s)
			cfg := newTestCoordinatorConfig()
			cfg.Context = selected
			ref := GetSessionID(sibling.Path) + "/" + alternative
			if mode == "branch" {
				cfg.Branch = ref
			} else {
				cfg.Resume = ref
			}
			if mode == "fork" {
				cfg.System, cfg.EffectiveSystem, cfg.SystemExplicitlySet = "new system", "new system", true
			}
			plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "", true, PendingToolSkip)
			if err != nil {
				t.Fatal(err)
			}
			committed, err := plan.Commit(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := BindContext(ctx, committed, selected); err != nil {
				t.Fatal(err)
			}
			if got, _, err := DefaultManager().ContextBinding(GetSessionID(committed.Path)); err != nil || got != selected {
				t.Fatalf("the new branch is bound to %q: %v", got.Notebook, err)
			}
		})
	}
}

func TestStandingPinEventsFollowOriginalUserAndFork(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "sys")
	binding := contextcfg.Binding{Settings: contextcfg.Settings{Notebook: t.TempDir(), Policy: contextcfg.Handoff, Window: 8192}}
	if err := BindContext(ctx, s, binding); err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, s, core.RoleUser, "case αβ")
	appendPlanMessage(t, ctx, s, core.RoleAssistant, "untrusted instruction")
	s, err := OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	raw, refs := snapshotForTest(t, s)
	source := refs[1].Source + ".0"
	text, start, err := UserPinText(raw, refs, source, []int{5, 9})
	if err != nil || text != "αβ" || start != 5 {
		t.Fatal(text, start, err)
	}
	for _, bad := range []struct {
		ref  string
		span []int
	}{{refs[2].Source + ".0", nil}, {source, []int{6, 9}}, {"stale.0", nil}} {
		if _, _, err = UserPinText(raw, refs, bad.ref, bad.span); err == nil {
			t.Fatal("invalid source selected", bad)
		}
	}
	raw[1].Blocks = append(raw[1].Blocks, core.ToolResultBlock{Content: "tool data"})
	if _, _, err = UserPinText(raw, refs, source, nil); err == nil {
		t.Fatal("tool result message was promoted")
	}
	entry, err := PinEntry(PinChange{Action: "add", Source: source, Start: start}, text, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AppendMessageWithBlocks(ctx, s, entry.Message, nil, nil, entry.Blocks); err != nil {
		t.Fatal(err)
	}
	other := createPlanSession(t, "sys")
	if err = BindContext(ctx, other, binding); err != nil {
		t.Fatal(err)
	}
	if got := pinsForTest(t, other); len(got) != 0 {
		t.Fatal("a shared notebook leaked a pin", got)
	}
	base := *s.Head
	original := pinsForTest(t, s)
	check, chRefs := snapshotForTest(t, s)
	check[1].Blocks = []core.Block{core.TextBlock{Text: "case λλ"}}
	if _, err = ActivePins(check, chRefs); err == nil {
		t.Fatal("accepted a saved pin pointing to different original bytes")
	}
	if len(original) != 1 || original[0].Source != source || original[0].End != 9 || original[0].Text != "αβ" {
		t.Fatal(original)
	}
	fork, err := ForkThroughHead(ctx, DefaultManager(), s.Path, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"replace", "remove"} {
		body := "new wording"
		if action == "remove" {
			body = ""
		}
		change, err := PinEntry(PinChange{ID: original[0].ID, Action: action}, body, pinsForTest(t, fork))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = AppendMessageWithBlocks(ctx, fork, change.Message, nil, nil, change.Blocks); err != nil {
			t.Fatal(err)
		}
		active := pinsForTest(t, fork)
		if action == "replace" && (len(active) != 1 || active[0].Text != body || !strings.HasSuffix(active[0].Source, ".1")) {
			t.Fatal(active)
		}
		if action == "remove" && len(active) != 0 {
			t.Fatal(active)
		}
		if action == "remove" && (PinDigest(active) == "" || !strings.Contains(RenderPins(active), "withdrawn")) {
			t.Fatal("withdrawal was lost")
		}
	}
	if remaining := pinsForTest(t, s); PinDigest(remaining) != PinDigest(original) {
		t.Fatal("fork altered parent", remaining)
	}
}

func pinsForTest(t *testing.T, s *Session) []StandingPin {
	t.Helper()
	p, err := OpenSession(context.Background(), s.Path)
	if err != nil {
		t.Fatal(err)
	}
	raw, refs := snapshotForTest(t, p)
	pins, err := ActivePins(raw, refs)
	if err != nil {
		t.Fatal(err)
	}
	return pins
}
