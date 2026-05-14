package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/apifixtures"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

const (
	fixtureOpenAIKey    = "test-openai-key"
	fixtureAnthropicKey = "test-anthropic-key"
	fixtureGoogleKey    = "test-google-key"
	fixtureArgoAPIKey   = "test-argo-key"
	fixtureMappedHaiku  = "claude-3-haiku-20240307"
	fixtureMaxBodySize  = 10 * 1024 * 1024
)

var fixtureBackendTargetOrder = []string{
	"openai",
	"anthropic",
	"google",
	"argo",
	"argo-openai",
	"argo-anthropic",
}

func TestAPIFixtureMessagesEndpointContracts(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	caseFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_CASE"))
	providerFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_PROVIDER"))

	for _, listedCase := range suite.Manifest.Cases {
		meta, err := apifixtures.LoadCaseMeta(suite.Root, listedCase.ID)
		if err != nil {
			t.Fatalf("LoadCaseMeta(%q) error = %v", listedCase.ID, err)
		}
		if !apifixtures.MatchesFilters(meta, caseFilter, providerFilter) {
			continue
		}
		if meta.IngressFamily != "anthropic" || !apifixtures.StringSliceContains(meta.Kinds, "request") {
			continue
		}

		t.Run(meta.ID, func(t *testing.T) {
			for _, targetBase := range fixtureBackendTargetOrder {
				if !fixtureHasCaptureTarget(suite.Root, meta, targetBase, false) {
					continue
				}

				t.Run(targetBase+"/json", func(t *testing.T) {
					runFixtureEndpointContract(t, suite.Root, meta, "anthropic", targetBase, false, true)
				})

				if fixtureHasCaptureTarget(suite.Root, meta, targetBase, true) {
					t.Run(targetBase+"/stream", func(t *testing.T) {
						runFixtureEndpointContract(t, suite.Root, meta, "anthropic", targetBase, true, true)
					})
				}
			}
		})
	}
}

func TestAPIFixtureChatCompletionsEndpointContracts(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	caseFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_CASE"))
	providerFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_PROVIDER"))

	for _, listedCase := range suite.Manifest.Cases {
		meta, err := apifixtures.LoadCaseMeta(suite.Root, listedCase.ID)
		if err != nil {
			t.Fatalf("LoadCaseMeta(%q) error = %v", listedCase.ID, err)
		}
		if !apifixtures.MatchesFilters(meta, caseFilter, providerFilter) {
			continue
		}
		if meta.IngressFamily != "openai" || !apifixtures.StringSliceContains(meta.Kinds, "request") {
			continue
		}

		t.Run(meta.ID, func(t *testing.T) {
			for _, targetBase := range fixtureBackendTargetOrder {
				if !fixtureHasCaptureTarget(suite.Root, meta, targetBase, false) {
					continue
				}

				t.Run(targetBase+"/json", func(t *testing.T) {
					runFixtureEndpointContract(t, suite.Root, meta, "openai", targetBase, false, true)
				})

				if fixtureHasCaptureTarget(suite.Root, meta, targetBase, true) {
					t.Run(targetBase+"/stream", func(t *testing.T) {
						runFixtureEndpointContract(t, suite.Root, meta, "openai", targetBase, true, true)
					})
				}
			}
		})
	}
}

