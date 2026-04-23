package proxy

import (
	"lmtools/internal/core"
	"strings"
)

func combineInstructionText(system, developer string) string {
	system = strings.TrimSpace(system)
	developer = strings.TrimSpace(developer)
	switch {
	case system == "":
		return developer
	case developer == "":
		return system
	default:
		return system + "\n" + developer
	}
}

func prependOpenAIInstructionMessages(messages []core.TypedMessage, system, developer, model string) []core.TypedMessage {
	instructions := make([]core.TypedMessage, 0, 2)
	system = strings.TrimSpace(system)
	developer = strings.TrimSpace(developer)

	if system != "" {
		role := string(core.RoleSystem)
		if developer == "" && openAIModelUsesDeveloperRole(model) {
			role = string(core.RoleDeveloper)
		}
		instructions = append(instructions, textInstructionMessage(role, system))
	}
	if developer != "" {
		instructions = append(instructions, textInstructionMessage(string(core.RoleDeveloper), developer))
	}
	if len(instructions) == 0 {
		return messages
	}
	out := make([]core.TypedMessage, 0, len(instructions)+len(messages))
	out = append(out, instructions...)
	out = append(out, messages...)
	return out
}

func textInstructionMessage(role, text string) core.TypedMessage {
	return core.TypedMessage{
		Role: role,
		Blocks: []core.Block{
			core.TextBlock{Text: text},
		},
	}
}

func prepareOutOfBandInstructionMessages(messages []core.TypedMessage, system, developer string) (string, []core.TypedMessage) {
	systemParts := make([]string, 0, len(messages)+1)
	if base := combineInstructionText(system, developer); base != "" {
		systemParts = append(systemParts, base)
	}

	leading, rest := splitLeadingInstructionMessages(messages)
	if text := typedMessagesText(leading); text != "" {
		systemParts = append(systemParts, text)
	}

	return strings.Join(systemParts, "\n"), coerceInstructionMessagesToUser(rest)
}

func splitLeadingInstructionMessages(messages []core.TypedMessage) ([]core.TypedMessage, []core.TypedMessage) {
	end := 0
	for end < len(messages) && isCollapsibleInstructionMessage(messages[end]) {
		end++
	}
	return messages[:end], messages[end:]
}

func typedMessagesText(messages []core.TypedMessage) string {
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		if text := typedMessageText(msg); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func coerceInstructionMessagesToUser(messages []core.TypedMessage) []core.TypedMessage {
	if len(messages) == 0 {
		return nil
	}

	normalized := make([]core.TypedMessage, 0, len(messages))
	for _, msg := range messages {
		if !isInstructionRole(msg.Role) {
			normalized = append(normalized, msg)
			continue
		}
		msg.Role = string(core.RoleUser)
		normalized = append(normalized, msg)
	}
	return normalized
}

func isCollapsibleInstructionMessage(msg core.TypedMessage) bool {
	if !isInstructionRole(msg.Role) || len(msg.Blocks) == 0 {
		return false
	}

	hasText := false
	for _, block := range msg.Blocks {
		switch value := block.(type) {
		case core.TextBlock:
			if value.Text != "" {
				hasText = true
			}
		case *core.TextBlock:
			if value != nil && value.Text != "" {
				hasText = true
			}
		default:
			return false
		}
	}
	return hasText
}

func isInstructionRole(role string) bool {
	return role == string(core.RoleSystem) || role == string(core.RoleDeveloper)
}

func mergeAnthropicOutputConfig(cfg *AnthropicOutputConfig, responseFormat *ResponseFormat, reasoningEffort string) *AnthropicOutputConfig {
	var merged *AnthropicOutputConfig
	if cfg != nil {
		merged = &AnthropicOutputConfig{
			Effort: cfg.Effort,
			Format: cfg.Format,
		}
	}

	if responseFormat != nil {
		if merged == nil {
			merged = &AnthropicOutputConfig{}
		}
		if merged.Format == nil {
			merged.Format = openAIResponseFormatToAnthropicFormat(responseFormat)
		}
	}

	if effort := reasoningEffortToAnthropicEffort(reasoningEffort); effort != "" {
		if merged == nil {
			merged = &AnthropicOutputConfig{}
		}
		if merged.Effort == "" {
			merged.Effort = effort
		}
	}

	if merged == nil || (merged.Effort == "" && merged.Format == nil) {
		return nil
	}
	return merged
}

func reasoningEffortToAnthropicEffort(reasoningEffort string) string {
	switch strings.ToLower(strings.TrimSpace(reasoningEffort)) {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	default:
		return ""
	}
}

func anthropicEffortToOpenAIReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "max":
		return "xhigh"
	default:
		return ""
	}
}

