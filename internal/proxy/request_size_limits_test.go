package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newSizeLimitTestServer(t *testing.T, maxBodySize int64) *Server {
	t.Helper()
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatalf("upstream must not be called for an oversized request")
		return nil, errUnexpectedUpstreamCall
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		MaxRequestBodySize: maxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(t, config, client)
}

func TestOversizedRequestBodyReturns413(t *testing.T) {
	const limit = 512
	oversized := strings.Repeat("x", 4096)

	cases := []struct {
		name     string
		path     string
		payload  map[string]interface{}
		wantType string
	}{
		{
			name:     "responses",
			path:     "/v1/responses",
			payload:  map[string]interface{}{"model": "gpt5", "input": oversized},
			wantType: ErrTypeInvalidRequest,
		},
		{
			name:     "messages",
			path:     "/v1/messages",
			payload:  map[string]interface{}{"model": "claudeopus4", "max_tokens": 16, "messages": []map[string]interface{}{{"role": "user", "content": oversized}}},
			wantType: ErrTypePayloadTooLarge,
		},
		{
			name:     "chat completions",
			path:     "/v1/chat/completions",
			payload:  map[string]interface{}{"model": "gpt5", "messages": []map[string]interface{}{{"role": "user", "content": oversized}}},
			wantType: ErrTypeInvalidRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newSizeLimitTestServer(t, limit)
			status, body := requestJSONStatus(t, server, http.MethodPost, tc.path, tc.payload)
			if status != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413; body = %s", status, string(body))
			}

			var decoded struct {
				Error struct {
					Type    string `json:"type"`
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("json.Unmarshal(error body) error = %v, body = %s", err, string(body))
			}
			if decoded.Error.Type != tc.wantType {
				t.Fatalf("error type = %q, want %q; body = %s", decoded.Error.Type, tc.wantType, string(body))
			}
			// Anthropic carries the identity in type, OpenAI in code. Either way
			// the response must name request_too_large somewhere.
			if !strings.Contains(string(body), ErrTypePayloadTooLarge) {
				t.Fatalf("body = %s, want it to mention %s", string(body), ErrTypePayloadTooLarge)
			}
			for _, want := range []string{"over the 512B limit", "-max-request-body-size"} {
				if !strings.Contains(decoded.Error.Message, want) {
					t.Fatalf("error message = %q, want it to contain %q", decoded.Error.Message, want)
				}
			}
		})
	}
}

func TestOversizedRequestReportsMeasuredSizeAndSuggestion(t *testing.T) {
	err := newRequestTooLargeError(10*1024*1024, 12*1024*1024)
	message := err.Error()
	for _, want := range []string{"request body is 12.0MB", "over the 10.0MB limit", "-max-request-body-size 12"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want it to contain %q", message, want)
		}
	}

	unknownSize := newRequestTooLargeError(10*1024*1024, -1).Error()
	if strings.Contains(unknownSize, "request body is") {
		t.Fatalf("message = %q, want no measured size when Content-Length is absent", unknownSize)
	}
	if !strings.Contains(unknownSize, "increase -max-request-body-size") {
		t.Fatalf("message = %q, want it to name the setting that raises the limit", unknownSize)
	}
	if strings.Contains(unknownSize, "-max-request-body-size 20") {
		t.Fatalf("message = %q, want no invented numeric suggestion when the size is unknown", unknownSize)
	}
}

