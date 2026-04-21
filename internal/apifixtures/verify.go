package apifixtures

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type VerifyOptions struct {
	CaseID        string
	CheckCaptures bool
	Provider      string
	Target        string
}

type fixtureCaptureMetadata struct {
	Target     string `json:"target"`
	StatusCode int    `json:"status_code"`
}

func VerifySuite(root string, opts VerifyOptions) error {
	manifest, err := LoadManifestFromRoot(root)
	if err != nil {
		return err
	}

	problems := make([]string, 0)
	seen := make(map[string]struct{}, len(manifest.Cases))
	foundCase := opts.CaseID == ""

	for _, entry := range manifest.Cases {
		if entry.ID == "" {
			problems = append(problems, "manifest contains a case with an empty id")
			continue
		}
		if _, ok := seen[entry.ID]; ok {
			problems = append(problems, fmt.Sprintf("manifest contains duplicate case id %q", entry.ID))
			continue
		}
		seen[entry.ID] = struct{}{}

		if opts.CaseID != "" && entry.ID != opts.CaseID {
			continue
		}

		meta, err := LoadCaseMeta(root, entry.ID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("case %q metadata could not be loaded: %v", entry.ID, err))
			foundCase = true
			continue
		}
		if opts.Provider != "" && SourceProvider(meta) != opts.Provider {
			continue
		}

		foundCase = true
		problems = append(problems, verifyLoadedCase(root, entry, meta, opts)...)
	}

	if !foundCase {
		switch {
		case opts.CaseID != "":
			problems = append(problems, fmt.Sprintf("case %q not found in manifest (valid cases: %s)", opts.CaseID, strings.Join(ValidCaseIDs(manifest), ", ")))
		case opts.Provider != "":
			problems = append(problems, fmt.Sprintf("no fixture cases found for provider %q", opts.Provider))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("fixture verification failed:\n- %s", strings.Join(problems, "\n- "))
	}

	return nil
}

func verifyLoadedCase(root string, entry ManifestCase, meta CaseMeta, opts VerifyOptions) []string {
	caseID := entry.ID
	problems := make([]string, 0)

	if meta.ID != entry.ID {
		problems = append(problems, fmt.Sprintf("case %q metadata id=%q does not match manifest", caseID, meta.ID))
	}
	if meta.Description != entry.Description {
		problems = append(problems, fmt.Sprintf("case %q metadata description does not match manifest", caseID))
	}
	if !sameStringSet(meta.Kinds, entry.Kinds) {
		problems = append(problems, fmt.Sprintf("case %q metadata kinds %v do not match manifest %v", caseID, meta.Kinds, entry.Kinds))
	}
	if len(meta.Kinds) == 0 {
		problems = append(problems, fmt.Sprintf("case %q has no kinds", caseID))
		return problems
	}

	for _, kind := range meta.Kinds {
		switch kind {
		case "request":
			problems = append(problems, verifyRequestCase(root, meta, opts)...)
		case "models":
			problems = append(problems, verifyModelsCase(root, meta, opts)...)
		case "response":
			problems = append(problems, verifyResponseCase(root, meta, opts)...)
		case "stream":
			problems = append(problems, verifyStreamCase(root, meta, opts)...)
		default:
			problems = append(problems, fmt.Sprintf("case %q has unsupported kind %q", caseID, kind))
		}
	}

	for _, targetID := range meta.CaptureTargets {
		target, err := ParseCaptureTarget(targetID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("case %q has invalid capture target %q: %v", caseID, targetID, err))
			continue
		}
		if !StringSliceContains(meta.Kinds, "request") && !StringSliceContains(meta.Kinds, "models") {
			problems = append(problems, fmt.Sprintf("case %q declares capture target %q but is not capture-capable", caseID, target.ID))
			continue
		}
		if opts.Target != "" && target.ID != opts.Target {
			continue
		}

		if opts.CheckCaptures {
			problems = append(problems, verifyCaptureArtifacts(root, caseID, target)...)
		}
	}

	return problems
}