func openAIResponseFormatToAnthropicFormat(responseFormat *ResponseFormat) interface{} {
	if responseFormat == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(responseFormat.Type)) {
	case "json_object":
		return map[string]interface{}{"type": "json_object"}
	case "json_schema":
		format := map[string]interface{}{"type": "json_schema"}
		if responseFormat.JSONSchema != nil && responseFormat.JSONSchema.Schema != nil {
			format["schema"] = responseFormat.JSONSchema.Schema
		}
		return format
	default:
		return nil
	}
}

func anthropicOutputConfigToOpenAIResponseFormat(outputConfig *AnthropicOutputConfig) *ResponseFormat {
	if outputConfig == nil || outputConfig.Format == nil {
		return nil
	}
	format, ok := outputConfig.Format.(map[string]interface{})
	if !ok {
		return nil
	}
	formatType, _ := format["type"].(string)
	switch strings.ToLower(strings.TrimSpace(formatType)) {
	case "json_object":
		return &ResponseFormat{Type: "json_object"}
	case "json_schema":
		jsonSchema := &OpenAIJSONSchema{Name: "response"}
		if name, ok := format["name"].(string); ok && strings.TrimSpace(name) != "" {
			jsonSchema.Name = name
		}
		if description, ok := format["description"].(string); ok {
			jsonSchema.Description = description
		}
		if schema, ok := format["schema"]; ok {
			jsonSchema.Schema = schema
		}
		if strict, ok := format["strict"].(bool); ok {
			jsonSchema.Strict = &strict
		}
		return &ResponseFormat{Type: "json_schema", JSONSchema: jsonSchema}
	default:
		return nil
	}
}

func serviceTierForAnthropic(serviceTier string) string {
	switch strings.ToLower(strings.TrimSpace(serviceTier)) {
	case "":
		return ""
	case "auto":
		return "auto"
	case "default", "standard", "standard_only":
		return "standard_only"
	case "priority":
		return "auto"
	default:
		return ""
	}
}

func serviceTierForOpenAI(serviceTier string) string {
	switch strings.ToLower(strings.TrimSpace(serviceTier)) {
	case "":
		return ""
	case "auto", "default", "flex", "priority":
		return strings.ToLower(strings.TrimSpace(serviceTier))
	case "standard", "standard_only":
		return "default"
	default:
		return ""
	}
}

func applyResponseFormatToGoogleConfig(config *GoogleGenConfig, responseFormat *ResponseFormat) {
	if config == nil || responseFormat == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(responseFormat.Type)) {
	case "json_object":
		config.ResponseMIMEType = "application/json"
	case "json_schema":
		config.ResponseMIMEType = "application/json"
		if responseFormat.JSONSchema != nil && responseFormat.JSONSchema.Schema != nil {
			config.ResponseJSONSchema = responseFormat.JSONSchema.Schema
		}
	}
}

func googleThinkingConfigForReasoning(model, reasoningEffort string) *GoogleThinkingConfig {
	effort := strings.ToLower(strings.TrimSpace(reasoningEffort))
	if effort == "" {
		return nil
	}

	modelLower := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(modelLower, "gemini-2.5") {
		budget := googleThinkingBudgetForEffort(effort)
		if budget == nil {
			return nil
		}
		return &GoogleThinkingConfig{ThinkingBudget: budget}
	}

	level := googleThinkingLevelForEffort(effort)
	if level == "" {
		return nil
	}
	return &GoogleThinkingConfig{ThinkingLevel: level}
}

func googleThinkingBudgetForEffort(effort string) *int {
	var budget int
	switch effort {
	case "none":
		budget = 0
	case "minimal", "low":
		budget = 1024
	case "medium":
		budget = 8192
	case "high", "xhigh":
		budget = 24576
	default:
		return nil
	}
	return &budget
}

func googleThinkingLevelForEffort(effort string) string {
	switch effort {
	case "minimal":
		return "minimal"
	case "low", "medium", "high":
		return effort
	case "xhigh":
		return "high"
	default:
		return ""
	}
}

func googleToolConfigFromChoice(choice *core.ToolChoice) *GoogleToolConfig {
	config := GoogleFunctionConfig{Mode: "AUTO"}
	if choice != nil {
		switch choice.Type {
		case "none":
			config.Mode = "NONE"
		case "tool":
			config.Mode = "ANY"
			if choice.Name != "" {
				config.AllowedFunctionNames = []string{choice.Name}
			}
		}
	}
	return &GoogleToolConfig{FunctionCallingConfig: config}
}
