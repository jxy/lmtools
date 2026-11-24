package oldsession

import (
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAttachFirstNotebookToContextBinding(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "system")
	b := contextcfg.Binding{Settings: contextcfg.Settings{Policy: contextcfg.Full, Window: 8192}}
	if err := BindContext(ctx, s, b); err != nil {
		t.Fatal(err)
	}
	b.Notebook, b.Initial = t.TempDir(), "original catalog"
	if err := BindContext(ctx, s, b); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := DefaultManager().ContextBinding(s.Path); err != nil || !ok || got != b {
		t.Fatal(got, ok, err)
	}
	for _, book := range []string{"", t.TempDir()} {
		other := b
		other.Notebook = book
		if err := BindContext(ctx, s, other); err == nil {
			t.Fatal("replaced the bound notebook", book)
		}
	}
}

// Write one pre-revision message with a pin and a receipt. Its source ID must
// stay the same through a symlink, a relative path and a fork, so both
// references still resolve.
func TestLegacySourceReferencesAcrossSessionDirectoryAliases(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	physical, err := filepath.EvalSymlinks(GetSessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	SetSessionsDir(physical)
	s := createPlanSession(t, "sys")
	id := appendPlanMessage(t, ctx, s, core.RoleUser, "Use periods.")
	path := buildMessageFilePaths(s.Path, id).JSONPath
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta MessageMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Revision = ""
	data, err = json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, refs := snapshotForTest(t, s)
	stable := refs[1].Source
	cp := Checkpoint{Tag: "abc", Count: 2, Prefix: PrefixKeys(refs)[1], Handoff: "saved handoff"}
	if _, err := PublishCheckpoint(ctx, refs[1], cp.Tag, cp); err != nil {
		t.Fatal(err)
	}
	s.WindowUsed = &WindowReceipt{Tag: cp.Tag, Count: cp.Count, Prefix: cp.Prefix}
	if _, err := SaveAssistantResponse(ctx, s, core.Response{Text: "ok"}, "model"); err != nil {
		t.Fatal(err)
	}
	entry, err := PinEntry(PinChange{Action: "add", Source: stable + ".0"}, "Use periods.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendMessageWithBlocks(ctx, s, entry.Message, nil, nil, entry.Blocks); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	SetSessionsDir(alias)
	s, err = OpenSession(ctx, filepath.Join(alias, filepath.Base(s.Path)))
	if err != nil {
		t.Fatal(err)
	}
	copy, err := ForkThroughHead(ctx, DefaultManager(), s.Path, *s.Head)
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range []*Session{s, copy} {
		raw, refs := snapshotForTest(t, saved)
		if refs[1].Source != stable {
			t.Fatalf("source changed: %s", refs[1].Source)
		}
		text, _, err := UserPinText(raw, refs, stable+".0", nil)
		if err != nil || text != "Use periods." {
			t.Fatal(text, err)
		}
		pins, err := ActivePins(raw, refs)
		if err != nil || len(pins) != 1 || pins[0].Source != stable+".0" {
			t.Fatal(pins, err)
		}
		active, err := ActiveCheckpoint(ctx, refs, PrefixKeys(refs))
		if err != nil || active.Handoff != cp.Handoff || active.Prefix != cp.Prefix {
			t.Fatal("lost the receipt", active, err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, alias)
	if err != nil {
		t.Fatal(err)
	}
	SetSessionsDir(relative)
	reopened, err := OpenSession(ctx, filepath.Base(s.Path))
	if err != nil {
		t.Fatal("relative sessions directory", err)
	}
	_, refs = snapshotForTest(t, reopened)
	if refs[1].Source != stable {
		t.Fatal("relative path changed the source", refs[1])
	}
}

func TestContextBuilderReadsPinnedLineageAfterCacheSetupFails(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "system")
	appendPlanMessage(t, ctx, s, core.RoleUser, "authorized prefix")
	pinned, err := OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	want, wantRefs := snapshotForTest(t, pinned)
	temp := filepath.Join(t.TempDir(), "temporarily-unavailable")
	if err := os.Rename(s.Path, temp); err != nil {
		t.Fatal(err)
	}
	read, err := CreateContextBuilder(ctx, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := read(); err == nil {
		t.Fatal("read a missing session")
	}
	if err := os.Rename(temp, s.Path); err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, s, core.RoleAssistant, "other writer")
	got, refs, err := read()
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(refs, wantRefs) {
		t.Fatalf("uncached contents or provenance differ: %v", err)
	}
	if err := DeleteNode(filepath.Join(s.Path, pinned.Head.ID)); err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, s, core.RoleUser, "replaced")
	var replaced *HeadReplacedError
	if _, _, err := read(); !errors.As(err, &replaced) {
		t.Fatalf("served a replaced head: %v", err)
	}
}

func snapshotForTest(t *testing.T, s *Session) ([]core.TypedMessage, []ContextRef) {
	t.Helper()
	s, err := OpenSession(context.Background(), s.Path)
	if err != nil {
		t.Fatal(err)
	}
	build, err := CreateContextBuilder(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	m, r, err := build()
	if err != nil {
		t.Fatal(err)
	}
	return m, r
}

func TestCheckpointFollowsOnlyTheCoveredLineage(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "system")
	appendPlanMessage(t, ctx, s, core.RoleUser, "initial")
	first := appendPlanMessage(t, ctx, s, core.RoleAssistant, "original answer")
	second := appendPlanMessage(t, ctx, s, core.RoleUser, "follow-up")
	appendPlanMessage(t, ctx, s, core.RoleAssistant, "later answer")
	_, refs := snapshotForTest(t, s)
	keys := PrefixKeys(refs)
	cp := Checkpoint{Tag: "abc123", Count: 3, Prefix: keys[2], Handoff: "first answer confirmed"}
	if _, err := PublishCheckpoint(ctx, refs[2], cp.Tag, cp); err != nil {
		t.Fatal(err)
	}
	different := cp
	different.Handoff = "second competing draft"
	got, err := PublishCheckpoint(ctx, refs[2], cp.Tag, different)
	if err != nil || got.Handoff != cp.Handoff {
		t.Fatal(got, err)
	}
	if inactive, err := ActiveCheckpoint(ctx, refs, keys); err != nil || inactive.Count != 0 {
		t.Fatalf("a candidate computed on a shared ancestor rewrote this branch: %+v %v", inactive, err)
	}
	s.WindowUsed = &WindowReceipt{Tag: cp.Tag, Count: cp.Count, Prefix: cp.Prefix}
	if _, err = SaveAssistantResponse(ctx, s, core.Response{Text: "reply actually produced from the handoff"}, "model"); err != nil {
		t.Fatal(err)
	}
	binding := contextcfg.Binding{Settings: contextcfg.Settings{Notebook: t.TempDir(), Policy: "handoff", Window: 8192}, Initial: "original catalog"}
	pinned, err := OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = BindContext(ctx, pinned, binding); err != nil {
		t.Fatal(err)
	}
	short, err := CreateSibling(ctx, s.Path, first)
	if err != nil {
		t.Fatal(err)
	}
	before, err := LoadSession(short)
	if err != nil {
		t.Fatal(err)
	}
	_, sr := snapshotForTest(t, before)
	found, err := ActiveCheckpoint(ctx, sr, PrefixKeys(sr))
	if err != nil || found.Count != 0 {
		t.Fatalf("earlier branch inherited future: %v %v", found, err)
	}
	if b, ok, err := DefaultManager().ContextBinding(short); err != nil || !ok || b != binding {
		t.Fatal(b, ok, err)
	}
	if b, ok, err := DefaultManager().ContextBinding(filepath.Join(s.Path, second)); err != nil || !ok || b != binding {
		t.Fatal(b, ok, err)
	}
	// A conflict's full copy has new storage revisions and the same original
	// evidence, checkpoint and catalog.
	clone, err := ForkThroughHead(ctx, DefaultManager(), pinned.Path, *pinned.Head)
	if err != nil {
		t.Fatal(err)
	}
	_, cr := snapshotForTest(t, clone)
	if cr[2].Revision == refs[2].Revision || cr[2].Source != refs[2].Source {
		t.Fatal("copy did not distinguish revision and origin")
	}
	got, err = ActiveCheckpoint(ctx, cr, PrefixKeys(cr))
	if err != nil || !reflect.DeepEqual(got, cp) {
		t.Fatal(got, err)
	}
	if b, ok, err := DefaultManager().ContextBinding(clone.Path); err != nil || !ok || b != binding {
		t.Fatal(b, ok, err)
	}
	if err = DeleteNode(filepath.Join(s.Path, first)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(checkpointDir(refs[2])); !os.IsNotExist(err) {
		t.Fatalf("checkpoint survived deletion %v", err)
	}
	appendPlanMessage(t, ctx, s, core.RoleAssistant, "replacement")
	if _, err = PublishCheckpoint(ctx, refs[2], cp.Tag, cp); err == nil {
		t.Fatal("published at reused message ID")
	}
}

func TestContextBuilderPinnedDuringAppend(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "")
	appendPlanMessage(t, ctx, s, core.RoleUser, "first")
	pinned, err := OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	build, err := CreateContextBuilder(ctx, pinned)
	if err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, s, core.RoleAssistant, "other writer")
	msgs, refs, err := build()
	if err != nil || len(msgs) != 1 || len(refs) != 1 {
		t.Fatal(len(msgs), len(refs), err)
	}
	if err = DeleteNode(s.Path); err != nil {
		t.Fatal(err)
	}
	if _, _, err = build(); err == nil {
		t.Fatal("retrieved a deleted head")
	}
}

func TestLegacyToolResultCanAnchorACheckpoint(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	s := createPlanSession(t, "system")
	appendPlanMessage(t, ctx, s, core.RoleUser, "read")
	appendPlanAssistantToolCall(t, ctx, s, "call-1", "toolA")
	result, err := SaveToolResults(ctx, s, []core.ToolResult{{ID: "call-1", Output: "original receipt"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(buildMessageFilePaths(s.Path, result.MessageID).BlocksPath); err != nil {
		t.Fatal(err)
	}
	stripRevisions(t, s.Path)
	msgs, refs := snapshotForTest(t, s)
	i := len(refs) - 1
	block := msgs[i].Blocks[0].(core.ToolResultBlock)
	if block.Name != "toolA" {
		t.Fatal(block)
	}
	cp := Checkpoint{Count: i + 1, Prefix: PrefixKeys(refs)[i], Continue: true, Handoff: "receipt saved"}
	if _, err = PublishCheckpoint(ctx, refs[i], "aa", cp); err != nil {
		t.Fatal(err)
	}
	pinned, err := OpenSession(ctx, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := ForkThroughHead(ctx, DefaultManager(), s.Path, *pinned.Head)
	if err != nil {
		t.Fatal(err)
	}
	_, copied := snapshotForTest(t, clone)
	if copied[i].Source != refs[i].Source {
		t.Fatal("legacy receipt identity changed on copy")
	}
}