func TestMessagesCountTokensContract(t *testing.T) {
	t.Run("argo native passthrough", func(t *testing.T) {
		wantReq := AnthropicTokenCountRequest{
			Model: "claude-haiku-4-5",
			Messages: []AnthropicMessage{
				{
					Role:    core.RoleUser,
					Content: json.RawMessage(`"Count these tokens"`),
				},
			},
		}
		wantReqBytes, err := json.Marshal(wantReq)
		if err != nil {
			t.Fatalf("json.Marshal(request) error = %v", err)
		}

		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("backend method = %s, want POST", r.Method)
				http.Error(w, "unexpected method", http.StatusBadRequest)
				return
			}
			if r.URL.Path != "/v1/messages/count_tokens" {
				t.Errorf("backend path = %s, want /v1/messages/count_tokens", r.URL.Path)
				http.Error(w, "unexpected path", http.StatusBadRequest)
				return
			}
			if got := r.Header.Get("x-api-key"); got != fixtureArgoAPIKey {
				t.Errorf("x-api-key = %q, want %q", got, fixtureArgoAPIKey)
				http.Error(w, "unexpected x-api-key", http.StatusBadRequest)
				return
			}
			if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
				t.Errorf("anthropic-version = %q, want 2023-06-01", got)
				http.Error(w, "unexpected anthropic-version", http.StatusBadRequest)
				return
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("ReadAll(body) error = %v", err)
				http.Error(w, "read body failed", http.StatusBadRequest)
				return
			}
			if err := compareCanonicalJSONBytes(wantReqBytes, body); err != nil {
				t.Error(err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(AnthropicTokenCountResponse{InputTokens: 123})
		}))
		t.Cleanup(backend.Close)

		config := &Config{
			Provider:           constants.ProviderArgo,
			ProviderURL:        backend.URL,
			ArgoAPIKey:         fixtureArgoAPIKey,
			ArgoUser:           "fixture-user",
			MaxRequestBodySize: fixtureMaxBodySize,
		}
		server, cleanup := NewTestServer(t, config)
		t.Cleanup(cleanup)

		proxyServer := httptest.NewServer(server)
		t.Cleanup(proxyServer.Close)

		resp, err := http.Post(proxyServer.URL+"/v1/messages/count_tokens", "application/json", bytes.NewReader(wantReqBytes))
		if err != nil {
			t.Fatalf("POST /v1/messages/count_tokens error = %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("Content-Type = %q, want application/json", ct)
		}

		var got AnthropicTokenCountResponse
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode response error = %v", err)
		}
		if got.InputTokens != 123 {
			t.Fatalf("input_tokens = %d, want 123", got.InputTokens)
		}
	})

	t.Run("local estimation fallback", func(t *testing.T) {
		reqBody := []byte(`{
		  "model": "gpt-4.1",
		  "messages": [
		    {
		      "role": "user",
		      "content": "Estimate token count locally"
		    }
		  ]
		}`)

		config := &Config{
			Provider:           constants.ProviderOpenAI,
			ProviderURL:        "http://unused.local",
			OpenAIAPIKey:       fixtureOpenAIKey,
			MaxRequestBodySize: fixtureMaxBodySize,
		}
		server, cleanup := NewTestServer(t, config)
		t.Cleanup(cleanup)

		proxyServer := httptest.NewServer(server)
		t.Cleanup(proxyServer.Close)

		resp, err := http.Post(proxyServer.URL+"/v1/messages/count_tokens", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("POST /v1/messages/count_tokens error = %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
		}

		var got AnthropicTokenCountResponse
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode response error = %v", err)
		}
		if got.InputTokens <= 0 {
			t.Fatalf("input_tokens = %d, want > 0", got.InputTokens)
		}
	})
}

