package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
	"net/http"
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

type endpointErrorHandlers struct {
	MethodNotAllowed func()
	BadRequest       func(string)
	ConfigError      func(string)
	AuthError        func(string)
}

func (s *Server) decodeEndpointRequest(r *http.Request, dst interface{}) error {
	body, err := s.readRequestBody(r)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid JSON in request body")
	}
	return nil
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

func (s *Server) resolveEndpointRoute(ctx context.Context, model string, errs endpointErrorHandlers) (*endpointRoute, bool) {
	log := logger.From(ctx)
	provider := s.config.Provider
	if provider == "" {
		log.Errorf("No provider configured")
		if errs.ConfigError != nil {
			errs.ConfigError("No provider configured")
		}
		return nil, false
	}

	if hasCredentials, diagnostic := s.hasCredentials(provider); !hasCredentials {
		log.Errorf("No credentials configured for provider %s: %s", provider, diagnostic)
		if errs.AuthError != nil {
			errs.AuthError(diagnostic)
		}
		return nil, false
	}

	route := &endpointRoute{
		OriginalModel: model,
		MappedModel:   s.mapper.MapModel(model),
		Provider:      provider,
	}

	log.Infof("Model routing: %s -> provider=%s, mapped=%s",
		route.OriginalModel, route.Provider, route.MappedModel)

	return route, true
}

func (s *Server) handlePOSTEndpoint(
	w http.ResponseWriter,
	r *http.Request,
	endpointName string,
	parse func(*http.Request) (*endpointRequestInfo, error),
	errs endpointErrorHandlers,
) (*endpointRequestInfo, *endpointRoute, bool) {
	ctx := r.Context()
	log := logger.From(ctx)

	log.Infof("%s %s | %s", r.Method, r.URL.Path, endpointName)

	if r.Method != http.MethodPost {
		if errs.MethodNotAllowed != nil {
			errs.MethodNotAllowed()
		}
		return nil, nil, false
	}

	info, err := parse(r)
	if err != nil {
		log.Errorf("Failed to parse request: %s", err)
		if errs.BadRequest != nil {
			errs.BadRequest(err.Error())
		}
		return nil, nil, false
	}

	logEndpointRequest(ctx, *info)

	route, ok := s.resolveEndpointRoute(ctx, info.Model, errs)
	if !ok {
		return nil, nil, false
	}

	return info, route, true
}
