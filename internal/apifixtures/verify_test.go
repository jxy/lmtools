package apifixtures

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifySuiteCurrentCorpus(t *testing.T) {
	suite, err := LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	if err := VerifySuite(suite.Root, VerifyOptions{}); err != nil {
		t.Fatalf("VerifySuite() error = %v", err)
	}
}

func TestVerifySuiteReportsMissingRequestFiles(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, SuiteDirName, "cases", "broken-request")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(caseDir) error = %v", err)
	}

	writeTestFile(t, filepath.Join(root, ManifestRel), `{
  "version": 1,
  "cases": [
    {
      "id": "broken-request",
      "description": "broken request case",
      "kinds": ["request"]
    }
  ]
}
`)

	writeTestFile(t, filepath.Join(caseDir, CaseMetaRel), `{
  "id": "broken-request",
  "description": "broken request case",
  "kinds": ["request"],
  "ingress_family": "openai",
  "models": {
    "openai": "gpt-5.4-nano",
    "anthropic": "claude-haiku-4-5",
    "google": "gemini-3.1-flash-lite-preview",
    "argo": "gpt5mini"
  }
}
`)

	err := VerifySuite(root, VerifyOptions{})
	if err == nil {
		t.Fatal("expected VerifySuite() error")
	}
	if !strings.Contains(err.Error(), "ingress.json") {
		t.Fatalf("error = %q, want missing ingress.json", err)
	}
}

func TestMatchesFilters(t *testing.T) {
	meta := CaseMeta{
		ID:            "anthropic-messages-basic-text",
		IngressFamily: "anthropic",
	}

	if !MatchesFilters(meta, "", "") {
		t.Fatal("expected empty filters to match")
	}
	if !MatchesFilters(meta, "anthropic-messages-basic-text", "") {
		t.Fatal("expected exact case filter to match")
	}
	if !MatchesFilters(meta, "", "anthropic") {
		t.Fatal("expected provider filter to match")
	}
	if MatchesFilters(meta, "openai-chat-audio-input", "") {
		t.Fatal("unexpected case filter match")
	}
	if MatchesFilters(meta, "", "openai") {
		t.Fatal("unexpected provider filter match")
	}
}