func runFixtureEndpointContract(t *testing.T, root string, meta apifixtures.CaseMeta, clientFamily, targetBase string, stream, assertBackendBody bool) {
	t.Helper()

	targetID := targetBase
	if stream {
		targetID += "-stream"
	}
	target, err := apifixtures.ParseCaptureTarget(targetID)
	if err != nil {
		t.Fatalf("ParseCaptureTarget(%q) error = %v", targetID, err)
	}

	clientBody, err := loadFixtureClientRequestBody(root, meta, clientFamily, targetBase, stream)
	if err != nil {
		t.Fatalf("loadFixtureClientRequestBody(%q) error = %v", targetID, err)
	}

	var expectedBackendBody []byte
	if assertBackendBody {
		expectedBackendBody, err = loadExpectedBackendBody(root, meta, clientFamily, targetBase, clientBody, stream)
		if err != nil {
			t.Fatalf("loadExpectedBackendBody(%q) error = %v", targetID, err)
		}
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateFixtureBackendRequest(r, target, expectedBackendBody); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if stream {
			streamBytes, err := apifixtures.ReadCaseFile(root, meta.ID, fixtureCaptureRel(targetBase, true))
			if err != nil {
				t.Fatalf("ReadCaseFile(%q) error = %v", fixtureCaptureRel(targetBase, true), err)
			}
			setFixtureStreamContentType(w, targetBase)
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(streamBytes); err != nil {
				t.Fatalf("stream write error: %v", err)
			}
			return
		}

		respBytes, err := apifixtures.ReadCaseFile(root, meta.ID, fixtureCaptureRel(targetBase, false))
		if err != nil {
			t.Fatalf("ReadCaseFile(%q) error = %v", fixtureCaptureRel(targetBase, false), err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(respBytes); err != nil {
			t.Fatalf("response write error: %v", err)
		}
	}))
	t.Cleanup(backend.Close)

	config := fixtureProxyConfig(meta, clientFamily, targetBase, backend.URL)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	proxyServer := httptest.NewServer(server)
	t.Cleanup(proxyServer.Close)

	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+fixtureClientPath(clientFamily), bytes.NewReader(clientBody))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client request error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll(response) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
	}

	if stream {
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
			t.Fatalf("Content-Type = %q, want text/event-stream", ct)
		}
	} else if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	expectedClientID := clientFamily
	if stream {
		expectedClientID += "-stream"
	}
	expectedClientTarget, err := apifixtures.ParseCaptureTarget(expectedClientID)
	if err != nil {
		t.Fatalf("ParseCaptureTarget(%q) error = %v", expectedClientID, err)
	}

	expectedBytes, err := loadExpectedClientPayload(root, meta, clientFamily, targetBase, stream, clientBody)
	if err != nil {
		t.Fatalf("loadExpectedClientPayload(%q) error = %v", targetID, err)
	}

	result, err := apifixtures.CompareCaptureShape(expectedClientTarget, expectedBytes, body)
	if err != nil {
		t.Fatalf("CompareCaptureShape(%q) error = %v", expectedClientID, err)
	}
	if clientFamily == "anthropic" {
		result.Differences = filterOptionalAnthropicUsageDifferences(result.Differences)
	}
	if len(result.Differences) > 0 {
		t.Fatalf("client %s contract mismatches for backend %s:\n- %s", clientFamily, targetID, strings.Join(result.Differences, "\n- "))
	}
}

func fixtureProxyConfig(meta apifixtures.CaseMeta, clientFamily, targetBase, providerURL string) *Config {
	targetModel := fixtureModelForTarget(meta, targetBase)
	argoUser := meta.ArgoUser
	if argoUser == "" {
		argoUser = apifixtures.DefaultArgoUser
	}

	cfg := &Config{
		ProviderURL:        providerURL,
		MaxRequestBodySize: fixtureMaxBodySize,
		OpenAIAPIKey:       fixtureOpenAIKey,
		AnthropicAPIKey:    fixtureAnthropicKey,
		GoogleAPIKey:       fixtureGoogleKey,
		ArgoAPIKey:         fixtureArgoAPIKey,
		ArgoUser:           argoUser,
	}
	if shouldMapFixtureModel(clientFamily, targetBase) && targetModel != "" {
		cfg.ModelMapRules = []ModelMapRule{{Pattern: ".*", Model: targetModel}}
	}

	switch targetBase {
	case "openai":
		cfg.Provider = constants.ProviderOpenAI
	case "anthropic":
		cfg.Provider = constants.ProviderAnthropic
	case "google":
		cfg.Provider = constants.ProviderGoogle
	case "argo":
		cfg.Provider = constants.ProviderArgo
		cfg.ArgoLegacy = true
	case "argo-openai", "argo-anthropic":
		cfg.Provider = constants.ProviderArgo
	default:
		cfg.Provider = constants.ProviderOpenAI
	}

	return cfg
}

