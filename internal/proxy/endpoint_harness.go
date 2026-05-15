package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"strings"
)

type endpointRequestInfo struct {
	Model        string
	Stream       bool
	MessageCount int
	ToolCount    int
	Payload      interface{}
	Tools        interface{}
}

type endpointRoute struct {
	OriginalModel string
	MappedModel   string
	Provider      string
}

type endpointRouteErrorKind string

const (
	endpointRouteConfigError endpointRouteErrorKind = "config"
	endpointRouteAuthError   endpointRouteErrorKind = "auth"
)

type endpointRouteError struct {
	Kind    endpointRouteErrorKind
	Message string
}

func (s *Server) decodeEndpointRequest(r *http.Request, dst interface{}) error {
	_, err := s.decodeEndpointRequestWithDisposition(r, dst, "ignored")
	return err
}

func (s *Server) decodeEndpointRequestWithDisposition(r *http.Request, dst interface{}, unknownFieldDisposition string) ([]byte, error) {
	body, err := s.readRequestBody(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	logWireHTTPRequest(r.Context(), "WIRE CLIENT REQUEST", r, body)
	warnUnknownFieldsWithDisposition(r.Context(), body, dst, "client request", unknownFieldDisposition)
	if err := decodeLenientJSON(body, dst); err != nil {
		return nil, err
	}
	return body, nil
}

func decodeLenientJSON(body []byte, dst interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(body))

	if err := decoder.Decode(dst); err != nil {
		return normalizeJSONDecodeError(err)
	}

	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid JSON in request body: unexpected trailing data")
		}
		return normalizeJSONDecodeError(err)
	}

	return nil
}

func decodeStrictJSON(body []byte, dst interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return normalizeJSONDecodeError(err)
	}

	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid JSON in request body: unexpected trailing data")
		}
		return normalizeJSONDecodeError(err)
	}

	return nil
}

func normalizeJSONDecodeError(err error) error {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Errorf("invalid JSON in request body")
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field != "" {
			return fmt.Errorf("invalid JSON in request body: wrong type for field %q", typeErr.Field)
		}
		return fmt.Errorf("invalid JSON in request body: wrong value type")
	}

	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		return fmt.Errorf("invalid JSON in request body: %s", strings.TrimPrefix(err.Error(), "json: "))
	}

	if errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("invalid JSON in request body")
	}

	return fmt.Errorf("invalid JSON in request body: %v", err)
}

func logEndpointRequest(ctx context.Context, info endpointRequestInfo) {
	log := logger.From(ctx)
	log.Debugf("Request received: model=%s, streaming=%v, messages=%d", info.Model, info.Stream, info.MessageCount)
	if info.Stream {
		logger.DebugJSON(log, "Streaming request details", info.Payload)
	} else {
		logger.DebugJSON(log, "Request details", info.Payload)
	}
	if info.ToolCount > 0 {
		logger.DebugJSON(log, "Tool information", info.Tools)
	}
}

func (s *Server) resolveEndpointRoute(ctx context.Context, model string) (*endpointRoute, *endpointRouteError) {
	log := logger.From(ctx)
	provider := s.config.Provider
	if provider == "" {
		log.Errorf("No provider configured")
		return nil, &endpointRouteError{Kind: endpointRouteConfigError, Message: "No provider configured"}
	}

	if hasCredentials, diagnostic := s.hasCredentials(provider); !hasCredentials {
		log.Errorf("No credentials configured for provider %s: %s", provider, diagnostic)
		return nil, &endpointRouteError{Kind: endpointRouteAuthError, Message: diagnostic}
	}

	route := &endpointRoute{
		OriginalModel: model,
		MappedModel:   s.mapper.MapModel(model),
		Provider:      provider,
	}

	log.Infof("Model routing: %s -> provider=%s, mapped=%s",
		route.OriginalModel, route.Provider, route.MappedModel)

	return route, nil
}
