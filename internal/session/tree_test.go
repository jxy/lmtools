package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/core"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const appenderEnv = "LMC_TEST_SESSION_APPENDER"

// TestMain lets the test binary append to a session from another process.
func TestMain(m *testing.M) {
	if job := os.Getenv(appenderEnv); job != "" {
		os.Exit(runAppender(job))
	}
	os.Exit(m.Run())
}

// runAppender appends n messages, one call each, after a message, for a
// job written as "DIR SESSION PARENT N".
func runAppender(job string) int {
	var dir, number, parent string
	var n int
	if _, err := fmt.Sscan(job, &dir, &number, &parent, &n); err != nil {
		fmt.Println(err)
		return 1
	}
	s := Open(dir)
	for i := 0; i < n; i++ {
		if _, err := s.Append(context.Background(), Ref{Session: number, Message: parent}, text("from another process")); err != nil {
			fmt.Println(err)
			return 1
		}
	}
	return 0
}

func text(s string) Entry {
	return Entry{Role: core.RoleUser, Content: Content{Blocks: []core.Block{core.TextBlock{Text: s}}}}
}

// textsOf returns the text of each message.
func textsOf(messages []core.TypedMessage) []string {
	var texts []string
	for _, m := range messages {
		var b strings.Builder
		for _, block := range m.Blocks {
			if text, ok := block.(core.TextBlock); ok {
				b.WriteString(text.Text)
			}
		}
		texts = append(texts, b.String())
	}
	return texts
}

func appendTo(t *testing.T, s *Store, at Ref, entries ...Entry) []Message {
	t.Helper()
	committed, err := s.Append(context.Background(), at, entries...)
	if err != nil {
		t.Fatal(err)
	}
	return committed
}

func ids(messages []Message) []string {
	var out []string
	for _, m := range messages {
		out = append(out, m.ID)
	}
	return out
}

// A batch is a chain after the message it follows, and a message appended
// after one that already has a child is its alternative.
func TestAppendChainsParents(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	chain := appendTo(t, s, session, text("q1"), text("a1"), text("q2"))
	if got := strings.Join([]string{chain[0].Parent, chain[1].Parent, chain[2].Parent}, ","); got != ",0001,0002" {
		t.Fatalf("parents %q", got)
	}
	alternative := appendTo(t, s, session.At("0002"), text("another q2"))[0]
	if alternative.ID != "0004" || alternative.Parent != "0002" {
		t.Fatalf("alternative %+v", alternative)
	}
	lineage, err := s.Lineage(session.At("0004"))
	if err != nil || !reflect.DeepEqual(ids(lineage), []string{"0001", "0002", "0004"}) {
		t.Fatalf("lineage %v: %v", ids(lineage), err)
	}
	tree, err := s.Tree(session.Session)
	if err != nil || !reflect.DeepEqual(tree.Children["0002"], []string{"0003", "0004"}) || !reflect.DeepEqual(tree.Children[""], []string{"0001"}) {
		t.Fatalf("tree %v: %v", tree.Children, err)
	}
	if newest, err := s.Newest(session.Session); err != nil || newest != "0004" {
		t.Fatalf("newest %q: %v", newest, err)
	}
	history, err := s.History(session.At("0004"))
	if err != nil {
		t.Fatal(err)
	}
	if texts := textsOf(history.Typed()); strings.Join(texts, "|") != "q1|a1|another q2" {
		t.Fatalf("history %q", texts)
	}
}