func shouldMapFixtureModel(clientFamily, targetBase string) bool {
	switch clientFamily {
	case "anthropic":
		return true
	case "openai":
		return targetBase != "openai" && targetBase != "argo-openai"
	default:
		return false
	}
}

func TestShouldMapFixtureModel(t *testing.T) {
	tests := []struct {
		name         string
		clientFamily string
		targetBase   string
		want         bool
	}{
		{name: "anthropic to openai maps", clientFamily: "anthropic", targetBase: "openai", want: true},
		{name: "anthropic to anthropic maps", clientFamily: "anthropic", targetBase: "anthropic", want: true},
		{name: "openai to openai passthrough", clientFamily: "openai", targetBase: "openai", want: false},
		{name: "openai to argo openai passthrough", clientFamily: "openai", targetBase: "argo-openai", want: false},
		{name: "openai to anthropic maps", clientFamily: "openai", targetBase: "anthropic", want: true},
		{name: "openai to google maps", clientFamily: "openai", targetBase: "google", want: true},
		{name: "unknown passthrough", clientFamily: "responses", targetBase: "openai", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldMapFixtureModel(tt.clientFamily, tt.targetBase); got != tt.want {
				t.Fatalf("shouldMapFixtureModel(%q, %q) = %v, want %v", tt.clientFamily, tt.targetBase, got, tt.want)
			}
		})
	}
}

func loadFixtureClientRequestBody(root string, meta apifixtures.CaseMeta, clientFamily, targetBase string, stream bool) ([]byte, error) {
	body, err := apifixtures.ReadCaseFile(root, meta.ID, "ingress.json")
	if err != nil {
		return nil, err
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}

	if stream {
		decoded["stream"] = true
	}

	if clientFamily == "openai" && targetBase != "openai" && targetBase != "argo-openai" {
		// Route OpenAI ingress through an explicitly mapped Claude model path so we
		// exercise the converted Anthropic/Google/Argo backends with the shared
		// fixture request semantics.
		decoded["model"] = fixtureMappedHaiku
	}

	return json.Marshal(decoded)
}

func fixtureHasCaptureTarget(root string, meta apifixtures.CaseMeta, targetBase string, stream bool) bool {
	targetID := targetBase
	if stream {
		targetID += "-stream"
	}
	if len(meta.CaptureTargets) > 0 {
		return apifixtures.StringSliceContains(meta.CaptureTargets, targetID) &&
			apifixtures.CaseFileExists(root, meta.ID, fixtureCaptureRel(targetBase, stream))
	}
	return apifixtures.CaseFileExists(root, meta.ID, fixtureCaptureRel(targetBase, stream))
}

