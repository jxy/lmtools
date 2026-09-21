package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// httpTransport is the Streamable HTTP binding. Every message is its own
// POST to the MCP endpoint; the answer is one JSON object or an SSE stream
// that ends with the response. In the modern era the routing headers ride
// along and nothing else is stateful. In the legacy era the server may
// hand out a session id, which every later request echoes, and may send
// requests of its own on the stream, which are answered with a POST.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client
	handler peerHandler
	log     Logf

	mu        sync.Mutex
	sessionID string
}

// httpStatusError is a response the transport could not use: the status,
// the JSON-RPC error the body carried when it carried one, and the
// WWW-Authenticate challenge of a 401, all of which the client reads to
// decide between an era fallback, a session restart, and giving up.
type httpStatusError struct {
	Status          int
	Body            string
	RPC             *RPCError
	WWWAuthenticate string
}

func (e *httpStatusError) Error() string {
	text := fmt.Sprintf("HTTP %d", e.Status)
	switch {
	case e.RPC != nil:
		text += ": " + e.RPC.Error()
	case e.WWWAuthenticate != "":
		text += ": authorization required (WWW-Authenticate: " + e.WWWAuthenticate + ")"
	case e.Body != "":
		text += ": " + e.Body
	}
	return text
}

func newHTTPTransport(cfg ServerConfig, client *http.Client, handler peerHandler, log Logf) *httpTransport {
	if client == nil {
		client = &http.Client{}
	}
	return &httpTransport{
		url:     cfg.URL,
		headers: cfg.Headers,
		client:  client,
		handler: handler,
		log:     log,
	}
}

func (t *httpTransport) session() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

func (t *httpTransport) setSession(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = id
}

// post sends one message and returns the response, or an httpStatusError
// for any status the caller cannot read as success.
func (t *httpTransport) post(ctx context.Context, msg *message, opts requestOptions) (*http.Response, error) {
	data, err := encodeMessage(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for name, value := range t.headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if opts.protocolVersion != "" {
		req.Header.Set(headerProtocolVersion, opts.protocolVersion)
	}
	if opts.modern && msg.Method != "" {
		req.Header.Set(headerMethod, msg.Method)
		if opts.name != "" {
			req.Header.Set(headerName, EncodeHeaderValue(opts.name))
		}
		for _, param := range opts.params {
			req.Header.Set(param.Name, param.Value)
		}
	}
	if id := t.session(); id != "" {
		req.Header.Set(headerSessionID, id)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post to %s: %w", t.url, err)
	}
	if id := resp.Header.Get(headerSessionID); id != "" {
		t.setSession(id)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, statusError(resp)
	}
	return resp, nil
}

func statusError(resp *http.Response) *httpStatusError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	statusErr := &httpStatusError{
		Status:          resp.StatusCode,
		WWWAuthenticate: resp.Header.Get("WWW-Authenticate"),
	}
	if msg, err := decodeMessage(body); err == nil && msg.Error != nil {
		statusErr.RPC = msg.Error
	} else {
		statusErr.Body = strings.TrimSpace(string(body))
		if len(statusErr.Body) > 512 {
			statusErr.Body = statusErr.Body[:512] + "..."
		}
	}
	return statusErr
}

func (t *httpTransport) roundTrip(ctx context.Context, req *message, opts requestOptions) (*message, error) {
	resp, err := t.post(ctx, req, opts)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mediaType {
	case "application/json":
		body, err := readLimited(resp.Body, maxMessageBytes)
		if err != nil {
			return nil, err
		}
		msg, err := decodeMessage(body)
		if err != nil {
			return nil, err
		}
		if msg.kind() != kindResponse || idKey(msg.ID) != idKey(req.ID) {
			return nil, fmt.Errorf("response is not the answer to request %s", idKey(req.ID))
		}
		return msg, nil
	case "text/event-stream":
		return t.readStream(ctx, resp.Body, req, opts)
	default:
		return nil, fmt.Errorf("response Content-Type %q is neither application/json nor text/event-stream", resp.Header.Get("Content-Type"))
	}
}

// readStream reads the SSE answer to one request: notifications go to the
// handler, a legacy server's requests are answered with a POST, and the
// response to req ends the read. A stream that ends without it lost the
// request; the specification says to re-issue, which is the caller's call.
func (t *httpTransport) readStream(ctx context.Context, body io.Reader, req *message, opts requestOptions) (*message, error) {
	want := idKey(req.ID)
	var response *message
	err := readSSE(bufio.NewReaderSize(body, 64<<10), maxMessageBytes, func(event sseEvent) error {
		if event.data == "" {
			return nil
		}
		msg, err := decodeMessage([]byte(event.data))
		if err != nil {
			t.log("ignoring stream event that is not a message: %v", err)
			return nil
		}
		switch msg.kind() {
		case kindResponse:
			if idKey(msg.ID) == want {
				response = msg
				return errStopSSE
			}
			t.log("dropping response for unknown request id %s", idKey(msg.ID))
		case kindNotification:
			if t.handler.onNotification != nil {
				t.handler.onNotification(msg)
			}
		case kindRequest:
			if t.handler.onRequest == nil {
				return nil
			}
			if reply := t.handler.onRequest(msg); reply != nil {
				if err := t.notify(ctx, reply, opts); err != nil {
					t.log("reply to server request %s failed: %v", msg.Method, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("read response stream: %w", err)
	}
	if response == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("response stream ended without a response")
	}
	return response, nil
}

// notify posts a notification or a response, which the server acknowledges
// with 202 and no body. Both eras use it; the modern era only for the
// cancellation notice of a legacy session, since closing the stream is the
// modern signal.
func (t *httpTransport) notify(ctx context.Context, note *message, opts requestOptions) error {
	resp, err := t.post(ctx, note, opts)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// close ends a legacy session with DELETE, which a server may refuse with
// 405; either way the transport is done.
func (t *httpTransport) close() error {
	defer t.client.CloseIdleConnections()
	id := t.session()
	if id == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
	if err != nil {
		return nil
	}
	for name, value := range t.headers {
		req.Header.Set(name, value)
	}
	req.Header.Set(headerSessionID, id)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	t.setSession("")
	return nil
}

// readLimited reads a body of at most max bytes and reports one that is
// larger instead of truncating it into a message that would not decode.
func readLimited(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}
