// Package notebook stores plain Markdown notes in an operator-selected directory.
package notebook

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxNoteBytes = 32 << 10
	maxPage      = 30
)

var (
	validID        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	errInvalidNote = errors.New("invalid notebook file")
)

type Store struct{ Root string }

type Header struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Revision string `json:"revision"`
}

type Edit struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Body     string   `json:"body"`
	Sources  []string `json:"sources"`
	Expected string   `json:"expected_revision"`
}

type Page struct {
	Notes           []Header `json:"notes"`
	Next            int      `json:"next_offset,omitempty"`
	Exhausted       bool     `json:"exhausted"`
	Invalid         []string `json:"invalid_notes,omitempty"`
	InvalidCount    int      `json:"invalid_count,omitempty"`
	Unreadable      []string `json:"unreadable_notes,omitempty"`
	UnreadableCount int      `json:"unreadable_count,omitempty"`
}

func oneLine(s string, limit int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsAny(s, "\r\n\x00")
}

func (e Edit) markdown() (string, error) {
	if !validID.MatchString(e.ID) || !oneLine(e.Title, 160) || !oneLine(e.Summary, 400) || strings.TrimSpace(e.Body) == "" || len(e.Sources) == 0 || len(e.Sources) > 64 || e.Expected == "" {
		return "", errors.New("put requires a valid note id, title (160 bytes), summary (400 bytes), body, 1–64 sources and expected_revision")
	}
	if !utf8.ValidString(e.Body) || strings.ContainsRune(e.Body, '\x00') {
		return "", errors.New("note body must be UTF-8 text")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n## Note\n%s\n\n## Sources\n", strings.TrimSpace(e.Title), strings.TrimSpace(e.Summary), strings.TrimSpace(e.Body))
	for _, s := range e.Sources {
		if !oneLine(s, 512) {
			return "", errors.New("each source must be one nonempty line of at most 512 bytes")
		}
		fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(s))
	}
	if b.Len() > maxNoteBytes {
		return "", fmt.Errorf("note exceeds %d bytes; split independently useful subjects", maxNoteBytes)
	}
	return b.String(), nil
}

func header(id, text string) (Header, error) {
	lines := strings.SplitN(text, "\n", 4)
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "# ") || lines[1] != "" || !oneLine(lines[0][2:], 160) || !oneLine(lines[2], 400) {
		return Header{}, fmt.Errorf("%w %q: expected a title and one-line summary", errInvalidNote, id)
	}
	rest, ok := strings.CutPrefix(lines[3], "\n## Note\n")
	sources := strings.LastIndex(rest, "\n\n## Sources\n")
	if !ok || sources < 0 || strings.TrimSpace(rest[:sources]) == "" {
		return Header{}, fmt.Errorf("%w %q: expected Note and Sources sections", errInvalidNote, id)
	}
	entries := strings.Split(strings.TrimSuffix(rest[sources+len("\n\n## Sources\n"):], "\n"), "\n")
	if len(entries) > 64 || strings.ContainsRune(rest, '\x00') {
		return Header{}, fmt.Errorf("%w %q: invalid note body or sources", errInvalidNote, id)
	}
	for _, line := range entries {
		s, ok := strings.CutPrefix(line, "- ")
		if !ok || !oneLine(s, 512) {
			return Header{}, fmt.Errorf("%w %q: invalid source entry", errInvalidNote, id)
		}
	}
	hash := sha256.Sum256([]byte(text))
	return Header{ID: id, Title: lines[0][2:], Summary: lines[2], Revision: hex.EncodeToString(hash[:])}, nil
}

// Check the descriptor itself before reading so a named pipe, device, or
// concurrently replaced symlink cannot become a notebook read.
func (s Store) openNote(id string) (*os.File, os.FileInfo, error) {
	if !validID.MatchString(id) {
		return nil, nil, errors.New("invalid note id")
	}
	path := filepath.Join(s.Root, id+".md")
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%w %q: must be a regular file", errInvalidNote, id)
	}
	return f, st, nil
}

func (s Store) Read(id string) (Header, string, error) {
	f, st, err := s.openNote(id)
	if err != nil {
		return Header{}, "", err
	}
	defer f.Close()
	return readNote(f, st, id)
}

func readNote(f *os.File, st os.FileInfo, id string) (Header, string, error) {
	if st.Size() > maxNoteBytes {
		return Header{}, "", fmt.Errorf("%w %q: exceeds %d bytes", errInvalidNote, id, maxNoteBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxNoteBytes+1))
	if err != nil {
		return Header{}, "", err
	}
	if len(data) > maxNoteBytes || !utf8.Valid(data) {
		return Header{}, "", fmt.Errorf("%w %q: invalid size or UTF-8", errInvalidNote, id)
	}
	text := string(data)
	h, err := header(id, text)
	return h, text, err
}

// rawPage hashes exactly the bytes from which it returns a page. Explicit
// repairs can read oversized files, so only the requested page is buffered.
func (s Store) rawPage(ctx context.Context, id string, offset, length int) (string, []byte, int64, error) {
	f, st, err := s.openNote(id)
	if err != nil {
		return "", nil, 0, err
	}
	defer f.Close()
	size := st.Size()
	if offset < 0 || int64(offset) > size || length < 0 {
		return "", nil, 0, errors.New("invalid note offset or length")
	}
	h := sha256.New()
	src := io.TeeReader(contextReader{ctx, io.NewSectionReader(f, 0, size)}, h)
	if _, err := io.CopyN(io.Discard, src, int64(offset)); err != nil {
		return "", nil, 0, err
	}
	part := make([]byte, min(int64(length), size-int64(offset)))
	if _, err := io.ReadFull(src, part); err != nil {
		return "", nil, 0, err
	}
	if _, err := io.CopyN(io.Discard, src, size-int64(offset)-int64(len(part))); err != nil {
		return "", nil, 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), part, size, nil
}

