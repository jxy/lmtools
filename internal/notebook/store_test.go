package notebook

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRepairHandEditedNotesThroughTools(t *testing.T) {
	ctx := context.Background()
	for name, original := range map[string]string{
		"layout":       "# Hand edited\nmissing headings\n",
		"oversized":    strings.Repeat("明", maxNoteBytes) + "final source",
		"invalid-utf8": "source\xff",
		"raw-pages":    strings.Repeat("\x80", 2*maxReadBytes+1) + "終",
	} {
		t.Run(name, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			path := filepath.Join(store.Root, "repair.md")
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			list, err := store.List(ctx, "", 0, 10)
			if err != nil || list.InvalidCount != 1 || len(list.Notes) != 0 {
				t.Fatal(list, err)
			}
			var recovered strings.Builder
			var rev string
			for offset := 0; ; {
				args, _ := json.Marshal(map[string]any{"id": "repair", "offset": offset, "revision": rev})
				result, err := store.CallTool(ctx, "read", args)
				if err != nil {
					t.Fatal(err)
				}
				p := result.(notePage)
				if p.Invalid == "" || p.Revision != fmt.Sprintf("%x", sha256.Sum256([]byte(original))) {
					t.Fatal("invalid file lacks exact revision or diagnosis")
				}
				rev = p.Revision
				if p.Base64 != "" {
					raw, err := base64.StdEncoding.DecodeString(p.Base64)
					if err != nil {
						t.Fatal(err)
					}
					recovered.Write(raw)
				} else {
					recovered.WriteString(p.Markdown)
				}
				if p.Next == 0 {
					break
				}
				if p.Next <= offset || p.Next-offset > maxReadBytes {
					t.Fatal("invalid repair pagination", p.Next, offset)
				}
				offset = p.Next
			}
			if recovered.String() != original {
				t.Fatal("original pages lost data")
			}
			e := edit("repair")
			if _, err := store.Put(e); err == nil {
				t.Fatal("new revision overwrote an existing malformed file")
			}
			e.Expected = rev
			if err := os.WriteFile(path, []byte(original+"!"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Put(e); err == nil {
				t.Fatal("stale repair overwrote an external edit")
			}
			after, err := store.CallTool(ctx, "read", json.RawMessage(`{"id":"repair"}`))
			if err != nil {
				t.Fatal(err)
			}
			e.Expected = after.(notePage).Revision
			args, _ := json.Marshal(e)
			if _, err := store.CallTool(ctx, "put", args); err != nil {
				t.Fatal(err)
			}
			h, _, err := store.Read("repair")
			if err != nil || h.Title != e.Title {
				t.Fatal(h, err)
			}
		})
	}
}

func TestListSearchAndSnapshotSkipUnreadableNote(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, id := range []string{"hidden", "readable"} {
		if _, err := store.Put(edit(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(store.Root, "hidden.md"), 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read("hidden"); err == nil && os.Geteuid() == 0 {
		t.Skip("root can read mode 000")
	}
	ctx := context.Background()
	for _, query := range []string{"", "build"} {
		list, err := store.List(ctx, query, 0, 12)
		if err != nil || len(list.Notes) != 1 || list.Notes[0].ID != "readable" || list.InvalidCount != 0 || list.UnreadableCount != 1 || len(list.Unreadable) != 1 || list.Unreadable[0] != "hidden" {
			t.Fatal(query, list, err)
		}
	}
	for _, view := range []string{"catalog", "full"} {
		text, err := Snapshot(ctx, store.Root, view)
		if err != nil || !strings.Contains(text, "readable @") || !strings.Contains(text, "1 unreadable") {
			t.Fatal(view, text, err)
		}
	}
}

func TestNotebookPutPreservesModeAndReportsCommittedRevision(t *testing.T) {
	s := Store{Root: t.TempDir()}
	e := edit("shared")
	h, err := s.Put(e)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "shared.md")
	mode := func() os.FileMode {
		t.Helper()
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return st.Mode().Perm()
	}
	reference, err := os.OpenFile(filepath.Join(t.TempDir(), "reference"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	st, err := reference.Stat()
	if closeErr := reference.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := mode(); got != st.Mode().Perm() {
		t.Fatal("new note did not follow the umask", got, st.Mode().Perm())
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	e.Expected, e.Body = h.Revision, "replacement"
	syncs := 0
	failSync := func(*os.File) error { syncs++; return syscall.EINVAL }
	h, err = s.put(context.Background(), e, failSync)
	if err != nil || h.Revision == e.Expected || h.Revision == "" || syncs != 1 || mode() != 0o640 {
		t.Fatal(h, err, syncs, mode())
	}
	read, body, err := s.Read("shared")
	if err != nil || read != h || !strings.Contains(body, e.Body) {
		t.Fatal(read, body, err)
	}
	if retry, err := s.put(context.Background(), e, failSync); err != nil || retry != h || syncs != 1 {
		t.Fatal(retry, err, syncs)
	}
	e.Body = "different"
	if _, err := s.Put(e); err == nil {
		t.Fatal("accepted a stale revision")
	}
}

// A replacement's text must never be open to an account that cannot read
// the note it replaces, even before put restores the note's mode.
func TestReplacementStartsWithinTheReplacedMode(t *testing.T) {
	dir := t.TempDir()
	for _, perm := range []os.FileMode{0o600, 0o640} {
		f, err := createTemp(dir, perm)
		if err != nil {
			t.Fatal(err)
		}
		st, err := f.Stat()
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil || st.Mode().Perm()&^perm != 0 {
			t.Fatal(perm, st.Mode().Perm(), err)
		}
	}
}

// A replacement keeps the note's group, so its group bits still name the same
// accounts after the rename publishes a new file.
func TestReplacementKeepsTheNotesGroup(t *testing.T) {
	s := Store{Root: t.TempDir()}
	e := edit("shared")
	h, err := s.Put(e)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "shared.md")
	group := func() uint32 {
		t.Helper()
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return st.Sys().(*syscall.Stat_t).Gid
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	other := -1
	for _, g := range groups {
		if uint32(g) != group() {
			other = g
			break
		}
	}
	if other < 0 {
		t.Skip("the test account belongs to a single group")
	}
	if err := os.Chown(path, -1, other); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	e.Expected, e.Body = h.Revision, "replacement"
	if _, err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || group() != uint32(other) || st.Mode().Perm() != 0o640 {
		t.Fatal(group(), other, st.Mode().Perm(), err)
	}
}

type foreignGroup struct {
	os.FileInfo
	gid uint32
}

func (f foreignGroup) Sys() any { return &syscall.Stat_t{Gid: f.gid} }

// A writer outside the note's group cannot keep it. The old group's
// members then fall under the other bits, and the new group's members were
// under the other bits or in the old group, so neither class may gain
// access that the other lacked.
func TestReplacementOutsideTheNotesGroupGrantsOnlyWhatBothClassesHad(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can give a file any group")
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	foreign := uint32(0)
	for slices.Contains(groups, int(foreign)) || int(foreign) == os.Getegid() {
		foreign++
	}
	dir := t.TempDir()
	f, err := createTemp(dir, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for mode, want := range map[os.FileMode]os.FileMode{
		0o600: 0o600, 0o640: 0o600, 0o660: 0o600, 0o604: 0o600, 0o606: 0o600,
		0o644: 0o644, 0o664: 0o644, 0o646: 0o644, 0o666: 0o666,
	} {
		if err := os.Chmod(f.Name(), mode); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if got := replacementMode(f, foreignGroup{st, foreign}); got != want {
			t.Fatalf("mode %o became %o, want %o", mode, got, want)
		}
	}
}

func TestCancelledPutLeavesLockedNotebookAlone(t *testing.T) {
	s := Store{Root: t.TempDir()}
	dir, err := os.Open(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	args, err := json.Marshal(edit("cancelled"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.CallTool(ctx, "put", args); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("put ignored the deadline")
	}
	if _, err := os.Stat(filepath.Join(s.Root, "cancelled.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func edit(id string) Edit {
	return Edit{ID: id, Title: "Build", Summary: "Use for release builds.", Body: "Use platform 明.", Sources: []string{"message1.0"}, Expected: "new"}
}

func TestNoteLifecycleAndConcurrency(t *testing.T) {
	s := Store{Root: t.TempDir()}
	e := edit("release")
	first, err := s.Put(e)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Put(e); err != nil || got != first {
		t.Fatalf("retry = %v %v", got, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, body := range []string{"correction one", "correction two"} {
		x := e
		x.Body = body
		x.Expected = first.Revision
		wg.Add(1)
		go func(e Edit) { defer wg.Done(); _, err := s.Put(e); results <- err }(x)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !strings.Contains(err.Error(), "changed") {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("%d concurrent edits succeeded", success)
	}
	h, body, err := s.Read(e.ID)
	if err != nil || h.Revision == first.Revision || !strings.Contains(body, "correction") {
		t.Fatalf("%v %q %v", h, body, err)
	}
	if _, err := s.CallTool(context.Background(), "read", json.RawMessage(`{"id":"release","revision":"stale","offset":2}`)); err == nil {
		t.Fatal("read accepted a replaced revision")
	}
}

func TestNoteBoundariesAndPagination(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "book")}
	if p, err := s.List(context.Background(), "", 0, 0); err != nil || !p.Exhausted || len(p.Notes) != 0 {
		t.Fatalf("empty = %v %v", p, err)
	}
	if _, err := os.Stat(s.Root); !os.IsNotExist(err) {
		t.Fatalf("listing an empty notebook created it: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.Put(edit(id)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.List(context.Background(), "release", 0, 2)
	if err != nil || p.Next != 2 || p.Exhausted || len(p.Notes) != 2 {
		t.Fatalf("page = %#v %v", p, err)
	}
	p, err = s.List(context.Background(), "release", p.Next, 2)
	if err != nil || !p.Exhausted || p.Notes[0].ID != "c" {
		t.Fatalf("next = %#v %v", p, err)
	}
	for _, id := range []string{"../a", "/tmp/a", "", "a/b"} {
		if _, err := s.Put(edit(id)); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	if err := os.Symlink(filepath.Join(s.Root, "a.md"), filepath.Join(s.Root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Read("link"); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := s.CallTool(context.Background(), "read", json.RawMessage(`{"id":"link"}`)); err == nil {
		t.Fatal("repair read followed a symlink")
	}
	linked := edit("link")
	target, _, err := s.Read("a")
	if err != nil {
		t.Fatal(err)
	}
	linked.Expected = target.Revision
	linked.Body = "replacement body"
	if _, err := s.Put(linked); err == nil {
		t.Fatal("repair write replaced a symlink")
	}
	if _, err := os.Readlink(filepath.Join(s.Root, "link.md")); err != nil {
		t.Fatal("repair changed the link", err)
	}
	x := edit("big")
	x.Body = strings.Repeat("a", maxNoteBytes)
	if _, err := s.Put(x); err == nil {
		t.Fatal("oversized note accepted")
	}
	for _, raw := range []string{`{"id":"a","path":"other"}`, `{"id":"a"} {}`, `[]`} {
		if _, err := s.CallTool(context.Background(), "read", json.RawMessage(raw)); err == nil {
			t.Fatal(raw)
		}
	}
}

func TestSnapshotBoundsAndMalformedNeighbors(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if _, err := s.List(context.Background(), strings.Repeat("x", 513), 0, 1); err == nil {
		t.Fatal("unbounded search query accepted")
	}
	if err := os.WriteFile(filepath.Join(s.Root, "broken.md"), []byte("# Bad\n\nIncomplete.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		v := edit(id)
		v.Body = strings.Repeat("明", 1200)
		if _, err := s.Put(v); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.List(context.Background(), "", 0, 20)
	if err != nil || len(page.Notes) != 6 || page.InvalidCount != 1 || page.Invalid[0] != "broken" {
		t.Fatal(page, err)
	}
	txt, err := Snapshot(context.Background(), s.Root, "full")
	if err != nil || len(txt) > 8192 || !strings.Contains(txt, "More entries") {
		t.Fatal(len(txt), err)
	}
	if _, _, err = s.Read("broken"); err == nil {
		t.Fatal("read malformed Markdown")
	}
}
