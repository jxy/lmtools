package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Usage captures the token counts a provider reported for one response.
// Fields are pointers so a count the provider did not report stays
// distinguishable from a reported zero; Anthropic reports no total, and
// legacy Argo reports nothing at all.
type Usage struct {
	InputTokens              *int
	OutputTokens             *int
	TotalTokens              *int
	CacheReadInputTokens     *int
	CacheCreationInputTokens *int
	ReasoningTokens          *int
	cacheReadIncluded        bool
	reasoningIncluded        bool
}

// usageEnvelope matches every location the supported providers put token
// counts: top-level `usage` (Anthropic Messages, OpenAI Chat Completions and
// embeddings, OpenAI Responses), top-level `usageMetadata` (Google),
// `message.usage` (Anthropic `message_start` stream events), and
// `response.usage` (OpenAI Responses stream events). The nested fields stay
// raw so a `message` or `response` value that is not an object — legacy
// Argo's string response — cannot fail the decode.
type usageEnvelope struct {
	Usage         json.RawMessage `json:"usage"`
	UsageMetadata json.RawMessage `json:"usageMetadata"`
	Message       json.RawMessage `json:"message"`
	Response      json.RawMessage `json:"response"`
}

// wireUsage is the union of the `usage` object spellings on the supported
// wires. The key sets are disjoint — OpenAI Chat Completions counts
// prompt/completion tokens, Anthropic and OpenAI Responses count input/output
// tokens, and each wire nests its details under its own key — so one decode
// serves every `usage` object without knowing which provider wrote it.
type wireUsage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
	InputTokens      *int `json:"input_tokens"`
	OutputTokens     *int `json:"output_tokens"`
	TotalTokens      *int `json:"total_tokens"`

	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`

	PromptTokensDetails struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	InputTokensDetails struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
		ThinkingTokens  *int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// UsageFromPayload extracts token counts from a provider response body or
// stream event payload, and returns nil when the payload carries none.
// Display-only: nothing downstream branches on it, so an unrecognized shape
// degrades to "no note" rather than to an error.
func UsageFromPayload(data []byte) *Usage {
	envelope, ok := decodeUsageEnvelope(data)
	if !ok {
		return nil
	}
	if usage := usageFromWireObject(envelope.Usage); usage != nil {
		return usage
	}
	if usage := usageFromGoogleMetadata(envelope.UsageMetadata); usage != nil {
		return usage
	}
	// One nesting level covers the two stream envelopes; the inner objects put
	// their counts under `usage` directly.
	for _, nested := range [][]byte{envelope.Message, envelope.Response} {
		inner, ok := decodeUsageEnvelope(nested)
		if !ok {
			continue
		}
		if usage := usageFromWireObject(inner.Usage); usage != nil {
			return usage
		}
	}
	return nil
}

func decodeUsageEnvelope(data []byte) (usageEnvelope, bool) {
	var envelope usageEnvelope
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return envelope, false
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return usageEnvelope{}, false
	}
	return envelope, true
}

func usageFromWireObject(raw json.RawMessage) *Usage {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var wire wireUsage
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil
	}

	usage := &Usage{
		InputTokens:              firstReported(wire.InputTokens, wire.PromptTokens),
		OutputTokens:             firstReported(wire.OutputTokens, wire.CompletionTokens),
		TotalTokens:              wire.TotalTokens,
		CacheReadInputTokens:     firstReported(wire.CacheReadInputTokens, wire.PromptTokensDetails.CachedTokens, wire.InputTokensDetails.CachedTokens),
		CacheCreationInputTokens: wire.CacheCreationInputTokens,
		ReasoningTokens: firstReported(
			wire.CompletionTokensDetails.ReasoningTokens,
			wire.OutputTokensDetails.ReasoningTokens,
			wire.OutputTokensDetails.ThinkingTokens,
		),
	}
	usage.cacheReadIncluded = wire.CacheReadInputTokens == nil && (wire.PromptTokensDetails.CachedTokens != nil || wire.InputTokensDetails.CachedTokens != nil)
	usage.reasoningIncluded = wire.CompletionTokensDetails.ReasoningTokens != nil || wire.OutputTokensDetails.ReasoningTokens != nil || wire.OutputTokensDetails.ThinkingTokens != nil
	if !usage.hasAny() {
		return nil
	}
	return usage
}

func usageFromGoogleMetadata(raw json.RawMessage) *Usage {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var meta struct {
		PromptTokenCount        *int `json:"promptTokenCount"`
		CandidatesTokenCount    *int `json:"candidatesTokenCount"`
		TotalTokenCount         *int `json:"totalTokenCount"`
		CachedContentTokenCount *int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      *int `json:"thoughtsTokenCount"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil
	}

	usage := &Usage{
		InputTokens:          meta.PromptTokenCount,
		OutputTokens:         meta.CandidatesTokenCount,
		TotalTokens:          meta.TotalTokenCount,
		CacheReadInputTokens: meta.CachedContentTokenCount,
		ReasoningTokens:      meta.ThoughtsTokenCount,
	}
	if !usage.hasAny() {
		return nil
	}
	return usage
}

