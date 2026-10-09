package core

import (
	"context"
	"encoding/json"
	"lmtools/internal/apifixtures"
	"reflect"
	"strings"
	"testing"
)

func developerFixtureMessages(t *testing.T) []OpenAIMessage {
	t.Helper()
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatal(err)
	}
	data, err := apifixtures.ReadCaseFile(suite.Root, "openai-modern-json-schema-developer", "ingress.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Messages []OpenAIMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture.Messages
}

func assertDeveloperRequest(t *testing.T, body []byte, responses bool, instruction string) {
	t.Helper()
	payload := decodeRequestBody(t, body)
	key := "messages"
	if responses {
		key = "input"
	}
	items, ok := payload[key].([]interface{})
	if !ok {
		t.Fatalf("missing %s: %#v", key, payload)
	}
	count := 0
	for _, item := range items {
		message := item.(map[string]interface{})
		if message["role"] != "developer" {
			continue
		}
		count++
		if responses {
			parts := message["content"].([]interface{})
			if len(parts) != 1 || parts[0].(map[string]interface{})["type"] != "input_text" || parts[0].(map[string]interface{})["text"] != instruction {
				t.Fatalf("developer input = %#v", message)
			}
		} else if message["content"] != instruction {
			t.Fatalf("developer message = %#v", message)
		}
	}
	if count != 1 {
		t.Fatalf("developer messages=%d, want one: %#v", count, items)
	}
}

func TestBuildRequestDeveloperRoleFromFixture(t *testing.T) {
	fixture := developerFixtureMessages(t)
	for _, provider := range []string{"openai", "argo"} {
		for _, responses := range []bool{false, true} {
			if provider == "argo" && !responses {
				continue
			}
			for _, stream := range []bool{false, true} {
				cfg := newProviderSpecTestConfig(provider, "gpt5", "http://example.test/v1")
				cfg.System, cfg.SystemExplicitlySet = *fixture[0].Content.Text, true
				cfg.Developer = *fixture[1].Content.Text
				cfg.StreamChat, cfg.OpenAIResponses = stream, responses
				_, body, err := BuildRequest(cfg, *fixture[2].Content.Text)
				if err != nil {
					t.Fatal(err)
				}
				assertDeveloperRequest(t, body, responses, cfg.Developer)
				payload := decodeRequestBody(t, body)
				if responses {
					if payload["instructions"] != cfg.System {
						t.Fatalf("instructions=%#v", payload["instructions"])
					}
					items := payload["input"].([]interface{})
					if len(items) != 2 || items[0].(map[string]interface{})["role"] != "developer" || items[1].(map[string]interface{})["role"] != "user" {
						t.Fatalf("input order=%#v", items)
					}
				} else {
					messages := payload["messages"].([]interface{})
					if !reflect.DeepEqual(messages, MarshalOpenAIMessagesForRequest(fixture)) {
						t.Fatalf("messages=%#v, want fixture roles and text", messages)
					}
				}
			}
		}
	}
}

func TestDeveloperInstructionSurvivesMemoryToolFollowups(t *testing.T) {
	fixture := developerFixtureMessages(t)
	for _, responses := range []bool{false, true} {
		cfg := newProviderSpecTestConfig("openai", "gpt5", "http://example.test/v1")
		cfg.System, cfg.SystemExplicitlySet = *fixture[0].Content.Text, true
		cfg.Developer, cfg.OpenAIResponses = *fixture[1].Content.Text, responses
		store := NewMemorySessionStoreForRequest(cfg, *fixture[2].Content.Text)
		for _, id := range []string{"call_1", "call_2"} {
			if _, _, err := store.SaveAssistant(context.Background(), "", []ToolCall{{ID: id, Name: "inspect", Args: json.RawMessage(`{}`)}}, cfg.Model); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.SaveToolResults(context.Background(), []ToolResult{{ID: id, Output: "ok"}}, ""); err != nil {
				t.Fatal(err)
			}
			messages, err := store.Messages("")
			if err != nil {
				t.Fatal(err)
			}
			_, body, err := BuildToolResultRequest(cfg, cfg.Model, cfg.System, nil, messages)
			if err != nil {
				t.Fatal(err)
			}
			assertDeveloperRequest(t, body, responses, cfg.Developer)
		}
	}
}

func TestBuildRequestRejectsUnsupportedDeveloperHistory(t *testing.T) {
	for _, test := range []struct {
		provider, model string
		legacy          bool
	}{
		{"anthropic", "claude-opus-4-7", false},
		{"google", "gemini-3.1-flash-lite-preview", false},
		{"argo", "claude-opus-4-7", false},
		{"argo", "gpt5", false},
		{"argo", "gpt5", true},
	} {
		cfg := newProviderSpecTestConfig(test.provider, test.model, "http://example.test/v1")
		cfg.ArgoLegacy = test.legacy
		messages := []TypedMessage{NewTextMessage("developer", "Persisted instruction"), NewTextMessage("user", "hello")}
		if _, _, err := BuildChatRequest(cfg, messages, ChatBuildOptions{}); err == nil || !strings.Contains(err.Error(), "cannot preserve the developer role") {
			t.Fatalf("provider=%s model=%s legacy=%v error=%v", test.provider, test.model, test.legacy, err)
		}
	}
}

func TestValidateDeveloperTurn(t *testing.T) {
	cfg := newProviderSpecTestConfig("openai", "gpt5", "http://example.test/v1")
	cfg.Developer = "Instruction"
	if err := ValidateDeveloperTurn(cfg, "question", true); err == nil || !strings.Contains(err.Error(), "requires a new user turn") {
		t.Fatalf("regeneration error=%v", err)
	}
	if err := ValidateDeveloperTurn(cfg, "", false); err == nil || !strings.Contains(err.Error(), "requires prompt text or an -image") {
		t.Fatalf("empty input error=%v", err)
	}
	cfg.Images = []ImageBlock{{URL: "data:image/png;base64,aGVsbG8="}}
	if err := ValidateDeveloperTurn(cfg, "", false); err != nil {
		t.Fatalf("image turn error=%v", err)
	}
}