func TestOversizedRequestSuggestionDoesNotOverflow(t *testing.T) {
	const mb int64 = 1024 * 1024
	const limit int64 = 512 * mb
	maxFlagMB := int64(math.MaxInt64) / mb
	maxSuggestibleSize := maxFlagMB * mb

	// The largest byte size representable by the integer-MB flag still gets an
	// exact recommendation, even though adding MB-1 to round it would overflow
	// for nearby values.
	message := newRequestTooLargeError(limit, maxSuggestibleSize).Error()
	wantSuggestion := fmt.Sprintf("-max-request-body-size %d", maxFlagMB)
	if !strings.Contains(message, wantSuggestion) {
		t.Fatalf("message = %q, want it to contain %q", message, wantSuggestion)
	}

	// No whole-MB flag value can represent the remaining positive int64 byte
	// lengths. Omitting the remedy is more useful than wrapping to a small value
	// that would immediately reject the same request again.
	message = newRequestTooLargeError(limit, math.MaxInt64).Error()
	if strings.Contains(message, "raise it with") {
		t.Fatalf("message = %q, want no unrepresentable numeric suggestion", message)
	}
	if !strings.Contains(message, "no whole-MB -max-request-body-size value") {
		t.Fatalf("message = %q, want it to explain why no flag suggestion is available", message)
	}

	// Exercise the remotely controlled Content-Length path, which rejects the
	// declaration before reading a body and feeds it into the same diagnostic.
	server := newSizeLimitTestServer(t, limit)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", http.NoBody)
	request.Header.Set("Content-Type", "application/json")
	request.ContentLength = math.MaxInt64
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", recorder.Code, recorder.Body.String())
	}
	if response := recorder.Body.String(); strings.Contains(response, "raise it with") || !strings.Contains(response, "no whole-MB -max-request-body-size value") {
		t.Fatalf("response = %q, want an overflow-safe no-suggestion diagnostic", response)
	}
}

// An oversized upload must be absorbed before the connection closes, otherwise
// the peer sees a reset instead of the 413 the proxy wrote.
func TestOversizedRequestOverRealConnectionReturns413(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		MaxRequestBodySize: 64 * 1024,
		SessionsDir:        t.TempDir(),
	}
	handler, cleanup := NewTestServer(t, config)
	defer cleanup()

	srv := httptest.NewServer(handler)
	defer srv.Close()

	payload, err := json.Marshal(map[string]interface{}{
		"model":  "gpt5",
		"stream": true,
		"input":  strings.Repeat("x", 4*1024*1024),
	})
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}

	resp, err := http.Post(srv.URL+"/v1/responses", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("client never received a response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response failed: %v", err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), ErrTypePayloadTooLarge) {
		t.Fatalf("body = %s, want it to mention %s", string(body), ErrTypePayloadTooLarge)
	}
}

// A client using Expect: 100-continue sends nothing until the server answers,
// and a declared Content-Length over the limit is refused before a byte is read,
// so the final status suppresses the 100 that would have unblocked it. Both
// halves of the fix are needed to get the 413 out of that standoff: the
// unwrapped body has to go back so net/http stops trying to consume an upload
// that was never authorized before it will write any of the response, and the
// response has to be flushed so it does not sit in the buffer until the handler
// returns on the far side of the drain. Either one alone still waits out the
// drain deadline.
func TestOversizedExpectContinueRequestGets413WithoutWaitingOutTheDrain(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		MaxRequestBodySize: 64 * 1024,
		SessionsDir:        t.TempDir(),
	}
	handler, cleanup := NewTestServer(t, config)
	defer cleanup()

	srv := httptest.NewServer(handler)
	defer srv.Close()

	address := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("net.Dial error = %v", err)
	}
	defer conn.Close()

	// Announce a body far over the limit and then wait, as a client honouring
	// 100-continue does. Nothing follows the headers.
	request := fmt.Sprintf("POST /v1/responses HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n",
		address, 8*1024*1024)
	start := time.Now()
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("writing the request head failed: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(rejectedRequestDrainTimeout / 2)); err != nil {
		t.Fatalf("conn.SetReadDeadline error = %v", err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("client never read a status line (waited %v): %v", elapsed.Round(time.Millisecond), err)
	}
	if !strings.Contains(status, strconv.Itoa(http.StatusRequestEntityTooLarge)) {
		t.Fatalf("status line = %q, want 413", strings.TrimSpace(status))
	}
	if elapsed >= rejectedRequestDrainTimeout {
		t.Fatalf("413 arrived after %v, want it ahead of the %v drain deadline", elapsed.Round(time.Millisecond), rejectedRequestDrainTimeout)
	}
}

