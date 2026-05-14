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
	"lmtools/internal/modelcatalog"
	"lmtools/internal/providers"
	"lmtools/internal/retry"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOpenAIURL          = "https://api.openai.com/v1/chat/completions"
	defaultOpenAIResponsesURL = "https://api.openai.com/v1/responses"
	defaultAnthropicURL       = "https://api.anthropic.com/v1/messages"
	defaultGoogleBase         = "https://generativelanguage.googleapis.com/v1beta/models"
	defaultArgoBase           = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
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

type statefulCaptureMetadata struct {
	CaseID         string              `json:"case_id"`
	Target         string              `json:"target"`
	StepID         string              `json:"step_id"`
	Method         string              `json:"method"`
	Path           string              `json:"path"`
	URL            string              `json:"url"`
	StatusCode     int                 `json:"status_code"`
	ContentType    string              `json:"content_type,omitempty"`
	CapturedAt     string              `json:"captured_at"`
	ResponseHeader map[string][]string `json:"response_headers,omitempty"`
}

type statefulCaptureSummary struct {
	CaseID     string                    `json:"case_id"`
	Target     string                    `json:"target"`
	CapturedAt string                    `json:"captured_at"`
	Steps      []statefulCaptureMetadata `json:"steps"`
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
	checkCaptures := fs.Bool("check-captures", false, "require capture metadata and expected capture status")
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
			if !isCaptureCapableCase(meta) {
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
		return fmt.Errorf("no capture-capable fixtures found for provider %q", *provider)
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

	if apifixtures.StringSliceContains(meta.Kinds, "models") {
		return captureModelsCase(root, meta, target)
	}
	if apifixtures.StringSliceContains(meta.Kinds, "token-count") {
		return captureTokenCountCase(root, meta, target)
	}
	if apifixtures.StringSliceContains(meta.Kinds, "stateful") {
		return captureStatefulCase(root, meta, target)
	}
	if !apifixtures.StringSliceContains(meta.Kinds, "request") {
		return fmt.Errorf("case %q is not capture-capable", caseID)
	}

	return captureRequestCase(root, meta, target)
}

func isCaptureCapableCase(meta apifixtures.CaseMeta) bool {
	return apifixtures.StringSliceContains(meta.Kinds, "request") ||
		apifixtures.StringSliceContains(meta.Kinds, "models") ||
		apifixtures.StringSliceContains(meta.Kinds, "token-count") ||
		apifixtures.StringSliceContains(meta.Kinds, "stateful")
}

func captureTokenCountCase(root string, meta apifixtures.CaseMeta, target targetConfig) error {
	return captureTokenCountCaseWithClient(root, meta, target, &http.Client{Timeout: 2 * time.Minute})
}

func captureTokenCountCaseWithClient(root string, meta apifixtures.CaseMeta, target targetConfig, client *http.Client) error {
	if target.Stream {
		return fmt.Errorf("token-count capture target %q must not be a stream target", target.ID)
	}
	if target.Provider != meta.Provider {
		return fmt.Errorf("token-count capture target %q must match provider %q", target.ID, meta.Provider)
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	body, err := loadTokenCountRequestBody(root, meta, target)
	if err != nil {
		return err
	}
	url, headers, err := tokenCountEndpointForTarget(target, meta)
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

	resp, data, err := doCaptureRequest(context.Background(), client, req, body, targetHost(target), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	metaOut := captureMetadata{
		CaseID:         meta.ID,
		Target:         target.ID,
		URL:            url,
		StatusCode:     resp.StatusCode,
		ContentType:    resp.Header.Get("Content-Type"),
		CapturedAt:     time.Now().Format(time.RFC3339),
		ResponseHeader: resp.Header,
	}

	capturesDir := filepath.Join(apifixtures.CaseDir(root, meta.ID), "captures")
	if err := os.MkdirAll(capturesDir, 0o755); err != nil {
		return err
	}
	if err := writeTokenCountCaptureRequest(root, meta.ID, filepath.Join("captures", target.ID+".request.json"), url, body); err != nil {
		return err
	}
	metaBytes, err := json.MarshalIndent(metaOut, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(capturesDir, target.ID+".meta.json"), append(metaBytes, '\n'), 0o644); err != nil {
		return err
	}
	if err := apifixtures.CanonicalizeToFile(root, meta.ID, filepath.Join("captures", target.ID+".response.json"), data); err != nil {
		return err
	}

	fmt.Printf("captured %s -> %s (%d)\n", meta.ID, target.ID, resp.StatusCode)
	return nil
}

func captureRequestCase(root string, meta apifixtures.CaseMeta, target targetConfig) error {
	body, err := loadCaptureRequestBody(root, meta.ID, meta, target)
	if err != nil {
		return fmt.Errorf("read %s body for %s: %w", target.ID, meta.ID, err)
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
		CaseID:         meta.ID,
		Target:         target.ID,
		URL:            url,
		StatusCode:     resp.StatusCode,
		ContentType:    resp.Header.Get("Content-Type"),
		CapturedAt:     time.Now().Format(time.RFC3339),
		ResponseHeader: resp.Header,
	}

	capturesDir := filepath.Join(apifixtures.CaseDir(root, meta.ID), "captures")
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
		data = normalizeStreamCapture(data)
		if err := os.WriteFile(filepath.Join(capturesDir, target.ID+".stream.txt"), data, 0o644); err != nil {
			return err
		}
	} else {
		if err := apifixtures.CanonicalizeToFile(root, meta.ID, filepath.Join("captures", target.ID+".response.json"), data); err != nil {
			return err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := refreshDerivedArtifacts(root, meta, target, data); err != nil {
				return err
			}
		}
	}

	fmt.Printf("captured %s -> %s (%d)\n", meta.ID, target.ID, resp.StatusCode)
	return nil
}

func captureStatefulCase(root string, meta apifixtures.CaseMeta, target targetConfig) error {
	return captureStatefulCaseWithClient(root, meta, target, &http.Client{Timeout: 2 * time.Minute})
}

func captureStatefulCaseWithClient(root string, meta apifixtures.CaseMeta, target targetConfig, client *http.Client) error {
	if target.Provider != "openai-responses" || target.Stream {
		return fmt.Errorf("stateful capture only supports openai-responses target, got %q", target.ID)
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	var scenario apifixtures.StatefulScenario
	data, err := apifixtures.ReadCaseFile(root, meta.ID, "scenario.json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		return err
	}

	baseURL, headers, err := endpointForTarget(target, meta)
	if err != nil {
		return err
	}

	vars := map[string]string{}
	capturedAt := time.Now().Format(time.RFC3339)
	summary := statefulCaptureSummary{
		CaseID:     meta.ID,
		Target:     target.ID,
		CapturedAt: capturedAt,
		Steps:      make([]statefulCaptureMetadata, 0, len(scenario.Steps)),
	}

	for i, step := range scenario.Steps {
		captured, err := captureStatefulStep(root, meta, target, scenario, step, i, baseURL, headers, client, vars)
		if err != nil {
			return err
		}
		summary.Steps = append(summary.Steps, captured)
	}

	summaryBytes, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	if err := apifixtures.CanonicalizeToFile(root, meta.ID, filepath.Join("captures", target.ID+".stateful.json"), summaryBytes); err != nil {
		return err
	}

	fmt.Printf("captured %s -> %s (%d steps)\n", meta.ID, target.ID, len(summary.Steps))
	return nil
}

func captureStatefulStep(root string, meta apifixtures.CaseMeta, target targetConfig, scenario apifixtures.StatefulScenario, step apifixtures.StatefulStep, index int, baseURL string, headers map[string]string, client *http.Client, vars map[string]string) (statefulCaptureMetadata, error) {
	method := strings.ToUpper(substituteStatefulCaptureString(step.Method, vars))
	path := substituteStatefulCaptureString(step.Path, vars)
	body, err := statefulCaptureRequestBody(step.Body, scenario, meta, target, vars)
	if err != nil {
		return statefulCaptureMetadata{}, err
	}
	url, err := statefulCaptureURL(baseURL, path)
	if err != nil {
		return statefulCaptureMetadata{}, err
	}

	resp, respBody, err := doStatefulCaptureRequest(context.Background(), client, method, url, headers, body, targetHost(target))
	if err != nil {
		return statefulCaptureMetadata{}, err
	}
	defer resp.Body.Close()

	pollUntil := statefulCapturePollUntil(step)
	if len(pollUntil) > 0 {
		resp, respBody, err = pollStatefulCapture(context.Background(), client, method, url, headers, body, targetHost(target), pollUntil, vars)
		if err != nil {
			return statefulCaptureMetadata{}, err
		}
		defer resp.Body.Close()
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return statefulCaptureMetadata{}, fmt.Errorf("parse stateful capture response for step %s: %w", step.ID, err)
	}
	for name, jsonPath := range statefulCaptureBindings(step) {
		value, ok := lookupStatefulCaptureJSONPath(decoded, jsonPath)
		if !ok {
			return statefulCaptureMetadata{}, fmt.Errorf("stateful capture step %s missing bind path %q", step.ID, jsonPath)
		}
		vars[name] = fmt.Sprint(value)
	}

	captured := statefulCaptureMetadata{
		CaseID:         meta.ID,
		Target:         target.ID,
		StepID:         step.ID,
		Method:         method,
		Path:           path,
		URL:            url,
		StatusCode:     resp.StatusCode,
		ContentType:    resp.Header.Get("Content-Type"),
		CapturedAt:     time.Now().Format(time.RFC3339),
		ResponseHeader: resp.Header,
	}

	prefix := statefulCaptureStepPrefix(target.ID, index, step.ID)
	if err := writeStatefulCaptureRequest(root, meta.ID, prefix+".request.json", method, path, url, body); err != nil {
		return statefulCaptureMetadata{}, err
	}
	if err := apifixtures.CanonicalizeToFile(root, meta.ID, prefix+".response.json", respBody); err != nil {
		return statefulCaptureMetadata{}, err
	}
	metaBytes, err := json.Marshal(captured)
	if err != nil {
		return statefulCaptureMetadata{}, err
	}
	if err := apifixtures.CanonicalizeToFile(root, meta.ID, prefix+".meta.json", metaBytes); err != nil {
		return statefulCaptureMetadata{}, err
	}
	return captured, nil
}

func doStatefulCaptureRequest(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte, provider string) (*http.Response, []byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return doCaptureRequest(ctx, client, req, body, provider, nil)
}

func pollStatefulCapture(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte, provider string, fields map[string]interface{}, vars map[string]string) (*http.Response, []byte, error) {
	deadline := time.Now().Add(2 * time.Minute)
	var lastResp *http.Response
	var lastBody []byte
	for {
		resp, data, err := doStatefulCaptureRequest(ctx, client, method, url, headers, body, provider)
		if err != nil {
			return nil, nil, err
		}
		if lastResp != nil {
			lastResp.Body.Close()
		}
		lastResp = resp
		lastBody = data
		var decoded map[string]interface{}
		if err := json.Unmarshal(data, &decoded); err == nil && statefulCaptureFieldsMatch(decoded, fields, vars) {
			return lastResp, lastBody, nil
		}
		if time.Now().After(deadline) {
			return lastResp, lastBody, nil
		}
		time.Sleep(1 * time.Second)
	}
}

func statefulCaptureBindings(step apifixtures.StatefulStep) map[string]string {
	if len(step.CaptureBind) == 0 {
		return step.Bind
	}
	bindings := make(map[string]string, len(step.Bind)+len(step.CaptureBind))
	for name, path := range step.Bind {
		bindings[name] = path
	}
	for name, path := range step.CaptureBind {
		bindings[name] = path
	}
	return bindings
}

func statefulCapturePollUntil(step apifixtures.StatefulStep) map[string]interface{} {
	if len(step.CapturePollUntil) > 0 {
		return step.CapturePollUntil
	}
	return step.PollUntil
}

func statefulCaptureRequestBody(body interface{}, scenario apifixtures.StatefulScenario, meta apifixtures.CaseMeta, target targetConfig, vars map[string]string) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	substituted := substituteStatefulCaptureValue(body, vars)
	if bodyMap, ok := substituted.(map[string]interface{}); ok {
		if _, hasModel := bodyMap["model"]; hasModel {
			model := strings.TrimSpace(meta.Models[target.ID])
			if model == "" {
				model = strings.TrimSpace(meta.Models[target.Provider])
			}
			if model == "" {
				model = captureModelForTarget(meta, target)
			}
			if model == "" {
				model = scenario.Model
			}
			if model == "" {
				return nil, fmt.Errorf("case %s is missing model for target %s", meta.ID, target.ID)
			}
			bodyMap["model"] = model
		}
	}
	return json.Marshal(substituted)
}

func statefulCaptureURL(baseResponsesURL, path string) (string, error) {
	base := strings.TrimRight(baseResponsesURL, "/")
	switch {
	case path == "/v1/responses":
		return base, nil
	case strings.HasPrefix(path, "/v1/responses/"):
		return base + strings.TrimPrefix(path, "/v1/responses"), nil
	case path == "/v1/conversations":
		return strings.TrimSuffix(base, "/responses") + "/conversations", nil
	case strings.HasPrefix(path, "/v1/conversations/"):
		return strings.TrimSuffix(base, "/responses") + strings.TrimPrefix(path, "/v1"), nil
	default:
		return "", fmt.Errorf("stateful capture path %q is not a Responses or Conversations path", path)
	}
}

func writeStatefulCaptureRequest(root, caseID, rel, method, path, url string, body []byte) error {
	envelope := map[string]interface{}{
		"method": method,
		"path":   path,
		"url":    url,
	}
	if len(body) > 0 {
		var decoded interface{}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return err
		}
		envelope["body"] = decoded
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return apifixtures.CanonicalizeToFile(root, caseID, rel, data)
}

func statefulCaptureStepPrefix(targetID string, index int, stepID string) string {
	return filepath.Join("captures", targetID, fmt.Sprintf("%03d-%s", index+1, sanitizeStatefulCaptureStepID(stepID)))
}

func sanitizeStatefulCaptureStepID(stepID string) string {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return "step"
	}
	var b strings.Builder
	for _, r := range stepID {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

var statefulCapturePlaceholderPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

func substituteStatefulCaptureValue(value interface{}, vars map[string]string) interface{} {
	switch typed := value.(type) {
	case string:
		return substituteStatefulCaptureString(typed, vars)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i := range typed {
			out[i] = substituteStatefulCaptureValue(typed[i], vars)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = substituteStatefulCaptureValue(item, vars)
		}
		return out
	default:
		return value
	}
}

func substituteStatefulCaptureString(value string, vars map[string]string) string {
	return statefulCapturePlaceholderPattern.ReplaceAllStringFunc(value, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		if replacement, ok := vars[name]; ok {
			return replacement
		}
		return match
	})
}

func statefulCaptureFieldsMatch(decoded map[string]interface{}, fields map[string]interface{}, vars map[string]string) bool {
	for path, rawWant := range fields {
		want := substituteStatefulCaptureValue(rawWant, vars)
		got, ok := lookupStatefulCaptureJSONPath(decoded, path)
		if !ok || !statefulCaptureValuesEqual(got, want) {
			return false
		}
	}
	return true
}

func lookupStatefulCaptureJSONPath(decoded interface{}, path string) (interface{}, bool) {
	current := decoded
	for _, part := range strings.Split(path, ".") {
		switch typed := current.(type) {
		case map[string]interface{}:
			value, ok := typed[part]
			if !ok {
				return nil, false
			}
			current = value
		case []interface{}:
			if part == "length" {
				current = float64(len(typed))
				continue
			}
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func statefulCaptureValuesEqual(got, want interface{}) bool {
	gotNumber, gotIsNumber := statefulCaptureNumber(got)
	wantNumber, wantIsNumber := statefulCaptureNumber(want)
	if gotIsNumber && wantIsNumber {
		return gotNumber == wantNumber
	}
	return reflect.DeepEqual(got, want)
}

func statefulCaptureNumber(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func normalizeStreamCapture(data []byte) []byte {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	data = bytes.TrimRight(data, "\n")
	return append(data, '\n')
}

func captureModelsCase(root string, meta apifixtures.CaseMeta, target targetConfig) error {
	if target.Stream {
		return fmt.Errorf("models capture target %q must not be a stream target", target.ID)
	}

	url, headers, err := modelsEndpointForTarget(target)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, data, err := doCaptureModelsRequest(context.Background(), &http.Client{Timeout: 2 * time.Minute}, req, target)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	metaOut := captureMetadata{
		CaseID:         meta.ID,
		Target:         target.ID,
		URL:            url,
		StatusCode:     resp.StatusCode,
		ContentType:    resp.Header.Get("Content-Type"),
		CapturedAt:     time.Now().Format(time.RFC3339),
		ResponseHeader: resp.Header,
	}

	capturesDir := filepath.Join(apifixtures.CaseDir(root, meta.ID), "captures")
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
	if err := apifixtures.CanonicalizeToFile(root, meta.ID, filepath.Join("captures", target.ID+".response.json"), data); err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := refreshDerivedArtifacts(root, meta, target, data); err != nil {
			return err
		}
	}

	fmt.Printf("captured %s -> %s (%d)\n", meta.ID, target.ID, resp.StatusCode)
	return nil
}

func doCaptureModelsRequest(ctx context.Context, client *http.Client, req *http.Request, target targetConfig) (*http.Response, []byte, error) {
	switch target.Provider {
	case "anthropic":
		return doCaptureAnthropicModelsRequest(ctx, client, req, target)
	case "google":
		return doCaptureGoogleModelsRequest(ctx, client, req, target)
	default:
		return doCaptureRequest(ctx, client, req, nil, targetHost(target), nil)
	}
}

func doCaptureAnthropicModelsRequest(ctx context.Context, client *http.Client, req *http.Request, target targetConfig) (*http.Response, []byte, error) {
	var combined modelcatalog.AnthropicModelsResponse
	afterID := ""
	seenCursors := map[string]struct{}{}
	var lastResp *http.Response

	for page := 0; page < maxModelsCapturePages; page++ {
		pageReq := cloneRequestWithQuery(req, map[string]string{
			"limit":    "1000",
			"after_id": afterID,
		})
		resp, data, err := doCaptureRequest(ctx, client, pageReq, nil, targetHost(target), nil)
		if err != nil {
			return nil, nil, err
		}
		lastResp = resp
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return resp, data, nil
		}

		var pageResponse modelcatalog.AnthropicModelsResponse
		if err := json.Unmarshal(data, &pageResponse); err != nil {
			return nil, nil, fmt.Errorf("parse Anthropic models page: %w", err)
		}
		pageModels := pageResponse.Data
		if len(pageModels) == 0 {
			pageModels = pageResponse.Models
		}
		if combined.FirstID == "" {
			combined.FirstID = firstNonEmptyModelID(pageResponse.FirstID, pageModels)
		}
		combined.Data = append(combined.Data, pageModels...)

		if !pageResponse.HasMore || pageResponse.LastID == "" {
			combined.LastID = lastModelID(pageResponse.LastID, pageModels)
			combined.HasMore = false
			if combined.Data == nil {
				combined.Data = []modelcatalog.AnthropicModelInfo{}
			}
			return marshalCapturedModelsResponse(lastResp, combined)
		}
		if _, ok := seenCursors[pageResponse.LastID]; ok {
			return nil, nil, fmt.Errorf("anthropic models pagination repeated cursor %q", pageResponse.LastID)
		}
		seenCursors[pageResponse.LastID] = struct{}{}
		afterID = pageResponse.LastID
	}

	return nil, nil, fmt.Errorf("anthropic models pagination exceeded %d pages", maxModelsCapturePages)
}

func doCaptureGoogleModelsRequest(ctx context.Context, client *http.Client, req *http.Request, target targetConfig) (*http.Response, []byte, error) {
	var combined modelcatalog.GoogleModelsResponse
	pageToken := ""
	seenTokens := map[string]struct{}{}
	var lastResp *http.Response

	for page := 0; page < maxModelsCapturePages; page++ {
		pageReq := cloneRequestWithQuery(req, map[string]string{
			"pageSize":  "1000",
			"pageToken": pageToken,
		})
		resp, data, err := doCaptureRequest(ctx, client, pageReq, nil, targetHost(target), nil)
		if err != nil {
			return nil, nil, err
		}
		lastResp = resp
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return resp, data, nil
		}

		var pageResponse modelcatalog.GoogleModelsResponse
		if err := json.Unmarshal(data, &pageResponse); err != nil {
			return nil, nil, fmt.Errorf("parse Google models page: %w", err)
		}
		combined.Models = append(combined.Models, pageResponse.Models...)

		if pageResponse.NextPageToken == "" {
			if combined.Models == nil {
				combined.Models = []modelcatalog.GoogleModelInfo{}
			}
			return marshalCapturedModelsResponse(lastResp, combined)
		}
		if _, ok := seenTokens[pageResponse.NextPageToken]; ok {
			return nil, nil, fmt.Errorf("google models pagination repeated token %q", pageResponse.NextPageToken)
		}
		seenTokens[pageResponse.NextPageToken] = struct{}{}
		pageToken = pageResponse.NextPageToken
	}

	return nil, nil, fmt.Errorf("google models pagination exceeded %d pages", maxModelsCapturePages)
}

func marshalCapturedModelsResponse(resp *http.Response, body interface{}) (*http.Response, []byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	return resp, data, nil
}

func cloneRequestWithQuery(req *http.Request, params map[string]string) *http.Request {
	cloned := req.Clone(req.Context())
	clonedURL := *cloned.URL
	query := clonedURL.Query()
	for key, value := range params {
		if value == "" {
			query.Del(key)
			continue
		}
		query.Set(key, value)
	}
	clonedURL.RawQuery = query.Encode()
	cloned.URL = &clonedURL
	return cloned
}

func firstNonEmptyModelID(fallback string, models []modelcatalog.AnthropicModelInfo) string {
	if fallback != "" {
		return fallback
	}
	if len(models) == 0 {
		return ""
	}
	return models[0].ID
}

func lastModelID(fallback string, models []modelcatalog.AnthropicModelInfo) string {
	if fallback != "" {
		return fallback
	}
	if len(models) == 0 {
		return ""
	}
	return models[len(models)-1].ID
}

const maxModelsCapturePages = 100

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
	if apifixtures.StringSliceContains(meta.Kinds, "models") && meta.Provider == target.Provider {
		items, err := modelcatalog.Parse(meta.Provider, data)
		if err != nil {
			return fmt.Errorf("refresh parsed models for %s: %w", meta.ID, err)
		}

		projectedBytes, err := json.Marshal(modelcatalog.Project(items))
		if err != nil {
			return err
		}
		return apifixtures.CanonicalizeToFile(root, meta.ID, filepath.Join("expected", "parsed.json"), projectedBytes)
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

func loadTokenCountRequestBody(root string, meta apifixtures.CaseMeta, target targetConfig) ([]byte, error) {
	body, err := apifixtures.ReadCaseFile(root, meta.ID, "request.json")
	if err != nil {
		return nil, err
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	if target.Provider == "anthropic" {
		model := strings.TrimSpace(meta.Models[target.Provider])
		if model == "" {
			return nil, fmt.Errorf("case %s is missing models.%s", meta.ID, target.Provider)
		}
		if _, hasModel := decoded["model"]; hasModel {
			decoded["model"] = model
		}
	}
	if target.Provider == "google" {
		model := strings.TrimSpace(meta.Models[target.Provider])
		if model == "" {
			return nil, fmt.Errorf("case %s is missing models.%s", meta.ID, target.Provider)
		}
		if request, ok := decoded["generateContentRequest"].(map[string]interface{}); ok {
			request["model"] = googleFixtureModelResourceName(model)
		}
	}
	return json.Marshal(decoded)
}

func googleFixtureModelResourceName(model string) string {
	if strings.HasPrefix(model, "models/") {
		return model
	}
	return "models/" + model
}

func writeTokenCountCaptureRequest(root, caseID, rel, rawURL string, body []byte) error {
	envelope := map[string]interface{}{
		"method": http.MethodPost,
		"url":    rawURL,
	}
	if parsedURL, err := url.Parse(rawURL); err == nil {
		envelope["path"] = parsedURL.Path
	}
	if len(body) > 0 {
		var decoded interface{}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return err
		}
		envelope["body"] = decoded
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return apifixtures.CanonicalizeToFile(root, caseID, rel, data)
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
	case "openai", "openai-responses", "anthropic":
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

func tokenCountEndpointForTarget(target targetConfig, meta apifixtures.CaseMeta) (string, map[string]string, error) {
	headers := map[string]string{}

	switch targetHost(target) {
	case "anthropic":
		apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
		if apiKey == "" {
			return "", nil, fmt.Errorf("ANTHROPIC_API_KEY is required for target %s", target.ID)
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		url, err := providers.ResolveCountTokensURL("anthropic", envOrDefault("ANTHROPIC_API_FIXTURE_URL", defaultAnthropicURL), "", strings.TrimSpace(meta.Models["anthropic"]))
		return url, headers, err

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
		base := strings.TrimRight(envOrDefault("GOOGLE_API_FIXTURE_URL", defaultGoogleBase), "/")
		url, err := providers.BuildGoogleModelURL(base, model, "countTokens")
		return url, headers, err

	default:
		return "", nil, fmt.Errorf("token-count capture is not supported for target %q", target.ID)
	}
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
		if target.Provider == "openai-responses" {
			return envOrDefault("OPENAI_RESPONSES_API_FIXTURE_URL", defaultOpenAIResponsesURL), headers, nil
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

func modelsEndpointForTarget(target targetConfig) (string, map[string]string, error) {
	headers := map[string]string{}

	switch target.Provider {
	case "openai":
		apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		if apiKey == "" {
			return "", nil, fmt.Errorf("OPENAI_API_KEY is required for target %s", target.ID)
		}
		headers["Authorization"] = "Bearer " + apiKey
		url, err := providers.ResolveModelsURL("openai", envOrDefault("OPENAI_API_FIXTURE_URL", defaultOpenAIURL), "")
		return url, headers, err
	case "anthropic":
		apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
		if apiKey == "" {
			return "", nil, fmt.Errorf("ANTHROPIC_API_KEY is required for target %s", target.ID)
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		url, err := providers.ResolveModelsURL("anthropic", envOrDefault("ANTHROPIC_API_FIXTURE_URL", defaultAnthropicURL), "")
		return url, headers, err
	case "google":
		apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
		if apiKey == "" {
			return "", nil, fmt.Errorf("GOOGLE_API_KEY is required for target %s", target.ID)
		}
		headers["x-goog-api-key"] = apiKey
		url, err := providers.ResolveModelsURL("google", envOrDefault("GOOGLE_API_FIXTURE_URL", defaultGoogleBase), "")
		return url, headers, err
	case "argo":
		url, err := providers.ResolveModelsURL("argo", envOrDefault("ARGO_API_FIXTURE_BASE_URL", defaultArgoBase), "")
		return url, headers, err
	default:
		return "", nil, fmt.Errorf("models capture is not supported for provider %q", target.Provider)
	}
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
