package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"lmtools/internal/apifixtures"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type compareArtifact struct {
	Kind   string
	Target targetConfig
	Source string
	Data   []byte
}

func runCompare(args []string) error {
	fs := newCompareFlagSet("compare")
	if err := fs.flagSet.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(fs.caseID) == "" || strings.TrimSpace(fs.targetID) == "" {
		return fmt.Errorf("-case and -target are required")
	}
	if fs.liveAgainst && strings.TrimSpace(fs.againstID) == "" {
		return fmt.Errorf("-live-against requires -against")
	}

	suite, err := apifixtures.LoadSuite()
	if err != nil {
		return err
	}

	_, err = compareCase(suite.Root, fs.caseID, fs.targetID, fs.againstID, fs.liveAgainst)
	return err
}

func runCompareAll(args []string) error {
	fs := newCompareFlagSet("compare-all")
	provider := fs.flagSet.String("provider", "", "optional source provider filter (anthropic|openai|google|argo)")
	if err := fs.flagSet.Parse(args); err != nil {
		return err
	}
	if fs.liveAgainst && strings.TrimSpace(fs.againstID) == "" {
		return fmt.Errorf("-live-against requires -against")
	}
	if strings.TrimSpace(fs.againstID) != "" && strings.TrimSpace(fs.targetID) == "" {
		return fmt.Errorf("-target is required when -against is set")
	}

	suite, err := apifixtures.LoadSuite()
	if err != nil {
		return err
	}

	foundCase := strings.TrimSpace(fs.caseID) == ""
	comparedAny := false
	skipped := make([]string, 0)
	failures := make([]string, 0)

	for _, fixtureCase := range suite.Manifest.Cases {
		if fs.caseID != "" && fixtureCase.ID != fs.caseID {
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
		if !apifixtures.StringSliceContains(meta.Kinds, "request") {
			continue
		}

		targets := meta.CaptureTargets
		if fs.targetID != "" {
			targets = []string{fs.targetID}
		}
		for _, targetID := range targets {
			target, err := parseTarget(targetID)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s %s: %v", meta.ID, targetID, err))
				continue
			}
			if !supportsShapeCompare(target) {
				skipped = append(skipped, fmt.Sprintf("%s %s (non-SSE raw stream target)", meta.ID, targetID))
				continue
			}
			if !apifixtures.StringSliceContains(meta.CaptureTargets, targetID) {
				continue
			}
			comparedAny = true
			if _, err := compareCase(suite.Root, meta.ID, targetID, fs.againstID, fs.liveAgainst); err != nil {
				failures = append(failures, fmt.Sprintf("%s %s: %v", meta.ID, targetID, err))
			}
		}
	}

	for _, entry := range skipped {
		fmt.Printf("skipped %s\n", entry)
	}

	if !foundCase {
		return fmt.Errorf("case %q not found in fixture manifest", fs.caseID)
	}
	if strings.TrimSpace(*provider) != "" && !comparedAny {
		return fmt.Errorf("no comparable request fixtures found for provider %q", *provider)
	}
	if fs.caseID != "" && fs.targetID != "" && !comparedAny {
		return fmt.Errorf("case %q does not support comparable target %q", fs.caseID, fs.targetID)
	}
	if len(failures) > 0 {
		return fmt.Errorf("compare-all failed:\n- %s", strings.Join(failures, "\n- "))
	}
	return nil
}

type compareFlags struct {
	flagSet     *flag.FlagSet
	caseID      string
	targetID    string
	againstID   string
	liveAgainst bool
}

func newCompareFlagSet(name string) *compareFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	opts := &compareFlags{flagSet: fs}
	fs.StringVar(&opts.caseID, "case", "", "fixture case id")
	fs.StringVar(&opts.targetID, "target", "", "capture target id")
	fs.StringVar(&opts.againstID, "against", "", "optional comparison capture target id")
	fs.BoolVar(&opts.liveAgainst, "live-against", false, "capture the comparison target live instead of reading the checked-in capture")
	return opts
}