// A declared length over the limit is refused before a byte is read, so the
// whole upload is still outstanding when the middleware flushes the 413. Below
// net/http's 256KB "too big to bother" threshold it does not give up on that
// upload, it absorbs it inside the header write — a read the proxy owns the
// deadline for but had not installed yet, against a client that may never send
// another byte. The rejection has to reach the client anyway, and ahead of any
// draining rather than behind it.
func TestOversizedRequestGets413WhenTheClientStopsSendingMidBody(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		MaxRequestBodySize: 64 * 1024,
		SessionsDir:        t.TempDir(),
	}
	handler, cleanup := NewTestServer(t, config)
	defer cleanup()

	srv := httptest.NewServer(handler)
	defer srv.Close()

	address := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("net.Dial error = %v", err)
	}
	defer conn.Close()

	// Over the proxy's 64KB limit and under the 256KB net/http reads rather than
	// abandons, which is the window where the header write does the absorbing.
	const declared = 200 * 1024
	head := fmt.Sprintf("POST /v1/responses HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", address, declared)
	start := time.Now()
	// The head plus a token of the body, then silence: a client mid-upload that
	// stops, without closing the connection or signalling anything.
	if _, err := conn.Write([]byte(head + `{"model":"gpt5","input":"`)); err != nil {
		t.Fatalf("writing the request head failed: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(rejectedRequestDrainTimeout / 2)); err != nil {
		t.Fatalf("conn.SetReadDeadline error = %v", err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("client never read a status line (waited %v): %v", elapsed.Round(time.Millisecond), err)
	}
	if !strings.Contains(status, strconv.Itoa(http.StatusRequestEntityTooLarge)) {
		t.Fatalf("status line = %q, want 413", strings.TrimSpace(status))
	}
	if elapsed >= rejectedRequestDrainTimeout {
		t.Fatalf("413 arrived after %v, want it ahead of the %v drain deadline", elapsed.Round(time.Millisecond), rejectedRequestDrainTimeout)
	}
}

// A deadline-limited drain that does not reach the declared end of an HTTP/1
// body must retire the connection. Otherwise the next bytes sent after the
// deadline can be parsed as a new request even though they still belong to the
// rejected request's body.
func TestIncompleteRejectedRequestCannotReuseConnection(t *testing.T) {
	var calls atomic.Int32
	handler := NewProxyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		markLocalBodyLimitRejection(w)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, "rejected")
	}), &Config{MaxRequestBodySize: 64 * 1024})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	address := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("net.Dial error = %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(rejectedRequestDrainTimeout + 2*time.Second)); err != nil {
		t.Fatalf("conn.SetDeadline error = %v", err)
	}

	const declared = 200 * 1024
	requestHead := fmt.Sprintf("POST /first HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n", address, declared)
	if _, err := io.WriteString(conn, requestHead); err != nil {
		t.Fatalf("writing the rejected request head failed: %v", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("reading the rejection failed: %v", err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Fatal("413 did not advertise connection closure for an incomplete request body")
	}
	body, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("reading the complete 413 body failed: %v", err)
	}
	if string(body) != "rejected" {
		t.Fatalf("response body = %q, want %q", string(body), "rejected")
	}

	second := fmt.Sprintf("GET /second HTTP/1.1\r\nHost: %s\r\n\r\n", address)
	if _, err := io.WriteString(conn, second); err == nil {
		if status, readErr := reader.ReadString('\n'); readErr == nil {
			t.Fatalf("incomplete-body connection accepted another request: %q", strings.TrimSpace(status))
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1; rejected body bytes were reused as another request", got)
	}
}

type readsAfterHandlerBody struct {
	reader           *strings.Reader
	handlerCompleted *bool
	readsAfter       int
}

func (b *readsAfterHandlerBody) Read(p []byte) (int, error) {
	if *b.handlerCompleted {
		b.readsAfter++
	}
	return b.reader.Read(p)
}

func (*readsAfterHandlerBody) Close() error { return nil }

// A provider may reject a request at a tighter payload limit after the proxy
// has already accepted and buffered the complete client body. That 413 is not a
// local body-limit rejection: the middleware must neither drain the body again
// nor promise to close an otherwise reusable HTTP/1 connection.
func TestProvider413DoesNotCloseOrDrainAcceptedRequest(t *testing.T) {
	const payload = "accepted by proxy, rejected by provider"
	handlerCompleted := false
	body := &readsAfterHandlerBody{
		reader:           strings.NewReader(payload),
		handlerCompleted: &handlerCompleted,
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading accepted request body failed: %v", err)
		}
		if string(got) != payload {
			t.Fatalf("request body = %q, want %q", string(got), payload)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, `{"error":{"type":"request_too_large","message":"provider limit"}}`)
		handlerCompleted = true
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", http.NoBody)
	request.Body = body
	request.ContentLength = int64(len(payload))
	recorder := httptest.NewRecorder()
	NewProxyMiddleware(next, &Config{MaxRequestBodySize: 1024}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
	if got := recorder.Header().Get("Connection"); got != "" {
		t.Fatalf("Connection header = %q, want provider 413 to preserve keep-alive", got)
	}
	if body.readsAfter != 0 {
		t.Fatalf("body reads after provider response handler completed = %d, want no rejection drain", body.readsAfter)
	}
}

// Exercise the same provider-side 413 over a real HTTP/1 connection. Reading
// the accepted request body leaves the connection framed for the next request,
// so the provider's status must not retire it.
func TestProvider413KeepsHTTP1ConnectionReusable(t *testing.T) {
	var calls atomic.Int32
	handler := NewProxyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/first":
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Fatalf("reading accepted request body failed: %v", err)
			}
			const rejection = "provider rejection"
			w.Header().Set("Content-Length", strconv.Itoa(len(rejection)))
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = io.WriteString(w, rejection)
		case "/second":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}), &Config{MaxRequestBodySize: 1024})

	srv := httptest.NewServer(handler)
	defer srv.Close()
	address := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("net.Dial error = %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("conn.SetDeadline error = %v", err)
	}

	const requestBody = "complete request"
	first := fmt.Sprintf("POST /first HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n%s", address, len(requestBody), requestBody)
	if _, err := io.WriteString(conn, first); err != nil {
		t.Fatalf("writing first request failed: %v", err)
	}

	reader := bufio.NewReader(conn)
	firstResponse, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("reading provider rejection failed: %v", err)
	}
	if firstResponse.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("first status = %d, want 413", firstResponse.StatusCode)
	}
	if firstResponse.Close {
		t.Fatal("provider 413 advertised connection closure")
	}
	if _, err := io.Copy(io.Discard, firstResponse.Body); err != nil {
		t.Fatalf("reading provider rejection body failed: %v", err)
	}
	if err := firstResponse.Body.Close(); err != nil {
		t.Fatalf("closing provider rejection body failed: %v", err)
	}

	second := fmt.Sprintf("GET /second HTTP/1.1\r\nHost: %s\r\n\r\n", address)
	if _, err := io.WriteString(conn, second); err != nil {
		t.Fatalf("writing second request on keep-alive connection failed: %v", err)
	}
	secondResponse, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("reading second response on keep-alive connection failed: %v", err)
	}
	defer secondResponse.Body.Close()
	if secondResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("second status = %d, want 204", secondResponse.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("handler calls = %d, want both requests on the connection", got)
	}
}