func verifyModelsCase(root string, meta CaseMeta, opts VerifyOptions) []string {
	caseID := meta.ID
	problems := make([]string, 0)

	target, err := ParseCaptureTarget(meta.Provider)
	if err != nil || target.Stream {
		problems = append(problems, fmt.Sprintf("case %q models provider must be one of openai, anthropic, google, argo", caseID))
		return problems
	}

	problems = append(problems, verifyJSONFile(root, caseID, filepath.Join("captures", target.ID+".response.json"))...)
	problems = append(problems, verifyJSONFile(root, caseID, filepath.Join("expected", "parsed.json"))...)

	if len(meta.CaptureTargets) == 0 {
		problems = append(problems, fmt.Sprintf("case %q models fixture is missing capture_targets", caseID))
		return problems
	}

	for _, targetID := range meta.CaptureTargets {
		captureTarget, err := ParseCaptureTarget(targetID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("case %q has invalid capture target %q: %v", caseID, targetID, err))
			continue
		}
		if captureTarget.Stream {
			problems = append(problems, fmt.Sprintf("case %q models capture target %q must not be a stream target", caseID, targetID))
			continue
		}
		if captureTarget.Provider != meta.Provider {
			problems = append(problems, fmt.Sprintf("case %q models capture target %q must match provider %q", caseID, targetID, meta.Provider))
		}
	}

	return problems
}

func verifyRequestCase(root string, meta CaseMeta, opts VerifyOptions) []string {
	caseID := meta.ID
	problems := make([]string, 0)

	switch meta.IngressFamily {
	case "openai", "anthropic":
	default:
		problems = append(problems, fmt.Sprintf("case %q request ingress_family must be openai or anthropic", caseID))
	}

	for _, provider := range requiredRequestModelKeys(meta) {
		if strings.TrimSpace(meta.Models[provider]) == "" {
			problems = append(problems, fmt.Sprintf("case %q request metadata is missing models.%s", caseID, provider))
		}
	}
	problems = append(problems, verifyFeatureConstraints(meta)...)

	problems = append(problems, verifyJSONFile(root, caseID, "ingress.json")...)
	problems = append(problems, verifyJSONFile(root, caseID, filepath.Join("expected", "typed.json"))...)
	for _, provider := range RequestRenderTargets(meta) {
		if !StringSliceContains(fixtureProviders, provider) {
			problems = append(problems, fmt.Sprintf("case %q has unsupported render target %q", caseID, provider))
			continue
		}
		problems = append(problems, verifyJSONFile(root, caseID, filepath.Join("expected", "render", provider+".request.json"))...)
	}

	return problems
}

func requiredRequestModelKeys(meta CaseMeta) []string {
	required := make(map[string]struct{})
	for _, provider := range RequestRenderTargets(meta) {
		if StringSliceContains(fixtureProviders, provider) {
			required[provider] = struct{}{}
		}
	}
	for _, targetID := range meta.CaptureTargets {
		target, err := ParseCaptureTarget(targetID)
		if err != nil {
			continue
		}
		if target.Host == "argo" {
			required["argo"] = struct{}{}
			continue
		}
		if StringSliceContains(fixtureProviders, target.Provider) {
			required[target.Provider] = struct{}{}
		}
	}
	keys := make([]string, 0, len(required))
	for key := range required {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func verifyFeatureConstraints(meta CaseMeta) []string {
	problems := make([]string, 0)
	if !StringSliceContains(meta.Features, "anthropic-opus-4.7") {
		return problems
	}

	if strings.TrimSpace(meta.Models["anthropic"]) == "" {
		problems = append(problems, fmt.Sprintf("case %q anthropic-opus-4.7 feature requires models.anthropic", meta.ID))
	} else if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(meta.Models["anthropic"])), "claude-opus-4-7") {
		problems = append(problems, fmt.Sprintf("case %q anthropic-opus-4.7 feature requires an Opus 4.7 Anthropic model", meta.ID))
	}
	for _, provider := range RequestRenderTargets(meta) {
		if provider != "anthropic" {
			problems = append(problems, fmt.Sprintf("case %q anthropic-opus-4.7 feature must only render anthropic requests, got %q", meta.ID, provider))
		}
	}
	for _, targetID := range meta.CaptureTargets {
		target, err := ParseCaptureTarget(targetID)
		if err != nil {
			continue
		}
		if target.Host != "anthropic" || target.Provider != "anthropic" {
			problems = append(problems, fmt.Sprintf("case %q anthropic-opus-4.7 feature must only capture official Anthropic targets, got %q", meta.ID, targetID))
		}
	}
	return problems
}

