package session

import (
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/core"
	"reflect"
	"strings"
	"testing"
)

func developerCoordinatorConfig() core.RequestOptions {
	cfg := newTestCoordinatorConfig()
	cfg.Provider, cfg.Model = "openai", "gpt5"
	cfg.System, cfg.Developer = "session system", "First instruction"
	return cfg
}

func prepareDeveloperPlan(t *testing.T, ctx context.Context, cfg core.RequestOptions, input string, regenerate bool) *RequestPlan {
	t.Helper()
	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, input, regenerate, PendingToolSkip)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func assertDeveloperPlanStored(t *testing.T, ctx context.Context, plan *RequestPlan, sess *Session) {
	t.Helper()
	messages, err := BuildMessagesForSession(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, plan.Messages) {
		t.Fatalf("stored=%#v, planned=%#v", typedPlanTexts(messages), typedPlanTexts(plan.Messages))
	}
}

func TestDeveloperInstructionNewSessionAndResume(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	cfg := developerCoordinatorConfig()
	plan := prepareDeveloperPlan(t, ctx, cfg, "first question", false)
	assertSessionsDirEntryCount(t, 0)
	want := []string{"system:session system", "developer:First instruction", "user:first question"}
	if got := typedPlanTexts(plan.Messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan=%#v, want=%#v", got, want)
	}
	sess, err := plan.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertDeveloperPlanStored(t, ctx, plan, sess)
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first answer")
	path := sess.Path
	cfg.Resume, cfg.Developer = path, ""
	plan = prepareDeveloperPlan(t, ctx, cfg, "second question", false)
	sess, err = plan.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Path != path {
		t.Fatal("resume created a fork for replayed developer instruction")
	}
	assertDeveloperPlanStored(t, ctx, plan, sess)
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "second answer")
	cfg.Developer = "New instruction"
	plan = prepareDeveloperPlan(t, ctx, cfg, "third question", false)
	sess, err = plan.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertDeveloperPlanStored(t, ctx, plan, sess)
	want = []string{
		"system:session system", "developer:First instruction", "user:first question", "assistant:first answer",
		"user:second question", "assistant:second answer", "developer:New instruction", "user:third question",
	}
	if got := typedPlanTexts(plan.Messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("resume=%#v, want=%#v", got, want)
	}
}

func TestDeveloperInstructionBranchesPreserveInstructionHistory(t *testing.T) {
	for _, anchor := range []string{"first_user", "second_user", "resume_message", "assistant_regeneration"} {
		t.Run(anchor, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			sess := createPlanSession(t, "session system")
			appendPlanMessage(t, ctx, sess, core.RoleDeveloper, "First instruction")
			firstUser := appendPlanMessage(t, ctx, sess, core.RoleUser, "first question")
			appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first answer")
			appendPlanMessage(t, ctx, sess, core.RoleDeveloper, "Updated instruction")
			secondUser := appendPlanMessage(t, ctx, sess, core.RoleUser, "second question")
			assistant := appendPlanMessage(t, ctx, sess, core.RoleAssistant, "second answer")
			before, err := GetLineage(sess.Path)
			if err != nil {
				t.Fatal(err)
			}
			cfg := developerCoordinatorConfig()
			cfg.Developer = "Branch instruction"
			cfg.Branch = GetSessionID(sess.Path) + "/" + secondUser
			want := []string{"system:session system", "developer:First instruction", "user:first question", "assistant:first answer", "developer:Updated instruction"}
			input, regenerate := "replacement question", false
			switch anchor {
			case "first_user":
				cfg.Branch = GetSessionID(sess.Path) + "/" + firstUser
				want = want[:2]
			case "resume_message":
				cfg.Resume, cfg.Branch = cfg.Branch, ""
			case "assistant_regeneration":
				cfg.Branch = GetSessionID(sess.Path) + "/" + assistant
				cfg.Developer, input, regenerate = "", "", true
				want = append(want, "user:second question")
			}
			if !regenerate {
				want = append(want, "developer:Branch instruction", "user:replacement question")
			}
			plan := prepareDeveloperPlan(t, ctx, cfg, input, regenerate)
			if got := typedPlanTexts(plan.Messages); !reflect.DeepEqual(got, want) {
				t.Fatalf("branch plan=%#v, want=%#v", got, want)
			}
			branch, err := plan.Commit(ctx)
			if err != nil {
				t.Fatal(err)
			}
			assertDeveloperPlanStored(t, ctx, plan, branch)
			after, err := GetLineage(sess.Path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("source history changed: error=%v", err)
			}
		})
	}
}

func TestDeveloperInstructionBranchReferenceRejectsDeveloperMessage(t *testing.T) {
	for _, mode := range []string{"branch", "resume_message"} {
		t.Run(mode, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			root := createPlanSession(t, "session system")
			userID := appendPlanMessage(t, ctx, root, core.RoleUser, "original question")
			appendPlanMessage(t, ctx, root, core.RoleAssistant, "original answer")

			cfg := developerCoordinatorConfig()
			cfg.Branch = GetSessionID(root.Path) + "/" + userID
			cfg.Developer = "Branch instruction"
			branch, err := prepareDeveloperPlan(t, ctx, cfg, "replacement question", false).Commit(ctx)
			if err != nil {
				t.Fatal(err)
			}
			refs, err := lineageMessageRefsWithManager(DefaultManager(), branch.Path)
			if err != nil {
				t.Fatal(err)
			}
			var developerRef string
			for _, ref := range refs {
				if ref.path == branch.Path && ref.message.Role == core.RoleDeveloper {
					developerRef = GetSessionID(branch.Path) + "/" + ref.message.ID
					break
				}
			}
			if developerRef == "" {
				t.Fatal("branch has no local developer instruction")
			}

			cfg.Developer, cfg.Branch = "", ""
			if mode == "branch" {
				cfg.Branch = developerRef
			} else {
				cfg.Resume = developerRef
			}
			plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "next question", false, PendingToolSkip)
			if err == nil || !strings.Contains(err.Error(), "cannot branch at a developer instruction") || plan != nil {
				t.Fatalf("preparing %s at %s returned plan=%+v error=%v; want developer refusal", mode, developerRef, plan, err)
			}
			after, err := lineageMessageRefsWithManager(DefaultManager(), branch.Path)
			if err != nil || !reflect.DeepEqual(refs, after) {
				t.Fatalf("refused reference changed branch history: error=%v", err)
			}
		})
	}
}

