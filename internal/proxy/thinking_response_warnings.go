package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
	"sort"
)

func warnMalformedAnthropicThinkingBlocks(ctx context.Context, body json.RawMessage, source string) {
	if len(body) == 0 {
		return
	}

	var value interface{}
	if err := json.Unmarshal(body, &value); err != nil {
		return
	}

	for _, path := range malformedAnthropicThinkingBlockPaths(value, "$") {
		warnMalformedThinkingBlock(ctx, source, path)
	}
}

// warnMalformedThinkingBlock emits the shared warning for a type=thinking block
// that is missing thinking content or a signature. location identifies where the
// block was found — a JSON path ("$.content[0]") for a buffered response, or an
// "index N" for a streamed block.
func warnMalformedThinkingBlock(ctx context.Context, source, location string) {
	logger.From(ctx).Warnf("%s response contains type=thinking block missing thinking or signature at %s", source, location)
}

func malformedAnthropicThinkingBlockPaths(value interface{}, path string) []string {
	var paths []string
	collectMalformedAnthropicThinkingBlockPaths(value, path, &paths)
	return paths
}

func collectMalformedAnthropicThinkingBlockPaths(value interface{}, path string, paths *[]string) {
	switch v := value.(type) {
	case map[string]interface{}:
		if blockType, _ := v["type"].(string); blockType == "thinking" {
			_, hasThinking := v["thinking"]
			_, hasSignature := v["signature"]
			if !hasThinking || !hasSignature {
				*paths = append(*paths, path)
			}
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		// Sort so emitted paths are stable regardless of Go's randomized map
		// iteration order (otherwise multiple malformed blocks warn in a random
		// order, producing flaky logs and tests).
		sort.Strings(keys)
		for _, key := range keys {
			collectMalformedAnthropicThinkingBlockPaths(v[key], path+"."+key, paths)
		}
	case []interface{}:
		for i, child := range v {
			collectMalformedAnthropicThinkingBlockPaths(child, fmt.Sprintf("%s[%d]", path, i), paths)
		}
	}
}

// thinkingStreamAuditor detects malformed thinking blocks in an Anthropic SSE
// stream. A thinking block streams across several events — content_block_start
// (empty thinking, no signature) → thinking_delta(s) → signature_delta →
// content_block_stop — so completeness can only be judged once the block closes.
// This auditor accumulates per-block-index progress from the already-parsed
// events and warns once, at content_block_stop, if the assembled block is
// missing thinking content or a signature (e.g. a provider that drops them
// mid-stream). Anthropic rejects such blocks when they are echoed back.
//
// It is driven from parseAnthropicStream's single-goroutine consume loop, so it
// needs no locking.
type thinkingStreamAuditor struct {
	blocks map[int]*thinkingBlockProgress
}

type thinkingBlockProgress struct {
	hasThinking  bool
	hasSignature bool
}

func newThinkingStreamAuditor() *thinkingStreamAuditor {
	return &thinkingStreamAuditor{blocks: make(map[int]*thinkingBlockProgress)}
}

// observeBlockStart begins tracking a thinking block. Non-thinking blocks
// (text, tool_use, redacted_thinking, ...) are ignored, matching the
// non-streaming check. Any content inlined on the start block is seeded.
func (a *thinkingStreamAuditor) observeBlockStart(index int, block AnthropicContentBlock) {
	if block.Type != "thinking" {
		return
	}
	a.blocks[index] = &thinkingBlockProgress{
		hasThinking:  block.Thinking != "",
		hasSignature: block.Signature != "",
	}
}

// observeDelta records thinking/signature content arriving via deltas for a
// tracked block. Deltas for untracked indices are ignored.
func (a *thinkingStreamAuditor) observeDelta(index int, delta DeltaContent) {
	progress := a.blocks[index]
	if progress == nil {
		return
	}
	switch delta.Type {
	case "thinking_delta":
		if delta.Thinking != "" {
			progress.hasThinking = true
		}
	case "signature_delta":
		if delta.Signature != "" {
			progress.hasSignature = true
		}
	}
}

// observeBlockStop finalizes a tracked thinking block and warns if it closed
// without both thinking content and a signature. A block that never closes
// (truncation / client disconnect) is intentionally not flagged: that is a
// different failure than a provider dropping a field.
func (a *thinkingStreamAuditor) observeBlockStop(ctx context.Context, index int, source string) {
	progress := a.blocks[index]
	if progress == nil {
		return
	}
	delete(a.blocks, index)
	if !progress.hasThinking || !progress.hasSignature {
		warnMalformedThinkingBlock(ctx, source, fmt.Sprintf("index %d", index))
	}
}
