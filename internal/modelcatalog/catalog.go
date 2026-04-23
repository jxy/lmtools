package modelcatalog

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"sort"
	"strings"
	"time"
)

type Item struct {
	ID                         string
	Object                     string
	Created                    int64
	OwnedBy                    string
	DisplayName                string
	CreatedAt                  string
	MaxInputTokens             int64
	MaxOutputTokens            int64
	SupportedGenerationMethods []string
	Capabilities               map[string]interface{}
	Metadata                   map[string]interface{}
}

type OpenAIModelsResponse struct {
	Object string            `json:"object"`
	Data   []OpenAIModelInfo `json:"data"`
}

type OpenAIModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ArgoModelsResponse struct {
	Object string          `json:"object"`
	Data   []ArgoModelInfo `json:"data"`
	Models []ArgoModelInfo `json:"models"`
}

type ArgoModelInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	InternalID string `json:"internal_id"`
	Object     string `json:"object"`
	Created    int64  `json:"created"`
	OwnedBy    string `json:"owned_by"`
}

type GoogleModelsResponse struct {
	Models        []GoogleModelInfo `json:"models"`
	NextPageToken string            `json:"nextPageToken"`
}

type GoogleModelInfo struct {
	Name                       string   `json:"name"`
	BaseModelID                string   `json:"baseModelId"`
	Version                    string   `json:"version"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	InputTokenLimit            int64    `json:"inputTokenLimit"`
	OutputTokenLimit           int64    `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	Thinking                   *bool    `json:"thinking"`
	Temperature                *float64 `json:"temperature"`
	MaxTemperature             *float64 `json:"maxTemperature"`
	TopP                       *float64 `json:"topP"`
	TopK                       *int64   `json:"topK"`
}

type AnthropicModelsResponse struct {
	Models  []AnthropicModelInfo `json:"models"`
	Data    []AnthropicModelInfo `json:"data"`
	FirstID string               `json:"first_id"`
	HasMore bool                 `json:"has_more"`
	LastID  string               `json:"last_id"`
}

type AnthropicModelInfo struct {
	ID             string                 `json:"id"`
	DisplayName    string                 `json:"display_name"`
	CreatedAt      string                 `json:"created_at"`
	MaxInputTokens int64                  `json:"max_input_tokens"`
	MaxTokens      int64                  `json:"max_tokens"`
	Type           string                 `json:"type"`
	Capabilities   map[string]interface{} `json:"capabilities"`
}

type Projection struct {
	Models []ProjectionItem `json:"models"`
}

type ProjectionItem struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	OwnedBy     string `json:"owned_by"`
	DisplayName string `json:"display_name,omitempty"`
}

func Parse(provider string, data []byte) ([]Item, error) {
	switch constants.NormalizeProvider(provider) {
	case constants.ProviderArgo:
		return ParseArgo(data)
	case constants.ProviderOpenAI:
		return ParseOpenAI(data)
	case constants.ProviderGoogle:
		return ParseGoogle(data)
	case constants.ProviderAnthropic:
		return ParseAnthropic(data)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

func ParseArgo(data []byte) ([]Item, error) {
	created := time.Now().Unix()

	var models []string
	if err := json.Unmarshal(data, &models); err == nil {
		return buildItems(constants.ProviderArgo, created, models, nil), nil
	}

	var response struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(data, &response); err == nil && len(response.Models) > 0 {
		return buildItems(constants.ProviderArgo, created, response.Models, nil), nil
	}

	var objectResponse ArgoModelsResponse
	if err := json.Unmarshal(data, &objectResponse); err == nil {
		if len(objectResponse.Data) > 0 {
			items := make([]structuredModel, 0, len(objectResponse.Data))
			for _, model := range objectResponse.Data {
				items = append(items, structuredModel{
					ID:          firstNonEmpty(model.InternalID, model.ID),
					DisplayName: structuredDisplayName(model.ID, model.Name, model.InternalID),
					OwnedBy:     firstNonEmpty(model.OwnedBy, constants.ProviderArgo),
					Created:     model.Created,
				})
			}
			return buildStructuredItems(created, items), nil
		}
		if len(objectResponse.Models) > 0 {
			items := make([]structuredModel, 0, len(objectResponse.Models))
			for _, model := range objectResponse.Models {
				items = append(items, structuredModel{
					ID:          firstNonEmpty(model.InternalID, model.ID),
					DisplayName: structuredDisplayName(model.ID, model.Name, model.InternalID),
					OwnedBy:     firstNonEmpty(model.OwnedBy, constants.ProviderArgo),
					Created:     model.Created,
				})
			}
			return buildStructuredItems(created, items), nil
		}
	}

	return nil, fmt.Errorf("unable to parse Argo models response")
}

func ParseOpenAI(data []byte) ([]Item, error) {
	var response OpenAIModelsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("parse OpenAI models: %w", err)
	}

	items := make([]Item, 0, len(response.Data))
	for _, model := range response.Data {
		object := model.Object
		if object == "" {
			object = "model"
		}
		owner := model.OwnedBy
		if owner == "" {
			owner = constants.ProviderOpenAI
		}
		items = append(items, Item{
			ID:      model.ID,
			Object:  object,
			Created: model.Created,
			OwnedBy: owner,
		})
	}
	return items, nil
}

func ParseGoogle(data []byte) ([]Item, error) {
	var response GoogleModelsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("parse Google models: %w", err)
	}

	items := make([]Item, 0, len(response.Models))
	for _, model := range response.Models {
		items = append(items, Item{
			ID:                         strings.TrimPrefix(model.Name, "models/"),
			Object:                     "model",
			OwnedBy:                    constants.ProviderGoogle,
			DisplayName:                model.DisplayName,
			MaxInputTokens:             model.InputTokenLimit,
			MaxOutputTokens:            model.OutputTokenLimit,
			SupportedGenerationMethods: append([]string(nil), model.SupportedGenerationMethods...),
			Metadata:                   googleModelMetadata(model),
		})
	}
	return items, nil
}