func TestDeveloperInstructionSystemForkKeepsHistory(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "session system")
	appendPlanMessage(t, ctx, sess, core.RoleDeveloper, "First instruction")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first question")
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first answer")
	cfg := developerCoordinatorConfig()
	cfg.System, cfg.SystemExplicitlySet, cfg.Resume = "Fork system", true, sess.Path
	cfg.Developer = "Fork instruction"
	plan := prepareDeveloperPlan(t, ctx, cfg, "next question", false)
	fork, err := plan.Commit(ctx)
	if err != nil || fork.Path == sess.Path {
		t.Fatalf("fork=%+v error=%v", fork, err)
	}
	assertDeveloperPlanStored(t, ctx, plan, fork)
	want := []string{"system:Fork system", "developer:First instruction", "user:first question", "assistant:first answer", "developer:Fork instruction", "user:next question"}
	if got := typedPlanTexts(plan.Messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("fork plan=%#v, want=%#v", got, want)
	}
}

func TestDeveloperInstructionCommitRetryReusesPendingHead(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	cfg := developerCoordinatorConfig()
	plan := prepareDeveloperPlan(t, ctx, cfg, "question", false)
	beforeBatchEntryCommitForTest = func(index int) error {
		if index == 1 {
			return errors.New("fixture user commit failed")
		}
		return nil
	}
	t.Cleanup(func() { beforeBatchEntryCommitForTest = nil })
	sess, err := plan.Commit(ctx)
	if err == nil || sess == nil {
		t.Fatalf("partial commit session=%+v error=%v", sess, err)
	}
	beforeBatchEntryCommitForTest = nil
	plan, err = PrepareRequestAt(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, sess, "question", false, PendingToolSkip)
	if err != nil {
		t.Fatal(err)
	}
	sess, err = plan.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertDeveloperPlanStored(t, ctx, plan, sess)
	want := []string{"system:session system", "developer:First instruction", "user:question"}
	if got := typedPlanTexts(plan.Messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("retried plan=%#v, want=%#v", got, want)
	}
	if cfg.Developer != "First instruction" {
		t.Fatal("preparation consumed the caller's pending instruction before commit")
	}
}

func TestDeveloperInstructionRequiresNewTurn(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	cfg := developerCoordinatorConfig()
	for _, regenerate := range []bool{false, true} {
		if _, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "", regenerate, PendingToolSkip); err == nil {
			t.Fatalf("empty developer turn, regenerate=%v, succeeded", regenerate)
		}
	}
	assertSessionsDirEntryCount(t, 0)
	cfg.Images = []core.ImageBlock{{URL: "data:image/png;base64,aGVsbG8="}}
	plan := prepareDeveloperPlan(t, ctx, cfg, "", false)
	sess, err := plan.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertDeveloperPlanStored(t, ctx, plan, sess)
}

func TestDeveloperInstructionSessionToolFollowups(t *testing.T) {
	for _, responses := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "responses"}[responses], func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			cfg := developerCoordinatorConfig()
			cfg.ProviderURL, cfg.OpenAIResponses = "http://example.test/v1", responses
			plan := prepareDeveloperPlan(t, ctx, cfg, "question", false)
			sess, err := plan.Commit(ctx)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore(sess, core.NewTestLogger(false))
			for _, id := range []string{"call_1", "call_2"} {
				if _, _, err := store.SaveAssistant(ctx, "", []core.ToolCall{{ID: id, Name: "inspect", Args: json.RawMessage(`{}`)}}, cfg.Model); err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.SaveToolResults(ctx, []core.ToolResult{{ID: id, Output: "ok"}}, ""); err != nil {
					t.Fatal(err)
				}
				messages, err := BuildMessagesForSession(ctx, sess)
				if err != nil {
					t.Fatal(err)
				}
				_, body, err := core.BuildToolResultRequest(cfg, cfg.Model, cfg.System, nil, messages)
				if err != nil {
					t.Fatal(err)
				}
				var request struct {
					Messages []struct{ Role string } `json:"messages"`
					Input    []struct{ Role string } `json:"input"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, message := range append(request.Messages, request.Input...) {
					if message.Role == "developer" {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("developer messages=%d, request=%s", count, body)
				}
			}
		})
	}
}

func TestDeveloperHistoryRejectsUnsupportedWireBeforePendingTools(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "session system")
	appendPlanMessage(t, ctx, sess, core.RoleDeveloper, "First instruction")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
	appendPlanAssistantToolCall(t, ctx, sess, "call_1", "universal_command")
	before, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := developerCoordinatorConfig()
	cfg.Developer, cfg.Provider, cfg.ToolEnabled = "", "argo", true
	resolution, err := ResolvePendingToolCalls(ctx, sess, cfg, core.NewTestLogger(false), core.NewTestNotifier(), core.TestToolUI{}, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot preserve the developer role") || resolution.Committed {
		t.Fatalf("resolution=%+v error=%v", resolution, err)
	}
	after, err := GetLineage(sess.Path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("pending tool resolution wrote history: error=%v", err)
	}
}
