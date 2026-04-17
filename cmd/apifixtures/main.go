package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"lmtools/internal/apifixtures"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"lmtools/internal/retry"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultOpenAIURL    = "https://api.openai.com/v1/chat/completions"
	defaultAnthropicURL = "https://api.anthropic.com/v1/messages"
	defaultGoogleBase   = "https://generativelanguage.googleapis.com/v1beta/models"
	defaultArgoBase     = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
)

type targetConfig = apifixtures.CaptureTarget

type captureMetadata struct {
	CaseID         string              `json:"case_id"`
	Target         string              `json:"target"`
	URL            string              `json:"url"`
	StatusCode     int                 `json:"status_code"`
	ContentType    string              `json:"content_type,omitempty"`
	CapturedAt     string              `json:"captured_at"`
	ResponseHeader map[string][]string `json:"response_headers,omitempty"`
}

func main() {
	command, args, ok := parseSubcommandArgs(os.Args[1:])
	if !ok {
		usage()
		os.Exit(2)
	}

	switch command {
	case "list":
		if err := runList(); err != nil {
			fatal(err)
		}
	case "verify":
		if err := runVerify(args); err != nil {
			fatal(err)
		}
	case "compare":
		if err := runCompare(args); err != nil {
			fatal(err)
		}
	case "compare-all":
		if err := runCompareAll(args); err != nil {
			fatal(err)
		}
	case "capture":
		if err := runCapture(args); err != nil {
			fatal(err)
		}
	case "capture-all":
		if err := runCaptureAll(args); err != nil {
			fatal(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func parseSubcommandArgs(args []string) (string, []string, bool) {
	if len(args) == 0 {
		return "", nil, false
	}
	if args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", nil, false
	}
	return args[0], args[1:], true
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: apifixtures <list|verify|compare|compare-all|capture|capture-all> [flags]\n")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func runList() error {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		return err
	}
	for _, fixtureCase := range suite.Manifest.Cases {
		meta, err := apifixtures.LoadCaseMeta(suite.Root, fixtureCase.ID)
		if err != nil {
			return err
		}
		fmt.Printf("%-32s %s | %s\n", fixtureCase.ID, apifixtures.SummaryLine(meta), fixtureCase.Description)
	}
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	caseID := fs.String("case", "", "optional fixture case id")
	checkCaptures := fs.Bool("check-captures", false, "require capture metadata and successful capture status")
	provider := fs.String("provider", "", "optional source provider filter (anthropic|openai|google|argo)")
	target := fs.String("target", "", "optional capture target filter")
	if err := fs.Parse(args); err != nil {
		return err
	}

	suite, err := apifixtures.LoadSuite()
	if err != nil {
		return err
	}

	return apifixtures.VerifySuite(suite.Root, apifixtures.VerifyOptions{
		CaseID:        *caseID,
		CheckCaptures: *checkCaptures,
		Provider:      strings.TrimSpace(*provider),
		Target:        strings.TrimSpace(*target),
	})
}

func runCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	caseID := fs.String("case", "", "fixture case id")
	targetID := fs.String("target", "", "capture target id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseID == "" || *targetID == "" {
		return fmt.Errorf("-case and -target are required")
	}

	suite, err := apifixtures.LoadSuite()
	if err != nil {
		return err
	}
	return captureCase(suite.Root, *caseID, *targetID)
}

func runCaptureAll(args []string) error {
	fs := flag.NewFlagSet("capture-all", flag.ContinueOnError)
	caseID := fs.String("case", "", "optional fixture case id")
	targetID := fs.String("target", "", "optional capture target id")
	provider := fs.String("provider", "", "optional source provider filter (anthropic|openai|google|argo)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	suite, err := apifixtures.LoadSuite()
	if err != nil {
		return err
	}

	foundCase := *caseID == ""
	capturedAny := false

	for _, fixtureCase := range suite.Manifest.Cases {
		if *caseID != "" && fixtureCase.ID != *caseID {
			continue
		}
		foundCase = true

		meta, err := apifixtures.LoadCaseMeta(suite.Root, fixtureCase.ID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*provider) != "" && apifixtures.SourceProvider(meta) != strings.TrimSpace(*provider) {
			continue
		}

		targets := meta.CaptureTargets
		if *targetID != "" {
			if !apifixtures.StringSliceContains(meta.Kinds, "request") {
				continue
			}
			if !apifixtures.StringSliceContains(meta.CaptureTargets, *targetID) {
				continue
			}
			targets = []string{*targetID}
		}
		for _, target := range targets {
			capturedAny = true
			if err := captureCase(suite.Root, fixtureCase.ID, target); err != nil {
				return err
			}
		}
	}

	if !foundCase {
		return fmt.Errorf("case %q not found in fixture manifest", *caseID)
	}
	if *provider != "" && !capturedAny {
		return fmt.Errorf("no capture-capable request fixtures found for provider %q", *provider)
	}
	if *caseID != "" && *targetID != "" && !capturedAny {
		return fmt.Errorf("case %q does not support capture target %q", *caseID, *targetID)
	}

	return nil
}

func captureCase(root, caseID, targetID string) error {
	meta, err := apifixtures.LoadCaseMeta(root, caseID)
	if err != nil {
		return err
	}

	target, err := parseTarget(targetID)
	if err != nil {
		return err
	}
	if !apifixtures.StringSliceContains(meta.CaptureTargets, targetID) {
		return fmt.Errorf("case %q does not support capture target %q", caseID, targetID)
	}

	body, err := loadCaptureRequestBody(root, caseID, meta, target)
	if err != nil {
		return fmt.Errorf("read %s body for %s: %w", target.ID, caseID, err)
	}

	url, headers, err := endpointForTarget(target, meta)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, data, err := doCaptureRequest(context.Background(), &http.Client{Timeout: 2 * time.Minute}, req, body, targetHost(target), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	metaOut := captureMetadata{
		CaseID:         caseID,
		Target:         target.ID,
		URL:            url,
		StatusCode:     resp.StatusCode,
		ContentType:    resp.Header.Get("Content-Type"),
		CapturedAt:     time.Now().Format(time.RFC3339),
		ResponseHeader: resp.Header,
	}

	capturesDir := filepath.Join(apifixtures.CaseDir(root, caseID), "captures")
	if err := os.MkdirAll(capturesDir, 0o755); err != nil {
		return err
	}

	metaBytes, err := json.MarshalIndent(metaOut, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(capturesDir, target.ID+".meta.json"), append(metaBytes, '\n'), 0o644); err != nil {
		return err
	}

	if target.Stream {
		if err := os.WriteFile(filepath.Join(capturesDir, target.ID+".stream.txt"), data, 0o644); err != nil {
			return err
		}
	} else {
		if err := apifixtures.CanonicalizeToFile(root, caseID, filepath.Join("captures", target.ID+".response.json"), data); err != nil {
			return err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := refreshDerivedArtifacts(root, meta, target, data); err != nil {
				return err
			}
		}
	}

	fmt.Printf("captured %s -> %s (%d)\n", caseID, target.ID, resp.StatusCode)
	return nil
}

func doCaptureRequest(ctx context.Context, client *http.Client, req *http.Request, body []byte, provider string, cfg *retry.Config) (*http.Response, []byte, error) {
	if cfg == nil {
		cfg = retry.ProviderConfig(provider)
	}
	if cfg == nil {
		cfg = retry.DefaultConfig()
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var lastResp *http.Response
	var lastBody []byte
	var overrideBackoff time.Duration

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := calculateCaptureBackoff(cfg, attempt-1, rng)
			if overrideBackoff > 0 {
				backoff = overrideBackoff
				overrideBackoff = 0
			}
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		reqClone := req.Clone(ctx)
		reqClone.Body = io.NopCloser(bytes.NewReader(body))
		reqClone.ContentLength = int64(len(body))

		resp, err := client.Do(reqClone)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() && attempt < cfg.MaxRetries {
				continue
			}
			return nil, nil, err
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, err
		}

		lastResp = cloneHTTPResponse(resp, data)
		lastBody = append(lastBody[:0], data...)

		if !shouldRetryCaptureStatus(resp.StatusCode) {
			return cloneHTTPResponse(resp, data), data, nil
		}

		if retryAfter := retry.ExtractRetryAfter(lastResp); retryAfter > 0 {
			nextBackoff := calculateCaptureBackoff(cfg, attempt, rng)
			if retryAfter > nextBackoff {
				overrideBackoff = retryAfter
			}
		} else if retryAfter := extractProviderRetryDelay(provider, data); retryAfter > 0 {
			nextBackoff := calculateCaptureBackoff(cfg, attempt, rng)
			if retryAfter > nextBackoff {
				overrideBackoff = retryAfter
			}
		}
	}

	if lastResp != nil {
		return lastResp, lastBody, nil
	}
	return nil, nil, fmt.Errorf("capture request failed without response")
}

func shouldRetryCaptureStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		425,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		if statusCode >= 400 && statusCode < 500 {
			return false
		}
		return statusCode >= 500
	}
}