func verifyResponseCase(root string, meta CaseMeta, opts VerifyOptions) []string {
	caseID := meta.ID
	problems := make([]string, 0)

	target, err := ParseCaptureTarget(meta.Provider)
	if err != nil || target.Stream {
		problems = append(problems, fmt.Sprintf("case %q response provider must be one of openai, anthropic, google, argo", caseID))
		return problems
	}

	problems = append(problems, verifyJSONFile(root, caseID, filepath.Join("captures", target.ID+".response.json"))...)
	problems = append(problems, verifyJSONFile(root, caseID, filepath.Join("expected", "parsed.json"))...)

	return problems
}

func verifyStreamCase(root string, meta CaseMeta, opts VerifyOptions) []string {
	caseID := meta.ID
	problems := make([]string, 0)

	source, err := ParseCaptureTarget(meta.StreamSource + "-stream")
	if err != nil || !source.Stream {
		problems = append(problems, fmt.Sprintf("case %q stream_source must be one of openai, anthropic, google, argo", caseID))
	} else {
		streamRel := filepath.Join("captures", source.ID+".stream.txt")
		if !CaseFileExists(root, caseID, streamRel) {
			problems = append(problems, fmt.Sprintf("case %q is missing %s", caseID, streamRel))
		}
	}

	if _, err := ParseCaptureTarget(meta.StreamTarget); err != nil {
		problems = append(problems, fmt.Sprintf("case %q stream_target must be one of openai, anthropic, google, argo", caseID))
	}

	problems = append(problems, verifyJSONFile(root, caseID, filepath.Join("expected", "stream_projection.json"))...)

	return problems
}

func verifyCaptureArtifacts(root, caseID string, target CaptureTarget) []string {
	problems := make([]string, 0)

	metaRel := filepath.Join("captures", target.ID+".meta.json")
	data, err := ReadCaseFile(root, caseID, metaRel)
	if err != nil {
		return []string{fmt.Sprintf("case %q is missing %s: %v", caseID, metaRel, err)}
	}

	var captureMeta fixtureCaptureMetadata
	if err := json.Unmarshal(data, &captureMeta); err != nil {
		return []string{fmt.Sprintf("case %q has invalid JSON in %s: %v", caseID, metaRel, err)}
	}
	if captureMeta.Target != "" && captureMeta.Target != target.ID {
		problems = append(problems, fmt.Sprintf("case %q capture metadata target=%q does not match %q", caseID, captureMeta.Target, target.ID))
	}
	if captureMeta.StatusCode < 200 || captureMeta.StatusCode >= 300 {
		problems = append(problems, fmt.Sprintf("case %q capture %q returned status %d", caseID, target.ID, captureMeta.StatusCode))
	}

	if target.Stream {
		streamRel := filepath.Join("captures", target.ID+".stream.txt")
		if !CaseFileExists(root, caseID, streamRel) {
			problems = append(problems, fmt.Sprintf("case %q is missing %s", caseID, streamRel))
		}
		return problems
	}

	responseRel := filepath.Join("captures", target.ID+".response.json")
	problems = append(problems, verifyJSONFile(root, caseID, responseRel)...)
	return problems
}

func verifyJSONFile(root, caseID, rel string) []string {
	data, err := ReadCaseFile(root, caseID, rel)
	if err != nil {
		return []string{fmt.Sprintf("case %q is missing %s: %v", caseID, rel, err)}
	}

	var decoded interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return []string{fmt.Sprintf("case %q has invalid JSON in %s: %v", caseID, rel, err)}
	}

	return nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)

	for i := range leftCopy {
		if leftCopy[i] != rightCopy[i] {
			return false
		}
	}

	return true
}
