package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
)

// responsesEncryptedStateRemoval records the two kinds of input item recovery
// mode drops. Both go whole. encrypted_content is optional on a reasoning item
// and required on a compaction item, but in both it is the entire model-visible
// content; what would be left of a reasoning item is its `rs_...` id, and an id
// is a handle on state the backend could not resolve in the first place — which
// is why the session needed recovery. Reasoning input is optional, so dropping
// the item replays the visible transcript without it.
type responsesEncryptedStateRemoval struct {
	ReasoningItems  int
	CompactionItems int
}

func (r responsesEncryptedStateRemoval) total() int {
	return r.ReasoningItems + r.CompactionItems
}

// prepareResponsesPassthroughRequestBody applies the two opt-in/request-specific
// mutations direct Responses passthrough permits. They share one decode and
// re-encode so a mapped recovery request does not duplicate another body-sized
// allocation. Requests needing neither mutation retain their original bytes.
func (s *Server) prepareResponsesPassthroughRequestBody(ctx context.Context, body []byte, mappedModel string, rewriteModel bool) ([]byte, error) {
	stripEncryptedState := s != nil && s.config != nil && s.config.StripEncryptedReasoning
	if !rewriteModel && !stripEncryptedState {
		return body, nil
	}

	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("decode Responses passthrough request: %w", err)
	}

	changed := false
	if rewriteModel {
		encodedModel, err := json.Marshal(mappedModel)
		if err != nil {
			return nil, fmt.Errorf("encode mapped Responses model: %w", err)
		}
		request["model"] = encodedModel
		changed = true
	}

	var removed responsesEncryptedStateRemoval
	if stripEncryptedState {
		if input, ok := request["input"]; ok {
			stripped, counts, err := stripEncryptedResponsesInput(input)
			if err != nil {
				return nil, fmt.Errorf("strip encrypted Responses input: %w", err)
			}
			removed = counts
			if counts.total() > 0 {
				request["input"] = stripped
				changed = true
			}
		}
	}

	if !changed {
		return body, nil
	}
	updated, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Responses passthrough request: %w", err)
	}
	if removed.total() > 0 {
		logger.From(ctx).Warnf("-strip-encrypted-reasoning dropped %d encrypted reasoning item(s) and %d encrypted compaction item(s) before direct Responses passthrough; prior opaque reasoning context is unavailable", removed.ReasoningItems, removed.CompactionItems)
	}
	return updated, nil
}

func stripEncryptedResponsesInput(input json.RawMessage) (json.RawMessage, responsesEncryptedStateRemoval, error) {
	var removed responsesEncryptedStateRemoval
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		// Responses also accepts a string input. Only the array form can carry
		// reasoning and compaction items, so all other valid shapes are unchanged.
		return input, removed, nil
	}

	stripped := make([]json.RawMessage, 0, len(items))
	for _, rawItem := range items {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &item); err != nil {
			stripped = append(stripped, rawItem)
			continue
		}
		var itemType string
		if err := json.Unmarshal(item["type"], &itemType); err != nil {
			stripped = append(stripped, rawItem)
			continue
		}
		if _, hasEncryptedContent := item["encrypted_content"]; !hasEncryptedContent {
			stripped = append(stripped, rawItem)
			continue
		}

		switch itemType {
		case "reasoning":
			removed.ReasoningItems++
		case "compaction":
			removed.CompactionItems++
		default:
			stripped = append(stripped, rawItem)
		}
	}

	if removed.total() == 0 {
		return input, removed, nil
	}
	updated, err := json.Marshal(stripped)
	if err != nil {
		return nil, removed, err
	}
	return updated, removed, nil
}