func TestVerifySuiteCheckCapturesHonorsTargetAndSkipsResponseMeta(t *testing.T) {
	root := t.TempDir()

	writeTestFile(t, filepath.Join(root, ManifestRel), `{
  "version": 1,
  "cases": [
    {
      "id": "request-case",
      "description": "request case",
      "kinds": ["request"]
    },
    {
      "id": "response-case",
      "description": "response case",
      "kinds": ["response"]
    }
  ]
}
`)

	requestDir := filepath.Join(root, SuiteDirName, "cases", "request-case")
	responseDir := filepath.Join(root, SuiteDirName, "cases", "response-case")
	if err := os.MkdirAll(filepath.Join(requestDir, "expected", "render"), 0o755); err != nil {
		t.Fatalf("MkdirAll(requestDir) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(requestDir, "captures"), 0o755); err != nil {
		t.Fatalf("MkdirAll(request captures) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(responseDir, "captures"), 0o755); err != nil {
		t.Fatalf("MkdirAll(response captures) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(responseDir, "expected"), 0o755); err != nil {
		t.Fatalf("MkdirAll(response expected) error = %v", err)
	}

	writeTestFile(t, filepath.Join(requestDir, CaseMetaRel), `{
  "id": "request-case",
  "description": "request case",
  "kinds": ["request"],
  "ingress_family": "anthropic",
  "models": {
    "openai": "gpt-5.4-nano",
    "anthropic": "claude-haiku-4-5",
    "google": "gemini-3.1-flash-lite-preview",
    "argo": "gpt5mini"
  },
  "capture_targets": ["openai", "anthropic"]
}
`)
	writeTestFile(t, filepath.Join(requestDir, "ingress.json"), `{"model":"claude-haiku-4-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	writeTestFile(t, filepath.Join(requestDir, "expected", "typed.json"), `{"messages":[{"role":"user","blocks":[{"type":"text","text":"hi"}]}],"max_tokens":1,"stream":false}`)
	writeTestFile(t, filepath.Join(requestDir, "expected", "render", "openai.request.json"), `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":1}`)
	writeTestFile(t, filepath.Join(requestDir, "expected", "render", "anthropic.request.json"), `{"model":"claude-haiku-4-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	writeTestFile(t, filepath.Join(requestDir, "expected", "render", "google.request.json"), `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	writeTestFile(t, filepath.Join(requestDir, "expected", "render", "argo.request.json"), `{"user":"fixture-user","model":"gpt5mini","messages":[{"role":"user","content":"hi"}]}`)
	writeTestFile(t, filepath.Join(requestDir, "captures", "openai.meta.json"), `{"target":"openai","status_code":200}`)
	writeTestFile(t, filepath.Join(requestDir, "captures", "openai.response.json"), `{"id":"ok"}`)

	writeTestFile(t, filepath.Join(responseDir, CaseMetaRel), `{
  "id": "response-case",
  "description": "response case",
  "kinds": ["response"],
  "provider": "openai"
}
`)
	writeTestFile(t, filepath.Join(responseDir, "captures", "openai.response.json"), `{"id":"resp","choices":[{"message":{"content":"hi"}}]}`)
	writeTestFile(t, filepath.Join(responseDir, "expected", "parsed.json"), `{"text":"hi","tool_calls":[]}`)

	if err := VerifySuite(root, VerifyOptions{CheckCaptures: true, Target: "openai"}); err != nil {
		t.Fatalf("VerifySuite() error = %v", err)
	}
}

func TestVerifySuiteCheckCapturesSupportsStreamTargets(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, SuiteDirName, "cases", "request-case")
	if err := os.MkdirAll(filepath.Join(caseDir, "expected", "render"), 0o755); err != nil {
		t.Fatalf("MkdirAll(caseDir) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(caseDir, "captures"), 0o755); err != nil {
		t.Fatalf("MkdirAll(captures) error = %v", err)
	}

	writeTestFile(t, filepath.Join(root, ManifestRel), `{
  "version": 1,
  "cases": [
    {
      "id": "request-case",
      "description": "request case",
      "kinds": ["request"]
    }
  ]
}
`)

	writeTestFile(t, filepath.Join(caseDir, CaseMetaRel), `{
  "id": "request-case",
  "description": "request case",
  "kinds": ["request"],
  "ingress_family": "anthropic",
  "models": {
    "openai": "gpt-5.4-nano",
    "anthropic": "claude-haiku-4-5",
    "google": "gemini-3.1-flash-lite-preview",
    "argo": "gpt5mini"
  },
  "capture_targets": ["openai-stream"]
}
`)
	writeTestFile(t, filepath.Join(caseDir, "ingress.json"), `{"model":"claude-haiku-4-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	writeTestFile(t, filepath.Join(caseDir, "expected", "typed.json"), `{"messages":[{"role":"user","blocks":[{"type":"text","text":"hi"}]}],"max_tokens":1,"stream":false}`)
	writeTestFile(t, filepath.Join(caseDir, "expected", "render", "openai.request.json"), `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":1}`)
	writeTestFile(t, filepath.Join(caseDir, "expected", "render", "anthropic.request.json"), `{"model":"claude-haiku-4-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	writeTestFile(t, filepath.Join(caseDir, "expected", "render", "google.request.json"), `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	writeTestFile(t, filepath.Join(caseDir, "expected", "render", "argo.request.json"), `{"user":"fixture-user","model":"gpt5mini","messages":[{"role":"user","content":"hi"}]}`)
	writeTestFile(t, filepath.Join(caseDir, "captures", "openai-stream.meta.json"), `{"target":"openai-stream","status_code":200}`)
	if err := os.WriteFile(filepath.Join(caseDir, "captures", "openai-stream.stream.txt"), []byte("data: [DONE]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stream.txt) error = %v", err)
	}

	if err := VerifySuite(root, VerifyOptions{CheckCaptures: true, Target: "openai-stream"}); err != nil {
		t.Fatalf("VerifySuite() error = %v", err)
	}
}

func TestVerifySuiteHonorsRenderTargets(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, SuiteDirName, "cases", "request-case")
	if err := os.MkdirAll(filepath.Join(caseDir, "expected", "render"), 0o755); err != nil {
		t.Fatalf("MkdirAll(caseDir) error = %v", err)
	}

	writeTestFile(t, filepath.Join(root, ManifestRel), `{
  "version": 1,
  "cases": [
    {
      "id": "request-case",
      "description": "request case",
      "kinds": ["request"]
    }
  ]
}
`)

	writeTestFile(t, filepath.Join(caseDir, CaseMetaRel), `{
  "id": "request-case",
  "description": "request case",
  "kinds": ["request"],
  "ingress_family": "openai",
  "models": {
    "openai": "gpt-5.4-nano",
    "anthropic": "claude-haiku-4-5",
    "google": "gemini-3.1-flash-lite-preview",
    "argo": "gpt5mini"
  },
  "render_targets": ["openai", "google"]
}
`)
	writeTestFile(t, filepath.Join(caseDir, "ingress.json"), `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`)
	writeTestFile(t, filepath.Join(caseDir, "expected", "typed.json"), `{"messages":[{"role":"user","blocks":[{"type":"text","text":"hi"}]}],"stream":false}`)
	writeTestFile(t, filepath.Join(caseDir, "expected", "render", "openai.request.json"), `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`)
	writeTestFile(t, filepath.Join(caseDir, "expected", "render", "google.request.json"), `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	if err := VerifySuite(root, VerifyOptions{}); err != nil {
		t.Fatalf("VerifySuite() error = %v", err)
	}
}

func TestReadCaseFileExpandsFixtureFileData(t *testing.T) {
	root := t.TempDir()
	caseID := "audio-case"
	caseDir := filepath.Join(root, SuiteDirName, "cases", caseID)

	if err := os.MkdirAll(filepath.Join(caseDir, "fixtures"), 0o755); err != nil {
		t.Fatalf("MkdirAll(fixtures) error = %v", err)
	}
	audioBytes := []byte("RIFFfixture")
	if err := os.WriteFile(filepath.Join(caseDir, "fixtures", "sample.wav"), audioBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(sample.wav) error = %v", err)
	}

	writeTestFile(t, filepath.Join(caseDir, "ingress.json"), `{
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "input_audio",
          "input_audio": {
            "$fixture_file": "fixtures/sample.wav",
            "format": "wav"
          }
        }
      ]
    }
  ]
}
`)

	data, err := ReadCaseFile(root, caseID, "ingress.json")
	if err != nil {
		t.Fatalf("ReadCaseFile() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	messages := decoded["messages"].([]interface{})
	message := messages[0].(map[string]interface{})
	content := message["content"].([]interface{})
	part := content[0].(map[string]interface{})
	inputAudio := part["input_audio"].(map[string]interface{})

	if _, exists := inputAudio[fixtureFileKey]; exists {
		t.Fatalf("expected %s to be removed after expansion", fixtureFileKey)
	}
	if got, ok := inputAudio["data"].(string); !ok || got != base64.StdEncoding.EncodeToString(audioBytes) {
		t.Fatalf("input_audio.data = %v, want expanded base64", inputAudio["data"])
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
