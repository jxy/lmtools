package providers

import "testing"

func TestOpenAIURLsAcceptsBaseAndChatEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		wantChat   string
		wantModels string
	}{
		{
			name:       "base url",
			base:       "https://api.openai.com/v1",
			wantChat:   "https://api.openai.com/v1/chat/completions",
			wantModels: "https://api.openai.com/v1/models",
		},
		{
			name:       "chat endpoint",
			base:       "https://api.openai.com/v1/chat/completions",
			wantChat:   "https://api.openai.com/v1/chat/completions",
			wantModels: "https://api.openai.com/v1/models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotChat, gotModels, err := OpenAIURLs(tt.base)
			if err != nil {
				t.Fatalf("OpenAIURLs() error = %v", err)
			}
			if gotChat != tt.wantChat {
				t.Fatalf("chat URL = %q, want %q", gotChat, tt.wantChat)
			}
			if gotModels != tt.wantModels {
				t.Fatalf("models URL = %q, want %q", gotModels, tt.wantModels)
			}
		})
	}
}

func TestGoogleURLsReturnsNormalizedModelsBase(t *testing.T) {
	base, models := GoogleURLs("https://example.test/v1beta")
	if base != "https://example.test/v1beta/models" {
		t.Fatalf("base = %q, want %q", base, "https://example.test/v1beta/models")
	}
	if models != "https://example.test/v1beta/models" {
		t.Fatalf("models = %q, want %q", models, "https://example.test/v1beta/models")
	}
}

func TestAnthropicURLsAcceptsBaseAndMessagesEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		base         string
		wantMessages string
		wantModels   string
	}{
		{
			name:         "base url",
			base:         "https://api.anthropic.com/v1",
			wantMessages: "https://api.anthropic.com/v1/messages",
			wantModels:   "https://api.anthropic.com/v1/models",
		},
		{
			name:         "messages endpoint",
			base:         "https://api.anthropic.com/v1/messages",
			wantMessages: "https://api.anthropic.com/v1/messages",
			wantModels:   "https://api.anthropic.com/v1/models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMessages, gotModels := AnthropicURLs(tt.base)
			if gotMessages != tt.wantMessages {
				t.Fatalf("messages URL = %q, want %q", gotMessages, tt.wantMessages)
			}
			if gotModels != tt.wantModels {
				t.Fatalf("models URL = %q, want %q", gotModels, tt.wantModels)
			}
		})
	}
}

func TestResolveChatURL(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		providerURL string
		argoEnv     string
		model       string
		stream      bool
		want        string
	}{
		{
			name:        "google stream",
			provider:    "google",
			providerURL: "https://example.test/v1beta",
			model:       "gemini-2.5-pro",
			stream:      true,
			want:        "https://example.test/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		},
		{
			name:     "argo custom env keeps direct base",
			provider: "argo",
			argoEnv:  "https://custom.example/api",
			want:     "https://custom.example/api/chat/",
		},
		{
			name:     "argo custom env stream keeps direct base",
			provider: "argo",
			argoEnv:  "https://custom.example/api",
			stream:   true,
			want:     "https://custom.example/api/streamchat/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, err := ResolveChatURL(tt.provider, tt.providerURL, tt.argoEnv, tt.model, tt.stream)
			if err != nil {
				t.Fatalf("ResolveChatURL() error = %v", err)
			}
			if url != tt.want {
				t.Fatalf("ResolveChatURL() = %q, want %q", url, tt.want)
			}
		})
	}
}

func TestResolveEmbedURL(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		providerURL string
		argoEnv     string
		want        string
	}{
		{
			name:        "openai embeddings from chat url",
			provider:    "openai",
			providerURL: "https://api.openai.com/v1/chat/completions",
			want:        "https://api.openai.com/v1/embeddings",
		},
		{
			name:     "argo custom env keeps direct base",
			provider: "argo",
			argoEnv:  "https://custom.example/api",
			want:     "https://custom.example/api/embed/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveEmbedURL(tt.provider, tt.providerURL, tt.argoEnv)
			if err != nil {
				t.Fatalf("ResolveEmbedURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolveEmbedURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveModelsURL(t *testing.T) {
	got, err := ResolveModelsURL("argo", "", "https://custom.example/api")
	if err != nil {
		t.Fatalf("ResolveModelsURL() error = %v", err)
	}
	if got != "https://custom.example/api/models/" {
		t.Fatalf("ResolveModelsURL() = %q", got)
	}
}

func TestResolveArgoBaseURL(t *testing.T) {
	if got := ResolveArgoBaseURL("prod"); got != ArgoProdURL {
		t.Fatalf("ResolveArgoBaseURL(prod) = %q, want %q", got, ArgoProdURL)
	}
	if got := ResolveArgoBaseURL("https://custom.example/api"); got != "https://custom.example/api" {
		t.Fatalf("ResolveArgoBaseURL(custom) = %q", got)
	}
}