// A session.json restored from an older copy, whose count of IDs fell
// behind the messages, makes no message take an ID in use, one a deleted
// message had while a newer message remains, or one older than its parent:
// the IDs start past the newest message.
func TestAppendSkipsIDsInUse(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	appendTo(t, s, session, text("q1"), text("a1"))
	appendTo(t, s, session.At("0001"), text("other a1"), text("q2"))
	rewind := func() {
		t.Helper()
		path := filepath.Join(s.Dir(), session.Session, sessionFileName)
		var record sessionRecord
		if ok, err := readJSON(path, &record); err != nil || !ok {
			t.Fatalf("read session record: %t %v", ok, err)
		}
		record.Next = "0002"
		if err := writeJSON(path, record); err != nil {
			t.Fatal(err)
		}
	}
	rewind()
	if m := appendTo(t, s, session.At("0001"), text("third a1"))[0]; m.ID != "0005" {
		t.Fatalf("after an ID in use, appended %s", m.ID)
	}
	if c, err := s.Content(session.At("0002")); err != nil || textsOf([]core.TypedMessage{Typed(Message{Role: core.RoleUser}, c)})[0] != "a1" {
		t.Fatalf("message 0002 changed: %v", err)
	}
	if err := s.Delete(context.Background(), session.At("0002")); err != nil {
		t.Fatal(err)
	}
	rewind()
	if m := appendTo(t, s, session.At("0001"), text("fourth a1"))[0]; m.ID != "0006" {
		t.Fatalf("after the ID of a deleted message older than others, appended %s", m.ID)
	}
	rewind()
	if m := appendTo(t, s, session.At("0006"), text("a2"))[0]; m.ID != "0007" {
		t.Fatalf("after a free ID older than its parent, appended %s", m.ID)
	}
	if err := s.Delete(context.Background(), session.At("0003")); err != nil {
		t.Fatal(err)
	}
	rewind()
	if m := appendTo(t, Open(s.Dir()), session.At("0001"), text("fifth a1"))[0]; m.ID != "0008" {
		t.Fatalf("in another store, after the IDs of deleted messages older than others, appended %s", m.ID)
	}
}

// rewind sets the records of the IDs session reserved, as files restored
// from an older copy leave them: session.json's count when next is set, and
// the greatest ID in reserved.json when last is.
func rewind(t *testing.T, s *Store, session Ref, next, last string) {
	t.Helper()
	dir := filepath.Join(s.Dir(), session.Session)
	if next != "" {
		var record sessionRecord
		if ok, err := readJSON(filepath.Join(dir, sessionFileName), &record); err != nil || !ok {
			t.Fatalf("read session record: %t %v", ok, err)
		}
		record.Next = next
		if err := writeJSON(filepath.Join(dir, sessionFileName), record); err != nil {
			t.Fatal(err)
		}
	}
	if last != "" {
		if err := writeJSON(filepath.Join(dir, reservedFileName), reservedRecord{Last: last}); err != nil {
			t.Fatal(err)
		}
	}
}

// Restoring session.json or reserved.json from an older copy gives no ID
// again: each holds the IDs reserved apart from the other, written before
// any message. This holds after the last message was deleted, for a store
// already open while another deleted an ID below a remaining one, and after
// a batch that stopped once it reserved its IDs.
func TestARestoredRecordGivesNoIDAgain(t *testing.T) {
	ctx := context.Background()
	t.Run("after the last message was deleted", func(t *testing.T) {
		s := newStore(t)
		session := Ref{Session: create(t, s).Number}
		appendTo(t, s, session, text("q1"), text("a1"))
		if err := s.Delete(ctx, session.At("0002")); err != nil {
			t.Fatal(err)
		}
		rewind(t, s, session, "0002", "")
		if m := appendTo(t, Open(s.Dir()), session.At("0001"), text("another a1"))[0]; m.ID != "0003" {
			t.Fatalf("a new store appended %s", m.ID)
		}
	})
	t.Run("in an open store, after another deleted an ID below a remaining one", func(t *testing.T) {
		s := newStore(t)
		session := Ref{Session: create(t, s).Number}
		appendTo(t, s, session, text("q1"))
		other := Open(s.Dir())
		appendTo(t, other, session.At("0001"), text("a1"))
		appendTo(t, other, session.At("0001"), text("other a1"))
		if err := other.Delete(ctx, session.At("0002")); err != nil {
			t.Fatal(err)
		}
		rewind(t, s, session, "0002", "")
		if m := appendTo(t, s, session.At("0001"), text("third a1"))[0]; m.ID != "0004" {
			t.Fatalf("the open store appended %s", m.ID)
		}
	})
	t.Run("from reserved.json", func(t *testing.T) {
		s := newStore(t)
		session := Ref{Session: create(t, s).Number}
		appendTo(t, s, session, text("q1"), text("a1"))
		if err := s.Delete(ctx, session.At("0002")); err != nil {
			t.Fatal(err)
		}
		rewind(t, s, session, "", "0001")
		if m := appendTo(t, s, session.At("0001"), text("another a1"))[0]; m.ID != "0003" {
			t.Fatalf("appended %s", m.ID)
		}
	})
	t.Run("after a batch stopped once it reserved its IDs", func(t *testing.T) {
		s := newStore(t)
		session := Ref{Session: create(t, s).Number}
		appendTo(t, s, session, text("q1"))
		reservedForTest = func() error { return errors.New("crash") }
		t.Cleanup(func() { reservedForTest = nil })
		if committed, err := s.Append(ctx, session.At("0001"), text("lost")); err == nil || len(committed) != 0 {
			t.Fatalf("committed %v: %v", ids(committed), err)
		}
		reservedForTest = nil
		rewind(t, s, session, "0002", "")
		if m := appendTo(t, s, session.At("0001"), text("a1"))[0]; m.ID != "0003" {
			t.Fatalf("after the stopped batch reserved 0002, appended %s", m.ID)
		}
	})
}

