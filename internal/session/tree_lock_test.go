//go:build !windows

package session

import (
	"context"
	"errors"
	"lmtools/internal/core"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// taggedImage returns an image whose data ends in tag, so images with
// different tags differ in every block that carries them.
func taggedImage(tag byte) core.ImageBlock {
	data := append(append([]byte(nil), sessionTestPNG...), tag)
	return core.ImageBlock{URL: core.ImageDataURL("image/png", data), Name: "shot.png", Detail: "low"}
}

// imagesIn returns the URL of every image block in messages, in order.
func imagesIn(messages []core.TypedMessage) []string {
	var urls []string
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if image, ok := block.(core.ImageBlock); ok {
				urls = append(urls, image.URL)
			}
		}
	}
	return urls
}

// lookAndAnswer appends a question carrying image and an answer, and returns
// the question's ID.
func lookAndAnswer(ctx context.Context, sess *Session, image core.ImageBlock) (string, error) {
	look, err := AppendMessageWithBlocks(ctx, sess, Message{Role: core.RoleUser, Content: "look", Timestamp: time.Now()}, nil, nil, core.UserMessageBlocks("look", []core.ImageBlock{image}))
	if err != nil {
		return "", err
	}
	_, err = AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleAssistant, Content: "seen", Timestamp: time.Now()}, nil, nil)
	return look.MessageID, err
}

// replaceSession deletes a session and writes another at the same path with
// the same messages under the same IDs, but another image.
func replaceSession(ctx context.Context, sessionPath string, image core.ImageBlock) error {
	if err := DeleteNode(sessionPath); err != nil {
		return err
	}
	if err := os.Mkdir(sessionPath, 0o700); err != nil {
		return err
	}
	sess := &Session{Path: sessionPath}
	if _, err := saveSystemMessage(sess, "source system"); err != nil {
		return err
	}
	_, err := lookAndAnswer(ctx, sess, image)
	return err
}

// imageReaders are the ways a turn reads a pinned lineage together with the
// images its sidecars hold. Each prepares before the read and returns it.
var imageReaders = []struct {
	name    string
	prepare func(t *testing.T, ctx context.Context, sess *Session) func() ([]string, error)
}{
	{name: "build", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() ([]string, error) {
		return func() ([]string, error) {
			messages, err := BuildMessagesForSession(ctx, sess)
			return imagesIn(messages), err
		}
	}},
	{name: "cached build", prepare: func(t *testing.T, ctx context.Context, sess *Session) func() ([]string, error) {
		build, err := CreateCachedMessageBuilderForSession(ctx, sess)
		if err != nil {
			t.Fatalf("CreateCachedMessageBuilderForSession() error = %v", err)
		}
		return func() ([]string, error) {
			messages, err := build(sess.Path)
			return imagesIn(messages), err
		}
	}},
	{name: "prepare", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() ([]string, error) {
		return func() ([]string, error) {
			plan, err := PrepareRequestAt(ctx, newTestCoordinatorConfig(), core.NewTestNotifier(), core.TestToolUI{}, sess, "next", false, PendingToolSkip)
			if err != nil {
				return nil, err
			}
			return imagesIn(plan.Messages), nil
		}
	}},
	{name: "fork through the head", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() ([]string, error) {
		return func() ([]string, error) {
			fork, err := ForkThroughHead(ctx, DefaultManager(), sess.Path, *sess.Head)
			if err != nil {
				return nil, err
			}
			messages, err := BuildMessagesWithToolInteractions(ctx, fork.Path)
			return imagesIn(messages), err
		}
	}},
}

// A reader checks its head and then reads the sidecars of the lineage,
// under one hold of the tree's lock. Another run that replaces the session
// between the two, with the same messages under the same IDs and another
// image, waits for the read to finish, and the read carries the image of the
// lineage it checked.
func TestReadThroughAHeadKeepsTheSidecarsItChecked(t *testing.T) {
	for _, tt := range imageReaders {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			source := createPlanSession(t, "source system")
			original := taggedImage('A')
			if _, err := lookAndAnswer(ctx, source, original); err != nil {
				t.Fatalf("lookAndAnswer() error = %v", err)
			}
			pinned, err := OpenSession(ctx, GetSessionID(source.Path))
			if err != nil {
				t.Fatalf("OpenSession() error = %v", err)
			}
			read := tt.prepare(t, ctx, pinned)

			replaced := make(chan error, 1)
			var once sync.Once
			afterHeadVerifiedForTest = func(string) {
				once.Do(func() {
					done := make(chan error, 1)
					go func() { done <- replaceSession(ctx, source.Path, taggedImage('B')) }()
					// The replacement needs the tree's lock, which the read
					// holds. Give it time to finish if it could.
					select {
					case err := <-done:
						replaced <- err
					case <-time.After(200 * time.Millisecond):
						go func() { replaced <- <-done }()
					}
				})
			}
			t.Cleanup(func() { afterHeadVerifiedForTest = nil })

			images, err := read()
			afterHeadVerifiedForTest = nil
			if err != nil {
				t.Fatalf("read error = %v", err)
			}
			if err := <-replaced; err != nil {
				t.Fatalf("replace the session: %v", err)
			}
			if len(images) != 1 || images[0] != original.URL {
				t.Fatalf("read %d images, first %.40q; want only the checked lineage's image", len(images), images)
			}
		})
	}
}