func loadExpectedBackendBody(root string, meta apifixtures.CaseMeta, clientFamily, targetBase string, clientBody []byte, stream bool) ([]byte, error) {
	_ = root
	_ = stream

	converter := newFixtureConverter()
	argoUser := meta.ArgoUser
	if argoUser == "" {
		argoUser = apifixtures.DefaultArgoUser
	}

	switch clientFamily {
	case "anthropic":
		var req AnthropicRequest
		if err := json.Unmarshal(clientBody, &req); err != nil {
			return nil, err
		}
		req.Model = fixtureModelForTarget(meta, targetBase)

		switch targetBase {
		case "anthropic", "argo-anthropic":
			return json.Marshal(req)
		case "openai", "argo-openai":
			rendered, err := converter.ConvertAnthropicToOpenAI(context.Background(), &req)
			if err != nil {
				return nil, err
			}
			return json.Marshal(rendered)
		case "google":
			rendered, err := converter.ConvertAnthropicToGoogle(context.Background(), &req)
			if err != nil {
				return nil, err
			}
			return json.Marshal(rendered)
		case "argo":
			rendered, err := converter.ConvertAnthropicToArgo(context.Background(), &req, argoUser)
			if err != nil {
				return nil, err
			}
			return json.Marshal(rendered)
		}

	case "openai":
		var req OpenAIRequest
		if err := json.Unmarshal(clientBody, &req); err != nil {
			return nil, err
		}

		switch targetBase {
		case "openai", "argo-openai":
			return json.Marshal(req)
		case "anthropic", "argo-anthropic":
			req.Model = fixtureModelForTarget(meta, targetBase)
			rendered, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), &req)
			if err != nil {
				return nil, err
			}
			return json.Marshal(rendered)
		case "google":
			req.Model = fixtureModelForTarget(meta, targetBase)
			if stream {
				anthReq, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), &req)
				if err != nil {
					return nil, err
				}
				rendered, err := converter.ConvertAnthropicToGoogle(context.Background(), anthReq)
				if err != nil {
					return nil, err
				}
				return json.Marshal(rendered)
			}
			rendered, err := TypedToGoogleRequest(OpenAIRequestToTyped(&req), req.Model, nil)
			if err != nil {
				return nil, err
			}
			return json.Marshal(rendered)
		case "argo":
			req.Model = fixtureModelForTarget(meta, targetBase)
			anthReq, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), &req)
			if err != nil {
				return nil, err
			}
			rendered, err := converter.ConvertAnthropicToArgo(context.Background(), anthReq, argoUser)
			if err != nil {
				return nil, err
			}
			return json.Marshal(rendered)
		}
	}

	return nil, fmt.Errorf("unsupported backend request expectation for %s -> %s", clientFamily, targetBase)
}

func loadExpectedClientPayload(root string, meta apifixtures.CaseMeta, clientFamily, targetBase string, stream bool, clientBody []byte) ([]byte, error) {
	if stream {
		return loadExpectedClientStream(root, meta, clientFamily, targetBase, clientBody)
	}
	return loadExpectedClientJSON(root, meta, clientFamily, targetBase, clientBody)
}

func loadExpectedClientJSON(root string, meta apifixtures.CaseMeta, clientFamily, targetBase string, clientBody []byte) ([]byte, error) {
	converter := newFixtureConverter()
	clientModel, err := extractRequestModel(clientBody)
	if err != nil {
		return nil, err
	}

	switch clientFamily {
	case "anthropic":
		switch targetBase {
		case "openai", "argo-openai":
			var resp OpenAIResponse
			if err := readFixtureJSON(root, meta.ID, fixtureCaptureRel(targetBase, false), &resp); err != nil {
				return nil, err
			}
			return json.Marshal(converter.ConvertOpenAIToAnthropic(&resp, clientModel))
		case "anthropic", "argo-anthropic":
			var resp AnthropicResponse
			if err := readFixtureJSON(root, meta.ID, fixtureCaptureRel(targetBase, false), &resp); err != nil {
				return nil, err
			}
			resp.Model = clientModel
			return json.Marshal(resp)
		case "google":
			var resp GoogleResponse
			if err := readFixtureJSON(root, meta.ID, fixtureCaptureRel(targetBase, false), &resp); err != nil {
				return nil, err
			}
			return json.Marshal(converter.ConvertGoogleToAnthropic(&resp, clientModel))
		case "argo":
			var req AnthropicRequest
			if err := json.Unmarshal(clientBody, &req); err != nil {
				return nil, err
			}
			var resp ArgoChatResponse
			if err := readFixtureJSON(root, meta.ID, fixtureCaptureRel(targetBase, false), &resp); err != nil {
				return nil, err
			}
			return json.Marshal(converter.ConvertArgoToAnthropicWithRequest(&resp, clientModel, &req))
		}

	case "openai":
		switch targetBase {
		case "openai", "argo-openai":
			var resp OpenAIResponse
			if err := readFixtureJSON(root, meta.ID, fixtureCaptureRel(targetBase, false), &resp); err != nil {
				return nil, err
			}
			resp.Model = clientModel
			return json.Marshal(resp)
		case "anthropic", "argo-anthropic":
			var resp AnthropicResponse
			if err := readFixtureJSON(root, meta.ID, fixtureCaptureRel(targetBase, false), &resp); err != nil {
				return nil, err
			}
			resp.Model = clientModel
			return json.Marshal(converter.ConvertAnthropicResponseToOpenAI(&resp, clientModel))
		case "google":
			var resp GoogleResponse
			if err := readFixtureJSON(root, meta.ID, fixtureCaptureRel(targetBase, false), &resp); err != nil {
				return nil, err
			}
			anthResp := converter.ConvertGoogleToAnthropic(&resp, clientModel)
			return json.Marshal(converter.ConvertAnthropicResponseToOpenAI(anthResp, clientModel))
		case "argo":
			var openAIReq OpenAIRequest
			if err := json.Unmarshal(clientBody, &openAIReq); err != nil {
				return nil, err
			}
			anthReq, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), &openAIReq)
			if err != nil {
				return nil, err
			}
			var resp ArgoChatResponse
			if err := readFixtureJSON(root, meta.ID, fixtureCaptureRel(targetBase, false), &resp); err != nil {
				return nil, err
			}
			anthResp := converter.ConvertArgoToAnthropicWithRequest(&resp, clientModel, anthReq)
			return json.Marshal(converter.ConvertAnthropicResponseToOpenAI(anthResp, clientModel))
		}
	}

	return nil, fmt.Errorf("unsupported client/backend pair %s -> %s", clientFamily, targetBase)
}

