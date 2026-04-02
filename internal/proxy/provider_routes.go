package proxy

import (
	"context"
)

type (
	anthropicResponseForwarder func(ctx context.Context, anthReq *AnthropicRequest, originalModel string) (*AnthropicResponse, error)
	anthropicStreamForwarder   func(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error
	openAIStreamForwarder      func(ctx context.Context, anthReq *AnthropicRequest, writer *OpenAIStreamWriter) error
)

func (s *Server) forwardAnthropicViaOpenAI(ctx context.Context, anthReq *AnthropicRequest, originalModel string) (*AnthropicResponse, error) {
	openAIResp, err := s.forwardToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, err
	}
	return s.converter.ConvertOpenAIToAnthropic(openAIResp, originalModel), nil
}

func (s *Server) forwardAnthropicViaGoogle(ctx context.Context, anthReq *AnthropicRequest, originalModel string) (*AnthropicResponse, error) {
	googleResp, err := s.forwardToGoogle(ctx, anthReq)
	if err != nil {
		return nil, err
	}
	return s.converter.ConvertGoogleToAnthropic(googleResp, originalModel), nil
}

func (s *Server) forwardAnthropicViaArgo(ctx context.Context, anthReq *AnthropicRequest, originalModel string) (*AnthropicResponse, error) {
	argoResp, err := s.forwardToArgo(ctx, anthReq)
	if err != nil {
		return nil, err
	}
	anthResp := s.converter.ConvertArgoToAnthropicWithRequest(argoResp, originalModel, anthReq)
	logToolUseBlocks(ctx, anthResp.Content, false)
	return anthResp, nil
}

func (s *Server) forwardAnthropicViaAnthropic(ctx context.Context, anthReq *AnthropicRequest, _ string) (*AnthropicResponse, error) {
	return s.forwardToAnthropic(ctx, anthReq)
}
