package proxy

// Terminal Stream Events
//
// Every client stream shape has a marker that says "this turn is over":
// response.completed/failed/incomplete for Responses, message_stop or error for
// Anthropic Messages, and [DONE] for OpenAI chat completions. An upstream that
// is truncated, killed by a gateway, or cut off by an SSE line over
// SSEMaxLineBytes must never leave the client waiting on a marker that will not
// arrive; clients report that as a dropped connection rather than a failure.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func sseRoundTripResponse(payload string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}
}

func newArgoStreamTestServer(t *testing.T, upstream string) *Server {
	t.Helper()
	return newArgoStreamBodyTestServer(t, io.NopCloser(strings.NewReader(upstream)))
}

func newArgoStreamBodyTestServer(t *testing.T, body io.ReadCloser) *Server {
	t.Helper()
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}, nil
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(t, config, client)
}

type cancelAfterWriteRecorder struct {
	*flushableRecorder
	cancel context.CancelFunc
}

func (r *cancelAfterWriteRecorder) Write(p []byte) (int, error) {
	n, err := r.flushableRecorder.Write(p)
	r.cancel()
	return n, err
}

func (r *cancelAfterWriteRecorder) WriteString(s string) (int, error) {
	return r.Write([]byte(s))
}

type cancelBeforeReadReader struct {
	cancel context.CancelFunc
}

func (r *cancelBeforeReadReader) Read([]byte) (int, error) {
	r.cancel()
	return 0, io.EOF
}

type eofResponseWriter struct {
	header http.Header
}

func (w *eofResponseWriter) Header() http.Header       { return w.header }
func (w *eofResponseWriter) Write([]byte) (int, error) { return 0, io.EOF }
func (w *eofResponseWriter) WriteHeader(int)           {}
func (w *eofResponseWriter) Flush()                    {}

type failOnceResponseWriter struct {
	header http.Header
	body   strings.Builder
	failAt int
	writes int
}

func (w *failOnceResponseWriter) Header() http.Header { return w.header }
func (w *failOnceResponseWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, io.ErrClosedPipe
	}
	return w.body.Write(p)
}
func (w *failOnceResponseWriter) WriteHeader(int) {}
func (w *failOnceResponseWriter) Flush()          {}

func TestOpenAIStreamWriterAdvancesOnlyAfterSuccessfulWrites(t *testing.T) {
	t.Run("retry failed done marker", func(t *testing.T) {
		response := &failOnceResponseWriter{header: make(http.Header), failAt: 2}
		writer, err := NewOpenAIStreamWriter(response, "client-model", context.Background())
		if err != nil {
			t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
		}

		if err := writer.WriteFinish("stop", nil); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("WriteFinish() error = %v, want io.ErrClosedPipe", err)
		}
		if err := writer.EnsureTerminated(nil); err != nil {
			t.Fatalf("EnsureTerminated() error = %v", err)
		}

		body := response.body.String()
		if got := strings.Count(body, `"finish_reason":"stop"`); got != 1 {
			t.Fatalf("client stream = %s, want one finish chunk, got %d", body, got)
		}
		if got := strings.Count(body, OpenAIDoneMarker); got != 1 {
			t.Fatalf("client stream = %s, want one retried %s, got %d", body, OpenAIDoneMarker, got)
		}
	})

	t.Run("failed finish remains unfinished", func(t *testing.T) {
		response := &failOnceResponseWriter{header: make(http.Header), failAt: 1}
		writer, err := NewOpenAIStreamWriter(response, "client-model", context.Background())
		if err != nil {
			t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
		}

		if err := writer.WriteFinish("stop", nil); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("WriteFinish() error = %v, want io.ErrClosedPipe", err)
		}
		if err := writer.EnsureTerminated(io.ErrUnexpectedEOF); err != nil {
			t.Fatalf("EnsureTerminated() error = %v", err)
		}

		body := response.body.String()
		if strings.Contains(body, `"finish_reason":"stop"`) {
			t.Fatalf("client stream = %s, want no finish chunk that failed to write", body)
		}
		if !strings.Contains(body, `"type":"server_error"`) {
			t.Fatalf("client stream = %s, want the unfinished stream reported as an error", body)
		}
		if got := strings.Count(body, OpenAIDoneMarker); got != 1 {
			t.Fatalf("client stream = %s, want one %s, got %d", body, OpenAIDoneMarker, got)
		}
	})
}

// Internal stop signals end record consumption successfully. A real io.EOF
// from a downstream writer is data-delivery failure and must still escape the
// relay boundary.
func TestSSERecordStopDoesNotSwallowWriterEOF(t *testing.T) {
	t.Run("Responses relay", func(t *testing.T) {
		relay := responsesSSERelay{ctx: context.Background(), w: &eofResponseWriter{header: make(http.Header)}}
		err := relay.run(strings.NewReader("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
		if !errors.Is(err, io.EOF) {
			t.Fatalf("run() error = %v, want writer io.EOF", err)
		}
	})

	t.Run("Chat Completions relay", func(t *testing.T) {
		w := &eofResponseWriter{header: make(http.Header)}
		err := forwardOpenAICompatibleSSEWithStops(
			context.Background(),
			w,
			strings.NewReader("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"),
			"client-model",
			"OpenAI",
			nil,
			1,
		)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v, want writer io.EOF", err)
		}
	})

	t.Run("Anthropic relay", func(t *testing.T) {
		w := &eofResponseWriter{header: make(http.Header)}
		handler, err := NewAnthropicStreamHandler(w, "client-model", context.Background())
		if err != nil {
			t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
		}
		server := &Server{}
		err = server.parseAnthropicStream(strings.NewReader("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"), handler)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("parseAnthropicStream() error = %v, want writer io.EOF", err)
		}
	})
}

// A downstream cancellation is already the end of the client stream. None of
// the four terminal owners should try to synthesize an error or marker through
// the canceled context; doing so turns ordinary disconnect cleanup into a
// second context.Canceled error and a false ERROR log at the caller.
func TestTerminalSynthesisStopsAfterDownstreamCancellation(t *testing.T) {
	t.Run("Anthropic converted stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := &cancelAfterWriteRecorder{flushableRecorder: newFlushableRecorder(), cancel: cancel}
		handler, err := NewAnthropicStreamHandler(w, "client-model", ctx)
		if err != nil {
			t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
		}
		if err := handler.SendMessageStart(); err != nil {
			t.Fatalf("SendMessageStart() error = %v", err)
		}
		before := w.Body.String()
		if err := handler.EnsureTerminated(io.ErrUnexpectedEOF); err != nil {
			t.Fatalf("EnsureTerminated() error = %v, want cancellation to skip synthesis", err)
		}
		if got := w.Body.String(); got != before {
			t.Fatalf("client stream changed after cancellation:\nbefore: %s\nafter: %s", before, got)
		}
	})

	t.Run("OpenAI converted stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := &cancelAfterWriteRecorder{flushableRecorder: newFlushableRecorder(), cancel: cancel}
		writer, err := NewOpenAIStreamWriter(w, "client-model", ctx)
		if err != nil {
			t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
		}
		if err := writer.WriteInitialAssistantTextDelta(); err != nil {
			t.Fatalf("WriteInitialAssistantTextDelta() error = %v", err)
		}
		before := w.Body.String()
		if err := writer.EnsureTerminated(io.ErrUnexpectedEOF); err != nil {
			t.Fatalf("EnsureTerminated() error = %v, want cancellation to skip synthesis", err)
		}
		if got := w.Body.String(); got != before {
			t.Fatalf("client stream changed after cancellation:\nbefore: %s\nafter: %s", before, got)
		}
	})

	t.Run("OpenAI direct stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := &cancelAfterWriteRecorder{flushableRecorder: newFlushableRecorder(), cancel: cancel}
		upstream := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"
		if err := forwardOpenAICompatibleSSEWithStops(ctx, w, strings.NewReader(upstream), "client-model", "OpenAI", nil, 1); err != nil {
			t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v, want cancellation to skip synthesis", err)
		}
		body := w.Body.String()
		if !strings.Contains(body, "partial") {
			t.Fatalf("client stream = %s, want the event written before cancellation", body)
		}
		if strings.Contains(body, "server_error") || strings.Contains(body, OpenAIDoneMarker) {
			t.Fatalf("client stream = %s, want no terminal synthesis after cancellation", body)
		}
	})

	t.Run("Responses direct stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := &cancelAfterWriteRecorder{flushableRecorder: newFlushableRecorder(), cancel: cancel}
		upstream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"client-model\"}}\n\n"
		if wroteAny := relayResponsesSSE(ctx, w, strings.NewReader(upstream), "client-model", nil, "OpenAI"); !wroteAny {
			t.Fatal("relayResponsesSSE() wrote no event before cancellation")
		}
		body := w.Body.String()
		if !strings.Contains(body, "response.created") {
			t.Fatalf("client stream = %s, want the event written before cancellation", body)
		}
		if strings.Contains(body, "response.failed") {
			t.Fatalf("client stream = %s, want no synthetic failure after cancellation", body)
		}
	})

	t.Run("Responses direct stream before first event", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		server := newArgoStreamBodyTestServer(t, io.NopCloser(&cancelBeforeReadReader{cancel: cancel}))
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		response := newFlushableRecorder()
		target := responsesPassthroughTarget{
			URL:      "http://argo.local/argoapi/v1/responses",
			Provider: constants.ProviderArgo,
		}
		responsesRequest := &OpenAIResponsesRequest{Model: "gpt-test", Stream: true}

		server.forwardOpenAIResponsesStreamDirectly(
			response,
			request,
			target,
			responsesRequest,
			[]byte(`{"model":"gpt-test","stream":true}`),
			"gpt-test",
		)

		if got := response.Body.String(); got != "" {
			t.Fatalf("client stream changed after cancellation before its first event: %s", got)
		}
	})
}