func loadExpectedClientStream(root string, meta apifixtures.CaseMeta, clientFamily, targetBase string, clientBody []byte) ([]byte, error) {
	raw, err := apifixtures.ReadCaseFile(root, meta.ID, fixtureCaptureRel(targetBase, true))
	if err != nil {
		return nil, err
	}
	clientModel, err := extractRequestModel(clientBody)
	if err != nil {
		return nil, err
	}

	server := &Server{converter: newFixtureConverter()}

	switch clientFamily {
	case "anthropic":
		switch targetBase {
		case "openai", "argo-openai":
			return renderAnthropicStreamFromOpenAI(raw, clientModel)
		case "anthropic", "argo-anthropic":
			return raw, nil
		case "google":
			return renderAnthropicStreamFromGoogle(raw, clientModel)
		case "argo":
			return renderAnthropicStreamFromArgo(raw, clientModel)
		}

	case "openai":
		switch targetBase {
		case "openai", "argo-openai":
			return raw, nil
		case "anthropic", "argo-anthropic":
			return renderOpenAIStreamFromAnthropic(server, raw, clientModel)
		case "google":
			return renderOpenAIStreamFromGoogle(server, raw, clientModel)
		case "argo":
			return renderOpenAIStreamFromArgo(raw, clientModel)
		}
	}

	return nil, fmt.Errorf("unsupported client/backend stream pair %s -> %s", clientFamily, targetBase)
}

func fixtureModelForTarget(meta apifixtures.CaseMeta, targetBase string) string {
	if model := strings.TrimSpace(meta.Models[targetBase]); model != "" {
		return model
	}
	switch targetBase {
	case "argo-openai":
		if model := strings.TrimSpace(meta.Models["argo"]); model != "" {
			return model
		}
		return strings.TrimSpace(meta.Models["openai"])
	case "argo-anthropic":
		if model := strings.TrimSpace(meta.Models["argo"]); strings.HasPrefix(strings.ToLower(model), "claude") {
			return model
		}
		return strings.TrimSpace(meta.Models["anthropic"])
	default:
		return strings.TrimSpace(meta.Models[targetBase])
	}
}

func fixtureCaptureRel(targetBase string, stream bool) string {
	if stream {
		return fmt.Sprintf("captures/%s-stream.stream.txt", targetBase)
	}
	return fmt.Sprintf("captures/%s.response.json", targetBase)
}

func fixtureClientPath(clientFamily string) string {
	if clientFamily == "anthropic" {
		return "/v1/messages"
	}
	return "/v1/chat/completions"
}

