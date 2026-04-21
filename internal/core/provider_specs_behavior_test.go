package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type providerSpecBehaviorLogger struct {
	dir string
}

func (l providerSpecBehaviorLogger) GetLogDir() string { return l.dir }

func (l providerSpecBehaviorLogger) LogJSON(string, string, []byte) error { return nil }

func (l providerSpecBehaviorLogger) CreateLogFile(logDir, prefix string) (*os.File, string, error) {
	f, err := os.CreateTemp(logDir, prefix+"-*.log")
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

func (providerSpecBehaviorLogger) Debugf(string, ...interface{}) {}

func (providerSpecBehaviorLogger) IsDebugEnabled() bool { return false }

func newProviderSpecTestConfig(provider, model, providerURL string) *TestRequestConfig {
	cfg := NewTestRequestConfig()
	cfg.Provider = provider
	cfg.Model = model
	cfg.ProviderURL = providerURL
	cfg.System = ""
	cfg.IsStreamChatMode = false
	cfg.IsToolEnabledFlag = false
	return cfg
}

func sampleProviderSpecToolDefs() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "get_weather",
			Description: "Get the current weather for a location",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"location": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []string{"location"},
			},
		},
	}
}

func decodeRequestBody(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("failed to decode request body: %v", err)
	}
	return req
}

func TestProviderSpecChatRequestBehavior(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		model       string
		providerURL string
		messages    []TypedMessage
		system      string
		verify      func(*testing.T, *http.Request, map[string]interface{})
	}{
		{
			name:        "openai keeps system inline",
			provider:    "openai",
			model:       "gpt-5",
			providerURL: "https://api.openai.com/v1",
			messages: []TypedMessage{
				NewTextMessage("system", "Be concise."),
				NewTextMessage("user", "Hello"),
			},
			verify: func(t *testing.T, req *http.Request, payload map[string]interface{}) {
				if got := req.URL.String(); got != "https://api.openai.com/v1/chat/completions" {
					t.Fatalf("URL = %q", got)
				}
				if _, ok := payload["system"]; ok {
					t.Fatal("unexpected top-level system field")
				}
				messages, ok := payload["messages"].([]interface{})
				if !ok || len(messages) != 2 {
					t.Fatalf("messages = %T len=%d, want 2 entries", payload["messages"], len(messages))
				}
				first, _ := messages[0].(map[string]interface{})
				if first["role"] != "system" {
					t.Fatalf("first role = %v, want system", first["role"])
				}
			},
		},
		{
			name:        "anthropic extracts top-level system",
			provider:    "anthropic",
			model:       "claude-opus-4-1-20250805",
			providerURL: "https://api.anthropic.com/v1",
			messages: []TypedMessage{
				NewTextMessage("system", "Be concise."),
				NewTextMessage("user", "Hello"),
			},
			verify: func(t *testing.T, req *http.Request, payload map[string]interface{}) {
				if got := req.URL.String(); got != "https://api.anthropic.com/v1/messages" {
					t.Fatalf("URL = %q", got)
				}
				if payload["system"] != "Be concise." {
					t.Fatalf("system = %v, want %q", payload["system"], "Be concise.")
				}
				messages, ok := payload["messages"].([]interface{})
				if !ok || len(messages) != 1 {
					t.Fatalf("messages = %T len=%d, want 1 entry", payload["messages"], len(messages))
				}
				first, _ := messages[0].(map[string]interface{})
				if first["role"] != "user" {
					t.Fatalf("first role = %v, want user", first["role"])
				}
			},
		},
		{
			name:        "google extracts system instruction",
			provider:    "google",
			model:       "gemini-2.5-pro",
			providerURL: "https://generativelanguage.googleapis.com/v1beta",
			messages: []TypedMessage{
				NewTextMessage("system", "Be concise."),
				NewTextMessage("user", "Hello"),
			},
			verify: func(t *testing.T, req *http.Request, payload map[string]interface{}) {
				if got := req.URL.String(); got != "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent" {
					t.Fatalf("URL = %q", got)
				}
				systemInstruction, ok := payload["systemInstruction"].(map[string]interface{})
				if !ok {
					t.Fatalf("systemInstruction type = %T", payload["systemInstruction"])
				}
				parts, ok := systemInstruction["parts"].([]interface{})
				if !ok || len(parts) != 1 {
					t.Fatalf("systemInstruction.parts = %T len=%d", systemInstruction["parts"], len(parts))
				}
				part, _ := parts[0].(map[string]interface{})
				if part["text"] != "Be concise." {
					t.Fatalf("systemInstruction text = %v, want %q", part["text"], "Be concise.")
				}
				contents, ok := payload["contents"].([]interface{})
				if !ok || len(contents) != 1 {
					t.Fatalf("contents = %T len=%d, want 1 entry", payload["contents"], len(contents))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newProviderSpecTestConfig(tt.provider, tt.model, tt.providerURL)
			spec, err := providerSpecForName(tt.provider)
			if err != nil {
				t.Fatalf("providerSpecForName(%q) error = %v", tt.provider, err)
			}
			buildChat, err := spec.requireChatBuilder()
			if err != nil {
				t.Fatalf("requireChatBuilder() error = %v", err)
			}

			req, body, err := buildChat(cfg, tt.messages, tt.model, tt.system, nil, nil, false)
			if err != nil {
				t.Fatalf("buildChat() error = %v", err)
			}

			tt.verify(t, req, decodeRequestBody(t, body))
		})
	}
}

