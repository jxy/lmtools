package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type providerJSONRequest struct {
	URL          string
	Provider     string
	RequestName  string
	Payload      interface{}
	ExtraHeaders map[string]string
	Configure    func(*http.Request) error
}

func noErrorRequestConfigurer(fn func(*http.Request)) func(*http.Request) error {
	if fn == nil {
		return nil
	}
	return func(req *http.Request) error {
		fn(req)
		return nil
	}
}

func buildProviderJSONRequest(ctx context.Context, spec providerJSONRequest) (*http.Request, []byte, error) {
	requestName := spec.RequestName
	if requestName == "" {
		requestName = spec.Provider
	}

	reqBody, err := json.Marshal(spec.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal %s request: %w", requestName, err)
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
		if err := spec.Configure(req); err != nil {
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

	resp, err := s.client.Do(ctx, req, spec.Provider)
	if err != nil {
		return nil, reqBody, fmt.Errorf("send %s request: %w", requestName, err)
	}
	logWireHTTPResponseHeaders(ctx, "WIRE BACKEND RESPONSE HEADERS "+requestName, resp)
	wrapWireLoggedResponseBody(ctx, "WIRE BACKEND RESPONSE BODY "+requestName, resp)

	return resp, reqBody, nil
}
