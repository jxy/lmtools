package limitio

import (
	"bytes"
	"errors"
	"io"
	"math"
	"runtime"
	"strings"
	"testing"
)

// stalledBody is a slow-loris request body: it declares a large Content-Length,
// delivers a couple of bytes, and stops. What the server reserves on the
// strength of the declaration is memory the client never had to pay bandwidth
// for, so it is the number this package has to keep small.
type stalledBody struct {
	delivered bool
}

func (b *stalledBody) Read(p []byte) (int, error) {
	if b.delivered {
		return 0, io.EOF
	}
	b.delivered = true
	return copy(p, "{}"), nil
}

// dribbleReader hands out at most chunk bytes per call, so the read loop runs
// the same number of times it would against a real network connection instead
// of being handed the whole body at once.
type dribbleReader struct {
	data  string
	chunk int
}

func (r *dribbleReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, io.EOF
	}
	limit := r.chunk
	if limit > len(p) {
		limit = len(p)
	}
	n := copy(p[:limit], r.data)
	r.data = r.data[n:]
	return n, nil
}

// allocatedBytes reports how many bytes fn allocated in total, including
// buffers it later discarded. Peak usage is what matters here, and TotalAlloc
// bounds it from above.
func allocatedBytes(t *testing.T, fn func()) uint64 {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestReadLimitedWithSizeHintDoesNotReserveDeclaredSize(t *testing.T) {
	const declared = 32 * 1024 * 1024
	const maxSize = 512 * 1024 * 1024

	var got []byte
	allocated := allocatedBytes(t, func() {
		var err error
		got, err = ReadLimitedWithSizeHint(&stalledBody{}, maxSize, "request body", declared)
		if err != nil {
			t.Fatalf("ReadLimitedWithSizeHint() error = %v", err)
		}
	})
	if string(got) != "{}" {
		t.Fatalf("body = %q, want %q", got, "{}")
	}

	// The bound is far above what the read actually needs and far below the
	// declared size, so it fails only if the declaration is being reserved.
	const bound = 1024 * 1024
	if allocated > bound {
		t.Errorf("a body declaring %d bytes and sending 2 allocated %d bytes, want at most %d",
			declared, allocated, bound)
	}
}

func TestReadLimitedWithSizeHintScalesWithDeliveredBytes(t *testing.T) {
	// Two clients declare the same large size; one sends 64KB and the other
	// sends nothing. The cost of the read should follow what arrived, not what
	// was promised.
	const declared = 32 * 1024 * 1024
	const maxSize = 512 * 1024 * 1024

	small := allocatedBytes(t, func() {
		if _, err := ReadLimitedWithSizeHint(&stalledBody{}, maxSize, "request body", declared); err != nil {
			t.Fatalf("ReadLimitedWithSizeHint() error = %v", err)
		}
	})
	larger := allocatedBytes(t, func() {
		body := &dribbleReader{data: strings.Repeat("a", 4*1024*1024), chunk: 32 * 1024}
		if _, err := ReadLimitedWithSizeHint(body, maxSize, "request body", declared); err != nil {
			t.Fatalf("ReadLimitedWithSizeHint() error = %v", err)
		}
	})
	if larger <= small {
		t.Errorf("delivering 4MB allocated %d bytes and delivering 2 allocated %d; cost should track delivery", larger, small)
	}
}

func TestReadLimitedWithSizeHintKeepsKnownEmptyAllocationMinimal(t *testing.T) {
	got, err := ReadLimitedWithSizeHint(strings.NewReader(""), 512*1024*1024, "request body", 0)
	if err != nil {
		t.Fatalf("ReadLimitedWithSizeHint() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(body) = %d, want 0", len(got))
	}
	if cap(got) != 1 {
		t.Fatalf("cap(body) = %d, want one overflow sentinel byte", cap(got))
	}
}

func TestReadLimitedWithoutSizeHintStillReadsUnknownLength(t *testing.T) {
	const body = "body whose size was not declared"
	got, err := ReadLimited(strings.NewReader(body), 1024)
	if err != nil {
		t.Fatalf("ReadLimited() error = %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestReadLimitedWithSizeHintEndsAtTheDeclaredSize(t *testing.T) {
	// An honest body should finish in a buffer sized to it, plus the one
	// sentinel byte that proves it did not run over, rather than in the next
	// step up from a geometric climb.
	for _, size := range []int{2 * 1024, 200 * 1024, 1024 * 1024, 5 * 1024 * 1024} {
		body := &dribbleReader{data: strings.Repeat("b", size), chunk: 16 * 1024}
		got, err := ReadLimitedWithSizeHint(body, 512*1024*1024, "request body", int64(size))
		if err != nil {
			t.Fatalf("ReadLimitedWithSizeHint(%d) error = %v", size, err)
		}
		if len(got) != size {
			t.Fatalf("len(body) = %d, want %d", len(got), size)
		}
		if cap(got) != size+1 {
			t.Errorf("a body declaring and sending %d bytes ended in a buffer of %d, want %d",
				size, cap(got), size+1)
		}
	}
}

func TestReadLimitedWithSizeHintKeepsReadingPastAnUnderstatedSize(t *testing.T) {
	// A client that declares less than it sends must not be able to stall the
	// read at its own number.
	const size = 300 * 1024
	body := &dribbleReader{data: strings.Repeat("c", size), chunk: 4 * 1024}
	got, err := ReadLimitedWithSizeHint(body, 512*1024*1024, "request body", 10)
	if err != nil {
		t.Fatalf("ReadLimitedWithSizeHint() error = %v", err)
	}
	if len(got) != size {
		t.Fatalf("len(body) = %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, []byte(strings.Repeat("c", size))) {
		t.Error("body content does not match what was sent")
	}
}

func TestReadLimitedWithSizeHintClampsAnOverstatedSize(t *testing.T) {
	// A declaration above the limit is capped at the limit, so it cannot be used
	// to reserve more than the limit allows.
	got, err := ReadLimitedWithSizeHint(strings.NewReader("hello"), 16, "request body", 1<<40)
	if err != nil {
		t.Fatalf("ReadLimitedWithSizeHint() error = %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("body = %q, want %q", got, "hello")
	}
	if int64(cap(got)) > 17 {
		t.Errorf("cap(body) = %d, want at most the limit plus the sentinel byte (17)", cap(got))
	}
}

func TestReadLimitedAcceptsABodyExactlyAtTheLimit(t *testing.T) {
	const maxSize = 4096
	for _, hint := range []int64{-1, 0, maxSize} {
		body := &dribbleReader{data: strings.Repeat("d", maxSize), chunk: 512}
		got, err := ReadLimitedWithSizeHint(body, maxSize, "request body", hint)
		if err != nil {
			t.Fatalf("ReadLimitedWithSizeHint(hint=%d) error = %v, want the body", hint, err)
		}
		if len(got) != maxSize {
			t.Fatalf("len(body) = %d, want %d", len(got), maxSize)
		}
	}
}

func TestReadLimitedRejectsABodyOverTheLimit(t *testing.T) {
	const maxSize = 4096
	for _, hint := range []int64{-1, 0, maxSize, maxSize + 1} {
		body := &dribbleReader{data: strings.Repeat("e", maxSize+1), chunk: 512}
		_, err := ReadLimitedWithSizeHint(body, maxSize, "request body", hint)
		var sizeErr *SizeLimitError
		if !errors.As(err, &sizeErr) {
			t.Fatalf("ReadLimitedWithSizeHint(hint=%d) error = %v, want a SizeLimitError", hint, err)
		}
		if sizeErr.MaxSize != maxSize || sizeErr.Kind != "request body" {
			t.Errorf("error = %+v, want kind %q and max %d", sizeErr, "request body", maxSize)
		}
	}
}

func TestReadLimitedStopsReadingOnceOverTheLimit(t *testing.T) {
	// The read must not drain an unbounded body to discover it is too large: an
	// over-limit verdict costs one byte past the limit, not the whole stream.
	const maxSize = 64 * 1024
	body := &dribbleReader{data: strings.Repeat("f", 8*1024*1024), chunk: 16 * 1024}
	_, err := ReadLimitedWithSizeHint(body, maxSize, "request body", 8*1024*1024)
	var sizeErr *SizeLimitError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("error = %v, want a SizeLimitError", err)
	}
	if consumed := 8*1024*1024 - len(body.data); consumed > 2*maxSize {
		t.Errorf("read %d bytes to reject a %d-byte limit, want to stop just past it", consumed, maxSize)
	}
}

func TestReadLimitedReportsReaderFailures(t *testing.T) {
	failure := errors.New("connection reset")
	_, err := ReadLimited(io.MultiReader(strings.NewReader("partial"), errReader{failure}), 4096)
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want it to wrap %v", err, failure)
	}
	var sizeErr *SizeLimitError
	if errors.As(err, &sizeErr) {
		t.Error("a transport failure was reported as a size limit violation")
	}
}

func TestReadLimitedRejectsNegativeLimit(t *testing.T) {
	if _, err := ReadLimitedWithSizeHint(strings.NewReader(""), -1, "request body", 0); err == nil {
		t.Fatal("ReadLimitedWithSizeHint() error = nil, want invalid negative limit")
	}
}

func TestReadLimitedArithmeticDoesNotOverflow(t *testing.T) {
	if _, err := ReadLimitedWithSizeHint(strings.NewReader(""), math.MaxInt64, "request body", math.MaxInt64); err != nil {
		t.Fatalf("ReadLimitedWithSizeHint(MaxInt64) error = %v", err)
	}
	if got := nextReadCapacity(math.MaxInt64/2, math.MaxInt64, math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("nextReadCapacity() = %d, want %d", got, int64(math.MaxInt64))
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