func TestProviderSpecStreamingRequestBehavior(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		model       string
		providerURL string
		verify      func(*testing.T, *http.Request, map[string]interface{})
	}{
		{
			name:        "openai enables stream flag on same endpoint",
			provider:    "openai",
			model:       "gpt-5",
			providerURL: "https://api.openai.com/v1",
			verify: func(t *testing.T, req *http.Request, payload map[string]interface{}) {
				if got := req.URL.String(); got != "https://api.openai.com/v1/chat/completions" {
					t.Fatalf("URL = %q", got)
				}
				if got := req.Header.Get("Accept"); got != "text/event-stream" {
					t.Fatalf("Accept = %q, want text/event-stream", got)
				}
				if payload["stream"] != true {
					t.Fatalf("stream = %v, want true", payload["stream"])
				}
			},
		},
		{
			name:        "anthropic enables stream flag on messages endpoint",
			provider:    "anthropic",
			model:       "claude-opus-4-1-20250805",
			providerURL: "https://api.anthropic.com/v1",
			verify: func(t *testing.T, req *http.Request, payload map[string]interface{}) {
				if got := req.URL.String(); got != "https://api.anthropic.com/v1/messages" {
					t.Fatalf("URL = %q", got)
				}
				if got := req.Header.Get("Accept"); got != "text/event-stream" {
					t.Fatalf("Accept = %q, want text/event-stream", got)
				}
				if payload["stream"] != true {
					t.Fatalf("stream = %v, want true", payload["stream"])
				}
			},
		},
		{
			name:        "google uses stream endpoint without body stream flag",
			provider:    "google",
			model:       "gemini-2.5-pro",
			providerURL: "https://generativelanguage.googleapis.com/v1beta",
			verify: func(t *testing.T, req *http.Request, payload map[string]interface{}) {
				if got := req.URL.String(); got != "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse" {
					t.Fatalf("URL = %q", got)
				}
				if got := req.Header.Get("Accept"); got != "text/event-stream" {
					t.Fatalf("Accept = %q, want text/event-stream", got)
				}
				if _, ok := payload["stream"]; ok {
					t.Fatalf("unexpected stream field in Google payload: %v", payload["stream"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newProviderSpecTestConfig(tt.provider, tt.model, tt.providerURL)
			spec, err := providerSpecForName(tt.provider)
			if err != nil {
				t.Fatalf("providerSpecForName(%q) error = %v", tt.provider, err)
			}
			buildChat, err := spec.requireChatBuilder()
			if err != nil {
				t.Fatalf("requireChatBuilder() error = %v", err)
			}

			req, body, err := buildChat(cfg, []TypedMessage{NewTextMessage("user", "Hello")}, tt.model, "", nil, nil, true)
			if err != nil {
				t.Fatalf("buildChat() error = %v", err)
			}

			tt.verify(t, req, decodeRequestBody(t, body))
		})
	}
}

func TestArgoProviderSpecChatRequestBehavior(t *testing.T) {
	spec, err := providerSpecForName("argo")
	if err != nil {
		t.Fatalf("providerSpecForName(argo) error = %v", err)
	}
	buildChat, err := spec.requireChatBuilder()
	if err != nil {
		t.Fatalf("requireChatBuilder() error = %v", err)
	}

	t.Run("gpt model uses native openai endpoint", func(t *testing.T) {
		cfg := newProviderSpecTestConfig("argo", "gpt5", "")
		cfg.Env = "http://argo.example.test"

		req, body, err := buildChat(cfg, []TypedMessage{NewTextMessage("user", "Hello")}, "gpt5", "Be concise.", nil, nil, true)
		if err != nil {
			t.Fatalf("buildChat() error = %v", err)
		}

		if got := req.URL.String(); got != "http://argo.example.test/v1/chat/completions" {
			t.Fatalf("URL = %q", got)
		}

		payload := decodeRequestBody(t, body)
		if payload["stream"] != true {
			t.Fatalf("stream = %v, want true", payload["stream"])
		}
		messages, ok := payload["messages"].([]interface{})
		if !ok || len(messages) != 2 {
			t.Fatalf("messages = %T len=%d, want 2 entries", payload["messages"], len(messages))
		}
		first, _ := messages[0].(map[string]interface{})
		if first["role"] != "system" {
			t.Fatalf("first role = %v, want system", first["role"])
		}
	})

	t.Run("explicit tools use native openai tools", func(t *testing.T) {
		cfg := newProviderSpecTestConfig("argo", "gpt5", "")
		cfg.Env = "http://argo.example.test"
		cfg.IsToolEnabledFlag = false

		req, body, err := buildChat(cfg, []TypedMessage{NewTextMessage("user", "Hello")}, "gpt5", "", sampleProviderSpecToolDefs(), nil, true)
		if err != nil {
			t.Fatalf("buildChat() error = %v", err)
		}

		if got := req.URL.String(); got != "http://argo.example.test/v1/chat/completions" {
			t.Fatalf("URL = %q", got)
		}

		payload := decodeRequestBody(t, body)
		if payload["tool_choice"] != "auto" {
			t.Fatalf("tool_choice = %v, want auto", payload["tool_choice"])
		}
		tools, ok := payload["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("tools = %T len=%d, want 1 entry", payload["tools"], len(tools))
		}
		tool, _ := tools[0].(map[string]interface{})
		if tool["type"] != "function" {
			t.Fatalf("tool type = %v, want function", tool["type"])
		}
	})

	t.Run("claude model uses native anthropic endpoint", func(t *testing.T) {
		cfg := newProviderSpecTestConfig("argo", "claude-opus-4-1", "")
		cfg.Env = "http://argo.example.test"

		req, body, err := buildChat(cfg, []TypedMessage{NewTextMessage("user", "Hello")}, "claude-opus-4-1", "Be concise.", nil, nil, true)
		if err != nil {
			t.Fatalf("buildChat() error = %v", err)
		}

		if got := req.URL.String(); got != "http://argo.example.test/v1/messages" {
			t.Fatalf("URL = %q", got)
		}

		payload := decodeRequestBody(t, body)
		if payload["stream"] != true {
			t.Fatalf("stream = %v, want true", payload["stream"])
		}
		if payload["system"] != "Be concise." {
			t.Fatalf("system = %v, want %q", payload["system"], "Be concise.")
		}
		messages, ok := payload["messages"].([]interface{})
		if !ok || len(messages) != 1 {
			t.Fatalf("messages = %T len=%d, want 1 entry", payload["messages"], len(messages))
		}
		first, _ := messages[0].(map[string]interface{})
		if first["role"] != "user" {
			t.Fatalf("first role = %v, want user", first["role"])
		}
	})
}

func TestProviderSpecParseResponseBehavior(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		data         string
		wantText     string
		wantToolName string
	}{
		{
			name:         "openai parses text and tool call",
			provider:     "openai",
			data:         `{"choices":[{"message":{"content":"Hello","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"Chicago\"}"}}]}}]}`,
			wantText:     "Hello",
			wantToolName: "get_weather",
		},
		{
			name:         "anthropic parses text and tool use",
			provider:     "anthropic",
			data:         `{"content":[{"type":"text","text":"Hello"},{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"location":"Chicago"}}]}`,
			wantText:     "Hello",
			wantToolName: "get_weather",
		},
		{
			name:         "google parses text and function call",
			provider:     "google",
			data:         `{"candidates":[{"content":{"parts":[{"text":"Hello"},{"functionCall":{"name":"get_weather","args":{"location":"Chicago"}}}]}}]}`,
			wantText:     "Hello",
			wantToolName: "get_weather",
		},
		{
			name:         "argo parses embedded tool call envelope",
			provider:     "argo",
			data:         `{"response":{"content":"Hello","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{\"location\":\"Chicago\"}"}}]}}`,
			wantText:     "Hello",
			wantToolName: "get_weather",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := providerSpecForName(tt.provider)
			if err != nil {
				t.Fatalf("providerSpecForName(%q) error = %v", tt.provider, err)
			}

			resp, err := spec.parseResponseData([]byte(tt.data), false)
			if err != nil {
				t.Fatalf("parseResponseData() error = %v", err)
			}
			if resp.Text != tt.wantText {
				t.Fatalf("Text = %q, want %q", resp.Text, tt.wantText)
			}
			if len(resp.ToolCalls) != 1 {
				t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
			}
			if resp.ToolCalls[0].Name != tt.wantToolName {
				t.Fatalf("ToolCalls[0].Name = %q, want %q", resp.ToolCalls[0].Name, tt.wantToolName)
			}
		})
	}
}

