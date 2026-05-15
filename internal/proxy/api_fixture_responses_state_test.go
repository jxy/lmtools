package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/apifixtures"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAPIFixtureStatefulResponses(t *testing.T) {
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
		if !apifixtures.StringSliceContains(meta.Kinds, "stateful") {
			continue
		}

		t.Run(meta.ID, func(t *testing.T) {
			runStatefulResponsesFixture(t, suite.Root, meta)
		})
	}
}

func runStatefulResponsesFixture(t *testing.T, root string, meta apifixtures.CaseMeta) {
	t.Helper()

	var scenario apifixtures.StatefulScenario
	scenarioBytes, err := apifixtures.ReadCaseFile(root, meta.ID, "scenario.json")
	if err != nil {
		t.Fatalf("ReadCaseFile(scenario.json) error = %v", err)
	}
	if err := json.Unmarshal(scenarioBytes, &scenario); err != nil {
		t.Fatalf("unmarshal scenario.json error = %v", err)
	}

	backend := &statefulFixtureBackend{t: t, root: root, caseID: meta.ID}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, roundTripFunc(backend.RoundTrip))
	server := NewTestServerDirectWithClient(t, statefulFixtureConfig(t, scenario), client)
	vars := map[string]string{}

	for _, step := range scenario.Steps {
		t.Run(step.ID, func(t *testing.T) {
			runStatefulStep(t, server, backend, step, vars)
		})
	}
	backend.WaitForIdle(t, 2*time.Second)
	waitForStatefulFixtureBackground(t, server)
}

func statefulFixtureConfig(t *testing.T, scenario apifixtures.StatefulScenario) *Config {
	t.Helper()

	provider := strings.TrimSpace(scenario.Provider)
	if provider == "" {
		provider = constants.ProviderAnthropic
	}
	model := strings.TrimSpace(scenario.Model)
	if model == "" {
		model = "claude-test"
	}
	providerURL := "http://anthropic.local"
	if provider == constants.ProviderOpenAI {
		providerURL = "http://openai.local"
	}

	return &Config{
		Provider:           provider,
		ProviderURL:        providerURL,
		OpenAIAPIKey:       fixtureOpenAIKey,
		AnthropicAPIKey:    fixtureAnthropicKey,
		ModelMapRules:      []ModelMapRule{{Pattern: ".*", Model: model}},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
}

func runStatefulStep(t *testing.T, server http.Handler, backend *statefulFixtureBackend, step apifixtures.StatefulStep, vars map[string]string) {
	t.Helper()

	if step.Upstream != nil {
		backend.Enqueue(step.Upstream, vars)
	}
	startBackendRequests := backend.RequestCount()

	bodyBytes := marshalStatefulStepBody(t, step.Body, vars)
	method := strings.ToUpper(apifixtures.SubstituteStatefulString(step.Method, vars))
	path := apifixtures.SubstituteStatefulString(step.Path, vars)

	var status int
	var responseBody []byte
	var decoded map[string]interface{}
	if len(step.PollUntil) > 0 {
		status, responseBody, decoded = pollStatefulRequest(t, server, method, path, bodyBytes, step.Expect.Status, step.PollUntil, vars)
	} else {
		status, responseBody, decoded = doStatefulRequest(t, server, method, path, bodyBytes)
	}

	assertStatefulResponse(t, step, vars, status, responseBody, decoded)
	assertStatefulBackendRequests(t, backend, startBackendRequests, step.BackendRequests, vars)
	bindStatefulVars(t, step.Bind, decoded, vars)
}

func marshalStatefulStepBody(t *testing.T, body interface{}, vars map[string]string) []byte {
	t.Helper()
	if body == nil {
		return nil
	}
	data, err := json.Marshal(apifixtures.SubstituteStatefulValue(body, vars))
	if err != nil {
		t.Fatalf("marshal step body error = %v", err)
	}
	return data
}

func doStatefulRequest(t *testing.T, server http.Handler, method, path string, body []byte) (int, []byte, map[string]interface{}) {
	t.Helper()

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll(response) error = %v", err)
	}
	decoded := map[string]interface{}{}
	if len(bytes.TrimSpace(responseBody)) > 0 {
		_ = json.Unmarshal(responseBody, &decoded)
	}
	return resp.StatusCode, responseBody, decoded
}

