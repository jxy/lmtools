package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"strings"
	"sync"
	"time"
)

type providerJSONRequest struct {
	URL                string
	Provider           string
	RequestName        string
	Payload            interface{}
	RawBody            []byte
	ExtraHeaders       map[string]string
	Configure          func(*http.Request)
	ConfigureWithError func(*http.Request) error
}

func buildProviderJSONRequest(ctx context.Context, spec providerJSONRequest) (*http.Request, []byte, error) {
	requestName := spec.RequestName
	if requestName == "" {
		requestName = spec.Provider
	}

	var reqBody []byte
	if spec.RawBody != nil {
		reqBody = append([]byte(nil), spec.RawBody...)
	} else {
		var err error
		reqBody, err = json.Marshal(spec.Payload)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal %s request: %w", requestName, err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.URL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, fmt.Errorf("create %s request: %w", requestName, err)
	}

	req.Header.Set("Content-Type", "application/json")
	for key, value := range spec.ExtraHeaders {
		req.Header.Set(key, value)
	}

	if spec.Configure != nil {
		spec.Configure(req)
	}
	if spec.ConfigureWithError != nil {
		if err := spec.ConfigureWithError(req); err != nil {
			return nil, nil, fmt.Errorf("configure %s request: %w", requestName, err)
		}
	}

	return req, reqBody, nil
}

func (s *Server) sendProviderJSONRequest(ctx context.Context, spec providerJSONRequest) (*http.Response, []byte, error) {
	req, reqBody, err := buildProviderJSONRequest(ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	requestName := spec.RequestName
	if requestName == "" {
		requestName = spec.Provider
	}
	logWireHTTPRequest(ctx, "WIRE BACKEND REQUEST "+requestName, req, reqBody)
	backendEndpoint := req.URL.String()
	logger.From(ctx).Infof("Backend request started: %s %s %s", requestName, req.Method, backendEndpoint)

	start := time.Now()
	resp, err := s.client.Do(ctx, req, spec.Provider)
	if err != nil {
		return nil, reqBody, fmt.Errorf("send %s request: %w", requestName, err)
	}
	logWireHTTPResponseHeaders(ctx, "WIRE BACKEND RESPONSE HEADERS "+requestName, resp)
	wrapWireLoggedResponseBody(ctx, "WIRE BACKEND RESPONSE BODY "+requestName, resp)
	wrapBackendResponseLifecycleLog(ctx, requestName, req.Method, backendEndpoint, resp, start, isBackendStreamResponse(req, resp))

	return resp, reqBody, nil
}

func wrapBackendResponseLifecycleLog(ctx context.Context, requestName, method, endpoint string, resp *http.Response, start time.Time, stream bool) {
	if resp == nil || resp.Body == nil {
		return
	}
	if stream {
		resp.Body = &backendStreamLogReadCloser{
			ReadCloser: resp.Body,
			ctx:        ctx,
			name:       requestName,
			method:     method,
			endpoint:   endpoint,
			status:     resp.StatusCode,
			start:      start,
		}
		return
	}
	resp.Body = &backendNonStreamLogReadCloser{
		ReadCloser: resp.Body,
		ctx:        ctx,
		name:       requestName,
		method:     method,
		endpoint:   endpoint,
		status:     resp.StatusCode,
		start:      start,
	}
}

func isBackendStreamResponse(req *http.Request, resp *http.Response) bool {
	if req != nil && strings.Contains(strings.ToLower(req.Header.Get("Accept")), "text/event-stream") {
		return true
	}
	if resp != nil && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return true
	}
	return false
}

type backendNonStreamLogReadCloser struct {
	io.ReadCloser
	ctx      context.Context
	name     string
	method   string
	endpoint string
	status   int
	start    time.Time
	once     sync.Once
	bytes    int64
}

func (r *backendNonStreamLogReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.bytes += int64(n)
	}
	if err == io.EOF {
		r.log()
	}
	return n, err
}

func (r *backendNonStreamLogReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.log()
	return err
}

func (r *backendNonStreamLogReadCloser) log() {
	r.once.Do(func() {
		logger.From(r.ctx).Infof("Backend response completed: %s %s %s | Status: %d | Bytes: %d | Duration: %s", r.name, r.method, r.endpoint, r.status, r.bytes, time.Since(r.start).Round(time.Millisecond))
	})
}

type backendStreamLogReadCloser struct {
	io.ReadCloser
	ctx      context.Context
	name     string
	method   string
	endpoint string
	status   int
	start    time.Time

	firstOnce sync.Once
	closeOnce sync.Once
	bytes     int64
}

func (r *backendStreamLogReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.bytes += int64(n)
		r.firstOnce.Do(func() {
			logger.From(r.ctx).Infof("Backend stream received first bytes: %s %s %s | Status: %d | Duration: %s", r.name, r.method, r.endpoint, r.status, time.Since(r.start).Round(time.Millisecond))
		})
	}
	if err == io.EOF {
		r.logClosed()
	}
	return n, err
}

func (r *backendStreamLogReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.logClosed()
	return err
}

func (r *backendStreamLogReadCloser) logClosed() {
	r.closeOnce.Do(func() {
		logger.From(r.ctx).Infof("Backend stream completed: %s %s %s | Status: %d | Bytes: %d | Duration: %s", r.name, r.method, r.endpoint, r.status, r.bytes, time.Since(r.start).Round(time.Millisecond))
	})
}