func calculateCaptureBackoff(cfg *retry.Config, attempt int, rng *rand.Rand) time.Duration {
	backoff := float64(cfg.InitialBackoff) * math.Pow(cfg.BackoffFactor, float64(attempt))
	jitter := (rng.Float64() - 0.5) * 0.5
	backoff = backoff * (1 + jitter)
	if cfg.MaxBackoff > 0 && backoff > float64(cfg.MaxBackoff) {
		backoff = float64(cfg.MaxBackoff)
	}
	return time.Duration(backoff)
}

func cloneHTTPResponse(resp *http.Response, body []byte) *http.Response {
	if resp == nil {
		return nil
	}
	clone := new(http.Response)
	*clone = *resp
	clone.Header = resp.Header.Clone()
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	return clone
}

func extractProviderRetryDelay(provider string, body []byte) time.Duration {
	switch provider {
	case "google":
		return extractGoogleRetryDelay(body)
	default:
		return 0
	}
}

func extractGoogleRetryDelay(body []byte) time.Duration {
	var payload struct {
		Error struct {
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	for _, detail := range payload.Error.Details {
		if detail.Type != "type.googleapis.com/google.rpc.RetryInfo" {
			continue
		}
		delay, err := time.ParseDuration(detail.RetryDelay)
		if err == nil && delay > 0 {
			return delay
		}
	}
	return 0
}

func refreshDerivedArtifacts(root string, meta apifixtures.CaseMeta, target targetConfig, data []byte) error {
	if target.Stream {
		return nil
	}
	if !apifixtures.StringSliceContains(meta.Kinds, "response") {
		return nil
	}
	if meta.Provider != target.ID {
		return nil
	}

	projected, err := core.ParseResponseProjection(meta.Provider, data)
	if err != nil {
		return fmt.Errorf("refresh parsed response for %s: %w", meta.ID, err)
	}

	projectedBytes, err := json.Marshal(projected)
	if err != nil {
		return err
	}
	return apifixtures.CanonicalizeToFile(root, meta.ID, filepath.Join("expected", "parsed.json"), projectedBytes)
}

func loadCaptureRequestBody(root, caseID string, meta apifixtures.CaseMeta, target targetConfig) ([]byte, error) {
	bodyRel := captureRequestRel(root, caseID, target)
	body, err := apifixtures.ReadCaseFile(root, caseID, bodyRel)
	if err != nil {
		return nil, err
	}
	return prepareCaptureRequestBody(meta, target, body)
}

func captureRequestRel(root, caseID string, target targetConfig) string {
	candidates := []string{
		fmt.Sprintf("expected/render/%s.capture.request.json", target.ID),
		fmt.Sprintf("expected/render/%s.capture.request.json", target.Provider),
		fmt.Sprintf("expected/render/%s.request.json", target.ID),
		fmt.Sprintf("expected/render/%s.request.json", target.Provider),
	}
	for _, rel := range candidates {
		if apifixtures.CaseFileExists(root, caseID, rel) {
			return rel
		}
	}
	return fmt.Sprintf("expected/render/%s.request.json", target.Provider)
}

func prepareCaptureRequestBody(meta apifixtures.CaseMeta, target targetConfig, body []byte) ([]byte, error) {
	var decoded map[string]interface{}
	needsDecode := target.Stream || isLegacyArgoTarget(target) || isArgoHostedCompatibilityTarget(target)
	if !needsDecode {
		return body, nil
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}

	if isArgoHostedCompatibilityTarget(target) {
		model := captureModelForTarget(meta, target)
		if model == "" {
			return nil, fmt.Errorf("case %s is missing a compatible model for target %s", meta.ID, target.ID)
		}
		decoded["model"] = model
	}

	switch target.Provider {
	case "openai", "anthropic":
		if target.Stream {
			decoded["stream"] = true
		}
	case "argo":
		apiKey := strings.TrimSpace(os.Getenv("ARGO_API_KEY"))
		if apiKey == "" {
			return nil, fmt.Errorf("ARGO_API_KEY is required for target %s", target.ID)
		}
		decoded["user"] = apiKey
	}

	return json.Marshal(decoded)
}

func parseTarget(targetID string) (targetConfig, error) {
	return apifixtures.ParseCaptureTarget(targetID)
}

func endpointForTarget(target targetConfig, meta apifixtures.CaseMeta) (string, map[string]string, error) {
	headers := map[string]string{}

	switch targetHost(target) {
	case "openai":
		apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		if apiKey == "" {
			return "", nil, fmt.Errorf("OPENAI_API_KEY is required for target %s", target.ID)
		}
		headers["Authorization"] = "Bearer " + apiKey
		if target.Stream {
			headers["Accept"] = "text/event-stream"
		}
		return envOrDefault("OPENAI_API_FIXTURE_URL", defaultOpenAIURL), headers, nil

	case "anthropic":
		apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
		if apiKey == "" {
			return "", nil, fmt.Errorf("ANTHROPIC_API_KEY is required for target %s", target.ID)
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		if target.Stream {
			headers["Accept"] = "text/event-stream"
		}
		return envOrDefault("ANTHROPIC_API_FIXTURE_URL", defaultAnthropicURL), headers, nil

	case "google":
		apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
		if apiKey == "" {
			return "", nil, fmt.Errorf("GOOGLE_API_KEY is required for target %s", target.ID)
		}
		headers["x-goog-api-key"] = apiKey
		model := strings.TrimSpace(meta.Models["google"])
		if model == "" {
			return "", nil, fmt.Errorf("case %s is missing models.google", meta.ID)
		}
		action := "generateContent"
		if target.Stream {
			action = "streamGenerateContent"
		}
		base := strings.TrimRight(envOrDefault("GOOGLE_API_FIXTURE_URL", defaultGoogleBase), "/")
		url, err := providers.BuildGoogleModelURL(base, model, action)
		if err != nil {
			return "", nil, err
		}
		return url, headers, nil

	case "argo":
		endpoints, err := argoFixtureEndpoints(envOrDefault("ARGO_API_FIXTURE_BASE_URL", defaultArgoBase))
		if err != nil {
			return "", nil, err
		}
		switch target.Provider {
		case "argo":
			if target.Stream {
				return endpoints.legacyStream, headers, nil
			}
			return endpoints.legacyChat, headers, nil
		case "openai":
			apiKey := strings.TrimSpace(os.Getenv("ARGO_API_KEY"))
			if apiKey == "" {
				return "", nil, fmt.Errorf("ARGO_API_KEY is required for target %s", target.ID)
			}
			headers["Authorization"] = "Bearer " + apiKey
			if target.Stream {
				headers["Accept"] = "text/event-stream"
			}
			return endpoints.openAI, headers, nil
		case "anthropic":
			apiKey := strings.TrimSpace(os.Getenv("ARGO_API_KEY"))
			if apiKey == "" {
				return "", nil, fmt.Errorf("ARGO_API_KEY is required for target %s", target.ID)
			}
			headers["x-api-key"] = apiKey
			headers["anthropic-version"] = "2023-06-01"
			if target.Stream {
				headers["Accept"] = "text/event-stream"
			}
			return endpoints.anthropic, headers, nil
		}
	}

	return "", nil, fmt.Errorf("unsupported provider %q", target.Provider)
}

type resolvedArgoFixtureEndpoints struct {
	root         string
	legacyChat   string
	legacyStream string
	openAI       string
	anthropic    string
}

func argoFixtureEndpoints(rawBase string) (resolvedArgoFixtureEndpoints, error) {
	root, err := argoFixtureRoot(rawBase)
	if err != nil {
		return resolvedArgoFixtureEndpoints{}, err
	}

	legacyChat, err := buildArgoFixtureURL(root, "api/v1/resource/chat", true)
	if err != nil {
		return resolvedArgoFixtureEndpoints{}, err
	}
	legacyStream, err := buildArgoFixtureURL(root, "api/v1/resource/streamchat", true)
	if err != nil {
		return resolvedArgoFixtureEndpoints{}, err
	}
	openAI, err := buildArgoFixtureURL(root, "v1/chat/completions", false)
	if err != nil {
		return resolvedArgoFixtureEndpoints{}, err
	}
	anthropic, err := buildArgoFixtureURL(root, "v1/messages", false)
	if err != nil {
		return resolvedArgoFixtureEndpoints{}, err
	}

	return resolvedArgoFixtureEndpoints{
		root:         root,
		legacyChat:   legacyChat,
		legacyStream: legacyStream,
		openAI:       openAI,
		anthropic:    anthropic,
	}, nil
}

func argoFixtureRoot(rawBase string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawBase))
	if err != nil {
		return "", fmt.Errorf("invalid Argo fixture base URL %q: %w", rawBase, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported Argo fixture base URL scheme %q", u.Scheme)
	}

	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(path, "/api/v1/resource"):
		path = strings.TrimSuffix(path, "/api/v1/resource")
	case strings.HasSuffix(path, "/api/v1"):
		path = strings.TrimSuffix(path, "/api/v1")
	case strings.HasSuffix(path, "/v1/chat/completions"):
		path = strings.TrimSuffix(path, "/v1/chat/completions")
	case strings.HasSuffix(path, "/v1/messages"):
		path = strings.TrimSuffix(path, "/v1/messages")
	case strings.Contains(path, "/api/v1"):
		path = path[:strings.Index(path, "/api/v1")]
	case strings.Contains(path, "/v1"):
		path = path[:strings.Index(path, "/v1")]
	}

	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func buildArgoFixtureURL(root, suffix string, trailingSlash bool) (string, error) {
	resolved, err := providers.BuildProviderURL(root, suffix)
	if err != nil {
		return "", err
	}
	if trailingSlash && !strings.HasSuffix(resolved, "/") {
		resolved += "/"
	}
	return resolved, nil
}

func isLegacyArgoTarget(target targetConfig) bool {
	return targetHost(target) == "argo" && target.Provider == "argo"
}

func isArgoHostedCompatibilityTarget(target targetConfig) bool {
	return targetHost(target) == "argo" && target.Provider != "argo"
}

func captureModelForTarget(meta apifixtures.CaseMeta, target targetConfig) string {
	if !isArgoHostedCompatibilityTarget(target) {
		return ""
	}

	for _, key := range []string{target.ID, "argo-" + target.Provider} {
		if model := strings.TrimSpace(meta.Models[key]); model != "" {
			return model
		}
	}

	argoModel := strings.TrimSpace(meta.Models["argo"])
	if argoModel == "" {
		return strings.TrimSpace(meta.Models[target.Provider])
	}
	if target.Provider == "anthropic" && providers.DetermineArgoModelProvider(argoModel) != "anthropic" {
		return strings.TrimSpace(meta.Models["anthropic"])
	}
	return argoModel
}

func targetHost(target targetConfig) string {
	if target.Host != "" {
		return target.Host
	}
	return target.Provider
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