func pollStatefulRequest(t *testing.T, server http.Handler, method, path string, body []byte, expectedStatus int, fields map[string]interface{}, vars map[string]string) (int, []byte, map[string]interface{}) {
	t.Helper()

	if expectedStatus == 0 {
		expectedStatus = http.StatusOK
	}
	deadline := time.Now().Add(2 * time.Second)
	var status int
	var responseBody []byte
	var decoded map[string]interface{}
	for {
		status, responseBody, decoded = doStatefulRequest(t, server, method, path, body)
		if status == expectedStatus && apifixtures.StatefulFieldsMatch(decoded, fields, vars) {
			return status, responseBody, decoded
		}
		if time.Now().After(deadline) {
			return status, responseBody, decoded
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertStatefulResponse(t *testing.T, step apifixtures.StatefulStep, vars map[string]string, status int, body []byte, decoded map[string]interface{}) {
	t.Helper()

	expectedStatus := step.Expect.Status
	if expectedStatus == 0 {
		expectedStatus = http.StatusOK
	}
	if status != expectedStatus {
		t.Fatalf("%s %s status = %d, want %d, body = %s", step.Method, step.Path, status, expectedStatus, string(body))
	}
	assertStatefulJSONFields(t, "response", decoded, step.Expect.JSONFields, vars)
	for _, rawWant := range step.Expect.BodyContains {
		want := apifixtures.SubstituteStatefulString(rawWant, vars)
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("response body does not contain %q: %s", want, string(body))
		}
	}
}

func assertStatefulJSONFields(t *testing.T, label string, decoded map[string]interface{}, fields map[string]interface{}, vars map[string]string) {
	t.Helper()
	for path, rawWant := range fields {
		want := apifixtures.SubstituteStatefulValue(rawWant, vars)
		got, ok := apifixtures.LookupStatefulJSONPath(decoded, path)
		if !ok {
			t.Fatalf("%s missing JSON field %q in %#v", label, path, decoded)
		}
		if !apifixtures.StatefulValuesEqual(got, want) {
			t.Fatalf("%s JSON field %q = %#v, want %#v", label, path, got, want)
		}
	}
}

func bindStatefulVars(t *testing.T, bindings map[string]string, decoded map[string]interface{}, vars map[string]string) {
	t.Helper()
	for name, path := range bindings {
		value, ok := apifixtures.LookupStatefulJSONPath(decoded, path)
		if !ok {
			t.Fatalf("binding %q path %q missing in %#v", name, path, decoded)
		}
		vars[name] = fmt.Sprint(value)
	}
}

func assertStatefulBackendRequests(t *testing.T, backend *statefulFixtureBackend, start int, expected []apifixtures.StatefulExpectedBackendRequest, vars map[string]string) {
	t.Helper()
	if expected == nil {
		return
	}
	requests := backend.WaitForRequests(start+len(expected), 2*time.Second)
	newRequests := requests[start:]
	if len(newRequests) != len(expected) {
		t.Fatalf("backend request count after step = %d, want %d", len(newRequests), len(expected))
	}
	for i, want := range expected {
		got := newRequests[i]
		if want.Method != "" && got.Method != apifixtures.SubstituteStatefulString(want.Method, vars) {
			t.Fatalf("backend request %d method = %s, want %s", i, got.Method, want.Method)
		}
		if want.Path != "" && got.Path != apifixtures.SubstituteStatefulString(want.Path, vars) {
			t.Fatalf("backend request %d path = %s, want %s", i, got.Path, want.Path)
		}
		for _, rawNeedle := range want.BodyContains {
			needle := apifixtures.SubstituteStatefulString(rawNeedle, vars)
			if !bytes.Contains(got.Body, []byte(needle)) {
				t.Fatalf("backend request %d body does not contain %q: %s", i, needle, string(got.Body))
			}
		}
		for _, rawNeedle := range want.BodyNotContains {
			needle := apifixtures.SubstituteStatefulString(rawNeedle, vars)
			if bytes.Contains(got.Body, []byte(needle)) {
				t.Fatalf("backend request %d body unexpectedly contains %q: %s", i, needle, string(got.Body))
			}
		}
		if len(want.JSONFields) > 0 {
			var decoded map[string]interface{}
			if err := json.Unmarshal(got.Body, &decoded); err != nil {
				t.Fatalf("backend request %d JSON decode error = %v, body = %s", i, err, string(got.Body))
			}
			assertStatefulJSONFields(t, fmt.Sprintf("backend request %d", i), decoded, want.JSONFields, vars)
		}
	}
}

type statefulFixtureBackend struct {
	t        *testing.T
	root     string
	caseID   string
	mu       sync.Mutex
	wg       sync.WaitGroup
	queue    []statefulFixtureUpstream
	requests []statefulFixtureBackendRequest
}

type statefulFixtureUpstream struct {
	status int
	body   []byte
	delay  time.Duration
}

type statefulFixtureBackendRequest struct {
	Method string
	Path   string
	Body   []byte
}

func (b *statefulFixtureBackend) Enqueue(upstream *apifixtures.StatefulUpstream, vars map[string]string) {
	b.t.Helper()
	bodyPath := apifixtures.SubstituteStatefulString(upstream.Body, vars)
	body, err := apifixtures.ReadCaseFile(b.root, b.caseID, bodyPath)
	if err != nil {
		b.t.Fatalf("ReadCaseFile(%q) error = %v", bodyPath, err)
	}
	status := upstream.Status
	if status == 0 {
		status = http.StatusOK
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue = append(b.queue, statefulFixtureUpstream{
		status: status,
		body:   body,
		delay:  time.Duration(upstream.DelayMS) * time.Millisecond,
	})
}

func (b *statefulFixtureBackend) RoundTrip(r *http.Request) (*http.Response, error) {
	b.wg.Add(1)
	defer b.wg.Done()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	b.requests = append(b.requests, statefulFixtureBackendRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   append([]byte(nil), body...),
	})
	if len(b.queue) == 0 {
		b.mu.Unlock()
		return jsonRoundTripResponse(http.StatusInternalServerError, map[string]interface{}{"error": "unexpected upstream request"}), nil
	}
	upstream := b.queue[0]
	b.queue = b.queue[1:]
	b.mu.Unlock()

	if upstream.delay > 0 {
		timer := time.NewTimer(upstream.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}

	resp := jsonRoundTripResponse(upstream.status, json.RawMessage(upstream.body))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

func (b *statefulFixtureBackend) RequestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.requests)
}

func (b *statefulFixtureBackend) WaitForRequests(count int, timeout time.Duration) []statefulFixtureBackendRequest {
	deadline := time.Now().Add(timeout)
	for {
		b.mu.Lock()
		requests := append([]statefulFixtureBackendRequest(nil), b.requests...)
		b.mu.Unlock()
		if len(requests) >= count || time.Now().After(deadline) {
			return requests
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *statefulFixtureBackend) WaitForIdle(t *testing.T, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("stateful fixture backend still has active requests")
	}
}

func waitForStatefulFixtureBackground(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		server.backgroundMu.Lock()
		count := len(server.backgroundCancel)
		server.backgroundMu.Unlock()
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background responses still running: %d", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
