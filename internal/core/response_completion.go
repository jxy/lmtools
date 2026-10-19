package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// prepareResponseCompletion retains partial text and reasoning while removing
// invocations that an incomplete response cannot authorize. This also keeps
// incomplete calls out of saved history, where they could otherwise be resumed.
func prepareResponseCompletion(cfg RequestOptions, response Response) Response {
	if cfg.Embed {
		return response
	}
	if response.IncompleteReason == "" && strings.TrimSpace(response.Text) == "" && len(response.ToolCalls) == 0 &&
		cfg.MaxTokens > 0 && response.Usage != nil && response.Usage.OutputTokens != nil && *response.Usage.OutputTokens >= cfg.MaxTokens {
		// Some compatible servers say completed after spending the whole
		// budget on thinking. A complete answer at the limit remains valid.
		response.IncompleteReason = "max_output_tokens"
	}
	if response.IncompleteReason == "" {
		return response
	}
	response.ToolCalls = nil
	blocks := make([]Block, 0, len(response.Blocks))
	for _, block := range response.Blocks {
		switch block.(type) {
		case ToolUseBlock, *ToolUseBlock:
			continue
		default:
			blocks = append(blocks, block)
		}
	}
	response.Blocks = blocks
	return response
}

// ValidateResponse reports an incomplete response or invalid JSON output.
// Schema constraints are enforced by the provider. Callers save any partial
// content before returning this error.
// Tool requests are intermediate responses and need not satisfy the final
// answer's JSON format.
func ValidateResponse(cfg RequestOptions, response Response) error {
	if cfg.Embed {
		return nil
	}
	switch response.IncompleteReason {
	case "":
	case "max_output_tokens":
		if strings.TrimSpace(response.Text) == "" {
			return fmt.Errorf("response incomplete: output token limit reached before an answer was produced; increase -max-tokens or reduce reasoning effort")
		}
		return fmt.Errorf("response incomplete: output token limit reached; partial answer retained; increase -max-tokens to allow a longer answer")
	case "content_filter":
		return fmt.Errorf("response incomplete: the provider stopped generation because of its content filter")
	case "invalid_stream":
		if response.CompletionError != nil {
			return fmt.Errorf("response incomplete: invalid stream event; partial content retained: %w", response.CompletionError)
		}
		return fmt.Errorf("response incomplete: invalid stream event; partial content retained")
	case "stream_interrupted":
		return fmt.Errorf("response incomplete: stream ended before the provider completed the response; partial content retained")
	default:
		return fmt.Errorf("response incomplete: provider reported %q", response.IncompleteReason)
	}
	if len(response.ToolCalls) > 0 {
		return nil
	}
	if strings.TrimSpace(response.Text) == "" && hasReasoning(response.Blocks) {
		return fmt.Errorf("provider returned thinking without an answer; check -max-tokens and the provider's reasoning settings")
	}
	if !cfg.JSONMode && len(cfg.JSONSchema) == 0 {
		return nil
	}
	if !json.Valid([]byte(response.Text)) {
		return fmt.Errorf("provider did not return the requested JSON; the endpoint may not support the requested output format")
	}
	if cfg.JSONMode && strings.TrimSpace(response.Text)[0] != '{' {
		return fmt.Errorf("provider returned JSON that is not an object required by -json")
	}
	return nil
}

func hasReasoning(blocks []Block) bool {
	for _, block := range blocks {
		switch block.(type) {
		case ReasoningBlock, *ReasoningBlock:
			return true
		}
	}
	return false
}