func TestResponsesPassthroughStreamTerminatesTruncatedUpstream(t *testing.T) {
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":123,\"status\":\"in_progress\",\"model\":\"gpt-test\"}}\n\n"

	cases := []struct {
		name     string
		upstream string
	}{
		{
			name:     "upstream stops early",
			upstream: created,
		},
		{
			// A single SSE line over SSEMaxLineBytes aborts the scan. The client
			// still has to be told the turn ended.
			name:     "sse line over the scanner limit",
			upstream: created + "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("z", SSEMaxLineBytes+1024) + "\"}\n\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
				return sseRoundTripResponse(tc.upstream), nil
			})
			status, body := postRawResponses(t, server, `{"model":"gpt-test","stream":true,"input":"say hi"}`)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", status, body)
			}
			if !strings.Contains(body, "event: response.failed") {
				t.Fatalf("client stream = %s, want a synthetic response.failed", body)
			}
			// The synthetic event has to carry the response identity the client
			// already saw, or the client cannot match it to its in-flight turn.
			if !strings.Contains(body, `"id":"resp_1"`) {
				t.Fatalf("client stream = %s, want the upstream response id in the terminal event", body)
			}
			if !strings.Contains(body, `"status":"failed"`) {
				t.Fatalf("client stream = %s, want a failed status", body)
			}
		})
	}
}

// [DONE] ends decoding for clients that share the Chat Completions SSE
// sentinel behavior. If the upstream sends it without a Responses terminal
// event, putting the synthetic response.failed behind it makes that failure
// unreachable even though its bytes are present in the HTTP body.
func TestResponsesPassthroughStreamWritesFailureBeforeDone(t *testing.T) {
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":123,\"status\":\"in_progress\",\"model\":\"gpt-test\"}}\n\n"

	for _, tc := range []struct {
		name     string
		upstream string
	}{
		{name: "after response.created", upstream: created + "data: [DONE]\n\n"},
		{name: "as the first record", upstream: "data: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
				return sseRoundTripResponse(tc.upstream), nil
			})
			status, body := postRawResponses(t, server, `{"model":"gpt-test","stream":true,"input":"say hi"}`)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", status, body)
			}

			failure := strings.Index(body, "event: response.failed")
			done := strings.Index(body, "data: [DONE]")
			if failure < 0 || done < 0 {
				t.Fatalf("client stream = %s, want response.failed and [DONE]", body)
			}
			if failure > done {
				t.Fatalf("client stream = %s, want response.failed before [DONE]", body)
			}
			if strings.Count(body, "event: response.failed") != 1 || strings.Count(body, "data: [DONE]") != 1 {
				t.Fatalf("client stream = %s, want exactly one response.failed and one [DONE]", body)
			}
			if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
				t.Fatalf("client stream = %s, want [DONE] last", body)
			}
		})
	}
}

// [DONE] is a client-visible terminal marker, so the relay must stop reading
// immediately after forwarding it even when the provider leaves the HTTP body
// open. Continuing to read can pin the handler indefinitely or expose records
// that appeared after the client was told the stream ended.
func TestResponsesPassthroughStopsReadingAndClosesBodyAfterDone(t *testing.T) {
	body := newBlockingAfterPayloadStreamBody("data: [DONE]\n\n")
	server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true,"input":"say hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := newFlushableRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
		// Expected: [DONE] stopped the scanner.
	case <-body.secondRead:
		body.releaseForTest()
		<-done
		t.Fatal("relay read upstream again after [DONE]")
	case <-time.After(2 * time.Second):
		body.releaseForTest()
		<-done
		t.Fatal("relay remained blocked on the open upstream body after [DONE]")
	}

	select {
	case <-body.closed:
	default:
		t.Fatal("upstream response body was not closed when the Responses relay returned")
	}
	stream := w.Body.String()
	failure := strings.Index(stream, "event: response.failed")
	doneMarker := strings.Index(stream, "data: "+OpenAIDoneMarker)
	if failure < 0 || doneMarker < 0 || failure > doneMarker {
		t.Fatalf("client stream = %s, want response.failed before %s", stream, OpenAIDoneMarker)
	}
}