func compareCase(root, caseID, targetID, againstID string, liveAgainst bool) (apifixtures.CompareResult, error) {
	meta, err := apifixtures.LoadCaseMeta(root, caseID)
	if err != nil {
		return apifixtures.CompareResult{}, err
	}
	if !apifixtures.StringSliceContains(meta.Kinds, "request") {
		return apifixtures.CompareResult{}, fmt.Errorf("case %q is not a request fixture", caseID)
	}

	target, err := parseTarget(targetID)
	if err != nil {
		return apifixtures.CompareResult{}, err
	}
	if !supportsShapeCompare(target) {
		return apifixtures.CompareResult{}, fmt.Errorf("shape comparison is not supported for target %q", targetID)
	}
	if !apifixtures.StringSliceContains(meta.CaptureTargets, targetID) {
		return apifixtures.CompareResult{}, fmt.Errorf("case %q does not support capture target %q", caseID, targetID)
	}

	liveTarget, err := captureArtifact(root, meta, target)
	if err != nil {
		return apifixtures.CompareResult{}, err
	}

	comparisonTarget := target
	selfCompare := true
	if strings.TrimSpace(againstID) != "" {
		selfCompare = false
		comparisonTarget, err = parseTarget(againstID)
		if err != nil {
			return apifixtures.CompareResult{}, err
		}
		if !supportsShapeCompare(comparisonTarget) {
			return apifixtures.CompareResult{}, fmt.Errorf("shape comparison is not supported for target %q", againstID)
		}
		if comparisonTarget.Provider != target.Provider || comparisonTarget.Stream != target.Stream {
			return apifixtures.CompareResult{}, fmt.Errorf("target %q and comparison target %q use incompatible wire formats", targetID, againstID)
		}
	} else if fallback, ok := defaultComparisonTarget(target); ok {
		comparisonTarget = fallback
		selfCompare = false
	}

	expectedArtifact, err := comparisonArtifact(root, meta, comparisonTarget, liveAgainst, selfCompare)
	if err != nil {
		return apifixtures.CompareResult{}, err
	}

	result, err := apifixtures.CompareCaptureShape(target, expectedArtifact.Data, liveTarget.Data)
	if err != nil {
		return apifixtures.CompareResult{}, err
	}
	if len(result.Differences) > 0 {
		return result, fmt.Errorf(
			"%s mismatch for case %q: live target %q vs %s\n- %s",
			result.Kind,
			caseID,
			targetID,
			expectedArtifact.Source,
			strings.Join(result.Differences, "\n- "),
		)
	}

	fmt.Printf(
		"match %s %s against %s (%s)\n",
		caseID,
		targetID,
		expectedArtifact.Source,
		result.Kind,
	)
	return result, nil
}