func TestProviderSpecConvertToolsBehavior(t *testing.T) {
	tests := []struct {
		provider string
		verify   func(*testing.T, ConvertedTools)
	}{
		{
			provider: "openai",
			verify: func(t *testing.T, converted ConvertedTools) {
				tools, ok := converted.Tools.([]OpenAITool)
				if !ok || len(tools) != 1 {
					t.Fatalf("Tools = %T len=%d, want []OpenAITool with 1 entry", converted.Tools, len(tools))
				}
				if tools[0].Type != "function" {
					t.Fatalf("tool type = %q, want function", tools[0].Type)
				}
				if converted.ToolChoice != "auto" {
					t.Fatalf("ToolChoice = %v, want auto", converted.ToolChoice)
				}
			},
		},
		{
			provider: "anthropic",
			verify: func(t *testing.T, converted ConvertedTools) {
				tools, ok := converted.Tools.([]AnthropicTool)
				if !ok || len(tools) != 1 {
					t.Fatalf("Tools = %T len=%d, want []AnthropicTool with 1 entry", converted.Tools, len(tools))
				}
				if tools[0].Name != "get_weather" {
					t.Fatalf("tool name = %q, want get_weather", tools[0].Name)
				}
				choice, ok := converted.ToolChoice.(AnthropicToolChoice)
				if !ok || choice.Type != "auto" {
					t.Fatalf("ToolChoice = %#v, want AnthropicToolChoice{Type:\"auto\"}", converted.ToolChoice)
				}
			},
		},
		{
			provider: "google",
			verify: func(t *testing.T, converted ConvertedTools) {
				tools, ok := converted.Tools.([]GoogleTool)
				if !ok || len(tools) != 1 {
					t.Fatalf("Tools = %T len=%d, want []GoogleTool with 1 entry", converted.Tools, len(tools))
				}
				if len(tools[0].FunctionDeclarations) != 1 || tools[0].FunctionDeclarations[0].Name != "get_weather" {
					t.Fatalf("unexpected Google tool declarations: %#v", tools[0].FunctionDeclarations)
				}
				if converted.ToolChoice != nil {
					t.Fatalf("ToolChoice = %v, want nil", converted.ToolChoice)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			spec, err := providerSpecForName(tt.provider)
			if err != nil {
				t.Fatalf("providerSpecForName(%q) error = %v", tt.provider, err)
			}
			tt.verify(t, spec.convertToolsForRequest(sampleProviderSpecToolDefs(), nil))
		})
	}
}

func TestUnknownProviderSpecHandleStreamFallback(t *testing.T) {
	logger := providerSpecBehaviorLogger{dir: t.TempDir()}
	notifier := NewTestNotifier()
	spec := unknownProviderSpec("unknown-provider")

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer reader.Close()

	oldStdout := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	printedCh := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		printedCh <- string(data)
	}()

	resp, err := spec.handleStreamResponse(context.Background(), io.NopCloser(strings.NewReader("fallback stream")), logger, notifier)
	writer.Close()
	if err != nil {
		t.Fatalf("handleStreamResponse() error = %v", err)
	}

	printed := <-printedCh
	if printed != "fallback stream" {
		t.Fatalf("printed output = %q, want %q", printed, "fallback stream")
	}
	if resp.Text != "fallback stream" {
		t.Fatalf("Response.Text = %q, want %q", resp.Text, "fallback stream")
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("len(ToolCalls) = %d, want 0", len(resp.ToolCalls))
	}
}
