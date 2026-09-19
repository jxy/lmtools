package session

import (
	"context"
	"lmtools/internal/core"
	"strings"
	"testing"
)

var sessionTestPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func sessionTestImage() core.ImageBlock {
	return core.ImageBlock{URL: core.ImageDataURL("image/png", sessionTestPNG), Name: "shot.png", Detail: "low"}
}

func prepareImagePlan(t *testing.T, ctx context.Context, cfg core.RequestOptions, input string) *RequestPlan {
	t.Helper()
	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, input, false, core.NewTestApprover(true), PendingToolExecute)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	return plan
}

func lastMessage(t *testing.T, messages []core.TypedMessage) core.TypedMessage {
	t.Helper()
	if len(messages) == 0 {
		t.Fatal("no messages")
	}
	return messages[len(messages)-1]
}

func assertImageThenText(t *testing.T, msg core.TypedMessage, image core.ImageBlock, text string) {
	t.Helper()
	if msg.Role != string(core.RoleUser) {
		t.Fatalf("role = %q, want user", msg.Role)
	}
	want := 1
	if text != "" {
		want = 2
	}
	if len(msg.Blocks) != want {
		t.Fatalf("blocks = %#v, want %d", msg.Blocks, want)
	}
	if got, ok := msg.Blocks[0].(core.ImageBlock); !ok || got != image {
		t.Fatalf("Blocks[0] = %#v, want %#v", msg.Blocks[0], image)
	}
	if text != "" {
		if got, ok := msg.Blocks[1].(core.TextBlock); !ok || got.Text != text {
			t.Fatalf("Blocks[1] = %#v, want text %q", msg.Blocks[1], text)
		}
	}
}

func TestImageTurnIsStagedCommittedAndReplayed(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	image := sessionTestImage()
	cfg := newTestCoordinatorConfig()
	cfg.Images = []core.ImageBlock{image}

	plan := prepareImagePlan(t, ctx, cfg, "What is this?")
	assertImageThenText(t, lastMessage(t, plan.Messages), image, "What is this?")

	sess, err := plan.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	lineage, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("GetLineage() error = %v", err)
	}
	userMsg := lineage[len(lineage)-1]
	if userMsg.Role != core.RoleUser || userMsg.Content != "What is this?" {
		t.Fatalf("committed user message = %+v, want the prompt as its text", userMsg)
	}

	// The prompt is in the .txt; the image is in the .blocks.json, inline.
	stored, ok, err := loadMessageBlocks(sess.Path, userMsg.ID)
	if err != nil || !ok {
		t.Fatalf("loadMessageBlocks() = ok %v, err %v", ok, err)
	}
	assertImageThenText(t, core.TypedMessage{Role: string(core.RoleUser), Blocks: stored}, image, "What is this?")

	// Replay reads the blocks file, so a resumed request carries the image.
	replayed, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	assertImageThenText(t, lastMessage(t, replayed), image, "What is this?")
}

func TestResumedSessionSendsEarlierImageOnceAndNewTextTurn(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	image := sessionTestImage()
	cfg := newTestCoordinatorConfig()
	cfg.Images = []core.ImageBlock{image}
	sess, err := prepareImagePlan(t, ctx, cfg, "What is this?").Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	resume := newTestCoordinatorConfig()
	resume.Resume = GetSessionID(sess.Path)
	plan := prepareImagePlan(t, ctx, resume, "And in more detail?")

	images := 0
	for _, msg := range plan.Messages {
		for _, block := range msg.Blocks {
			if got, ok := block.(core.ImageBlock); ok {
				images++
				if got != image {
					t.Fatalf("replayed image = %#v, want %#v", got, image)
				}
			}
		}
	}
	if images != 1 {
		t.Fatalf("plan carries %d images, want the one earlier turn", images)
	}
	last := lastMessage(t, plan.Messages)
	if len(last.Blocks) != 1 {
		t.Fatalf("new turn = %#v, want text alone", last.Blocks)
	}
	if got, ok := last.Blocks[0].(core.TextBlock); !ok || got.Text != "And in more detail?" {
		t.Fatalf("new turn = %#v, want the new prompt", last.Blocks[0])
	}
}

func TestImageOnlyTurnIsATurn(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	image := sessionTestImage()
	cfg := newTestCoordinatorConfig()
	cfg.Images = []core.ImageBlock{image}

	plan := prepareImagePlan(t, ctx, cfg, "")
	assertImageThenText(t, lastMessage(t, plan.Messages), image, "")

	sess, err := plan.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	lineage, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("GetLineage() error = %v", err)
	}
	userMsg := lineage[len(lineage)-1]
	if userMsg.Role != core.RoleUser || userMsg.Content != "" {
		t.Fatalf("committed message = %+v, want an empty user turn", userMsg)
	}
	if fileExists(buildMessageFilePaths(sess.Path, userMsg.ID).TxtPath) {
		t.Fatal("an image-only turn wrote a .txt file")
	}
	replayed, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	assertImageThenText(t, lastMessage(t, replayed), image, "")
}

func TestShouldAppendUserInputCountsImages(t *testing.T) {
	images := []core.ImageBlock{sessionTestImage()}
	tests := []struct {
		name         string
		input        string
		images       []core.ImageBlock
		regeneration bool
		want         bool
	}{
		{name: "text", input: "hi", want: true},
		{name: "image only", images: images, want: true},
		{name: "nothing", want: false},
		{name: "regeneration ignores text", input: "hi", regeneration: true, want: false},
		{name: "regeneration ignores images", images: images, regeneration: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAppendUserInput(tt.input, tt.images, tt.regeneration); got != tt.want {
				t.Fatalf("shouldAppendUserInput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTextOnlyTurnStillWritesTheSameBlocksFile(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	cfg := newTestCoordinatorConfig()
	sess, err := prepareImagePlan(t, ctx, cfg, "plain prompt").Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	lineage, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("GetLineage() error = %v", err)
	}
	userMsg := lineage[len(lineage)-1]
	stored, ok, err := loadMessageBlocks(sess.Path, userMsg.ID)
	if err != nil || !ok {
		t.Fatalf("loadMessageBlocks() = ok %v, err %v", ok, err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored blocks = %#v, want the text alone", stored)
	}
	if got, isText := stored[0].(core.TextBlock); !isText || got.Text != "plain prompt" {
		t.Fatalf("stored[0] = %#v, want the prompt", stored[0])
	}
	if strings.TrimSpace(userMsg.Content) != "plain prompt" {
		t.Fatalf("Content = %q, want the prompt", userMsg.Content)
	}
}
