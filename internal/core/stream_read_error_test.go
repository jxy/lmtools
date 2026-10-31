package core

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// errorAfterReader reads data, then fails with err.
type errorAfterReader struct {
	data string
	err  error
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// streamReaders read a response stream the ways lmc does: as events, and
// as legacy Argo's plain text.
var streamReaders = map[string]func(ctx context.Context, body io.Reader, logFile *os.File) error{
	"events": func(ctx context.Context, body io.Reader, logFile *os.File) error {
		var collected []string
		_, _, err := RunStream(ctx, io.NopCloser(body), logFile, textResponseOutput{out: &strings.Builder{}}, &testNotifier{}, &collectingStreamState{collected: &collected}, "test")
		return err
	},
	"legacy argo text": func(ctx context.Context, body io.Reader, logFile *os.File) error {
		_, err := handleArgoStream(ctx, io.NopCloser(body), logFile, textResponseOutput{out: &strings.Builder{}})
		return err
	},
}

// A stream whose connection breaks partway says so: the error names the
// stream it came from, and the stream's log ends with it. On 2026-10-30 an
// Argo stream closed before any event, and the REPL printed only "unexpected
// EOF" over an empty log.
func TestStreamReadErrorIsNamedAndLogged(t *testing.T) {
	for name, read := range streamReaders {
		t.Run(name, func(t *testing.T) {
			logFile, err := os.CreateTemp(t.TempDir(), "stream-*.log")
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()
			err = read(context.Background(), &errorAfterReader{data: "data: {\"text\": \"partial\"}\n\n", err: io.ErrUnexpectedEOF}, logFile)
			if err == nil || err.Error() != "read response stream: unexpected EOF" || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("err = %v, want read response stream: unexpected EOF", err)
			}
			logged, _ := os.ReadFile(logFile.Name())
			if !strings.HasSuffix(string(logged), "# read error: unexpected EOF\n") {
				t.Fatalf("log = %q, want it to end with the read error", logged)
			}
		})
	}
}

// A read the caller cancelled ends with the cancellation as it came.
func TestStreamReadCancelledIsReturnedAsItIs(t *testing.T) {
	for name, read := range streamReaders {
		t.Run(name, func(t *testing.T) {
			logFile, err := os.CreateTemp(t.TempDir(), "stream-*.log")
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := read(ctx, &errorAfterReader{err: context.Canceled}, logFile); err != context.Canceled {
				t.Fatalf("err = %v, want context.Canceled as it is", err)
			}
		})
	}
}
