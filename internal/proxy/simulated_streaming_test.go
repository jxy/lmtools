package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmitSimulatedTextChunksPreservesUTF8(t *testing.T) {
	text := strings.Repeat("alpha ", 4) + "Hello, \u4e16\u754c \U0001F600. Done."
	var chunks []string
	err := emitSimulatedTextChunks(context.Background(), text, func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("emitSimulatedTextChunks() error = %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunks = %#v, want multiple chunks", chunks)
	}
	if got := strings.Join(chunks, ""); got != text {
		t.Fatalf("joined chunks = %q, want %q", got, text)
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i, chunk)
		}
	}
}

func TestEmitSimulatedToolInputChunksPreservesJSONEscapes(t *testing.T) {
	input := map[string]interface{}{
		"command": strings.Repeat(`echo "\u1234" && `, 8) + "done",
		"note":    "line1\nline2\tquoted",
	}
	want, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal(input) error = %v", err)
	}
	var chunks []string
	err = emitSimulatedToolInputChunks(context.Background(), input, func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("emitSimulatedToolInputChunks() error = %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunks = %#v, want multiple chunks", chunks)
	}
	joined := strings.Join(chunks, "")
	if joined != string(want) {
		t.Fatalf("joined chunks = %q, want %q", joined, string(want))
	}
	if !json.Valid([]byte(joined)) {
		t.Fatalf("joined chunks are not valid JSON: %q", joined)
	}
	for i, chunk := range chunks {
		if strings.HasSuffix(chunk, `\`) || strings.HasSuffix(chunk, `\u`) {
			t.Fatalf("chunk %d ends mid escape: %q", i, chunk)
		}
	}
}
