package modelcatalog

import (
	"fmt"
	"lmtools/internal/core"
	"sort"
	"strings"
)

// Features is what a provider's model list says a model can do, in the terms
// a user picks lmc's flags by: token limits, inputs, thinking and effort,
// tools, structured output, and the APIs that serve it. Of the providers lmc
// lists, Anthropic, Google, and the ALCF inference gateway describe their
// models; a list without descriptions leaves Features empty.
type Features struct {
	ContextTokens    int64
	OutputTokens     int64
	Inputs           []string
	Thinking         bool
	ThinkingTypes    []string
	Efforts          []string
	DefaultEffort    string
	Tools            bool
	StructuredOutput bool
	APIs             []string
}

// Describe reads an item's features. Anthropic reports capabilities as
// {"name": {"supported": bool}}; the ALCF gateway as context_window_tokens,
// input_modalities, reasoning, tool_calling, and api_protocols; Google as
// token limits and a thinking flag.
func Describe(item Item) Features {
	f := Features{ContextTokens: item.MaxInputTokens, OutputTokens: item.MaxOutputTokens}
	caps := item.Capabilities

	// The ALCF gateway.
	if f.ContextTokens == 0 {
		f.ContextTokens = jsonInt(caps["context_window_tokens"])
	}
	f.Inputs = jsonStrings(caps["input_modalities"])
	if reasoning, ok := caps["reasoning"].(map[string]interface{}); ok {
		f.Thinking = supported(reasoning)
		f.Efforts = jsonStrings(reasoning["effort_levels"])
		f.DefaultEffort, _ = reasoning["default_effort"].(string)
	}
	f.Tools = supported(caps["tool_calling"])
	f.APIs = jsonStrings(caps["api_protocols"])

	// Anthropic.
	if supported(caps["image_input"]) || supported(caps["pdf_input"]) {
		f.Inputs = []string{"text"}
		if supported(caps["image_input"]) {
			f.Inputs = append(f.Inputs, "image")
		}
		if supported(caps["pdf_input"]) {
			f.Inputs = append(f.Inputs, "pdf")
		}
	}
	if thinking, ok := caps["thinking"].(map[string]interface{}); ok && supported(thinking) {
		f.Thinking = true
		f.ThinkingTypes = supportedKeys(thinking["types"], nil)
	}
	if effort, ok := caps["effort"].(map[string]interface{}); ok && supported(effort) {
		f.Efforts = supportedKeys(effort, core.EffortLevels)
	}
	f.StructuredOutput = supported(caps["structured_outputs"])

	// Google.
	if thinking, ok := item.Metadata["thinking"].(bool); ok && thinking {
		f.Thinking = true
	}
	return f
}

// String renders the features on one line, or "" when there are none.
func (f Features) String() string {
	var parts []string
	if f.ContextTokens > 0 {
		parts = append(parts, fmt.Sprintf("context %d tokens", f.ContextTokens))
	}
	if f.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("output %d tokens", f.OutputTokens))
	}
	if len(f.Inputs) > 0 {
		parts = append(parts, "input "+strings.Join(f.Inputs, ", "))
	}
	if f.Thinking {
		thinking := "thinking"
		if len(f.ThinkingTypes) > 0 {
			thinking += " (" + strings.Join(f.ThinkingTypes, ", ") + ")"
		}
		parts = append(parts, thinking)
	}
	if len(f.Efforts) > 0 {
		effort := "effort " + strings.Join(f.Efforts, ", ")
		if f.DefaultEffort != "" {
			effort += " (default " + f.DefaultEffort + ")"
		}
		parts = append(parts, effort)
	}
	if f.Tools {
		parts = append(parts, "tools")
	}
	if f.StructuredOutput {
		parts = append(parts, "structured output")
	}
	if len(f.APIs) > 0 {
		parts = append(parts, "APIs "+strings.Join(f.APIs, ", "))
	}
	return strings.Join(parts, "; ")
}

// supported reads {"supported": true}.
func supported(value interface{}) bool {
	fields, ok := value.(map[string]interface{})
	if !ok {
		return false
	}
	yes, _ := fields["supported"].(bool)
	return yes
}

// supportedKeys lists the keys of an Anthropic capability whose value is
// supported, in the order given, then any others alphabetically.
func supportedKeys(value interface{}, order []string) []string {
	fields, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	var keys []string
	seen := make(map[string]bool)
	for _, key := range order {
		if supported(fields[key]) {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var rest []string
	for key, entry := range fields {
		if !seen[key] && supported(entry) {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func jsonStrings(value interface{}) []string {
	list, ok := value.([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, entry := range list {
		if s, ok := entry.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func jsonInt(value interface{}) int64 {
	if n, ok := value.(float64); ok && n > 0 {
		return int64(n)
	}
	return 0
}