func captureArtifact(root string, meta apifixtures.CaseMeta, target targetConfig) (compareArtifact, error) {
	body, err := loadCaptureRequestBody(root, meta.ID, meta, target)
	if err != nil {
		return compareArtifact{}, fmt.Errorf("read %s body for %s: %w", target.ID, meta.ID, err)
	}

	url, headers, err := endpointForTarget(target, meta)
	if err != nil {
		return compareArtifact{}, err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return compareArtifact{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, data, err := apifixtures.DoCaptureRequest(context.Background(), &http.Client{Timeout: 2 * time.Minute}, req, body, targetHost(target), nil)
	if err != nil {
		return compareArtifact{}, err
	}
	defer resp.Body.Close()

	expectedStatus, hasExpectedStatus := apifixtures.ExpectedCaptureStatus(meta, target)
	if hasExpectedStatus {
		if resp.StatusCode != expectedStatus {
			return compareArtifact{}, fmt.Errorf("live target %q returned status %d, want %d%s", target.ID, resp.StatusCode, expectedStatus, summarizeHTTPErrorBody(data))
		}
	} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return compareArtifact{}, fmt.Errorf("live target %q returned status %d%s", target.ID, resp.StatusCode, summarizeHTTPErrorBody(data))
	}
	if hasExpectedStatus && (expectedStatus < 200 || expectedStatus >= 300) {
		return compareArtifact{
			Kind:   artifactKind(target),
			Target: target,
			Source: "live:" + target.ID,
			Data:   data,
		}, nil
	}
	if target.Stream && !looksLikeSSE(data) {
		return compareArtifact{}, fmt.Errorf("live target %q returned non-SSE payload%s", target.ID, summarizeHTTPErrorBody(data))
	}
	if target.Stream {
		if summary, ok := summarizeSSEErrorBody(target.Provider, data); ok {
			return compareArtifact{}, fmt.Errorf("live target %q returned streamed error%s", target.ID, summary)
		}
	}

	return compareArtifact{
		Kind:   artifactKind(target),
		Target: target,
		Source: "live:" + target.ID,
		Data:   data,
	}, nil
}

func comparisonArtifact(root string, meta apifixtures.CaseMeta, target targetConfig, live bool, selfCompare bool) (compareArtifact, error) {
	if live {
		return captureArtifact(root, meta, target)
	}
	if !selfCompare && !apifixtures.StringSliceContains(meta.CaptureTargets, target.ID) {
		return compareArtifact{}, fmt.Errorf("case %q does not support comparison target %q", meta.ID, target.ID)
	}

	rel := filepath.Join("captures", target.ID+".response.json")
	if target.Stream {
		rel = filepath.Join("captures", target.ID+".stream.txt")
	}
	data, err := apifixtures.ReadCaseFile(root, meta.ID, rel)
	if err != nil {
		return compareArtifact{}, err
	}

	return compareArtifact{
		Kind:   artifactKind(target),
		Target: target,
		Source: "capture:" + target.ID,
		Data:   data,
	}, nil
}

func artifactKind(target targetConfig) string {
	if target.Stream {
		return "stream"
	}
	return "response"
}

func supportsShapeCompare(target targetConfig) bool {
	return !target.Stream || target.Provider != "argo"
}

func summarizeHTTPErrorBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\n", " ")
	trimmed = strings.ReplaceAll(trimmed, "\r", " ")
	if len(trimmed) > 240 {
		trimmed = trimmed[:240] + "..."
	}
	return ": " + trimmed
}

func looksLikeSSE(body []byte) bool {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":")
	}
	return false
}

func summarizeSSEErrorBody(provider string, body []byte) (string, bool) {
	currentEvent := ""
	for _, rawLine := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			if currentEvent == "error" {
				return summarizeHTTPErrorBody([]byte(payload)), true
			}

			var decoded map[string]interface{}
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				continue
			}
			if _, ok := decoded["error"]; ok {
				if provider != "openai" || decoded["choices"] == nil {
					return summarizeHTTPErrorBody([]byte(payload)), true
				}
			}
			if msgType, _ := decoded["type"].(string); msgType == "error" {
				return summarizeHTTPErrorBody([]byte(payload)), true
			}
		}
	}
	return "", false
}

func defaultComparisonTarget(target targetConfig) (targetConfig, bool) {
	switch target.ID {
	case "argo-openai":
		return targetConfig{ID: "openai", Provider: "openai", Host: "openai"}, true
	case "argo-openai-stream":
		return targetConfig{ID: "openai-stream", Provider: "openai", Host: "openai", Stream: true}, true
	case "argo-anthropic":
		return targetConfig{ID: "anthropic", Provider: "anthropic", Host: "anthropic"}, true
	case "argo-anthropic-stream":
		return targetConfig{ID: "anthropic-stream", Provider: "anthropic", Host: "anthropic", Stream: true}, true
	default:
		return targetConfig{}, false
	}
}