func firstReported(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (u *Usage) hasAny() bool {
	return u.InputTokens != nil || u.OutputTokens != nil || u.TotalTokens != nil ||
		u.CacheReadInputTokens != nil || u.CacheCreationInputTokens != nil || u.ReasoningTokens != nil
}

// MergedWith returns the union of u and next, preferring next where both
// report a count. Streams need the merge because providers split one
// response's counts across events: Anthropic reports input tokens on
// `message_start` and final output tokens on `message_delta`.
func (u *Usage) MergedWith(next *Usage) *Usage {
	if u == nil {
		return next
	}
	if next == nil {
		return u
	}
	merged := *u
	merged.InputTokens = firstReported(next.InputTokens, u.InputTokens)
	merged.OutputTokens = firstReported(next.OutputTokens, u.OutputTokens)
	merged.TotalTokens = firstReported(next.TotalTokens, u.TotalTokens)
	merged.CacheReadInputTokens = firstReported(next.CacheReadInputTokens, u.CacheReadInputTokens)
	merged.CacheCreationInputTokens = firstReported(next.CacheCreationInputTokens, u.CacheCreationInputTokens)
	merged.ReasoningTokens = firstReported(next.ReasoningTokens, u.ReasoningTokens)
	if next.CacheReadInputTokens != nil {
		merged.cacheReadIncluded = next.cacheReadIncluded
	}
	if next.ReasoningTokens != nil {
		merged.reasoningIncluded = next.reasoningIncluded
	}
	return &merged
}

// DisplaySummary groups details only when the wire explicitly reports them
// within input/output token details. Other counts retain their reported scope.
func (u *Usage) DisplaySummary() string {
	if u == nil {
		return ""
	}
	var parts []string
	add := func(label string, value *int, detail string, count *int, included bool) {
		if value == nil {
			return
		}
		part := fmt.Sprintf("%s %d", label, *value)
		if included && count != nil && *count != 0 {
			part += fmt.Sprintf(" (%s %d)", detail, *count)
		}
		parts = append(parts, part)
	}
	add("input", u.InputTokens, "cached", u.CacheReadInputTokens, u.cacheReadIncluded)
	add("output", u.OutputTokens, "reasoning", u.ReasoningTokens, u.reasoningIncluded)
	detail := func(label string, value *int, grouped bool) {
		if !grouped && value != nil && *value != 0 {
			add(label, value, "", nil, false)
		}
	}
	detail("cache read", u.CacheReadInputTokens, u.cacheReadIncluded && u.InputTokens != nil)
	detail("cache write", u.CacheCreationInputTokens, false)
	detail("reasoning", u.ReasoningTokens, u.reasoningIncluded && u.OutputTokens != nil)
	add("total", u.TotalTokens, "", nil, false)
	return strings.Join(parts, " | ")
}

// notifyTokenUsage reports a response's token counts on the diagnostic
// stream. It runs after response presentation is finalized and before any tool
// approval prompt or execution the response goes on to trigger.
func notifyTokenUsage(notifier Notifier, usage *Usage) {
	if notifier == nil {
		return
	}
	if reporter, ok := notifier.(interface{ ReportUsage(*Usage) }); ok {
		reporter.ReportUsage(usage)
		return
	}
	if usage != nil {
		notifier.Infof("Tokens: %s", usage.DisplaySummary())
	}
}
