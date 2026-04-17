package apifixtures

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	SuiteDirName    = "testdata/api-fixtures"
	ManifestRel     = SuiteDirName + "/manifest.json"
	CaseMetaRel     = "case.json"
	DefaultArgoUser = "fixture-user"
	fixtureFileKey  = "$fixture_file"
)

type Manifest struct {
	Version int            `json:"version"`
	Cases   []ManifestCase `json:"cases"`
}

type ManifestCase struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Kinds       []string `json:"kinds,omitempty"`
}

type CaseMeta struct {
	ID             string            `json:"id"`
	Description    string            `json:"description"`
	Kinds          []string          `json:"kinds,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	IngressFamily  string            `json:"ingress_family,omitempty"`
	StreamSource   string            `json:"stream_source,omitempty"`
	StreamTarget   string            `json:"stream_target,omitempty"`
	Models         map[string]string `json:"models,omitempty"`
	ArgoUser       string            `json:"argo_user,omitempty"`
	RenderTargets  []string          `json:"render_targets,omitempty"`
	CaptureTargets []string          `json:"capture_targets,omitempty"`
}

var fixtureProviders = []string{"openai", "anthropic", "google", "argo"}

type Suite struct {
	Root     string
	Manifest Manifest
}

func FindRepoRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(current, ManifestRel)); err == nil {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("could not locate %s from %s", ManifestRel, start)
		}
		current = parent
	}
}

func LoadSuite() (*Suite, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := FindRepoRoot(wd)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifestFromRoot(root)
	if err != nil {
		return nil, err
	}
	return &Suite{Root: root, Manifest: manifest}, nil
}

func LoadManifestFromRoot(root string) (Manifest, error) {
	var manifest Manifest
	data, err := os.ReadFile(filepath.Join(root, ManifestRel))
	if err != nil {
		return Manifest{}, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func CaseDir(root, caseID string) string {
	return filepath.Join(root, SuiteDirName, "cases", caseID)
}

func LoadCaseMeta(root, caseID string) (CaseMeta, error) {
	var meta CaseMeta
	data, err := os.ReadFile(filepath.Join(CaseDir(root, caseID), CaseMetaRel))
	if err != nil {
		return CaseMeta{}, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return CaseMeta{}, err
	}
	return meta, nil
}

func ReadCaseFile(root, caseID, rel string) ([]byte, error) {
	path := filepath.Join(CaseDir(root, caseID), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.ToLower(filepath.Ext(rel)) != ".json" {
		return data, nil
	}
	return expandJSONFixtureFiles(CaseDir(root, caseID), data)
}

func CaseFileExists(root, caseID, rel string) bool {
	_, err := os.Stat(filepath.Join(CaseDir(root, caseID), rel))
	return err == nil
}

func CanonicalJSON(data []byte) ([]byte, error) {
	var decoded interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return json.MarshalIndent(decoded, "", "  ")
}

func CanonicalizeToFile(root, caseID, rel string, data []byte) error {
	canonical, err := CanonicalJSON(data)
	if err != nil {
		return err
	}
	path := filepath.Join(CaseDir(root, caseID), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(canonical, '\n'), 0o644)
}

func StringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func SourceProvider(meta CaseMeta) string {
	switch {
	case meta.IngressFamily != "":
		return meta.IngressFamily
	case meta.Provider != "":
		return meta.Provider
	case meta.StreamSource != "":
		return meta.StreamSource
	default:
		return ""
	}
}

func PrimaryEndpoint(meta CaseMeta) string {
	switch {
	case StringSliceContains(meta.Kinds, "models"):
		return "/v1/models"
	case StringSliceContains(meta.Kinds, "stream"):
		if meta.StreamSource != "" && meta.StreamTarget != "" {
			return meta.StreamSource + "->" + meta.StreamTarget + " stream"
		}
		return "stream"
	case meta.IngressFamily == "anthropic":
		return "/v1/messages"
	case meta.IngressFamily == "openai":
		return "/v1/chat/completions"
	case meta.Provider != "":
		return meta.Provider + " response"
	default:
		return ""
	}
}

func SummaryLine(meta CaseMeta) string {
	parts := make([]string, 0, 5)
	if len(meta.Kinds) > 0 {
		kinds := append([]string(nil), meta.Kinds...)
		sort.Strings(kinds)
		parts = append(parts, "kinds="+strings.Join(kinds, ","))
	}
	if source := SourceProvider(meta); source != "" {
		parts = append(parts, "source="+source)
	}
	if endpoint := PrimaryEndpoint(meta); endpoint != "" {
		parts = append(parts, "endpoint="+endpoint)
	}
	if len(meta.CaptureTargets) > 0 {
		targets := append([]string(nil), meta.CaptureTargets...)
		sort.Strings(targets)
		parts = append(parts, "targets="+strings.Join(targets, ","))
	}
	return strings.Join(parts, " ")
}

func RequestRenderTargets(meta CaseMeta) []string {
	if len(meta.RenderTargets) > 0 {
		return append([]string(nil), meta.RenderTargets...)
	}
	return append([]string(nil), fixtureProviders...)
}

func ValidCaseIDs(manifest Manifest) []string {
	ids := make([]string, 0, len(manifest.Cases))
	for _, entry := range manifest.Cases {
		if entry.ID != "" {
			ids = append(ids, entry.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func MatchesFilters(meta CaseMeta, caseID, provider string) bool {
	if caseID != "" && meta.ID != caseID {
		return false
	}
	if provider != "" && SourceProvider(meta) != provider {
		return false
	}
	return true
}

func expandJSONFixtureFiles(caseDir string, data []byte) ([]byte, error) {
	var decoded interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}

	expanded, err := expandFixtureValue(caseDir, decoded)
	if err != nil {
		return nil, err
	}

	return json.Marshal(expanded)
}

func expandFixtureValue(caseDir string, value interface{}) (interface{}, error) {
	switch typed := value.(type) {
	case []interface{}:
		for i := range typed {
			expanded, err := expandFixtureValue(caseDir, typed[i])
			if err != nil {
				return nil, err
			}
			typed[i] = expanded
		}
		return typed, nil

	case map[string]interface{}:
		for key := range typed {
			expanded, err := expandFixtureValue(caseDir, typed[key])
			if err != nil {
				return nil, err
			}
			typed[key] = expanded
		}

		fixturePath, ok := typed[fixtureFileKey].(string)
		if !ok || strings.TrimSpace(fixturePath) == "" {
			return typed, nil
		}
		if _, exists := typed["data"]; exists {
			return nil, fmt.Errorf("%s cannot be combined with data", fixtureFileKey)
		}

		raw, err := os.ReadFile(filepath.Join(caseDir, fixturePath))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", fixturePath, err)
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		mediaType := strings.TrimSpace(stringValue(typed["media_type"]))
		if _, exists := typed["url"]; exists && mediaType != "" {
			if mediaType == "" {
				return nil, fmt.Errorf("%s with url requires media_type or a known file extension", fixtureFileKey)
			}
			typed["url"] = fmt.Sprintf("data:%s;base64,%s", mediaType, encoded)
			delete(typed, "media_type")
			delete(typed, fixtureFileKey)
			return typed, nil
		}

		typed["data"] = encoded
		delete(typed, fixtureFileKey)
		return typed, nil

	default:
		return value, nil
	}
}

func stringValue(value interface{}) string {
	text, _ := value.(string)
	return text
}
