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
	ID          string
	Object      string
	Created     int64
	OwnedBy     string
	DisplayName string
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

	var objectResponse struct {
		Data []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			InternalID string `json:"internal_id"`
			OwnedBy    string `json:"owned_by"`
		} `json:"data"`
		Models []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			InternalID string `json:"internal_id"`
			OwnedBy    string `json:"owned_by"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &objectResponse); err == nil {
		if len(objectResponse.Data) > 0 {
			items := make([]structuredModel, 0, len(objectResponse.Data))
			for _, model := range objectResponse.Data {
				items = append(items, structuredModel{
					ID:          firstNonEmpty(model.InternalID, model.ID),
					DisplayName: structuredDisplayName(model.ID, model.Name, model.InternalID),
					OwnedBy:     firstNonEmpty(model.OwnedBy, constants.ProviderArgo),
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
				})
			}
			return buildStructuredItems(created, items), nil
		}
	}

	return nil, fmt.Errorf("unable to parse Argo models response")
}

func ParseOpenAI(data []byte) ([]Item, error) {
	var response struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
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
	var response struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("parse Google models: %w", err)
	}

	created := time.Now().Unix()
	items := make([]Item, 0, len(response.Models))
	for _, model := range response.Models {
		items = append(items, Item{
			ID:          strings.TrimPrefix(model.Name, "models/"),
			Object:      "model",
			Created:     created,
			OwnedBy:     constants.ProviderGoogle,
			DisplayName: model.DisplayName,
		})
	}
	return items, nil
}

func ParseAnthropic(data []byte) ([]Item, error) {
	var response struct {
		Models []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
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

	created := time.Now().Unix()
	items := make([]Item, 0, len(models))
	for _, model := range models {
		items = append(items, Item{
			ID:          model.ID,
			Object:      "model",
			Created:     created,
			OwnedBy:     constants.ProviderAnthropic,
			DisplayName: model.DisplayName,
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
}

func buildStructuredItems(created int64, models []structuredModel) []Item {
	items := make([]Item, 0, len(models))
	for _, model := range models {
		items = append(items, Item{
			ID:          model.ID,
			Object:      "model",
			Created:     created,
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