// A batch whose IDs, or the count past them, would go beyond the last ID
// formatID spells is refused before either record of the reservation is
// written, and no message is written over.
func TestAppendStopsAtTheLastID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	appendTo(t, s, session, text("q1"))
	record := func() (string, string) {
		t.Helper()
		var next sessionRecord
		var reserved reservedRecord
		dir := filepath.Join(s.Dir(), session.Session)
		if _, err := readJSON(filepath.Join(dir, sessionFileName), &next); err != nil {
			t.Fatal(err)
		}
		if _, err := readJSON(filepath.Join(dir, reservedFileName), &reserved); err != nil {
			t.Fatal(err)
		}
		return next.Next, reserved.Last
	}
	for _, c := range []struct{ next, last string }{
		{"ffffffffffffffff", "0001"},
		{"0002", "ffffffffffffffff"},
	} {
		rewind(t, s, session, c.next, c.last)
		if committed, err := s.Append(ctx, session.At("0001"), text("a"), text("b"), text("c")); err == nil || len(committed) != 0 {
			t.Fatalf("with the count at %s and %s reserved, committed %v: %v", c.next, c.last, ids(committed), err)
		}
		if next, last := record(); next != c.next || last != c.last {
			t.Fatalf("the refused batch left the records at %s and %s", next, last)
		}
	}
	if c, err := s.Content(session.At("0001")); err != nil || textsOf([]core.TypedMessage{Typed(Message{Role: core.RoleUser}, c)})[0] != "q1" {
		t.Fatalf("message 0001 changed: %v", err)
	}
	rewind(t, s, session, "fffffffffffffffd", "fffffffffffffffc")
	if committed, err := s.Append(ctx, session.At("0001"), text("a"), text("b")); err != nil || strings.Join(ids(committed), " ") != "fffffffffffffffd fffffffffffffffe" {
		t.Fatalf("the last two IDs: committed %v: %v", ids(committed), err)
	}
	if committed, err := s.Append(ctx, session.At("0001"), text("c")); err == nil || len(committed) != 0 {
		t.Fatalf("past the last ID, committed %v: %v", ids(committed), err)
	}
}

// Appends take the IDs the records give and read no other IDs, also after
// a record was restored from an older copy.
func TestAppendsReadNoIDs(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	scans := 0
	scanForTest = func() { scans++ }
	t.Cleanup(func() { scanForTest = nil })
	head := appendTo(t, s, session, text("q1"))[0]
	for i := 0; i < 4; i++ {
		head = appendTo(t, s, session.At(head.ID), text(fmt.Sprint("m", i)))[0]
	}
	other := appendTo(t, Open(s.Dir()), session.At(head.ID), text("from another store"))[0]
	appendTo(t, s, session.At(other.ID), text("after it"))
	rewind(t, s, session, "0002", "")
	if m := appendTo(t, s, session.At("0001"), text("after the rewind"))[0]; m.ID != "0008" || scans != 0 {
		t.Fatalf("after the rewind, appended %s with %d reads of every ID", m.ID, scans)
	}
}

// When both records fall behind the messages, as restoring both from an
// older copy can leave them, the store reads every ID when an ID it would
// take is in use or the parent is newer, and starts past the newest one.
func TestBothRecordsBehindMissIDsInUse(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	appendTo(t, s, session, text("q1"), text("a1"), text("q2"), text("a2"))
	rewind(t, s, session, "0002", "0001")
	if m := appendTo(t, s, session.At("0001"), text("other a1"))[0]; m.ID != "0005" {
		t.Fatalf("with 0002 in use, appended %s", m.ID)
	}
	appendTo(t, s, session.At("0005"), text("q3"))
	appendTo(t, s, session.At("0001"), text("third a1"))
	if err := s.Delete(context.Background(), session.At("0006")); err != nil {
		t.Fatal(err)
	}
	rewind(t, s, session, "0006", "0005")
	if m := appendTo(t, s, session.At("0007"), text("q4"))[0]; m.ID != "0008" {
		t.Fatalf("after parent 0007 with 0006 free, appended %s", m.ID)
	}
}

