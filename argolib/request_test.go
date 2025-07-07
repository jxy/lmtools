package argo

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBuildRequestEmbed(t *testing.T) {
	cfg := Config{Embed: true, User: "alice"}

	req, body, err := BuildRequest(cfg, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantURL := DevURL + "/embed/"
	if req.URL.String() != wantURL {
		t.Errorf("URL = %q; want %q", req.URL.String(), wantURL)
	}
	var er EmbedRequest
	if err := json.Unmarshal(body, &er); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	if er.User != "alice" || er.Model != DefaultEmbedModel || !reflect.DeepEqual(er.Prompt, []string{"hello"}) {
		t.Errorf("EmbedRequest = %+v; want User=alice, Model=%s, Prompt=[hello]", er, DefaultEmbedModel)
	}
}

func TestBuildRequestChatVariants(t *testing.T) {
	cases := []struct {
		name      string
		cfg       Config
		wantEP    string
		checkBody func([]byte) error
	}{
		{
			name:   "chat default",
			cfg:    Config{User: "bob", System: "sys"},
			wantEP: DevURL + "/chat/",
			checkBody: func(body []byte) error {
				var cr ChatRequest
				if err := json.Unmarshal(body, &cr); err != nil {
					return err
				}
				if cr.User != "bob" || cr.Model != DefaultChatModel {
					return fmt.Errorf("got User=%s Model=%s", cr.User, cr.Model)
				}
				if len(cr.Messages) != 2 || cr.Messages[0].Role != "system" || cr.Messages[1].Role != "user" {
					return fmt.Errorf("messages = %+v", cr.Messages)
				}
				return nil
			},
		},
		{
			name:   "chat prompt",
			cfg:    Config{User: "bob", System: "sys", PromptChat: true},
			wantEP: DevURL + "/chat/",
			checkBody: func(body []byte) error {
				var er EmbedRequest
				if err := json.Unmarshal(body, &er); err != nil {
					return err
				}
				if er.User != "bob" || er.Model != DefaultChatModel {
					return fmt.Errorf("got %+v", er)
				}
				return nil
			},
		},
		{
			name:   "stream chat",
			cfg:    Config{User: "bob", System: "sys", StreamChat: true},
			wantEP: DevURL + "/streamchat/",
			checkBody: func(body []byte) error {
				var cr ChatRequest
				if err := json.Unmarshal(body, &cr); err != nil {
					return err
				}
				if cr.User != "bob" {
					return fmt.Errorf("got %+v", cr)
				}
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, body, err := BuildRequest(tc.cfg, "hey")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.URL.String() != tc.wantEP {
				t.Errorf("URL = %q; want %q", req.URL.String(), tc.wantEP)
			}
			if err := tc.checkBody(body); err != nil {
				t.Errorf("body validation failed: %v", err)
			}
		})
	}
}

func TestBuildRequestEnv(t *testing.T) {
	cfg := Config{Env: "prod", User: "u"}
	req, _, err := BuildRequest(cfg, "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(req.URL.String(), ProdURL) {
		t.Errorf("URL = %q; want prefix %q", req.URL.String(), ProdURL)
	}

	custom := "https://custom.example/api"
	cfg.Env = custom
	req, _, err = BuildRequest(cfg, "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(req.URL.String(), custom) {
		t.Errorf("URL = %q; want prefix %q", req.URL.String(), custom)
	}
}
