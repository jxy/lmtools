package proxy

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"strings"
	"time"
)

// parseArgoModels parses Argo's models response format
func (s *Server) parseArgoModels(data []byte) ([]ModelItem, error) {
	var result []ModelItem
	// Create a single timestamp for all models in this response
	created := time.Now().Unix()

	// Try to parse as array first (old format)
	var models []string
	if err := json.Unmarshal(data, &models); err == nil {
		for _, model := range models {
			result = append(result, ModelItem{
				ID:      model,
				Object:  "model",
				Created: created,
				OwnedBy: constants.ProviderArgo,
			})
		}
		return result, nil
	}

	// Try to parse as object with models field
	var response struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(data, &response); err == nil && len(response.Models) > 0 {
		for _, model := range response.Models {
			result = append(result, ModelItem{
				ID:      model,
				Object:  "model",
				Created: created,
				OwnedBy: constants.ProviderArgo,
			})
		}
		return result, nil
	}

	// Try to parse as object with data field containing model objects
	var objectResponse struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &objectResponse); err == nil {
		if len(objectResponse.Data) > 0 {
			for _, model := range objectResponse.Data {
				result = append(result, ModelItem{
					ID:      model.ID,
					Object:  "model",
					Created: created,
					OwnedBy: constants.ProviderArgo,
				})
			}
			return result, nil
		}
		if len(objectResponse.Models) > 0 {
			for _, model := range objectResponse.Models {
				result = append(result, ModelItem{
					ID:      model.ID,
					Object:  "model",
					Created: created,
					OwnedBy: constants.ProviderArgo,
				})
			}
			return result, nil
		}
	}

	return nil, fmt.Errorf("unable to parse Argo models response")
}

// parseOpenAIModels parses OpenAI's models response format
func (s *Server) parseOpenAIModels(data []byte) ([]ModelItem, error) {
	var response struct {
		Data []ModelItem `json:"data"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("parse OpenAI models: %w", err)
	}

	return response.Data, nil
}

// parseGoogleModels parses Google's models response format
func (s *Server) parseGoogleModels(data []byte) ([]ModelItem, error) {
	var response struct {
		Models []struct {
			Name             string   `json:"name"`
			DisplayName      string   `json:"displayName"`
			SupportedMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("parse Google models: %w", err)
	}

	var result []ModelItem
	// Create a single timestamp for all models in this response
	created := time.Now().Unix()

	for _, model := range response.Models {
		// Extract model ID from name (e.g., "models/gemini-pro" -> "gemini-pro")
		modelID := strings.TrimPrefix(model.Name, "models/")

		result = append(result, ModelItem{
			ID:      modelID,
			Object:  "model",
			Created: created,
			OwnedBy: constants.ProviderGoogle,
		})
	}

	return result, nil
}

// parseAnthropicModels parses Anthropic's models response format
func (s *Server) parseAnthropicModels(data []byte) ([]ModelItem, error) {
	// Anthropic's models endpoint format may vary
	// Try to parse as a list of models
	var response struct {
		Models []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
		} `json:"models"`
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("parse Anthropic models: %w", err)
	}

	var result []ModelItem
	// Create a single timestamp for all models in this response
	created := time.Now().Unix()

	// Check which field has data
	models := response.Models
	if len(models) == 0 {
		models = response.Data
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("no models found in Anthropic response")
	}

	for _, model := range models {
		result = append(result, ModelItem{
			ID:      model.ID,
			Object:  "model",
			Created: created,
			OwnedBy: constants.ProviderAnthropic,
		})
	}

	return result, nil
}
