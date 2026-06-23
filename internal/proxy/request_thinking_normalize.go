package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"lmtools/internal/logger"
)

// normalizeAnthropicThinkingBlocks ensures every content block of type
// "thinking" carries a "thinking" field, inserting an empty string when the
// client omitted it. Each insertion is logged at WARN level.
//
// The Anthropic Messages API (and the Argo Anthropic-wire endpoint) reject a
// thinking block that carries only a signature without the "thinking" text
// field. Some clients replay assistant turns with the thinking text stripped,
// leaving blocks shaped like {"type":"thinking","signature":"..."}. Per-message
// content is forwarded verbatim as json.RawMessage on the Anthropic and Argo
// Anthropic paths, so the normalization rewrites the raw content bytes in place.
func normalizeAnthropicThinkingBlocks(ctx context.Context, messages []AnthropicMessage) {
	for i := range messages {
		fixed, fixedBlocks := normalizeThinkingRawContent(messages[i].Content)
		if len(fixedBlocks) == 0 {
			continue
		}
		messages[i].Content = fixed
		for _, blockIdx := range fixedBlocks {
			logger.From(ctx).Warnf("Inserted empty \"thinking\" field into thinking block missing it (message[%d] content block[%d])", i, blockIdx)
		}
	}
}

// normalizeThinkingRawContent rewrites a single message's raw content, inserting
// an empty "thinking" field into any thinking block that omits it. It returns
// the (possibly rewritten) content and the indices of the blocks that were
// changed. String or scalar content is left untouched.
func normalizeThinkingRawContent(content json.RawMessage) (json.RawMessage, []int) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return content, nil
	}

	switch trimmed[0] {
	case '[':
		var blocks []json.RawMessage
		if err := json.Unmarshal(trimmed, &blocks); err != nil {
			return content, nil
		}
		var fixedBlocks []int
		for j := range blocks {
			if fixed, ok := ensureThinkingField(blocks[j]); ok {
				blocks[j] = fixed
				fixedBlocks = append(fixedBlocks, j)
			}
		}
		if len(fixedBlocks) == 0 {
			return content, nil
		}
		rewritten, err := json.Marshal(blocks)
		if err != nil {
			return content, nil
		}
		return rewritten, fixedBlocks
	case '{':
		if fixed, ok := ensureThinkingField(trimmed); ok {
			return fixed, []int{0}
		}
		return content, nil
	default:
		return content, nil
	}
}

// ensureThinkingField inserts an empty "thinking" field into a single content
// block when it is a thinking block missing that field. It returns the
// (possibly rewritten) block and whether a change was made. All other fields are
// preserved with their original raw values; only redacted_thinking and thinking
// blocks that already include "thinking" are left untouched.
func ensureThinkingField(block json.RawMessage) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(block, &fields); err != nil {
		return block, false
	}

	typeRaw, ok := fields["type"]
	if !ok {
		return block, false
	}
	var blockType string
	if err := json.Unmarshal(typeRaw, &blockType); err != nil {
		return block, false
	}
	if blockType != "thinking" {
		return block, false
	}
	if _, exists := fields["thinking"]; exists {
		return block, false
	}

	fields["thinking"] = json.RawMessage(`""`)
	rewritten, err := json.Marshal(fields)
	if err != nil {
		return block, false
	}
	return rewritten, true
}
