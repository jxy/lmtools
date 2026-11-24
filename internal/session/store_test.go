package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "sessions"))
}

func create(t *testing.T, s *Store) SessionInfo {
	t.Helper()
	info, _, err := s.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// makeOld makes a directory as the format this store replaced left it: a
// session number holding message files and no session.json.
func makeOld(t *testing.T, s *Store, number string) {
	t.Helper()
	dir := filepath.Join(s.Dir(), number)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0001.json"), []byte(`{"role":"user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Session numbers count up and never return, even after the newest session
// is deleted, whichever store value reserves the next one.
func TestSessionNumbersCountUpAndNeverReturn(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, want := range []string{"0001", "0002", "0003"} {
		if got := create(t, s).Number; got != want {
			t.Fatalf("created session %s, want %s", got, want)
		}
	}
	if err := s.Delete(ctx, Ref{Session: "0003"}); err != nil {
		t.Fatal(err)
	}
	if got := create(t, s).Number; got != "0004" {
		t.Fatalf("after deleting 0003, created %s", got)
	}
	if err := s.Delete(ctx, Ref{Session: "0004"}); err != nil {
		t.Fatal(err)
	}
	if got := create(t, Open(s.Dir())).Number; got != "0005" {
		t.Fatalf("after deleting 0004, created %s", got)
	}
}

// A directory that holds the number the count reaches, as a restored copy or
// an older lmc leaves one, is skipped, and deleting a directory past the
// count moves the count beyond it, so its number does not return.
func TestCreationSkipsNumbersInUse(t *testing.T) {
	s := newStore(t)
	create(t, s)
	makeOld(t, s, "0002")
	if got := create(t, s).Number; got != "0003" {
		t.Fatalf("created %s beside an old 0002", got)
	}
	makeOld(t, s, "0006")
	if err := s.Delete(context.Background(), Ref{Session: "0006"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < 3; i++ {
		got = append(got, create(t, s).Number)
	}
	if strings.Join(got, " ") != "0007 0008 0009" {
		t.Fatalf("after deleting 0006, created %v", got)
	}
}

// A count of sessions restored from an older copy gives no number a session
// holds or held: a deleted session's lock file stays, and creation starts
// past the highest number a directory or a lock file has. Without
// sessions.json, the count starts there too.
func TestRestoredCountGivesNoNumberAgain(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i := 0; i < 3; i++ {
		create(t, s)
	}
	restore := func(next string) {
		t.Helper()
		path := filepath.Join(s.Dir(), rootFileName)
		var root rootRecord
		if ok, err := readJSON(path, &root); err != nil || !ok {
			t.Fatalf("read %s: %t %v", path, ok, err)
		}
		root.Next = next
		if err := writeJSON(path, root); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(ctx, Ref{Session: "0002"}); err != nil {
		t.Fatal(err)
	}
	restore("0002")
	if got := create(t, s).Number; got != "0004" {
		t.Fatalf("with 0002 deleted and 0003 kept, created %s", got)
	}
	for _, number := range []string{"0003", "0004"} {
		if err := s.Delete(ctx, Ref{Session: number}); err != nil {
			t.Fatal(err)
		}
	}
	restore("0002")
	if got := create(t, s).Number; got != "0005" {
		t.Fatalf("with 0002 to 0004 deleted, created %s", got)
	}
	if err := s.Delete(ctx, Ref{Session: "0005"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.Dir(), rootFileName)); err != nil {
		t.Fatal(err)
	}
	if got := create(t, s).Number; got != "0006" {
		t.Fatalf("without sessions.json, created %s", got)
	}
}

// Creation stops at the last session number formatID spells, with a count
// at it and with a directory at it.
func TestCreationStopsAtTheLastNumber(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	create(t, s)
	path := filepath.Join(s.Dir(), rootFileName)
	var root rootRecord
	if ok, err := readJSON(path, &root); err != nil || !ok {
		t.Fatalf("read %s: %t %v", path, ok, err)
	}
	root.Next = "fffffffffffffffe"
	if err := writeJSON(path, root); err != nil {
		t.Fatal(err)
	}
	if got := create(t, s).Number; got != "fffffffffffffffe" {
		t.Fatalf("created %s", got)
	}
	if _, _, err := s.Create(ctx); !errors.Is(err, errNoNumbers) {
		t.Fatalf("past the last number: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.Dir(), "ffffffffffffffff"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx); !errors.Is(err, errNoNumbers) {
		t.Fatalf("beside a directory at the last number: %v", err)
	}
}

// Sessions can be deleted after the numbers ran out, without sessions.json:
// the count stays at the last number, which creation never gives.
func TestDeletionAfterTheNumbersRanOut(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	create(t, s)
	if err := os.Remove(filepath.Join(s.Dir(), rootFileName)); err != nil {
		t.Fatal(err)
	}
	makeOld(t, s, "ffffffffffffffff")
	for _, number := range []string{"0001", "ffffffffffffffff"} {
		if err := s.Delete(ctx, Ref{Session: number}); err != nil {
			t.Fatalf("deleting %s: %v", number, err)
		}
	}
	if _, _, err := s.Create(ctx); !errors.Is(err, errNoNumbers) {
		t.Fatalf("creating after the last number: %v", err)
	}
}

// An entry of the sessions directory named with a number takes the number,
// also when it is a file or a link to nothing: creation gives the next one,
// with and without sessions.json.
func TestAFileNamedWithANumberTakesIt(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, name := range []string{"0001", "0003"} {
		if err := os.WriteFile(filepath.Join(s.Dir(), name), []byte("not a session"), 0o600); err != nil {
			t.Fatal(err)
		}
		got = append(got, create(t, s).Number)
	}
	if err := os.Symlink(filepath.Join(s.Dir(), "nowhere"), filepath.Join(s.Dir(), "0005")); err != nil {
		t.Fatal(err)
	}
	got = append(got, create(t, s).Number)
	if strings.Join(got, " ") != "0002 0004 0006" {
		t.Fatalf("after files 0001 and 0003 and a link 0005, created %v", got)
	}
}

// A session whose record cannot be read is listed as unreadable and hides
// no other session: identities still resolve, and creation still works.
func TestADamagedSessionHidesNoOther(t *testing.T) {
	s := newStore(t)
	good := create(t, s)
	damaged := filepath.Join(s.Dir(), "0002")
	if err := os.MkdirAll(damaged, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(damaged, sessionFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := Open(s.Dir())
	if err := os.Remove(filepath.Join(s.Dir(), rootFileName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fresh.Create(context.Background()); err != nil {
		t.Fatalf("creating beside a damaged session: %v", err)
	}
	sessions, err := s.Sessions()
	if err != nil || len(sessions) != 3 || sessions[1].Unreadable == nil || sessions[0].Unreadable != nil {
		t.Fatalf("sessions %+v: %v", sessions, err)
	}
	if located, err := s.Locate(Ref{Session: good.Identity}); err != nil || located.Session != good.Number {
		t.Fatalf("identity %s: %+v %v", good.Identity, located, err)
	}
}

// The count starts above every numbered directory, old ones included, and
// only the first creation reports the old sessions it found. Deleting an old
// session before any creation still keeps its number from returning.
func TestCountStartsAboveExistingDirectories(t *testing.T) {
	s := newStore(t)
	makeOld(t, s, "0001")
	makeOld(t, s, "0007")
	makeOld(t, s, "0009")
	if err := s.Delete(context.Background(), Ref{Session: "0009"}); err != nil {
		t.Fatal(err)
	}
	info, old, err := s.Create(context.Background())
	if err != nil || info.Number != "000a" || old != 2 {
		t.Fatalf("first creation: %s with %d old sessions: %v; want 000a with 2", info.Number, old, err)
	}
	if info, old, err = s.Create(context.Background()); err != nil || info.Number != "000b" || old != 0 {
		t.Fatalf("second creation: %s with %d old sessions: %v", info.Number, old, err)
	}
}

func TestConcurrentCreationGivesDistinctNumbers(t *testing.T) {
	s := newStore(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, _, err := s.Create(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[info.Number] {
				t.Errorf("number %s given twice", info.Number)
			}
			seen[info.Number] = true
		}()
	}
	wg.Wait()
	if len(seen) != 16 {
		t.Fatalf("%d distinct numbers", len(seen))
	}
}

// An identity maps to its session. A copied session shares its identity,
// and then no command picks one of the two.
func TestIdentityNamesOneSession(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	create(t, s)
	info := create(t, s)
	if !validIdentity(info.Identity) {
		t.Fatalf("identity %q", info.Identity)
	}
	if ref, err := s.Locate(Ref{Session: info.Identity, Message: "0001"}); err != nil || ref != (Ref{Session: info.Number, Message: "0001"}) {
		t.Fatalf("Locate = %v, %v", ref, err)
	}
	if out, err := exec.Command("cp", "-R", filepath.Join(s.Dir(), info.Number), filepath.Join(s.Dir(), "0005")).CombinedOutput(); err != nil {
		t.Fatalf("copy: %v: %s", err, out)
	}
	_, err := s.Locate(Ref{Session: info.Identity})
	if err == nil || !strings.Contains(err.Error(), "0002 and 0005") {
		t.Fatalf("Locate of a shared identity: %v", err)
	}
	if err := s.Delete(ctx, Ref{Session: info.Identity}); err == nil {
		t.Fatal("deleting by a shared identity picked a session")
	}
	for _, number := range []string{"0002", "0005"} {
		if _, err := s.Info(number); err != nil {
			t.Fatalf("session %s after the refused deletion: %v", number, err)
		}
	}
	if _, err := s.Locate(Ref{Session: identityPrefix + strings.Repeat("0", 32)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown identity: %v", err)
	}
}

// An old session is listed as old, refused by every reader and writer, and
// deleted like any other.
func TestOldSessionsAreListedRefusedAndDeleted(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	create(t, s)
	makeOld(t, s, "0003")
	sessions, err := s.Sessions()
	if err != nil || len(sessions) != 2 || sessions[0].Old || !sessions[1].Old {
		t.Fatalf("Sessions = %+v, %v", sessions, err)
	}
	old := Ref{Session: "0003"}
	if _, err := s.Locate(old); !errors.Is(err, ErrOldSession) {
		t.Fatalf("Locate: %v", err)
	}
	if _, err := s.Newest("0003"); !errors.Is(err, ErrOldSession) {
		t.Fatalf("Newest: %v", err)
	}
	if _, err := s.Append(ctx, old, text("hello")); !errors.Is(err, ErrOldSession) {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Info("0003"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after deletion: %v", err)
	}
}

// A deleted session leaves the listing in one rename, and what an
// interrupted deletion left behind goes with the next deletion.
func TestSessionDeletionLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	create(t, s)
	create(t, s)
	leftover := filepath.Join(s.Dir(), deletedPrefix+"0009-x")
	if err := os.MkdirAll(filepath.Join(leftover, "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if sessions, err := s.Sessions(); err != nil || len(sessions) != 2 {
		t.Fatalf("Sessions = %+v, %v", sessions, err)
	}
	if err := s.Delete(ctx, Ref{Session: "0001"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), deletedPrefix) || entry.Name() == "0001" {
			t.Errorf("%s remains", entry.Name())
		}
	}
}
