package proxy

import (
	"context"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
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
	if s.useLegacyArgo() {
		argoResp, err := s.forwardToArgo(ctx, anthReq)
		if err != nil {
			return nil, err
		}
		return s.converter.ConvertArgoToAnthropicWithRequest(argoResp, originalModel, anthReq), nil
	}

	switch providers.DetermineArgoModelProvider(anthReq.Model) {
	case constants.ProviderAnthropic:
		return s.forwardToArgoAnthropic(ctx, anthReq)
	default:
		openAIResp, err := s.forwardToArgoOpenAI(ctx, anthReq)
		if err != nil {
			return nil, err
		}
		return s.converter.ConvertOpenAIToAnthropic(openAIResp, originalModel), nil
	}
}

func (s *Server) forwardAnthropicViaAnthropic(ctx context.Context, anthReq *AnthropicRequest, _ string) (*AnthropicResponse, error) {
	return s.forwardToAnthropic(ctx, anthReq)
}