// recordingRejectionWriter is a ResponseWriter that logs the ResponseController
// calls the drain makes. It deliberately offers no EnableFullDuplex, which is
// the case where the deadline is the only thing standing between a stalled
// upload and net/http's own drain inside the flush.
type recordingRejectionWriter struct {
	http.ResponseWriter
	calls          []string
	readDeadline   time.Time
	onReadDeadline func(time.Time)
}

func (w *recordingRejectionWriter) SetReadDeadline(deadline time.Time) error {
	w.calls = append(w.calls, "SetReadDeadline")
	w.readDeadline = deadline
	if w.onReadDeadline != nil {
		w.onReadDeadline(deadline)
	}
	return nil
}

func (w *recordingRejectionWriter) Flush() {
	w.calls = append(w.calls, "Flush")
}

// Flushing writes the response header, and net/http may read the request body
// from in there. Whatever the flush turns out to do, the deadline that bounds
// this drain has to already be in force when it happens.
func TestRejectedRequestDrainBoundsTheFlushItTriggers(t *testing.T) {
	writer := &recordingRejectionWriter{ResponseWriter: httptest.NewRecorder()}
	body := io.NopCloser(strings.NewReader(strings.Repeat("x", 32)))

	start := time.Now()
	drainRejectedRequestBody(context.Background(), writer, body, nil)
	ceiling := time.Now().Add(rejectedRequestDrainTimeout)

	deadlineAt := -1
	flushAt := -1
	for i, call := range writer.calls {
		switch call {
		case "SetReadDeadline":
			deadlineAt = i
		case "Flush":
			flushAt = i
		}
	}
	if deadlineAt < 0 || flushAt < 0 {
		t.Fatalf("calls = %v, want both a read deadline and a flush", writer.calls)
	}
	if deadlineAt > flushAt {
		t.Fatalf("calls = %v, want the read deadline installed before the flush", writer.calls)
	}
	if writer.readDeadline.Before(start) || writer.readDeadline.After(ceiling) {
		t.Fatalf("read deadline is %v out, want it within the %v drain timeout", writer.readDeadline.Sub(start), rejectedRequestDrainTimeout)
	}
	if remaining, err := io.ReadAll(body); err != nil || len(remaining) != 0 {
		t.Fatalf("body remainder = %q, %v; want the body drained", string(remaining), err)
	}
}