func TestAppendAfterAMissingMessageFails(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	committed, err := s.Append(context.Background(), session.At("0009"), text("q"))
	if !errors.Is(err, ErrNotFound) || len(committed) != 0 || !strings.Contains(err.Error(), "0001/0009") {
		t.Fatalf("committed %v: %v", ids(committed), err)
	}
	if newest, _ := s.Newest(session.Session); newest != "" {
		t.Fatalf("a message was written: %s", newest)
	}
}

// IDs are reserved before anything uses them, so neither a crash after the
// reservation nor deleting the newest message lets an ID return.
func TestReservedIDsNeverReturn(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	reservedForTest = func() error { return errors.New("crash") }
	t.Cleanup(func() { reservedForTest = nil })
	if committed, err := s.Append(ctx, session, text("lost")); err == nil || len(committed) != 0 {
		t.Fatalf("committed %v: %v", ids(committed), err)
	}
	reservedForTest = nil
	first := appendTo(t, s, session, text("q"))[0]
	if first.ID != "0002" {
		t.Fatalf("after a crash that reserved 0001, the next message is %s", first.ID)
	}
	if err := s.Delete(ctx, session.At(first.ID)); err != nil {
		t.Fatal(err)
	}
	if next := appendTo(t, s, session, text("q again"))[0]; next.ID != "0003" {
		t.Fatalf("after deleting the newest message 0002, the next is %s", next.ID)
	}
}