func validateFixtureBackendRequest(r *http.Request, target apifixtures.CaptureTarget, expectedBody []byte) error {
	if err := validateFixtureBackendPath(r, target); err != nil {
		return err
	}
	if err := validateFixtureBackendHeaders(r, target); err != nil {
		return err
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("ReadAll(backend body) error = %w", err)
	}
	if expectedBody != nil {
		if err := compareCanonicalJSONBytes(expectedBody, body); err != nil {
			return err
		}
	}
	return nil
}

func validateFixtureBackendPath(r *http.Request, target apifixtures.CaptureTarget) error {
	switch target.ID {
	case "openai", "openai-stream", "argo-openai", "argo-openai-stream":
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			return fmt.Errorf("backend path = %s, want /chat/completions suffix", r.URL.Path)
		}
	case "anthropic", "anthropic-stream", "argo-anthropic", "argo-anthropic-stream":
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			return fmt.Errorf("backend path = %s, want /messages suffix", r.URL.Path)
		}
	case "google":
		if !strings.Contains(r.URL.Path, ":generateContent") {
			return fmt.Errorf("backend path = %s, want :generateContent suffix", r.URL.Path)
		}
	case "google-stream":
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			return fmt.Errorf("backend path = %s, want :streamGenerateContent suffix", r.URL.Path)
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || values.Get("alt") != "sse" {
			return fmt.Errorf("backend query = %q, want alt=sse", r.URL.RawQuery)
		}
	case "argo":
		if r.URL.Path != "/api/v1/resource/chat/" {
			return fmt.Errorf("backend path = %s, want /api/v1/resource/chat/", r.URL.Path)
		}
	case "argo-stream":
		if r.URL.Path != "/api/v1/resource/streamchat/" {
			return fmt.Errorf("backend path = %s, want /api/v1/resource/streamchat/", r.URL.Path)
		}
	default:
		return fmt.Errorf("unsupported target id %q", target.ID)
	}
	return nil
}

func validateFixtureBackendHeaders(r *http.Request, target apifixtures.CaptureTarget) error {
	switch target.ID {
	case "openai", "openai-stream":
		if got := r.Header.Get("Authorization"); got != "Bearer "+fixtureOpenAIKey {
			return fmt.Errorf("Authorization = %q, want Bearer %s", got, fixtureOpenAIKey)
		}
	case "anthropic", "anthropic-stream":
		if got := r.Header.Get("x-api-key"); got != fixtureAnthropicKey {
			return fmt.Errorf("x-api-key = %q, want %q", got, fixtureAnthropicKey)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			return fmt.Errorf("anthropic-version = %q, want 2023-06-01", got)
		}
	case "google", "google-stream":
		if got := r.Header.Get("x-goog-api-key"); got != fixtureGoogleKey {
			return fmt.Errorf("x-goog-api-key = %q, want %q", got, fixtureGoogleKey)
		}
	case "argo-openai", "argo-openai-stream":
		if got := r.Header.Get("Authorization"); got != "Bearer "+fixtureArgoAPIKey {
			return fmt.Errorf("Authorization = %q, want Bearer %s", got, fixtureArgoAPIKey)
		}
	case "argo-anthropic", "argo-anthropic-stream":
		if got := r.Header.Get("x-api-key"); got != fixtureArgoAPIKey {
			return fmt.Errorf("x-api-key = %q, want %q", got, fixtureArgoAPIKey)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			return fmt.Errorf("anthropic-version = %q, want 2023-06-01", got)
		}
	}

	if target.Stream {
		switch target.ID {
		case "anthropic-stream", "argo-openai-stream", "argo-anthropic-stream":
			if got := r.Header.Get("Accept"); got != "text/event-stream" {
				return fmt.Errorf("Accept = %q, want text/event-stream", got)
			}
		}
	}
	return nil
}

