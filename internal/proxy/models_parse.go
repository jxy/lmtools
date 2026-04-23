package proxy

import (
	"context"
	"lmtools/internal/modelcatalog"
)

func catalogItemsToModelItems(items []modelcatalog.Item) []ModelItem {
	models := make([]ModelItem, 0, len(items))
	for _, item := range items {
		models = append(models, ModelItem{
			ID:                         item.ID,
			Object:                     item.Object,
			Created:                    item.Created,
			OwnedBy:                    item.OwnedBy,
			DisplayName:                item.DisplayName,
			CreatedAt:                  item.CreatedAt,
			MaxInputTokens:             item.MaxInputTokens,
			MaxOutputTokens:            item.MaxOutputTokens,
			SupportedGenerationMethods: append([]string(nil), item.SupportedGenerationMethods...),
			Capabilities:               item.Capabilities,
			Metadata:                   item.Metadata,
		})
	}
	return models
}

func (s *Server) parseArgoModelsForProvider(ctx context.Context, data []byte) ([]ModelItem, error) {
	warnUnknownFields(ctx, data, modelcatalog.ArgoModelsResponse{}, "Argo models response")
	return s.parseArgoModels(data)
}

// parseArgoModels parses Argo's models response format.
func (s *Server) parseArgoModels(data []byte) ([]ModelItem, error) {
	items, err := modelcatalog.ParseArgo(data)
	if err != nil {
		return nil, err
	}
	return catalogItemsToModelItems(items), nil
}

func (s *Server) parseOpenAIModelsForProvider(ctx context.Context, data []byte) ([]ModelItem, error) {
	warnUnknownFields(ctx, data, modelcatalog.OpenAIModelsResponse{}, "OpenAI models response")
	return s.parseOpenAIModels(data)
}

// parseOpenAIModels parses OpenAI's models response format.
func (s *Server) parseOpenAIModels(data []byte) ([]ModelItem, error) {
	items, err := modelcatalog.ParseOpenAI(data)
	if err != nil {
		return nil, err
	}
	return catalogItemsToModelItems(items), nil
}

func (s *Server) parseGoogleModelsForProvider(ctx context.Context, data []byte) ([]ModelItem, error) {
	warnUnknownFields(ctx, data, modelcatalog.GoogleModelsResponse{}, "Google models response")
	return s.parseGoogleModels(data)
}

// parseGoogleModels parses Google's models response format.
func (s *Server) parseGoogleModels(data []byte) ([]ModelItem, error) {
	items, err := modelcatalog.ParseGoogle(data)
	if err != nil {
		return nil, err
	}
	return catalogItemsToModelItems(items), nil
}

func (s *Server) parseAnthropicModelsForProvider(ctx context.Context, data []byte) ([]ModelItem, error) {
	warnUnknownFields(ctx, data, modelcatalog.AnthropicModelsResponse{}, "Anthropic models response")
	return s.parseAnthropicModels(data)
}

// parseAnthropicModels parses Anthropic's models response format.
func (s *Server) parseAnthropicModels(data []byte) ([]ModelItem, error) {
	items, err := modelcatalog.ParseAnthropic(data)
	if err != nil {
		return nil, err
	}
	return catalogItemsToModelItems(items), nil
}