// On the mapped path every event the client sees carries the client's own model
// name. The synthetic failure event is an event the client sees, so it has to
// agree; quoting the upstream name there would expose the mapping.
func TestResponsesStreamFailureKeepsClientModel(t *testing.T) {
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":123,\"status\":\"in_progress\",\"model\":\"gpt-upstream\"}}\n\n"

	server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
		return sseRoundTripResponse(created), nil
	}, "^gpt-client$=gpt-upstream")
	status, body := postRawResponses(t, server, `{"model":"gpt-client","stream":true,"input":"say hi"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("client stream = %s, want a synthetic response.failed", body)
	}
	failed := body[strings.Index(body, "event: response.failed"):]
	if !strings.Contains(failed, `"model":"gpt-client"`) {
		t.Fatalf("terminal event = %s, want the client's model name", failed)
	}
	if strings.Contains(failed, "gpt-upstream") {
		t.Fatalf("terminal event = %s, want no upstream model name", failed)
	}
}

// The request model is the only client-visible model available when an
// upstream ends before its first Responses event. A synthetic failure still
// has to carry it: [DONE] and heartbeat records do not populate event metadata.
func TestResponsesStreamFailureUsesClientModelBeforeFirstEvent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream string
	}{
		{name: "DONE is the first record", upstream: "data: [DONE]\n\n"},
		{name: "heartbeats are the only records", upstream: ": keep-alive\n\n: still-alive\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
				return sseRoundTripResponse(tc.upstream), nil
			}, "^gpt-client$=gpt-upstream")
			status, body := postRawResponses(t, server, `{"model":"gpt-client","stream":true,"input":"say hi"}`)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", status, body)
			}

			failure := syntheticResponsesFailure(t, body)
			response, ok := failure["response"].(map[string]interface{})
			if !ok {
				t.Fatalf("response.failed payload = %#v, want a response object", failure)
			}
			if got := response["model"]; got != "gpt-client" {
				t.Fatalf("synthetic response model = %#v, want the client-visible model %q", got, "gpt-client")
			}
		})
	}
}

// syntheticResponsesFailure decodes the data payload of the relay's synthetic
// terminal event.
func syntheticResponsesFailure(t *testing.T, stream string) map[string]interface{} {
	t.Helper()
	const marker = "event: response.failed\n"
	index := strings.Index(stream, marker)
	if index < 0 {
		t.Fatalf("client stream = %s, want a synthetic response.failed", stream)
	}
	var payload strings.Builder
	for _, line := range strings.Split(stream[index+len(marker):], "\n") {
		if line == "" {
			break
		}
		data, ok := sseFieldValue(line, "data")
		if !ok {
			t.Fatalf("terminal event line = %q, want a data line", line)
		}
		payload.WriteString(data)
	}
	decoder := json.NewDecoder(strings.NewReader(payload.String()))
	// Sequence numbers are compared exactly, and float64 cannot hold the ones at
	// the top of the range.
	decoder.UseNumber()
	var decoded map[string]interface{}
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decoding %s error = %v", payload.String(), err)
	}
	return decoded
}

// A client watching sequence_number for gaps reads a number it never saw as an
// event it never received, so the synthetic terminal event has to land on the
// next number in the series the upstream was actually using. Counting relayed
// records instead gets that wrong twice over: comments and [DONE] are not
// events and inflate it, and a stream that starts numbering anywhere but zero
// is answered in the wrong series entirely.
func TestResponsesStreamFailureContinuesUpstreamSequence(t *testing.T) {
	created := func(fields string) string {
		return "event: response.created\ndata: {\"type\":\"response.created\"," + fields +
			"\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":123,\"status\":\"in_progress\",\"model\":\"gpt-test\"}}\n\n"
	}
	const heartbeat = ": keep-alive\n\n"

	cases := []struct {
		name     string
		upstream string
		want     string
	}{
		{
			// Nothing here but the one event carries a sequence number, and the
			// client saw number 0.
			name:     "heartbeats and DONE do not advance the series",
			upstream: heartbeat + created(`"sequence_number":0,`) + heartbeat + "data: [DONE]\n\n",
			want:     "1",
		},
		{
			name:     "resumed stream keeps the upstream numbering",
			upstream: created(`"sequence_number":41,`),
			want:     "42",
		},
		{
			// No numbering to follow, so the events the client saw are counted.
			name: "upstream without sequence numbers falls back to counting events",
			upstream: created("") +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
			want: "2",
		},
		{
			// There is no number after this one. Saturating keeps the terminal
			// event level with the last event the client saw; wrapping would put
			// it before the whole stream, and keeping the count from before it
			// would put it before the event that carried it.
			name: "a series at the top of the range saturates",
			upstream: created(`"sequence_number":0,`) +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":9223372036854775807,\"delta\":\"hi\"}\n\n",
			want: "9223372036854775807",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
				return sseRoundTripResponse(tc.upstream), nil
			})
			status, body := postRawResponses(t, server, `{"model":"gpt-test","stream":true,"input":"say hi"}`)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", status, body)
			}
			got, ok := syntheticResponsesFailure(t, body)["sequence_number"].(json.Number)
			if !ok || got.String() != tc.want {
				t.Fatalf("terminal sequence_number = %v, want %s; stream = %s", got, tc.want, body)
			}
		})
	}
}

func TestResponsesPassthroughKeepsUpstreamTerminalEvent(t *testing.T) {
	server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
		return sseRoundTripResponse(responsesCompletedEvent("resp_ok", "gpt-test")), nil
	})
	status, body := postRawResponses(t, server, `{"model":"gpt-test","stream":true,"input":"say hi"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	if strings.Contains(body, "response.failed") {
		t.Fatalf("client stream = %s, want no synthetic failure after a clean completion", body)
	}
	if strings.Count(body, "event: response.completed") != 1 {
		t.Fatalf("client stream = %s, want exactly one response.completed", body)
	}
}

// Every Responses terminal event ends the relay, even if the provider leaves
// the HTTP body open. Reading beyond one can block the handler and can expose
// records that the provider sent after declaring the response finished.
func TestResponsesPassthroughStopsAfterTerminalResponseEvent(t *testing.T) {
	for _, eventType := range []string{"response.completed", "response.failed", "response.incomplete"} {
		t.Run(eventType, func(t *testing.T) {
			status := strings.TrimPrefix(eventType, "response.")
			upstream := strings.Join([]string{
				"event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-test\"}}\n\n",
				fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"sequence_number\":1,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":%q,\"model\":\"gpt-test\"}}\n\n", eventType, eventType, status),
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"delta\":\"kept-going\"}\n\n",
			}, "")
			body := newBlockingAfterPayloadStreamBody(upstream)
			server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       body,
				}, nil
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true,"input":"say hi"}`))
			req.Header.Set("Content-Type", "application/json")
			w := newFlushableRecorder()
			done := make(chan struct{})
			go func() {
				server.ServeHTTP(w, req)
				close(done)
			}()

			select {
			case <-done:
				// Expected: the terminal callback stopped the scanner.
			case <-body.secondRead:
				body.releaseForTest()
				<-done
				t.Fatal("relay read upstream again after its terminal Responses event")
			case <-time.After(2 * time.Second):
				body.releaseForTest()
				<-done
				t.Fatal("relay remained blocked on the open upstream body after its terminal Responses event")
			}

			select {
			case <-body.closed:
			default:
				t.Fatal("upstream response body was not closed when the Responses relay returned")
			}
			stream := w.Body.String()
			if got := strings.Count(stream, `"type":"`+eventType+`"`); got != 1 {
				t.Fatalf("client stream = %s, want terminal event %s exactly once, got %d", stream, eventType, got)
			}
			if strings.Contains(stream, "kept-going") {
				t.Fatalf("client stream = %s, want no records relayed after %s", stream, eventType)
			}
		})
	}
}

// A provider-owned Responses error is itself the terminal event. The relay
// must return after forwarding it even when the upstream leaves the HTTP body
// open; otherwise it can pass through later deltas and add a synthetic
// response.failed when that body eventually closes.
func TestResponsesPassthroughStopsAfterUpstreamErrorEvent(t *testing.T) {
	upstream := strings.Join([]string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-test\"}}\n\n",
		// Some Responses-compatible backends put the event type only in the
		// SSE field. The JSON body is still the provider's error and must end
		// the stream without a synthetic response.failed after it.
		"event: error\ndata: {\"error\":{\"type\":\"invalid_request_error\",\"message\":\"backend failed\"}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"delta\":\"kept-going\"}\n\n",
	}, "")
	body := newBlockingAfterPayloadStreamBody(upstream)
	server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true,"input":"say hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := newFlushableRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
		// Expected: the terminal callback stopped the scanner.
	case <-body.secondRead:
		body.releaseForTest()
		<-done
		t.Fatal("relay read upstream again after its terminal Responses error event")
	case <-time.After(2 * time.Second):
		body.releaseForTest()
		<-done
		t.Fatal("relay remained blocked on the open upstream body after its terminal Responses error event")
	}

	select {
	case <-body.closed:
	default:
		t.Fatal("upstream response body was not closed when the Responses relay returned")
	}
	stream := w.Body.String()
	if got := strings.Count(stream, "backend failed"); got != 1 {
		t.Fatalf("client stream = %s, want the provider error exactly once, got %d", stream, got)
	}
	if got := strings.Count(stream, "event: error"); got != 1 {
		t.Fatalf("client stream = %s, want exactly one provider error event, got %d", stream, got)
	}
	if strings.Contains(stream, "response.failed") {
		t.Fatalf("client stream = %s, want no synthetic response.failed after the provider error", stream)
	}
	if strings.Contains(stream, "kept-going") {
		t.Fatalf("client stream = %s, want no records relayed after the provider error", stream)
	}
}

func TestAnthropicStreamTerminatesTruncatedUpstream(t *testing.T) {
	upstream := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claudeopus4\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n",
	}, "")

	server := newArgoStreamTestServer(t, upstream)
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", map[string]interface{}{
		"model":      "claudeopus4",
		"stream":     true,
		"max_tokens": 64,
		"messages":   []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	if !strings.Contains(string(body), "event: error") {
		t.Fatalf("client stream = %s, want a terminal error event", string(body))
	}
	if !strings.Contains(string(body), "before message_stop") {
		t.Fatalf("client stream = %s, want the truncation explained", string(body))
	}
}

func TestAnthropicStreamKeepsCleanCompletion(t *testing.T) {
	upstream := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claudeopus4\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")

	server := newArgoStreamTestServer(t, upstream)
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", map[string]interface{}{
		"model":      "claudeopus4",
		"stream":     true,
		"max_tokens": 64,
		"messages":   []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	if strings.Contains(string(body), "event: error") {
		t.Fatalf("client stream = %s, want no error event on a clean stream", string(body))
	}
	if !strings.Contains(string(body), "event: message_stop") {
		t.Fatalf("client stream = %s, want message_stop", string(body))
	}
}

// An upstream that stalls and then dies before message_start leaves the client
// holding a stream of keep-alives. Pings say nothing about how the turn ended,
// so the failure still has to be spelled out.
func TestAnthropicStreamTerminatesPingOnlyUpstream(t *testing.T) {
	upstream := "event: ping\ndata: {\"type\":\"ping\"}\n\n"

	server := newArgoStreamTestServer(t, upstream)
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", map[string]interface{}{
		"model":      "claudeopus4",
		"stream":     true,
		"max_tokens": 64,
		"messages":   []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	if !strings.Contains(string(body), "event: ping") {
		t.Fatalf("client stream = %s, want the upstream ping forwarded", string(body))
	}
	if !strings.Contains(string(body), "event: error") {
		t.Fatalf("client stream = %s, want a terminal error event after a ping-only stream", string(body))
	}
}

// A provider 200 that closes before its first event is still a failed stream.
// There is no caller-side HTTP error on this clean-EOF path, so leaving the
// terminal owner silent returns an empty 200 that clients read as a disconnect.
func TestAnthropicStreamTerminatesBeforeFirstEvent(t *testing.T) {
	server := newArgoStreamTestServer(t, "")
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", map[string]interface{}{
		"model":      "claudeopus4",
		"stream":     true,
		"max_tokens": 64,
		"messages":   []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	if !strings.Contains(string(body), "event: error") || !strings.Contains(string(body), "before message_stop") {
		t.Fatalf("client stream = %q, want a terminal truncation error", string(body))
	}
}

// A partial answer followed by a clean upstream close is a truncation, not a
// finished turn. Sending only [DONE] would let the client bank the partial text
// as the model's complete answer.
func TestOpenAIChatStreamTerminatesTruncatedUpstream(t *testing.T) {
	upstream := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"partial\"},\"index\":0}]}\n\n"

	server := newArgoStreamTestServer(t, upstream)
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":    "gpt5",
		"stream":   true,
		"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	if !strings.Contains(string(body), OpenAIDoneMarker) {
		t.Fatalf("client stream = %s, want a terminal %s", string(body), OpenAIDoneMarker)
	}
	if !strings.Contains(string(body), "ended before the terminal chunk") {
		t.Fatalf("client stream = %s, want an error chunk before %s", string(body), OpenAIDoneMarker)
	}
	if !strings.Contains(string(body), "finish_reason") {
		t.Fatalf("client stream = %s, want the missing finish_reason explained", string(body))
	}
	if index := strings.Index(string(body), "ended before the terminal chunk"); index > strings.Index(string(body), OpenAIDoneMarker) {
		t.Fatalf("client stream = %s, want the error chunk before %s", string(body), OpenAIDoneMarker)
	}
}

// The direct Chat relay owns the stream ending even when the provider closes
// before emitting a single record. It must spell out the failure and [DONE]
// rather than treating the absence of downstream bytes as a reason to do
// nothing.
func TestOpenAIChatStreamTerminatesBeforeFirstEvent(t *testing.T) {
	server := newArgoStreamTestServer(t, "")
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":    "gpt5",
		"stream":   true,
		"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	stream := string(body)
	if !strings.Contains(stream, "ended before the terminal chunk") || !strings.Contains(stream, OpenAIDoneMarker) {
		t.Fatalf("client stream = %q, want a truncation error followed by %s", stream, OpenAIDoneMarker)
	}
}

// Anthropic message_start and ping records have no Chat Completions equivalent.
// If the provider closes there, the converted writer has received upstream
// records but written no downstream chunk; that distinction must not turn the
// failed stream into an empty 200.
func TestConvertedOpenAIChatStreamTerminatesBeforeFirstConvertibleEvent(t *testing.T) {
	upstream := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claudeopus4\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		"event: ping\ndata: {\"type\":\"ping\"}\n\n",
	}, "")

	server := newArgoStreamTestServer(t, upstream)
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":    "claudeopus4",
		"stream":   true,
		"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	stream := string(body)
	if !strings.Contains(stream, "ended before the terminal chunk") || !strings.Contains(stream, OpenAIDoneMarker) {
		t.Fatalf("client stream = %q, want a truncation error followed by %s", stream, OpenAIDoneMarker)
	}
}

// The turn ended properly; only the [DONE] marker was missing. Backends that end
// that way must not be reported as failures.
func TestOpenAIChatStreamAddsDoneWithoutErrorAfterFinishReason(t *testing.T) {
	upstream := strings.Join([]string{
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}]}\n\n",
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n",
	}, "")

	server := newArgoStreamTestServer(t, upstream)
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":    "gpt5",
		"stream":   true,
		"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	if strings.Count(string(body), OpenAIDoneMarker) != 1 {
		t.Fatalf("client stream = %s, want exactly one %s", string(body), OpenAIDoneMarker)
	}
	if strings.Contains(string(body), "ended before the terminal chunk") {
		t.Fatalf("client stream = %s, want no truncation error after a finish_reason", string(body))
	}
}

// With n > 1 each choice ends on its own. One choice finishing says nothing
// about the others, so a stream that abandons choice 1 is still a truncation
// even though choice 0 completed — including when choice 1 never appears at all,
// which the client cannot notice from the chunks it was sent.
func TestOpenAIChatStreamTerminatesPartiallyFinishedChoices(t *testing.T) {
	chunk := func(index int, body string) string {
		return "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":" + strconv.Itoa(index) + "," + body + "}]}\n\n"
	}

	t.Run("one choice left unfinished", func(t *testing.T) {
		upstream := strings.Join([]string{
			chunk(0, `"delta":{"content":"first"}`),
			chunk(1, `"delta":{"content":"second"}`),
			chunk(0, `"delta":{},"finish_reason":"stop"`),
		}, "")

		server := newArgoStreamTestServer(t, upstream)
		status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
			"model":    "gpt5",
			"stream":   true,
			"n":        2,
			"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", status, string(body))
		}
		if !strings.Contains(string(body), "ended before the terminal chunk") {
			t.Fatalf("client stream = %s, want a truncation error for the unfinished choice", string(body))
		}
		if !strings.Contains(string(body), "one or more choices never reached a finish_reason") {
			t.Fatalf("client stream = %s, want the unfinished-choice cause", string(body))
		}
	})

	t.Run("requested choice never emitted", func(t *testing.T) {
		upstream := strings.Join([]string{
			chunk(0, `"delta":{"content":"first"}`),
			chunk(0, `"delta":{},"finish_reason":"stop"`),
		}, "")

		server := newArgoStreamTestServer(t, upstream)
		status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
			"model":    "gpt5",
			"stream":   true,
			"n":        2,
			"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", status, string(body))
		}
		if !strings.Contains(string(body), "one or more choices never reached a finish_reason") {
			t.Fatalf("client stream = %s, want a truncation error for the choice the upstream never sent", string(body))
		}
		if strings.Count(string(body), OpenAIDoneMarker) != 1 {
			t.Fatalf("client stream = %s, want exactly one %s", string(body), OpenAIDoneMarker)
		}
	})

	t.Run("every choice finished", func(t *testing.T) {
		upstream := strings.Join([]string{
			chunk(0, `"delta":{"content":"first"}`),
			chunk(1, `"delta":{"content":"second"}`),
			chunk(0, `"delta":{},"finish_reason":"stop"`),
			chunk(1, `"delta":{},"finish_reason":"stop"`),
		}, "")

		server := newArgoStreamTestServer(t, upstream)
		status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
			"model":    "gpt5",
			"stream":   true,
			"n":        2,
			"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", status, string(body))
		}
		if strings.Contains(string(body), "ended before the terminal chunk") {
			t.Fatalf("client stream = %s, want no truncation error once every choice finished", string(body))
		}
		if strings.Count(string(body), OpenAIDoneMarker) != 1 {
			t.Fatalf("client stream = %s, want exactly one %s", string(body), OpenAIDoneMarker)
		}
	})
}

// Choice completion costs what the upstream sent, not the client's n, and any
// extra choice shown to the client must finish too.
func TestOpenAIChatStreamChoiceCompletion(t *testing.T) {
	tests := []struct {
		name            string
		expectedChoices int
		choiceFinished  map[int]bool
		wantAllFinished bool
	}{
		{name: "all requested choices", expectedChoices: 2, choiceFinished: map[int]bool{0: true, 1: true}, wantAllFinished: true},
		{name: "requested choice missing at max n", expectedChoices: math.MaxInt, choiceFinished: map[int]bool{0: true, 2: true}},
		{name: "extra choice unfinished", expectedChoices: 1, choiceFinished: map[int]bool{0: true, 4: false}},
		{name: "extra choice finished", expectedChoices: 1, choiceFinished: map[int]bool{0: true, 4: true}, wantAllFinished: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forwarder := &openAICompatibleStreamStopForwarder{
				expectedChoices: tc.expectedChoices,
				choiceFinished:  tc.choiceFinished,
			}
			if got := forwarder.allChoicesFinished(); got != tc.wantAllFinished {
				t.Fatalf("allChoicesFinished() = %t, want %t", got, tc.wantAllFinished)
			}
		})
	}
}

// Stop-sequence enforcement rewrites finish_reason as it goes, so the truncation
// check has to see the stop it synthesized rather than the upstream's.
func TestOpenAIChatStreamWithStopSequencesReportsTruncationOnce(t *testing.T) {
	t.Run("stop matched", func(t *testing.T) {
		upstream := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi HALT there\"},\"index\":0}]}\n\n"
		server := newArgoStreamTestServer(t, upstream)
		status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
			"model":    "gpt5",
			"stream":   true,
			"stop":     []string{"HALT"},
			"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", status, string(body))
		}
		if strings.Contains(string(body), "ended before the terminal chunk") {
			t.Fatalf("client stream = %s, want no truncation error after a synthesized stop", string(body))
		}
	})

	t.Run("no stop matched", func(t *testing.T) {
		upstream := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"partial\"},\"index\":0}]}\n\n"
		server := newArgoStreamTestServer(t, upstream)
		status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
			"model":    "gpt5",
			"stream":   true,
			"stop":     []string{"HALT"},
			"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", status, string(body))
		}
		if !strings.Contains(string(body), "ended before the terminal chunk") {
			t.Fatalf("client stream = %s, want a truncation error", string(body))
		}
		if strings.Count(string(body), OpenAIDoneMarker) != 1 {
			t.Fatalf("client stream = %s, want exactly one %s", string(body), OpenAIDoneMarker)
		}
	})
}

func TestOpenAIChatStreamKeepsCleanCompletion(t *testing.T) {
	upstream := strings.Join([]string{
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}]}\n\n",
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n",
		"data: [DONE]\n\n",
	}, "")

	server := newArgoStreamTestServer(t, upstream)
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":    "gpt5",
		"stream":   true,
		"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	if strings.Count(string(body), OpenAIDoneMarker) != 1 {
		t.Fatalf("client stream = %s, want exactly one %s", string(body), OpenAIDoneMarker)
	}
	if strings.Contains(string(body), "ended before the terminal chunk") {
		t.Fatalf("client stream = %s, want no truncation error on a clean stream", string(body))
	}
}

type blockingAfterPayloadStreamBody struct {
	payload        []byte
	secondRead     chan struct{}
	release        chan struct{}
	closed         chan struct{}
	secondReadOnce sync.Once
	releaseOnce    sync.Once
	closeOnce      sync.Once
}

func newBlockingAfterPayloadStreamBody(payload string) *blockingAfterPayloadStreamBody {
	return &blockingAfterPayloadStreamBody{
		payload:    []byte(payload),
		secondRead: make(chan struct{}),
		release:    make(chan struct{}),
		closed:     make(chan struct{}),
	}
}

func (b *blockingAfterPayloadStreamBody) Read(p []byte) (int, error) {
	if len(b.payload) > 0 {
		n := copy(p, b.payload)
		b.payload = b.payload[n:]
		return n, nil
	}
	b.secondReadOnce.Do(func() { close(b.secondRead) })
	<-b.release
	return 0, io.EOF
}

func (b *blockingAfterPayloadStreamBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	b.releaseForTest()
	return nil
}

func (b *blockingAfterPayloadStreamBody) releaseForTest() {
	b.releaseOnce.Do(func() { close(b.release) })
}

// An upstream error is terminal even if the backend leaves its HTTP body open.
// The record callback must stop the generic scanner after forwarding the error
// and [DONE], allowing the handler's deferred body close to run immediately.
func TestOpenAIChatStreamStopsReadingAndClosesBodyAfterUpstreamError(t *testing.T) {
	errorRecord := "data: {\"error\":{\"message\":\"backend failed\",\"type\":\"server_error\"}}\n\n"
	finishedChoice := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	for _, tc := range []struct {
		name             string
		upstream         string
		wantBackendError bool
		wantFinishReason bool
	}{
		{name: "error ends unfinished turn", upstream: errorRecord, wantBackendError: true},
		{name: "error behind finished turn is dropped", upstream: finishedChoice + errorRecord, wantFinishReason: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := newBlockingAfterPayloadStreamBody(tc.upstream)
			server := newArgoStreamBodyTestServer(t, body)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt5","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Content-Type", "application/json")
			w := newFlushableRecorder()
			done := make(chan struct{})
			go func() {
				server.ServeHTTP(w, req)
				close(done)
			}()

			select {
			case <-done:
				// Expected: the terminal callback stopped the scanner.
			case <-body.secondRead:
				body.releaseForTest()
				<-done
				t.Fatal("relay read upstream again after its terminal error record")
			case <-time.After(2 * time.Second):
				body.releaseForTest()
				<-done
				t.Fatal("relay remained blocked on the open upstream body after its terminal error record")
			}

			select {
			case <-body.closed:
			default:
				t.Fatal("upstream response body was not closed when the relay returned")
			}
			stream := w.Body.String()
			if got := strings.Contains(stream, "backend failed"); got != tc.wantBackendError {
				t.Fatalf("client stream backend error = %v, want %v; stream = %s", got, tc.wantBackendError, stream)
			}
			if got := strings.Contains(stream, `"finish_reason":"stop"`); got != tc.wantFinishReason {
				t.Fatalf("client stream finish_reason = %v, want %v; stream = %s", got, tc.wantFinishReason, stream)
			}
			if strings.Count(stream, OpenAIDoneMarker) != 1 {
				t.Fatalf("client stream = %s, want exactly one %s", stream, OpenAIDoneMarker)
			}
		})
	}
}

// [DONE] ends an OpenAI Chat stream in both directions. The downstream marker
// must not be followed by another provider read merely because the HTTP body is
// still open.
func TestOpenAIChatStreamStopsReadingAndClosesBodyAfterDone(t *testing.T) {
	body := newBlockingAfterPayloadStreamBody("data: [DONE]\n\n")
	server := newArgoStreamBodyTestServer(t, body)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt5","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := newFlushableRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
		// Expected: [DONE] stopped the scanner.
	case <-body.secondRead:
		body.releaseForTest()
		<-done
		t.Fatal("relay read upstream again after [DONE]")
	case <-time.After(2 * time.Second):
		body.releaseForTest()
		<-done
		t.Fatal("relay remained blocked on the open upstream body after [DONE]")
	}

	select {
	case <-body.closed:
	default:
		t.Fatal("upstream response body was not closed when the Chat relay returned")
	}
	if stream := w.Body.String(); strings.Count(stream, OpenAIDoneMarker) != 1 {
		t.Fatalf("client stream = %s, want exactly one %s", stream, OpenAIDoneMarker)
	}
}

// severedReader replays a stream and then reports a transport failure in place
// of EOF, which is what a reset connection looks like to the forwarder.
type severedReader struct {
	remaining string
	severed   bool
}

func (r *severedReader) Read(p []byte) (int, error) {
	if r.severed {
		return 0, io.ErrUnexpectedEOF
	}
	if r.remaining == "" {
		r.severed = true
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.remaining)
	r.remaining = r.remaining[n:]
	return n, nil
}

func newGoogleStreamBodyTestServer(t *testing.T, body io.ReadCloser) *Server {
	t.Helper()
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}, nil
	})
	config := &Config{
		Provider:           constants.ProviderGoogle,
		ProviderURL:        "http://google.local/v1beta/models",
		ProviderKeySet:     ProviderKeySet{GoogleAPIKey: "google-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(t, config, client)
}

// Gemini's finishReason is the only upstream assertion that the generation
// completed. In particular, io.ErrUnexpectedEOF is classified as recoverable
// by the shared logging helper, but it cannot be swallowed here and replaced
// with a successful end_turn. A failure after finishReason is the converse: it
// must not unfinish an answer the client already received in full.
func TestGoogleAnthropicStreamPreservesTruncationUntilTermination(t *testing.T) {
	partial := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
	finished := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"complete\"}]},\"finishReason\":\"STOP\"}]}\n\n"

	cases := []struct {
		name        string
		body        io.ReadCloser
		wantText    string
		wantError   bool
		wantStop    bool
		wantFailure string
	}{
		{
			name:        "unexpected EOF before finishReason",
			body:        io.NopCloser(&severedReader{remaining: partial}),
			wantText:    "partial",
			wantError:   true,
			wantFailure: "unexpected EOF",
		},
		{
			name:        "clean EOF before finishReason",
			body:        io.NopCloser(strings.NewReader(partial)),
			wantText:    "partial",
			wantError:   true,
			wantFailure: "before finishReason",
		},
		{
			name:     "unexpected EOF after finishReason",
			body:     io.NopCloser(&severedReader{remaining: finished}),
			wantText: "complete",
			wantStop: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newGoogleStreamBodyTestServer(t, tc.body)
			status, raw := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", map[string]interface{}{
				"model":      "gemini-test",
				"stream":     true,
				"max_tokens": 64,
				"messages":   []map[string]interface{}{{"role": "user", "content": "hi"}},
			})
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", status, string(raw))
			}
			stream := string(raw)
			if !strings.Contains(stream, tc.wantText) {
				t.Fatalf("client stream = %s, want text %q", stream, tc.wantText)
			}
			if got := strings.Contains(stream, "event: error"); got != tc.wantError {
				t.Fatalf("client stream error event = %v, want %v; stream = %s", got, tc.wantError, stream)
			}
			if got := strings.Contains(stream, "event: message_stop"); got != tc.wantStop {
				t.Fatalf("client stream message_stop = %v, want %v; stream = %s", got, tc.wantStop, stream)
			}
			if tc.wantFailure != "" && !strings.Contains(stream, tc.wantFailure) {
				t.Fatalf("client stream = %s, want failure detail %q", stream, tc.wantFailure)
			}
		})
	}
}

func forwardSeveredChatStream(t *testing.T, upstream string, stops []string, expectedChoices int) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	reader := &severedReader{remaining: upstream}
	if err := forwardOpenAICompatibleSSEWithStops(context.Background(), recorder, reader, "client-model", "OpenAI", stops, expectedChoices); err == nil {
		t.Fatal("forwardOpenAICompatibleSSEWithStops() error = nil, want the transport failure")
	}
	return recorder.Body.String()
}

// forwardChatStream is forwardSeveredChatStream's counterpart for an upstream
// that closes cleanly: the transport succeeded, and whatever the client is told
// about the turn comes from the records themselves.
func forwardChatStream(t *testing.T, upstream string, stops []string, expectedChoices int) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	if err := forwardOpenAICompatibleSSEWithStops(context.Background(), recorder, strings.NewReader(upstream), "client-model", "OpenAI", stops, expectedChoices); err != nil {
		t.Fatalf("forwardOpenAICompatibleSSEWithStops() error = %v", err)
	}
	return recorder.Body.String()
}

// Several OpenAI-compatible backends stamp "finish_reason":"" on every ongoing
// chunk. That is the upstream saying it has not finished, so a stream ending
// there is as truncated as one that never mentioned finish_reason at all.
// Counting it as an ending would mark the choice done on its first delta and let
// the whole truncation check pass, silently, on every such backend.
func TestOpenAIChatStreamTreatsEmptyFinishReasonAsUnfinished(t *testing.T) {
	const partial = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"\"}]}\n\n"

	// The empty reason reaches the client either way: the enforcer parses chunks
	// only when stop sequences are configured, so the two paths read finish_reason
	// out of different shapes and both had the same nil-only test.
	for _, tc := range []struct {
		name  string
		stops []string
	}{
		{name: "no stop sequences"},
		{name: "stop sequences enforced", stops: []string{"HALT"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := forwardChatStream(t, partial, tc.stops, 1)
			if !strings.Contains(body, "ended before the terminal chunk") {
				t.Fatalf("client stream = %s, want a truncation error after an empty finish_reason", body)
			}
			if !strings.Contains(body, "one or more choices never reached a finish_reason") {
				t.Fatalf("client stream = %s, want the unfinished-choice cause", body)
			}
			// The proxy judges the reason; it does not rewrite it. A client that
			// tolerates the backend's spelling must still see it.
			if !strings.Contains(body, "\"finish_reason\":\"\"") {
				t.Fatalf("client stream = %s, want the upstream's empty finish_reason forwarded", body)
			}
			if strings.Count(body, OpenAIDoneMarker) != 1 {
				t.Fatalf("client stream = %s, want exactly one %s", body, OpenAIDoneMarker)
			}
		})
	}

	// The same backend's real ending still ends the turn.
	t.Run("empty reason followed by a real one", func(t *testing.T) {
		body := forwardChatStream(t, partial+"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", nil, 1)
		if strings.Contains(body, "ended before the terminal chunk") {
			t.Fatalf("client stream = %s, want no truncation error once the choice finished", body)
		}
		if strings.Count(body, OpenAIDoneMarker) != 1 {
			t.Fatalf("client stream = %s, want exactly one %s", body, OpenAIDoneMarker)
		}
	})
}

// The generation finished; only the connection did not. A transport failure
// after every requested choice reached its finish_reason must read the same as
// the clean EOF carrying the identical bytes, or a client discards — or pays to
// regenerate — an answer it already holds in full.
func TestOpenAIChatStreamKeepsFinishedChoicesWhenReaderFails(t *testing.T) {
	upstream := strings.Join([]string{
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}]}\n\n",
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n",
	}, "")

	body := forwardSeveredChatStream(t, upstream, nil, 1)
	if strings.Contains(body, "ended before the terminal chunk") {
		t.Fatalf("client stream = %s, want no truncation error after every choice finished", body)
	}
	if strings.Count(body, OpenAIDoneMarker) != 1 {
		t.Fatalf("client stream = %s, want exactly one %s", body, OpenAIDoneMarker)
	}

	// A choice still owed a finish_reason is a real truncation and keeps its error.
	partial := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}]}\n\n"
	partialBody := forwardSeveredChatStream(t, partial, nil, 1)
	if !strings.Contains(partialBody, "ended before the terminal chunk") {
		t.Fatalf("client stream = %s, want a truncation error while a choice is unfinished", partialBody)
	}
}

// Stop enforcement holds back a suffix that might still turn into a stop
// sequence. The terminal path releases it either way, so releasing it after the error
// puts ordinary content behind a marker the client stops reading at.
func TestOpenAIChatStreamFlushesStopTailBeforeErrorChunk(t *testing.T) {
	upstream := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"abcX\"},\"index\":0}]}\n\n"

	body := forwardSeveredChatStream(t, upstream, []string{"XYZ"}, 1)
	tail := strings.Index(body, "\"content\":\"cX\"")
	failure := strings.Index(body, "ended before the terminal chunk")
	done := strings.Index(body, OpenAIDoneMarker)
	if tail < 0 || failure < 0 || done < 0 {
		t.Fatalf("client stream = %s, want the held tail, the error, and %s", body, OpenAIDoneMarker)
	}
	if tail > failure {
		t.Fatalf("client stream = %s, want the held tail before the error chunk", body)
	}
	if failure > done {
		t.Fatalf("client stream = %s, want the error chunk before %s", body, OpenAIDoneMarker)
	}
}

// A final chunk can be valid JSON while failing typed OpenAIStreamChunk
// decoding. The raw fallback still recognizes its finish_reason, but that
// recognition must happen after releasing any text held as a possible stop
// prefix: marking the choice finished first makes flushStopTail skip it.
func TestOpenAIChatStreamFlushesStopTailBeforeRawFinishChunk(t *testing.T) {
	upstream := strings.Join([]string{
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream\",\"choices\":[{\"delta\":{\"content\":\"abcX\"},\"index\":0}]}\n\n",
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":\"not-an-integer\",\"model\":\"upstream\",\"provider_counter\":9007199254740993,\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n",
		"data: [DONE]\n\n",
	}, "")

	body := forwardChatStream(t, upstream, []string{"XYZ"}, 1)
	tail := strings.Index(body, `"content":"cX"`)
	finish := strings.Index(body, `"finish_reason":"stop"`)
	done := strings.Index(body, "data: [DONE]")
	if tail < 0 {
		t.Fatalf("client stream = %s, want the held tail released", body)
	}
	if finish < 0 || done < 0 {
		t.Fatalf("client stream = %s, want the raw finish chunk and [DONE]", body)
	}
	if tail > finish || finish > done {
		t.Fatalf("client stream = %s, want the held tail, then raw finish, then [DONE]", body)
	}
	if strings.Contains(body, "ended before the terminal chunk") {
		t.Fatalf("client stream = %s, want no truncation error after the raw finish", body)
	}
	if strings.Count(body, "data: [DONE]") != 1 {
		t.Fatalf("client stream = %s, want exactly one [DONE]", body)
	}
	if !strings.Contains(body, `"provider_counter":9007199254740993`) {
		t.Fatalf("client stream = %s, want the raw provider number preserved exactly", body)
	}
}

// An upstream that reports its own failure has already told the client what went
// wrong, with the provider's code attached. The proxy preserves those details,
// rewrites only a mapped backend model to the client's alias, and adds [DONE]; a
// second, vaguer error would compete with the first. The record is also the end
// of the turn: everything generated before it goes out in front of it, and
// nothing the upstream says afterwards is a turn continuing.
func TestOpenAIChatStreamKeepsUpstreamErrorRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		// stops decides whether the enforcer holds "cX" back as a possible prefix of
		// "XYZ" or lets "abcX" through whole. The two configurations failed
		// differently before the fix, so both are checked.
		stops    []string
		lastText string
	}{
		{name: "no stop sequences", lastText: "\"content\":\"abcX\""},
		{name: "stop sequences enforced", stops: []string{"XYZ"}, lastText: "\"content\":\"cX\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := strings.Join([]string{
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"abcX\"},\"index\":0}]}\n\n",
				"data: {\"model\":\"backend-model\",\"request_id\":\"req-provider-123\",\"error\":{\"message\":\"rate limit reached\",\"type\":\"rate_limit_exceeded\",\"code\":\"429\",\"details\":{\"limit_id\":9007199254740993,\"retry_after_ms\":250}}}\n\n",
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"kept-going\"},\"index\":0}]}\n\n",
			}, "")

			body := forwardChatStream(t, upstream, tc.stops, 1)
			if !strings.Contains(body, "rate_limit_exceeded") || !strings.Contains(body, "rate limit reached") {
				t.Fatalf("client stream = %s, want the upstream error forwarded", body)
			}
			if strings.Contains(body, "ended before the terminal chunk") {
				t.Fatalf("client stream = %s, want no second error after the upstream's own", body)
			}
			if strings.Count(body, "\"error\"") != 1 {
				t.Fatalf("client stream = %s, want exactly one error record", body)
			}
			if strings.Count(body, OpenAIDoneMarker) != 1 {
				t.Fatalf("client stream = %s, want exactly one %s", body, OpenAIDoneMarker)
			}
			if strings.Contains(body, "kept-going") {
				t.Fatalf("client stream = %s, want nothing forwarded after the upstream error", body)
			}
			if strings.Contains(body, "backend-model") {
				t.Fatalf("client stream = %s, want the terminal error to hide the backend model", body)
			}

			var forwardedError struct {
				Model     string `json:"model"`
				RequestID string `json:"request_id"`
				Error     struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
					Details struct {
						LimitID      json.Number `json:"limit_id"`
						RetryAfterMS int         `json:"retry_after_ms"`
					} `json:"details"`
				} `json:"error"`
			}
			foundError := false
			scanner := NewTestSSEScanner(strings.NewReader(body))
			for scanner.Scan() {
				if !isOpenAIStreamErrorRecord(scanner.Data()) {
					continue
				}
				if err := json.Unmarshal([]byte(scanner.Data()), &forwardedError); err != nil {
					t.Fatalf("decoding forwarded error record failed: %v; record = %s", err, scanner.Data())
				}
				foundError = true
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("scanning client stream failed: %v", err)
			}
			if !foundError {
				t.Fatalf("client stream = %s, want a forwarded error record", body)
			}
			if forwardedError.Model != "client-model" {
				t.Fatalf("forwarded error model = %q, want client-model", forwardedError.Model)
			}
			if forwardedError.RequestID != "req-provider-123" {
				t.Fatalf("forwarded request_id = %q, want provider value", forwardedError.RequestID)
			}
			if forwardedError.Error.Message != "rate limit reached" || forwardedError.Error.Type != "rate_limit_exceeded" || forwardedError.Error.Code != "429" {
				t.Fatalf("forwarded provider error = %+v, want message/type/code preserved", forwardedError.Error)
			}
			if got := forwardedError.Error.Details.LimitID.String(); got != "9007199254740993" {
				t.Fatalf("forwarded provider limit_id = %q, want exact large numeric value", got)
			}
			if got := forwardedError.Error.Details.RetryAfterMS; got != 250 {
				t.Fatalf("forwarded provider retry_after_ms = %d, want 250", got)
			}
			failure := strings.Index(body, "rate_limit_exceeded")
			text := strings.Index(body, tc.lastText)
			if text < 0 {
				t.Fatalf("client stream = %s, want the generated text %s", body, tc.lastText)
			}
			if text > failure {
				t.Fatalf("client stream = %s, want the generated text before the error", body)
			}
			if !strings.HasSuffix(strings.TrimSpace(body), "data: "+OpenAIDoneMarker) {
				t.Fatalf("client stream = %s, want %s last", body, OpenAIDoneMarker)
			}
		})
	}
}

// Not every record with an "error" key is a failure. Ending a healthy stream at
// one that holds no error would be a worse bug than the duplicate error the
// check exists to prevent, so the two spellings of "nothing went wrong" are
// pinned.
func TestOpenAIChatStreamTreatsEmptyErrorFieldAsChunk(t *testing.T) {
	for _, data := range []string{
		"{\"error\":null,\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[]}",
		"{\"error\":\"\",\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[]}",
		"{\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}],\"error\":{\"message\":\"boom\"}}",
	} {
		if isOpenAIStreamErrorRecord(data) {
			t.Errorf("isOpenAIStreamErrorRecord(%s) = true, want false", data)
		}
	}
	if !isOpenAIStreamErrorRecord("{\"error\":{\"message\":\"boom\",\"type\":\"server_error\"}}") {
		t.Error("isOpenAIStreamErrorRecord() = false on a real error envelope, want true")
	}
}

// A backend that sends no separate role chunk and whose first delta is entirely
// a stop prefix leaves the forwarder holding real output with nothing written
// yet. Deciding the stream is empty at that point drops the text and the
// terminal marker with it, and an empty 200 reads to the client as a dropped
// connection rather than a failed turn.
func TestOpenAIChatStreamFlushesHeldStopTextWhenNothingElseWasSent(t *testing.T) {
	upstream := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"X\"},\"index\":0}]}\n\n"

	body := forwardSeveredChatStream(t, upstream, []string{"XYZ"}, 1)
	tail := strings.Index(body, "\"content\":\"X\"")
	failure := strings.Index(body, "ended before the terminal chunk")
	done := strings.Index(body, OpenAIDoneMarker)
	if tail < 0 {
		t.Fatalf("client stream = %q, want the held text released", body)
	}
	if failure < 0 || done < 0 {
		t.Fatalf("client stream = %q, want the truncation error and %s", body, OpenAIDoneMarker)
	}
	if tail > failure || failure > done {
		t.Fatalf("client stream = %q, want the held text, then the error, then %s", body, OpenAIDoneMarker)
	}
}

// A turn the client was told had finished does not un-finish because the
// upstream ran into trouble behind it. Local stop enforcement makes that
// ordinary rather than exotic: the stop sequences are stripped from the
// forwarded request, so the upstream keeps generating past the point the client
// saw finish_reason and can fail on text that will never be sent. Forwarding
// that failure invites the client to throw away a complete answer it already
// holds.
func TestOpenAIChatStreamIgnoresErrorsAfterTheTurnFinished(t *testing.T) {
	const errorRecord = "data: {\"error\":{\"message\":\"rate limit reached\",\"type\":\"rate_limit_exceeded\",\"code\":\"429\"}}\n\n"

	for _, tc := range []struct {
		name string
		// stops chooses who ended the turn: the proxy's own stop enforcement, or
		// the upstream's finish_reason. Both leave nothing unfinished.
		stops    []string
		upstream string
	}{
		{
			name:  "local stop enforcement finished the turn",
			stops: []string{"XYZ"},
			upstream: strings.Join([]string{
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"the answer XYZ\"},\"index\":0}]}\n\n",
				errorRecord,
			}, ""),
		},
		{
			name: "upstream finished the turn",
			upstream: strings.Join([]string{
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"the answer\"},\"index\":0}]}\n\n",
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n",
				errorRecord,
			}, ""),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := forwardChatStream(t, tc.upstream, tc.stops, 1)
			if !strings.Contains(body, "the answer") {
				t.Fatalf("client stream = %s, want the generated answer", body)
			}
			if !strings.Contains(body, "\"finish_reason\":\"stop\"") {
				t.Fatalf("client stream = %s, want the finished turn", body)
			}
			if strings.Contains(body, "rate_limit_exceeded") || strings.Contains(body, "rate limit reached") {
				t.Fatalf("client stream = %s, want no error behind a finished turn", body)
			}
			if strings.Contains(body, "ended before the terminal chunk") {
				t.Fatalf("client stream = %s, want no truncation error on a finished turn", body)
			}
			if strings.Count(body, OpenAIDoneMarker) != 1 {
				t.Fatalf("client stream = %s, want exactly one %s", body, OpenAIDoneMarker)
			}
			if !strings.HasSuffix(strings.TrimSpace(body), "data: "+OpenAIDoneMarker) {
				t.Fatalf("client stream = %s, want %s last", body, OpenAIDoneMarker)
			}
		})
	}
}

// anthropicStreamPreamble is the opening of an Anthropic upstream stream: enough
// for the converter to have started a text block and shown the client bytes.
const anthropicStreamPreamble = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claudeopus4\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"

// anthropicStreamErrorEvent is an Anthropic upstream failing mid-stream, which
// the converter hands to OpenAIStreamWriter.WriteError.
const anthropicStreamErrorEvent = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"rate limit reached\"}}\n\n"

func anthropicTextDeltaEvent(text string) string {
	return "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n"
}

// postConvertedChatCompletionStream drives the converted Chat Completions path:
// an Anthropic upstream turned into OpenAI chunks by OpenAIStreamWriter, rather
// than an OpenAI-compatible stream relayed as-is.
func postConvertedChatCompletionStream(t *testing.T, upstream string, stops []string) string {
	t.Helper()
	server := newArgoStreamTestServer(t, upstream)
	payload := map[string]interface{}{
		"model":    "claudeopus4",
		"stream":   true,
		"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
	}
	if len(stops) > 0 {
		payload["stop"] = stops
	}
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", payload)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(body))
	}
	return string(body)
}

// On the converted path the error chunk is the writer's own rather than a
// relayed record, so the terminal rule has to be enforced in both directions.
// Behind a finished turn an error is dropped: local stop enforcement means the
// upstream keeps generating past the point the client saw finish_reason and
// [DONE], and a failure arriving then would have the client discard a complete
// answer. Ahead of the deltas an accepted error ends the turn: content forwarded
// after it describes a turn that continued past a failure the client was already
// told about, and a client that stops reading at the error never sees it anyway.
func TestConvertedOpenAIStreamMakesErrorsTerminal(t *testing.T) {
	const errorEvent = anthropicStreamErrorEvent

	for _, tc := range []struct {
		name      string
		stops     []string
		upstream  string
		wantText  string
		wantError bool
		forbid    string
	}{
		{
			name:     "error behind a locally finished turn",
			stops:    []string{"XYZ"},
			upstream: anthropicStreamPreamble + anthropicTextDeltaEvent("the answer XYZ") + errorEvent,
			wantText: "the answer",
			forbid:   "rate limit reached",
		},
		{
			name:      "deltas behind an accepted error",
			upstream:  anthropicStreamPreamble + anthropicTextDeltaEvent("hello") + errorEvent + anthropicTextDeltaEvent("kept-going") + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			wantText:  "hello",
			wantError: true,
			forbid:    "kept-going",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := postConvertedChatCompletionStream(t, tc.upstream, tc.stops)
			if !strings.Contains(body, tc.wantText) {
				t.Fatalf("client stream = %s, want the generated text %q", body, tc.wantText)
			}
			if strings.Contains(body, tc.forbid) {
				t.Fatalf("client stream = %s, want no %q", body, tc.forbid)
			}
			if got := strings.Count(body, "rate limit reached"); tc.wantError && got != 1 {
				t.Fatalf("client stream = %s, want exactly one upstream error, got %d", body, got)
			}
			if strings.Count(body, OpenAIDoneMarker) != 1 {
				t.Fatalf("client stream = %s, want exactly one %s", body, OpenAIDoneMarker)
			}
			if !strings.HasSuffix(strings.TrimSpace(body), "data: "+OpenAIDoneMarker) {
				t.Fatalf("client stream = %s, want %s last", body, OpenAIDoneMarker)
			}
		})
	}
}

// A stream that ends properly releases the text the enforcer is holding as a
// possible stop prefix in WriteFinish, and one cut short never reaches it. The
// held text is real output generated before the failure, so it goes out in
// front of the error rather than disappearing behind it.
func TestConvertedOpenAIStreamFlushesStopTailBeforeTerminalError(t *testing.T) {
	body := postConvertedChatCompletionStream(t, anthropicStreamPreamble+anthropicTextDeltaEvent("abcX"), []string{"XYZ"})

	tail := strings.Index(body, "\"content\":\"cX\"")
	failure := strings.Index(body, "ended before the terminal chunk")
	done := strings.Index(body, OpenAIDoneMarker)
	if tail < 0 {
		t.Fatalf("client stream = %s, want the held tail released", body)
	}
	if failure < 0 || done < 0 {
		t.Fatalf("client stream = %s, want the truncation error and %s", body, OpenAIDoneMarker)
	}
	if tail > failure || failure > done {
		t.Fatalf("client stream = %s, want the held tail, then the error, then %s", body, OpenAIDoneMarker)
	}
}

// A local stop under include_usage finishes the turn but deliberately holds
// [DONE] back, waiting on a usage chunk. An upstream error arriving in that
// window is dropped — the client holds a complete answer — but dropping it must
// not leave the marker outstanding: the usage it was waiting for is not coming,
// and the client has no way to tell a stream that is over from one that stalled.
// Ending it there also puts everything the upstream sends afterwards behind the
// marker, which is the point.
func TestConvertedOpenAIStreamClosesWhenDroppingAnErrorAfterALocalStop(t *testing.T) {
	upstream := anthropicStreamPreamble + anthropicTextDeltaEvent("the answer XYZ") + anthropicStreamErrorEvent +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":7,\"output_tokens\":11}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	server := newArgoStreamTestServer(t, upstream)
	status, raw := requestJSONStatus(t, server, http.MethodPost, "/v1/chat/completions", map[string]interface{}{
		"model":          "claudeopus4",
		"stream":         true,
		"stop":           []string{"XYZ"},
		"stream_options": map[string]interface{}{"include_usage": true},
		"messages":       []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(raw))
	}
	body := string(raw)

	if !strings.Contains(body, "the answer") || !strings.Contains(body, "\"finish_reason\":\"stop\"") {
		t.Fatalf("client stream = %s, want the answer and its finish_reason", body)
	}
	if strings.Contains(body, "rate limit reached") {
		t.Fatalf("client stream = %s, want no error behind a finished turn", body)
	}
	if strings.Count(body, OpenAIDoneMarker) != 1 {
		t.Fatalf("client stream = %s, want exactly one %s", body, OpenAIDoneMarker)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: "+OpenAIDoneMarker) {
		t.Fatalf("client stream = %s, want %s last", body, OpenAIDoneMarker)
	}
	// The usage chunk is what the marker was being held for, and it arrived after
	// the failure. Forfeiting it is the trade: usage is best-effort, and an
	// upstream that just reported an error is not a source worth waiting on.
	if strings.Contains(body, "\"completion_tokens\"") {
		t.Fatalf("client stream = %s, want no usage chunk behind the closed stream", body)
	}
}

// The same held tail, lost a different way: here the stream is not cut short,
// the upstream reports a failure. Accepting an error ends the turn — it marks
// the writer finished and writes [DONE] — so EnsureTerminated's flush arrives
// to a stream that has already declined to release anything. abcX under stop
// XYZ leaves cX held, unmatched and owed to the client, so the release belongs
// ahead of the error rather than behind it.
func TestConvertedOpenAIStreamFlushesStopTailBeforeUpstreamError(t *testing.T) {
	body := postConvertedChatCompletionStream(t, anthropicStreamPreamble+anthropicTextDeltaEvent("abcX")+anthropicStreamErrorEvent, []string{"XYZ"})

	head := strings.Index(body, "\"content\":\"ab\"")
	tail := strings.Index(body, "\"content\":\"cX\"")
	failure := strings.Index(body, "rate limit reached")
	done := strings.Index(body, OpenAIDoneMarker)
	if head < 0 || tail < 0 {
		t.Fatalf("client stream = %s, want the generated text and the held tail", body)
	}
	if failure < 0 {
		t.Fatalf("client stream = %s, want the upstream error", body)
	}
	if head > tail || tail > failure || failure > done {
		t.Fatalf("client stream = %s, want the text, the held tail, the error, then %s", body, OpenAIDoneMarker)
	}
	if strings.Count(body, OpenAIDoneMarker) != 1 {
		t.Fatalf("client stream = %s, want exactly one %s", body, OpenAIDoneMarker)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: "+OpenAIDoneMarker) {
		t.Fatalf("client stream = %s, want %s last", body, OpenAIDoneMarker)
	}
}

// /v1/messages backed by an OpenAI-compatible upstream enforces stops in the
// parser, and only finishPending flushes what the enforcer holds. An upstream
// that dies early skips that, and the handler's terminal error event goes out
// over the held text, so the flush has to happen on the way out of the parser
// instead.
func TestAnthropicStreamFlushesParserStopTailBeforeTerminalError(t *testing.T) {
	upstream := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"abcX\"},\"index\":0}]}\n\n"

	server := newArgoStreamTestServer(t, upstream)
	status, raw := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", map[string]interface{}{
		"model":          "gpt5",
		"stream":         true,
		"max_tokens":     64,
		"stop_sequences": []string{"XYZ"},
		"messages":       []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(raw))
	}
	body := string(raw)

	tail := strings.Index(body, "\"text\":\"cX\"")
	failure := strings.Index(body, "event: error")
	if tail < 0 {
		t.Fatalf("client stream = %s, want the held tail released", body)
	}
	if failure < 0 {
		t.Fatalf("client stream = %s, want a terminal error event", body)
	}
	if tail > failure {
		t.Fatalf("client stream = %s, want the held tail before the error event", body)
	}
}

// newSeveredArgoStreamTestServer is newArgoStreamTestServer over a connection
// that reports a transport failure in place of EOF, so the proxy sees the
// upstream's bytes and then a reset rather than a clean close.
func newSeveredArgoStreamTestServer(t *testing.T, upstream string) *Server {
	t.Helper()
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(&severedReader{remaining: upstream}),
		}, nil
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(t, config, client)
}

// The same rule as TestOpenAIChatStreamKeepsFinishedChoicesWhenReaderFails, on
// the path that converts an OpenAI-compatible stream into Anthropic events. The
// upstream sent its finish_reason and then the connection dropped before
// [DONE]: the generation finished, so the client owes message_delta and
// message_stop, not the error event that has it discard a complete answer.
func TestAnthropicStreamKeepsFinishedTurnWhenReaderFails(t *testing.T) {
	chunk := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"the answer\"},\"index\":0}]}\n\n"
	finish := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n"

	t.Run("finish_reason before the failure", func(t *testing.T) {
		body := postSeveredAnthropicStream(t, chunk+finish, nil)
		if !strings.Contains(body, "the answer") {
			t.Fatalf("client stream = %s, want the generated answer", body)
		}
		if strings.Contains(body, "event: error") {
			t.Fatalf("client stream = %s, want no error event behind a finished turn", body)
		}
		if !strings.Contains(body, "event: message_delta") || !strings.Contains(body, "\"stop_reason\":\"end_turn\"") {
			t.Fatalf("client stream = %s, want the upstream's stop reason in message_delta", body)
		}
		if !strings.HasSuffix(strings.TrimSpace(body), "data: {\"type\":\"message_stop\"}") {
			t.Fatalf("client stream = %s, want message_stop last", body)
		}
	})

	// The converse still holds: a failure while the turn is unfinished is a real
	// truncation and keeps its error.
	t.Run("no finish_reason before the failure", func(t *testing.T) {
		body := postSeveredAnthropicStream(t, chunk, nil)
		if !strings.Contains(body, "event: error") {
			t.Fatalf("client stream = %s, want a terminal error event on a truncated turn", body)
		}
		if strings.Contains(body, "event: message_stop") {
			t.Fatalf("client stream = %s, want no message_stop behind the error", body)
		}
	})

	// Local stop enforcement ends the turn the same way, and it holds text back
	// while doing so. A failure behind it must not cost the client either.
	t.Run("local stop enforcement finished the turn", func(t *testing.T) {
		body := postSeveredAnthropicStream(t, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"the answer XYZ\"},\"index\":0}]}\n\n", []string{"XYZ"})
		if !strings.Contains(body, "the answer") {
			t.Fatalf("client stream = %s, want the generated answer", body)
		}
		if strings.Contains(body, "XYZ") {
			t.Fatalf("client stream = %s, want the stop sequence withheld", body)
		}
		if strings.Contains(body, "event: error") {
			t.Fatalf("client stream = %s, want no error event behind a finished turn", body)
		}
		if !strings.HasSuffix(strings.TrimSpace(body), "data: {\"type\":\"message_stop\"}") {
			t.Fatalf("client stream = %s, want message_stop last", body)
		}
	})
}

func postSeveredAnthropicStream(t *testing.T, upstream string, stops []string) string {
	t.Helper()
	server := newSeveredArgoStreamTestServer(t, upstream)
	payload := map[string]interface{}{
		"model":      "gpt5",
		"stream":     true,
		"max_tokens": 64,
		"messages":   []map[string]interface{}{{"role": "user", "content": "hi"}},
	}
	if len(stops) > 0 {
		payload["stop_sequences"] = stops
	}
	status, raw := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", payload)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(raw))
	}
	return string(raw)
}

// An upstream that reports its own failure has already told the client what
// went wrong, with the provider's error type attached. That record travels the
// Anthropic path twice — the parser forwards it, and the handler is handed the
// same failure again on the way out — so without a terminal gate the client
// reads two conflicting endings and the actionable one first.
func TestAnthropicStreamKeepsUpstreamErrorAlone(t *testing.T) {
	upstream := anthropicStreamPreamble + anthropicTextDeltaEvent("partial") + anthropicStreamErrorEvent

	server := newArgoStreamTestServer(t, upstream)
	status, raw := requestJSONStatus(t, server, http.MethodPost, "/v1/messages", map[string]interface{}{
		"model":      "claudeopus4",
		"stream":     true,
		"max_tokens": 64,
		"messages":   []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, string(raw))
	}
	body := string(raw)

	if !strings.Contains(body, "overloaded_error") || !strings.Contains(body, "rate limit reached") {
		t.Fatalf("client stream = %s, want the upstream error forwarded", body)
	}
	if got := strings.Count(body, "event: error"); got != 1 {
		t.Fatalf("client stream = %s, want exactly one error event, got %d", body, got)
	}
	if strings.Contains(body, "Stream processing error") {
		t.Fatalf("client stream = %s, want no proxy error behind the provider's own", body)
	}
	if strings.Contains(body, "event: message_stop") {
		t.Fatalf("client stream = %s, want no message_stop after a terminal error event", body)
	}
}
