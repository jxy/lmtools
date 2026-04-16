package core

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"net/http"
)

type argoChatRequestPlan struct {
	User       string
	Model      string
	Messages   []TypedMessage
	Tools      interface{}
	ToolChoice interface{}
	Stream     bool
	Endpoint   string
}

func buildArgoChatRequest(cfg ChatRequestConfig, messages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	if err := validateMessagesForProvider(constants.ProviderArgo, messages); err != nil {
		return nil, nil, err
	}
	plan, err := newArgoChatRequestPlan(cfg, messages, model, system, toolDefs, toolChoice, stream)
	if err != nil {
		return nil, nil, err
	}
	req := map[string]interface{}{
		"user":     plan.User,
		"model":    plan.Model,
		"messages": marshalArgoTypedMessages(plan.Model, plan.Messages),
	}
	addToolFields(req, PreparedRequestPayload{Tools: plan.Tools, ToolChoice: plan.ToolChoice})

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	return buildProviderRequest(cfg, plan.Endpoint, body, constants.ProviderArgo, plan.Stream)
}

func newArgoChatRequestPlan(cfg ChatRequestConfig, messages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (argoChatRequestPlan, error) {
	if model == "" {
		model = GetDefaultChatModel(constants.ProviderArgo)
	}

	plan := argoChatRequestPlan{
		User:     cfg.GetUser(),
		Model:    model,
		Messages: PrependSystemMessage(messages, system),
		Stream:   stream && len(toolDefs) == 0,
	}
	if len(toolDefs) > 0 {
		converted := providerSpecForModel(model).convertToolsForRequest(toolDefs, toolChoice)
		plan.Tools = converted.Tools
		plan.ToolChoice = converted.ToolChoice
	}
	endpoint, err := providers.ResolveChatURL(constants.ProviderArgo, "", cfg.GetEnv(), "", plan.Stream)
	if err != nil {
		return argoChatRequestPlan{}, err
	}
	plan.Endpoint = endpoint
	return plan, nil
}
