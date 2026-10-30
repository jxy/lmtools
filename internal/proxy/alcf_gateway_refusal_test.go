package proxy

import (
	"lmtools/internal/apifixtures"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The ALCF inference gateway answers a refused streamed request with 200, one
// Chat Completions chunk whose content is "[ERROR] ..." with the refusal, and
// [DONE], on every route. Each client must get its own failure form carrying
// the refusal, and no answer text.
func TestALCFGatewayRefusalReachesClientsAsAnError(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatal(err)
	}
	capture := func(name string) []byte {
		t.Helper()
		data, err := apifixtures.ReadCaseFile(suite.Root, "response-openai-vllm-reasoning", "captures/"+name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, tt := range []struct {
		name     string
		provider string
		capture  string
		path     string
		body     string
		want     []string
	}{
		{
			name: "chat passthrough", provider: constants.ProviderOpenAI, capture: "openai-gateway-error.stream.txt",
			path: "/v1/chat/completions",
			body: `{"model":"mistral-large-4-nvfp4","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			want: []string{`"error":{"message":"Error: Unexpected error calling stream API`, "Unknown message role: developer", "data: [DONE]"},
		},
		{
			name: "messages converted from chat", provider: constants.ProviderOpenAI, capture: "openai-gateway-error.stream.txt",
			path: "/v1/messages",
			body: `{"model":"mistral-large-4-nvfp4","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			want: []string{"event: error", "Unknown message role: developer"},
		},
		{
			name: "responses passthrough", provider: constants.ProviderOpenAI, capture: "openai-responses-gateway-error.stream.txt",
			path: "/v1/responses",
			body: `{"model":"mistral-large-4-nvfp4","stream":true,"input":"hi"}`,
			want: []string{"event: response.failed", "'input_text' is not a valid ChunkTypes"},
		},
		{
			name: "messages relayed from messages", provider: constants.ProviderAnthropic, capture: "anthropic-gateway-error.stream.txt",
			path: "/v1/messages",
			body: `{"model":"beam-1.0-bf16","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			want: []string{"event: error", "beam-1.0-bf16 is not a multimodal model"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stream := capture(tt.capture)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(stream)
			}))
			defer upstream.Close()
			config := &Config{
				Provider:           tt.provider,
				ProviderURL:        upstream.URL + "/v1",
				MaxRequestBodySize: fixtureMaxBodySize,
			}
			if tt.provider == constants.ProviderOpenAI {
				config.ProviderKeySet = ProviderKeySet{OpenAIAPIKey: "token"}
			} else {
				config.ProviderKeySet = ProviderKeySet{AnthropicAPIKey: "token"}
			}
			handler, cleanup := NewTestServer(t, config)
			defer cleanup()

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			body := w.Body.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("client stream lacks %q:\n%s", want, body)
				}
			}
			for _, unwanted := range []string{"[ERROR]", "chat.completion.chunk", "text_delta", "output_text.delta"} {
				if strings.Contains(body, unwanted) {
					t.Errorf("client stream carries %q:\n%s", unwanted, body)
				}
			}
		})
	}
}