func (s Store) Put(e Edit) (Header, error) {
	return s.put(context.Background(), e, (*os.File).Sync)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func lockNotebook(ctx context.Context, dir *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// createTemp opens a temporary note with perm less the umask. A new note gets
// 0666, the mode an editor would give it; os.CreateTemp always uses 0600. A
// replacement starts with the old note's owner bits, so its text is never
// open to more accounts than the note it replaces.
func createTemp(dir string, perm os.FileMode) (*os.File, error) {
	var b [8]byte
	for i := 0; i < 100; i++ {
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filepath.Join(dir, ".note-"+hex.EncodeToString(b[:])), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if !errors.Is(err, os.ErrExist) {
			return f, err
		}
	}
	return nil, errors.New("cannot create a temporary note")
}

// replacementMode gives a replacement the replaced note's group and mode,
// including bits the umask removed. A rename publishes a new file, whose
// group comes from the writer or the directory. A writer outside the note's
// group cannot keep it. The old group's members then fall under the other
// bits, so the group and other bits each keep only what both granted.
func replacementMode(f *os.File, st os.FileInfo) os.FileMode {
	mode := st.Mode().Perm()
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && f.Chown(-1, int(sys.Gid)) == nil {
		return mode
	}
	shared := (mode >> 3) & mode & 0o007
	return mode&0o700 | shared<<3 | shared
}

func (s Store) put(ctx context.Context, e Edit, syncDir func(*os.File) error) (Header, error) {
	text, err := e.markdown()
	if err != nil {
		return Header{}, err
	}
	if err = os.MkdirAll(s.Root, 0o700); err != nil {
		return Header{}, err
	}
	dir, err := os.Open(s.Root)
	if err != nil {
		return Header{}, err
	}
	defer dir.Close()
	if err = lockNotebook(ctx, dir); err != nil {
		return Header{}, err
	}
	var old Header
	var prior string
	file, st, err := s.openNote(e.ID)
	if err == nil {
		old, prior, err = readNote(file, st, e.ID)
		file.Close()
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		old.Revision = "new"
	case errors.Is(err, errInvalidNote):
		if e.Expected == "new" {
			return Header{}, fmt.Errorf("note %q exists; read its revision before repairing it", e.ID)
		}
		old.Revision, _, _, err = s.rawPage(ctx, e.ID, 0, 0)
		if err != nil {
			return Header{}, err
		}
	case err != nil:
		return Header{}, err
	case prior == text:
		return old, nil
	}
	if e.Expected != old.Revision {
		return Header{}, fmt.Errorf("note %q changed; read its current revision before updating", e.ID)
	}
	perm := os.FileMode(0o666)
	if st != nil {
		// Only the owner can open a replacement until it has the note's group.
		perm = st.Mode().Perm() & 0o700
	}
	f, err := createTemp(s.Root, perm)
	if err != nil {
		return Header{}, err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(text); err == nil && st != nil {
		err = f.Chmod(replacementMode(f, st))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return Header{}, err
	}
	if err := ctx.Err(); err != nil {
		return Header{}, err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.Root, e.ID+".md")); err != nil {
		return Header{}, err
	}
	if err = syncDir(dir); err != nil {
		log.Printf("notebook: saved %q, syncing its directory failed: %v", e.ID, err)
	}
	return header(e.ID, text)
}

func (s Store) List(ctx context.Context, query string, offset, limit int) (Page, error) {
	page := Page{Notes: []Header{}, Exhausted: true}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if offset < 0 || limit < 0 {
		return page, errors.New("offset and limit must be nonnegative")
	}
	if len(query) > 512 {
		return page, errors.New("search query exceeds 512 bytes")
	}
	if limit == 0 {
		limit = 12
	}
	limit = min(limit, maxPage)
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return page, nil
	}
	if err != nil {
		return page, err
	}
	terms := strings.Fields(strings.ToLower(query))
	type match struct {
		header Header
		score  int
	}
	var matches []match
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		id, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || !validID.MatchString(id) || !e.Type().IsRegular() {
			continue
		}
		h, body, err := s.Read(id)
		if err != nil {
			page.skipNote(id, err)
			continue
		}
		title := strings.ToLower(h.ID + " " + h.Title + " " + h.Summary)
		body = strings.ToLower(body)
		score := 0
		for _, term := range terms {
			switch {
			case strings.Contains(title, term):
				score += 3
			case strings.Contains(body, term):
				score++
			}
		}
		if len(terms) == 0 || score > 0 {
			matches = append(matches, match{h, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	for i := min(offset, len(matches)); i < len(matches); i++ {
		if len(page.Notes) == limit {
			page.Next = i
			page.Exhausted = false
			break
		}
		page.Notes = append(page.Notes, matches[i].header)
	}
	return page, nil
}

func (p *Page) skipNote(id string, err error) {
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	names, count := &p.Unreadable, &p.UnreadableCount
	if errors.Is(err, errInvalidNote) {
		names, count = &p.Invalid, &p.InvalidCount
	}
	(*count)++
	if len(*names) < maxPage {
		*names = append(*names, id)
	}
}