func ParseAnthropic(data []byte) ([]Item, error) {
	var response AnthropicModelsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("parse Anthropic models: %w", err)
	}

	models := response.Models
	if len(models) == 0 {
		models = response.Data
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no models found in Anthropic response")
	}

	items := make([]Item, 0, len(models))
	for _, model := range models {
		created := unixFromRFC3339(model.CreatedAt)
		items = append(items, Item{
			ID:              model.ID,
			Object:          firstNonEmpty(model.Type, "model"),
			Created:         created,
			OwnedBy:         constants.ProviderAnthropic,
			DisplayName:     model.DisplayName,
			CreatedAt:       model.CreatedAt,
			MaxInputTokens:  model.MaxInputTokens,
			MaxOutputTokens: model.MaxTokens,
			Capabilities:    cloneMap(model.Capabilities),
		})
	}
	return items, nil
}

func Sort(items []Item) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})
}

func Project(items []Item) Projection {
	sorted := append([]Item(nil), items...)
	Sort(sorted)

	projected := make([]ProjectionItem, 0, len(sorted))
	for _, item := range sorted {
		object := item.Object
		if object == "" {
			object = "model"
		}
		projected = append(projected, ProjectionItem{
			ID:          item.ID,
			Object:      object,
			OwnedBy:     item.OwnedBy,
			DisplayName: item.DisplayName,
		})
	}

	return Projection{Models: projected}
}

func buildItems(owner string, created int64, ids []string, names map[string]string) []Item {
	items := make([]Item, 0, len(ids))
	for _, id := range ids {
		items = append(items, Item{
			ID:          id,
			Object:      "model",
			Created:     created,
			OwnedBy:     owner,
			DisplayName: names[id],
		})
	}
	return items
}

type structuredModel struct {
	ID          string
	DisplayName string
	OwnedBy     string
	Created     int64
}

func buildStructuredItems(created int64, models []structuredModel) []Item {
	items := make([]Item, 0, len(models))
	for _, model := range models {
		modelCreated := model.Created
		if modelCreated == 0 {
			modelCreated = created
		}
		items = append(items, Item{
			ID:          model.ID,
			Object:      "model",
			Created:     modelCreated,
			OwnedBy:     model.OwnedBy,
			DisplayName: model.DisplayName,
		})
	}
	return items
}

func structuredDisplayName(id, name, internalID string) string {
	switch {
	case name != "":
		return name
	case internalID != "" && id != internalID:
		return id
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func unixFromRFC3339(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

func googleModelMetadata(model GoogleModelInfo) map[string]interface{} {
	metadata := make(map[string]interface{})
	addStringMetadata(metadata, "base_model_id", model.BaseModelID)
	addStringMetadata(metadata, "version", model.Version)
	addStringMetadata(metadata, "description", model.Description)
	addFloatMetadata(metadata, "temperature", model.Temperature)
	addFloatMetadata(metadata, "max_temperature", model.MaxTemperature)
	addFloatMetadata(metadata, "top_p", model.TopP)
	if model.TopK != nil {
		metadata["top_k"] = *model.TopK
	}
	if model.Thinking != nil {
		metadata["thinking"] = *model.Thinking
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func addStringMetadata(metadata map[string]interface{}, key, value string) {
	if value != "" {
		metadata[key] = value
	}
}

func addFloatMetadata(metadata map[string]interface{}, key string, value *float64) {
	if value != nil {
		metadata[key] = *value
	}
}

func cloneMap(values map[string]interface{}) map[string]interface{} {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
