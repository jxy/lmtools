package retry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type temporaryError struct{}

func (temporaryError) Error() string   { return "temporary" }
func (temporaryError) Timeout() bool   { return false }
func (temporaryError) Temporary() bool { return true }

func TestNewClientUsesProviderDefaultRetries(t *testing.T) {
	var attempts int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := atomic.AddInt32(&attempts, 1)
		status := http.StatusOK
		if attempt == 1 {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	client := newClient(clientOptions{timeout: time.Second, useDefaultRetries: true, transport: transport})
	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := client.Do(context.Background(), req, "openai")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want provider default retry", attempts)
	}
}

func TestNewClientWithRetriesZeroIsExplicit(t *testing.T) {
	var attempts int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&attempts, 1)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("retryable")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	client := newClient(clientOptions{timeout: time.Second, maxRetries: 0, transport: transport})
	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := client.Do(context.Background(), req, "openai")
	if err == nil {
		defer resp.Body.Close()
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestClientDoRetriesUnknownLengthWithGetBody(t *testing.T) {
	var attempts int32
	var getBodyCalls int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := atomic.AddInt32(&attempts, 1)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		if string(body) != "unknown-length" {
			t.Fatalf("body = %q, want unknown-length", body)
		}
		status := http.StatusOK
		if attempt == 1 {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	client := NewClientWithTransport(time.Second, 2, nil, nil, transport)
	req, err := http.NewRequest(http.MethodPost, "https://example.test", io.NopCloser(strings.NewReader("unknown-length")))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.ContentLength = -1
	req.GetBody = func() (io.ReadCloser, error) {
		atomic.AddInt32(&getBodyCalls, 1)
		return io.NopCloser(strings.NewReader("unknown-length")), nil
	}
	resp, err := client.Do(context.Background(), req, "default")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if getBodyCalls != 1 {
		t.Fatalf("GetBody calls = %d, want 1", getBodyCalls)
	}
}

func TestClientDoRetriesSmallBufferedBody(t *testing.T) {
	var attempts int32
	var bodies []string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := atomic.AddInt32(&attempts, 1)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		bodies = append(bodies, string(body))
		status := http.StatusOK
		if attempt == 1 {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	client := NewClientWithTransport(time.Second, 2, nil, nil, transport)
	req, err := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := client.Do(context.Background(), req, "default")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	for i, body := range bodies {
		if body != "hello" {
			t.Fatalf("body[%d] = %q, want hello", i, body)
		}
	}
}

func TestClientDoRetriesWithGetBody(t *testing.T) {
	var attempts int32
	var getBodyCalls int32
	originalBody := io.NopCloser(strings.NewReader("large-body"))
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := atomic.AddInt32(&attempts, 1)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		if string(body) != "large-body" {
			t.Fatalf("body = %q, want large-body", body)
		}
		status := http.StatusOK
		if attempt == 1 {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	client := NewClientWithTransport(time.Second, 2, nil, nil, transport)
	req, err := http.NewRequest(http.MethodPost, "https://example.test", originalBody)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.ContentLength = 2 * 1024 * 1024
	req.GetBody = func() (io.ReadCloser, error) {
		atomic.AddInt32(&getBodyCalls, 1)
		return io.NopCloser(strings.NewReader("large-body")), nil
	}
	resp, err := client.Do(context.Background(), req, "default")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if getBodyCalls != 1 {
		t.Fatalf("GetBody calls = %d, want 1", getBodyCalls)
	}
	if req.Body != originalBody {
		t.Fatal("Do mutated the caller's request Body")
	}
}

func TestClientDoDoesNotRetryLargeBodyWithoutGetBody(t *testing.T) {
	var attempts int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&attempts, 1)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("retryable")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	client := NewClientWithTransport(time.Second, 2, nil, nil, transport)
	req, err := http.NewRequest(http.MethodPost, "https://example.test", io.NopCloser(strings.NewReader("large-body")))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.ContentLength = 2 * 1024 * 1024
	resp, err := client.Do(context.Background(), req, "default")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestClientDoDoesNotRetryPermanentTransportError(t *testing.T) {
	var attempts int32
	transportErr := errors.New("permanent failure")
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, transportErr
	})
	client := NewClientWithTransport(time.Second, 2, nil, nil, transport)
	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	_, err = client.Do(context.Background(), req, "default")
	if err == nil {
		t.Fatal("Do() succeeded, want error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestClientDoRetriesTimeoutTransportError(t *testing.T) {
	var attempts int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			return nil, timeoutError{}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	client := NewClientWithTransport(time.Second, 2, nil, nil, transport)
	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := client.Do(context.Background(), req, "default")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestClientDoRetriesTemporaryTransportError(t *testing.T) {
	var attempts int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			return nil, temporaryError{}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header), Request: req}, nil
	})
	client := NewClientWithTransport(time.Second, 2, nil, nil, transport)
	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := client.Do(context.Background(), req, "default")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestClientDoRetriesUnexpectedEOFTransportError(t *testing.T) {
	var attempts int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header), Request: req}, nil
	})
	client := NewClientWithTransport(time.Second, 2, nil, nil, transport)
	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := client.Do(context.Background(), req, "default")
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestClientRetryerCachesByProvider(t *testing.T) {
	client := NewClientWithProviderDefaults(time.Second, nil, nil)
	if got, again := client.retryer("openai"), client.retryer("openai"); got != again {
		t.Fatal("retryer(openai) returned different pointers")
	}
	if got, other := client.retryer("openai"), client.retryer("google"); got == other {
		t.Fatal("retryer(openai) and retryer(google) returned same pointer")
	}
}