// branchWithImage creates a session with a branch that holds a question
// carrying an image and an answer, and returns a value pinned at the answer
// with the question's ID.
func branchWithImage(t *testing.T, ctx context.Context) (*Session, string) {
	t.Helper()
	root := createPlanSession(t, "source system")
	appendPlanMessage(t, ctx, root, core.RoleUser, "question")
	appendPlanMessage(t, ctx, root, core.RoleAssistant, "answer")
	followUp := appendPlanMessage(t, ctx, root, core.RoleUser, "follow up")
	branchPath, err := CreateSibling(ctx, root.Path, followUp)
	if err != nil {
		t.Fatalf("CreateSibling() error = %v", err)
	}
	imageID, err := lookAndAnswer(ctx, &Session{Path: branchPath}, taggedImage('A'))
	if err != nil {
		t.Fatalf("lookAndAnswer() error = %v", err)
	}
	pinned, err := OpenSession(ctx, GetSessionID(branchPath))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	return pinned, imageID
}

// A deletion removes files one at a time under the tree's lock. Every way
// of continuing a head that arrives while one is paused, after an earlier
// image sidecar went and before the head's metadata does, waits for the
// deletion to finish and then finds the head gone: none of them reads or
// writes the half deleted lineage. The head is in a branch, so a commit
// waits because it takes the tree's lock and not only its directory's.
func TestContinuationsWaitForADeletionInProgress(t *testing.T) {
	for _, tt := range continuations {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			branch, imageID := branchWithImage(t, ctx)
			continueHead := tt.prepare(t, ctx, branch)
			from, err := strconv.ParseUint(imageID, 16, 64)
			if err != nil {
				t.Fatalf("ParseUint() error = %v", err)
			}

			paused, resume := make(chan struct{}), make(chan struct{})
			deleted := make(chan error, 1)
			go func() {
				deleted <- WithSessionLock(treeRoot(branch.Path), 0, func() error {
					if err := os.Remove(buildMessageFilePaths(branch.Path, imageID).BlocksPath); err != nil {
						return err
					}
					close(paused)
					<-resume
					return deleteMessageAndDescendants(branch.Path, int(from))
				})
			}()
			select {
			case <-paused:
			case err := <-deleted:
				t.Fatalf("the deletion did not pause: %v", err)
			}

			done := make(chan error, 1)
			go func() { done <- continueHead() }()
			select {
			case err := <-done:
				close(resume)
				<-deleted
				t.Fatalf("continued while a deletion held the tree's lock: error = %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			close(resume)
			if err := <-deleted; err != nil {
				t.Fatalf("deletion: %v", err)
			}
			if err := <-done; !errors.Is(err, ErrHeadReplaced) {
				t.Fatalf("after the deletion: error = %v, want %v", err, ErrHeadReplaced)
			}
		})
	}
}

// expectCancelledWait holds the tree's lock of sessionPath as another run
// would, starts run, cancels run's context once run is waiting, and checks
// that run then returns the context's error at once.
func expectCancelledWait(t *testing.T, sessionPath string, cancel context.CancelFunc, run func() error) {
	t.Helper()
	held, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- WithSessionLock(treeRoot(sessionPath), 0, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer func() {
		close(release)
		if err := <-holder; err != nil {
			t.Errorf("holding the lock: %v", err)
		}
	}()

	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		t.Fatalf("finished while another run held the tree's lock: error = %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancelled := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error after the cancellation = %v, want %v", err, context.Canceled)
		}
		if waited := time.Since(cancelled); waited > time.Second {
			t.Fatalf("returned %v after the cancellation, want at once", waited)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting 5s after the cancellation")
	}
}

// Every way of continuing a head, and opening a session, waits while another
// run holds the tree's lock. Cancelling the context, as Ctrl-C does, ends
// that wait at once with the context's error, long before the lock's timeout.
func TestLockWaitsEndWhenTheContextIsCancelled(t *testing.T) {
	for _, tt := range continuations {
		t.Run(tt.name, func(t *testing.T) {
			base := setupCoordinatorTestEnv(t)
			source, _ := pinnedSource(t, base)
			sess, err := OpenSession(base, GetSessionID(source.Path))
			if err != nil {
				t.Fatalf("OpenSession() error = %v", err)
			}
			ctx, cancel := context.WithCancel(base)
			defer cancel()
			expectCancelledWait(t, sess.Path, cancel, tt.prepare(t, ctx, sess))
		})
	}
	t.Run("open", func(t *testing.T) {
		base := setupCoordinatorTestEnv(t)
		source, _ := pinnedSource(t, base)
		ctx, cancel := context.WithCancel(base)
		defer cancel()
		expectCancelledWait(t, source.Path, cancel, func() error {
			_, err := OpenSession(ctx, GetSessionID(source.Path))
			return err
		})
	})
}
