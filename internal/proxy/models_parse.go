package proxy

import "lmtools/internal/modelcatalog"

func catalogItemsToModelItems(items []modelcatalog.Item) []ModelItem {
	models := make([]ModelItem, 0, len(items))
	for _, item := range items {
		models = append(models, ModelItem{
			ID:      item.ID,
			Object:  item.Object,
			Created: item.Created,
			OwnedBy: item.OwnedBy,
		})
	}
	return models
}

// parseArgoModels parses Argo's models response format.
func (s *Server) parseArgoModels(data []byte) ([]ModelItem, error) {
	items, err := modelcatalog.ParseArgo(data)
	if err != nil {
		return nil, err
	}
	return catalogItemsToModelItems(items), nil
}

// parseOpenAIModels parses OpenAI's models response format.
func (s *Server) parseOpenAIModels(data []byte) ([]ModelItem, error) {
	items, err := modelcatalog.ParseOpenAI(data)
	if err != nil {
		return nil, err
	}
	return catalogItemsToModelItems(items), nil
}

// parseGoogleModels parses Google's models response format.
func (s *Server) parseGoogleModels(data []byte) ([]ModelItem, error) {
	items, err := modelcatalog.ParseGoogle(data)
	if err != nil {
		return nil, err
	}
	return catalogItemsToModelItems(items), nil
}

// parseAnthropicModels parses Anthropic's models response format.
func (s *Server) parseAnthropicModels(data []byte) ([]ModelItem, error) {
	items, err := modelcatalog.ParseAnthropic(data)
	if err != nil {
		return nil, err
	}
	return catalogItemsToModelItems(items), nil
}
