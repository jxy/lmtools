package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

// At DEBUG a response body is logged as it came, read by read: a stream on
// every stream path, with the event lines, comments, and carriage returns
// that the stream_chat_output file leaves out; an answer not streamed; an
// error; and a body whose connection broke, as far as it was read, though
// the reader then returns none of it. The body still reaches its parser
// unchanged, and below DEBUG none of it is logged.
func TestResponseBodyIsLoggedAsItCameAtDebug(t *testing.T) {
	chat := newStreamTestRequestConfig("openai", true, false, "")
	unstreamed := newStreamTestRequestConfig("openai", false, false, "")
	responses := newStreamTestRequestConfig("openai", true, false, "")
	responses.OpenAIResponses = true
	legacyArgo := newStreamTestRequestConfig("argo", true, false, "")
	legacyArgo.ArgoLegacy = true
	long := strings.Repeat("x", 300<<10)
	for _, tt := range []struct {
		name        string
		cfg         RequestOptions
		status      int // 200 when zero
		contentType string
		body        string
		readErr     error  // what the body fails with once read, if not the end
		text        string // the answer, when there is one
		err         string // what the error says, when there is one
		read        func(io.Reader) io.Reader
		longerThan  int // the length one logged read must pass
	}{
		{
			name:        "chat completions a byte at a time",
			cfg:         chat,
			contentType: "text/event-stream",
			body: ": keepalive\r\n\r\n" +
				`data: {"choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}` + "\r\n\r\n" +
				`data: {"choices":[{"index":0,` + "\r\n" + `data: "delta":{"content":"lo"},"finish_reason":null}]}` + "\r\n\r\n" +
				"data: [DONE]\r\n\r\n",
			text: "Hello",
			read: iotest.OneByteReader,
		},
		{
			name:        "anthropic a byte at a time",
			cfg:         newStreamTestRequestConfig("anthropic", true, false, ""),
			contentType: "text/event-stream",
			body: "event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-test"}}` + "\n\n" +
				"event: ping\n" + `data: {"type":"ping"}` + "\n\n" +
				"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n" +
				"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
				"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n",
			text: "Hello",
			read: iotest.OneByteReader,
		},
		{
			name:        "responses a byte at a time",
			cfg:         responses,
			contentType: "text/event-stream",
			body: "event: response.created\n" + `data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}` + "\n\n" +
				"event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"Hello"}` + "\n\n" +
				"event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Hello"}]}]}}` + "\n\n",
			text: "Hello",
			read: iotest.OneByteReader,
		},
		{
			name:        "legacy argo text a byte at a time",
			cfg:         legacyArgo,
			contentType: "text/plain; charset=utf-8",
			body:        "Hello,\r\nworld",
			text:        "Hello,\r\nworld",
			read:        iotest.OneByteReader,
		},
		{
			name:        "a long event in large reads",
			cfg:         chat,
			contentType: "text/event-stream",
			body:        `data: {"choices":[{"index":0,"delta":{"content":"` + long + `"},"finish_reason":null}]}` + "\n\ndata: [DONE]\n\n",
			text:        long,
			read:        func(r io.Reader) io.Reader { return r },
			longerThan:  64 << 10,
		},
		{
			name:        "a stream cut off a byte at a time",
			cfg:         chat,
			contentType: "text/event-stream",
			body:        ": keepalive\r\n\r\n" + `data: {"choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}` + "\r\n\r\n",
			readErr:     io.ErrUnexpectedEOF,
			err:         "read response stream: unexpected EOF",
			read:        iotest.OneByteReader,
		},
		{
			name:        "an answer not streamed a byte at a time",
			cfg:         unstreamed,
			contentType: "application/json",
			body:        `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`,
			text:        "Hello",
			read:        iotest.OneByteReader,
		},
		{
			name:        "an answer not streamed cut off",
			cfg:         unstreamed,
			contentType: "application/json",
			body:        `{"id":"c1","object":"chat.completion","choices":[{"index":0,`,
			readErr:     io.ErrUnexpectedEOF,
			err:         "failed to read response body: read API response body: unexpected EOF",
			read:        iotest.OneByteReader,
		},
		{
			name:        "an error a byte at a time",
			cfg:         chat,
			status:      http.StatusServiceUnavailable,
			contentType: "application/json",
			body:        `{"error":{"message":"busy","type":"server_error","request_id":"req_1"}}`,
			err:         "HTTP 503",
			read:        iotest.OneByteReader,
		},
		{
			name:        "an error cut off",
			cfg:         chat,
			status:      http.StatusServiceUnavailable,
			contentType: "application/json",
			body:        `{"error":{"message":"bu`,
			readErr:     io.ErrUnexpectedEOF,
			err:         "HTTP 503: failed to read error response",
			read:        iotest.OneByteReader,
		},
	} {
		for _, debug := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, debug %t", tt.name, debug), func(t *testing.T) {
				logger := &MockLogger{debugEnabled: debug, logDir: t.TempDir()}
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				body := io.Reader(strings.NewReader(tt.body))
				if tt.readErr != nil {
					body = io.MultiReader(body, iotest.ErrReader(tt.readErr))
				}
				resp := &http.Response{
					StatusCode: status,
					Header:     http.Header{"Content-Type": []string{tt.contentType}},
					Body:       io.NopCloser(tt.read(body)),
				}
				response, err := HandleResponseWithOptions(context.Background(), tt.cfg, resp, logger, &MockNotifier{}, ResponseParseOptions{Output: textResponseOutput{out: &strings.Builder{}}})
				if tt.err == "" && (err != nil || response.Text != tt.text) {
					t.Fatalf("text = %.80q, err = %v; want %.80q", response.Text, err, tt.text)
				}
				if tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
					t.Fatalf("err = %v, want it to say %q", err, tt.err)
				}
				var logged strings.Builder
				longest := 0
				for _, message := range logger.debugMessages {
					if piece, ok := strings.CutPrefix(message, "WIRE BACKEND RESPONSE BODY:\n"); ok {
						logged.WriteString(piece)
						longest = max(longest, len(piece))
					}
				}
				want := tt.body
				if !debug {
					want = ""
				}
				if logged.String() != want {
					t.Fatalf("logged %d bytes, want %d: %.300q", logged.Len(), len(want), logged.String())
				}
				if debug && longest <= tt.longerThan {
					t.Fatalf("longest logged read = %d bytes, want one over %d", longest, tt.longerThan)
				}
			})
		}
	}
}
