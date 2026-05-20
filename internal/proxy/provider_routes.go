package proxy

import (
	"context"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
)

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