// deadlineBufferedBody models the two sources a server-side request body can
// read from: bytes net/http already buffered and bytes still waiting on the
// socket. An expired socket deadline does not erase the former, so the drain
// must read them through the DEBUG logger before Close abandons the latter.
type deadlineBufferedBody struct {
	prefix     *bytes.Reader
	buffered   *bytes.Reader
	tail       *bytes.Reader
	deadline   time.Time
	closed     bool
	closeReads int64
}

func newDeadlineBufferedBody(prefix, buffered, tail string) *deadlineBufferedBody {
	return &deadlineBufferedBody{
		prefix:   bytes.NewReader([]byte(prefix)),
		buffered: bytes.NewReader([]byte(buffered)),
		tail:     bytes.NewReader([]byte(tail)),
	}
}

func (b *deadlineBufferedBody) Read(p []byte) (int, error) {
	if b.closed {
		return 0, http.ErrBodyReadAfterClose
	}
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	if !b.deadline.IsZero() && time.Now().After(b.deadline) {
		if b.buffered.Len() > 0 {
			return b.buffered.Read(p)
		}
		return 0, os.ErrDeadlineExceeded
	}
	if b.buffered.Len() > 0 {
		return b.buffered.Read(p)
	}
	return b.tail.Read(p)
}

func (b *deadlineBufferedBody) Close() error {
	if b.closed {
		return nil
	}
	if b.deadline.IsZero() || time.Now().Before(b.deadline) {
		n, _ := io.Copy(io.Discard, io.MultiReader(b.prefix, b.buffered, b.tail))
		b.closeReads += n
	}
	b.closed = true
	return nil
}

func TestDefaultRequestBodySizeMatchesOpenAIPayloadLimit(t *testing.T) {
	if got, want := int64(constants.DefaultMaxRequestBodySize), int64(512*1024*1024); got != want {
		t.Fatalf("DefaultMaxRequestBodySize = %d, want %d (OpenAI's documented payload limit)", got, want)
	}
	for _, config := range []*Config{nil, {}, {MaxRequestBodySize: -1}} {
		if got := effectiveMaxRequestBodySize(config); got != int64(constants.DefaultMaxRequestBodySize) {
			t.Fatalf("effectiveMaxRequestBodySize(%+v) = %d, want the default %d", config, got, constants.DefaultMaxRequestBodySize)
		}
	}
}

func TestProxyMiddlewareUsesDefaultRequestBodySize(t *testing.T) {
	const payload = "nonempty"
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		got = string(body)
		w.WriteHeader(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
	NewProxyMiddleware(next, &Config{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
	if got != payload {
		t.Fatalf("body = %q, want %q", got, payload)
	}
}