// A batch that fails part way returns what it committed, which stays
// readable; the rest does not exist.
func TestFailedBatchReturnsWhatItCommitted(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	writeForTest = func(m Message) error {
		if m.ID == "0002" {
			return errors.New("disk full")
		}
		return nil
	}
	t.Cleanup(func() { writeForTest = nil })
	committed, err := s.Append(context.Background(), session, text("settings"), text("question"))
	if err == nil || !reflect.DeepEqual(ids(committed), []string{"0001"}) {
		t.Fatalf("committed %v: %v", ids(committed), err)
	}
	writeForTest = nil
	if _, err := s.Lineage(session.At("0001")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Message(session.At("0002")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message 0002: %v", err)
	}
}

// A batch whose context ends stops before its next message and returns
// what it committed.
func TestCancelledBatchStopsBeforeItsNextMessage(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writeForTest = func(m Message) error {
		if m.ID == "0002" {
			cancel()
		}
		return nil
	}
	t.Cleanup(func() { writeForTest = nil })
	committed, err := s.Append(ctx, session, text("settings"), text("question"), text("answer"))
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(ids(committed), []string{"0001", "0002"}) {
		t.Fatalf("committed %v: %v", ids(committed), err)
	}
	if _, err := s.Message(session.At("0003")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message 0003: %v", err)
	}
}

// Deleting a message again after a deletion that stopped once its metadata
// was gone removes the files it left; once they are gone, the message is not
// found.
func TestDeleteFinishesAnInterruptedDeletion(t *testing.T) {
	for _, c := range []struct {
		name                 string
		content, checkpoints bool
	}{
		{"content and checkpoints", true, true},
		{"checkpoints", false, true},
		{"content of a message without checkpoints", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			session := Ref{Session: create(t, s).Number}
			appendTo(t, s, session, text("q"), text("a"))
			dir := filepath.Join(s.Dir(), session.Session)
			checkpoints := filepath.Join(dir, contextDirectory, "0002")
			if c.checkpoints {
				if err := os.MkdirAll(checkpoints, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(checkpoints, "tag.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			removed := []string{"0002.json"}
			if !c.content {
				removed = append(removed, "0002.content.json")
			}
			for _, name := range removed {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Delete(ctx, session.At("0002")); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{filepath.Join(dir, "0002.content.json"), checkpoints} {
				if exists(path) {
					t.Errorf("%s remains", path)
				}
			}
			if err := s.Delete(ctx, session.At("0002")); !errors.Is(err, ErrNotFound) {
				t.Fatalf("deleting the deleted message again: %v", err)
			}
			if _, err := s.Lineage(session.At("0001")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Files a failed write left without metadata are not messages, and the
// next deletion clears them.
func TestLeftoversAreNotMessages(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	appendTo(t, s, session, text("q1"), text("a1"))
	dir := filepath.Join(s.Dir(), session.Session)
	for _, name := range []string{"0007.content.json", tempPrefix + "partial"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"0001", "0007"} {
		if err := os.MkdirAll(filepath.Join(dir, contextDirectory, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := s.Tree(session.Session)
	if err != nil || len(tree.Messages) != 2 {
		t.Fatalf("tree %v: %v", tree.Messages, err)
	}
	if newest, err := s.Newest(session.Session); err != nil || newest != "0002" {
		t.Fatalf("newest %q: %v", newest, err)
	}
	if err := s.Delete(context.Background(), session.At("0002")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0007.content.json", tempPrefix + "partial", "0002.json", "0002.content.json", filepath.Join(contextDirectory, "0007")} {
		if exists(filepath.Join(dir, name)) {
			t.Errorf("%s remains", name)
		}
	}
	if !exists(filepath.Join(dir, contextDirectory, "0001")) {
		t.Error("the checkpoints of message 0001 were removed")
	}
}

// A lineage whose ancestor was deleted while it was read names the
// ancestor that is gone.
func TestLineageNamesADeletedAncestor(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	appendTo(t, s, session, text("q1"), text("a1"), text("q2"))
	if err := os.Remove(filepath.Join(s.Dir(), session.Session, "0002"+metadataSuffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lineage(session.At("0003")); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "ancestor "+session.At("0002").String()+" was deleted") {
		t.Fatalf("lineage: %v", err)
	}
}

// Two requests can both start a session that has no message yet.
func TestTwoFirstMessagesFollowTheRoot(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	appendTo(t, s, session, text("first"))
	appendTo(t, s, session, text("also first"))
	tree, err := s.Tree(session.Session)
	if err != nil || !reflect.DeepEqual(tree.Children[""], []string{"0001", "0002"}) {
		t.Fatalf("root children %v: %v", tree.Children[""], err)
	}
}

// Writers in this process and in others that append after one message all
// make alternatives, each with its own ID.
func TestConcurrentAppendsMakeAlternatives(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	base := appendTo(t, s, session, text("q"))[0]
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				if _, err := s.Append(context.Background(), session.At(base.ID), text("answer")); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	var helpers []*exec.Cmd
	for i := 0; i < 2; i++ {
		helper := exec.Command(os.Args[0], "-test.run=^$")
		helper.Env = append(os.Environ(), appenderEnv+"="+strings.Join([]string{s.Dir(), session.Session, base.ID, strconv.Itoa(8)}, " "))
		if err := helper.Start(); err != nil {
			t.Fatal(err)
		}
		helpers = append(helpers, helper)
	}
	wg.Wait()
	for _, helper := range helpers {
		if err := helper.Wait(); err != nil {
			t.Fatalf("appender: %v", err)
		}
	}
	tree, err := s.Tree(session.Session)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(tree.Children[base.ID]); n != 48 || len(tree.Messages) != 49 {
		t.Fatalf("%d alternatives and %d messages, want 48 and 49", n, len(tree.Messages))
	}
}

// Deletion removes a message's descendants before it, so an interruption
// at any step leaves every remaining lineage readable. Alternatives stay.
func TestDeleteRemovesChildrenFirst(t *testing.T) {
	ctx := context.Background()
	for stop := 0; stop < 3; stop++ {
		t.Run(fmt.Sprint("interrupted after ", stop), func(t *testing.T) {
			s := newStore(t)
			session := Ref{Session: create(t, s).Number}
			appendTo(t, s, session, text("q"), text("a"), text("q2"), text("a2"))
			appendTo(t, s, session.At("0001"), text("other a"))
			checkpoint := filepath.Join(s.Dir(), session.Session, contextDirectory, "0003", "tag.json")
			if err := os.MkdirAll(filepath.Dir(checkpoint), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(checkpoint, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			removed := 0
			removeForTest = func(string) error {
				if removed == stop {
					return errors.New("interrupted")
				}
				removed++
				return nil
			}
			t.Cleanup(func() { removeForTest = nil })
			if err := s.Delete(ctx, session.At("0002")); err == nil {
				t.Fatal("the interrupted deletion succeeded")
			}
			tree, err := s.Tree(session.Session)
			if err != nil {
				t.Fatal(err)
			}
			for id := range tree.Messages {
				if _, err := s.Lineage(session.At(id)); err != nil {
					t.Errorf("lineage of %s after the interruption: %v", id, err)
				}
			}
			removeForTest = nil
			if err := s.Delete(ctx, session.At("0002")); err != nil {
				t.Fatal(err)
			}
			if tree, err = s.Tree(session.Session); err != nil || len(tree.Messages) != 2 || tree.Children["0001"][0] != "0005" {
				t.Fatalf("after the deletion: %v: %v", tree.Children, err)
			}
			if exists(filepath.Dir(checkpoint)) {
				t.Error("the checkpoints of a deleted message remain")
			}
		})
	}
}

// Lineage reads need no lock while another writer appends.
func TestLineageReadsBesideAppends(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	parent := appendTo(t, s, session, text("start"))[0].ID
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			m, err := s.Append(context.Background(), session.At(parent), text("next"))
			if err != nil {
				t.Error(err)
				return
			}
			parent = m[0].ID
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		newest, err := s.Newest(session.Session)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.History(session.At(newest)); err != nil {
			t.Fatal(err)
		}
	}
}

// Every kind of block and the tool record come back as they were written.
func TestContentRoundTrips(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	content := Content{
		Blocks: []core.Block{
			core.ReasoningBlock{Provider: "openai", Type: "reasoning", ID: "rs_1", Status: "completed", Text: "thinking", Summary: json.RawMessage(`[{"t":1}]`), Content: json.RawMessage(`[2]`), Signature: "sig", EncryptedContent: "enc", Raw: json.RawMessage(`{"r":3}`)},
			core.TextBlock{Text: "text"},
			core.ToolUseBlock{ID: "call_1", Type: "function", Namespace: "mcp", OriginalName: "orig", Name: "lookup", Input: json.RawMessage(`{"k":"v"}`), InputString: "raw input"},
			core.ToolResultBlock{ToolUseID: "call_1", Type: "function", Namespace: "mcp", Name: "lookup", Content: "result", IsError: true, Images: []core.ImageBlock{{URL: "data:image/png;base64,AA==", Detail: "low", Name: "a.png"}}},
			core.ImageBlock{URL: "data:image/png;base64,AQ==", Detail: "high", Name: "b.png"},
			core.AudioBlock{ID: "audio_1", Data: "AAA", Format: "wav", URL: "u", Duration: 3},
			core.FileBlock{FileID: "file_1"},
		},
		Tools: &core.ToolInteraction{
			Calls:   []core.ToolCall{{ID: "call_1", Name: "lookup", Args: json.RawMessage(`{"k":"v"}`), InvocationID: "inv_1", MCPServer: "srv", MCPTool: "lookup"}},
			Results: []core.ToolResult{{ID: "call_1", Output: "result", Error: "failed", Code: "E1", Elapsed: 12}},
		},
	}
	m := appendTo(t, s, session, Entry{Role: core.RoleAssistant, Model: "model-x", Incomplete: true, Content: content})[0]
	got, err := s.Content(session.At(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, content) {
		t.Fatalf("content\n got %#v\nwant %#v", got, content)
	}
	stored, err := s.Message(session.At(m.ID))
	if err != nil || stored.Role != core.RoleAssistant || stored.Model != "model-x" || !stored.Incomplete || stored.Time.IsZero() {
		t.Fatalf("metadata %+v: %v", stored, err)
	}
}

// A tool result saved without its tool's name takes the name of the call it
// answers when the history is read.
func TestHistoryNamesToolResults(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	committed := appendTo(t, s, session,
		Entry{Role: core.RoleAssistant, Content: Content{Blocks: []core.Block{core.ToolUseBlock{ID: "c1", Name: "lookup", Input: json.RawMessage(`{}`)}}}},
		Entry{Role: core.RoleUser, Content: Content{Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "c1", Content: "found"}}}},
	)
	history, err := s.History(session.At(committed[1].ID))
	if err != nil {
		t.Fatal(err)
	}
	result := history.Typed()[1].Blocks[0].(core.ToolResultBlock)
	if result.Name != "lookup" {
		t.Fatalf("result name %q", result.Name)
	}
}

// A parent is always older than its child, so a damaged file cannot send a
// walk up a lineage around a loop.
func TestParentMustBeOlder(t *testing.T) {
	s := newStore(t)
	session := Ref{Session: create(t, s).Number}
	appendTo(t, s, session, text("q"), text("a"))
	damaged := Message{ID: "0001", Parent: "0002", Role: core.RoleUser}
	if err := writeJSON(filepath.Join(s.Dir(), session.Session, "0001.json"), damaged); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lineage(session.At("0002")); err == nil || !strings.Contains(err.Error(), "invalid ID or parent") {
		t.Fatalf("lineage through a damaged parent: %v", err)
	}
}