func setFixtureStreamContentType(w http.ResponseWriter, targetBase string) {
	switch targetBase {
	case "argo":
		w.Header().Set("Content-Type", "text/plain")
	default:
		w.Header().Set("Content-Type", "text/event-stream")
	}
}

func compareCanonicalJSONBytes(want, got []byte) error {
	wantCanonical, err := apifixtures.CanonicalJSON(want)
	if err != nil {
		return fmt.Errorf("CanonicalJSON(want) error = %w", err)
	}
	gotCanonical, err := apifixtures.CanonicalJSON(got)
	if err != nil {
		return fmt.Errorf("CanonicalJSON(got) error = %w", err)
	}

	if !bytes.Equal(wantCanonical, gotCanonical) {
		return fmt.Errorf("JSON mismatch\nwant:\n%s\n\ngot:\n%s", wantCanonical, gotCanonical)
	}
	return nil
}

func readFixtureJSON(root, caseID, rel string, dest interface{}) error {
	body, err := apifixtures.ReadCaseFile(root, caseID, rel)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dest)
}

func extractRequestModel(body []byte) (string, error) {
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	model, _ := decoded["model"].(string)
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("request model missing")
	}
	return model, nil
}

func newFixtureConverter() *Converter {
	return NewConverter(NewModelMapper(&Config{}))
}

func renderAnthropicStreamFromOpenAI(raw []byte, model string) ([]byte, error) {
	recorder := newFlushableRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, model, context.Background())
	if err != nil {
		return nil, err
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		return nil, err
	}
	parser := NewOpenAIStreamParser(handler)
	if err := parser.Parse(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return recorder.Body.Bytes(), nil
}

func renderAnthropicStreamFromGoogle(raw []byte, model string) ([]byte, error) {
	recorder := newFlushableRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, model, context.Background())
	if err != nil {
		return nil, err
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		return nil, err
	}
	parser := NewGoogleStreamParser(handler)
	if err := parser.Parse(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return recorder.Body.Bytes(), nil
}

func renderAnthropicStreamFromArgo(raw []byte, model string) ([]byte, error) {
	recorder := newFlushableRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, model, context.Background())
	if err != nil {
		return nil, err
	}
	if err := ensureAnthropicTextPreamble(handler); err != nil {
		return nil, err
	}
	parser := NewArgoStreamParser(handler)
	if err := parser.Parse(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return recorder.Body.Bytes(), nil
}

func renderOpenAIStreamFromAnthropic(server *Server, raw []byte, model string) ([]byte, error) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, model, context.Background())
	if err != nil {
		return nil, err
	}
	if err := server.convertAnthropicStreamToOpenAI(context.Background(), bytes.NewReader(raw), writer); err != nil {
		return nil, err
	}
	return recorder.Body.Bytes(), nil
}

func renderOpenAIStreamFromGoogle(server *Server, raw []byte, model string) ([]byte, error) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, model, context.Background())
	if err != nil {
		return nil, err
	}
	if err := server.convertGoogleStreamToOpenAI(context.Background(), bytes.NewReader(raw), writer); err != nil {
		return nil, err
	}
	return recorder.Body.Bytes(), nil
}

func renderOpenAIStreamFromArgo(raw []byte, model string) ([]byte, error) {
	recorder := newFlushableRecorder()
	writer, err := NewOpenAIStreamWriter(recorder, model, context.Background())
	if err != nil {
		return nil, err
	}
	converter := NewOpenAIStreamConverter(writer, context.Background())
	if err := converter.HandleArgoText(string(raw)); err != nil {
		return nil, err
	}
	if err := converter.FinishStream("stop", nil); err != nil {
		return nil, err
	}
	return recorder.Body.Bytes(), nil
}

func filterOptionalAnthropicUsageDifferences(differences []string) []string {
	filtered := make([]string, 0, len(differences))
	for _, diff := range differences {
		switch {
		case strings.Contains(diff, "cache_creation"),
			strings.Contains(diff, "cache_read_input_tokens"):
			continue
		default:
			filtered = append(filtered, diff)
		}
	}
	return filtered
}
