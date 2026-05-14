package proxy

import "testing"

func TestModelMapper(t *testing.T) {
	tests := []struct {
		name          string
		rules         []ModelMapRule
		inputModel    string
		expectedModel string
	}{
		{
			name: "first matching rule wins",
			rules: []ModelMapRule{
				mustModelMapRule(t, "^claude-.*=first-backend"),
				mustModelMapRule(t, "^claude-3-haiku.*=second-backend"),
			},
			inputModel:    "claude-3-haiku-20240307",
			expectedModel: "first-backend",
		},
		{
			name: "later rule used when earlier rule misses",
			rules: []ModelMapRule{
				mustModelMapRule(t, "^gpt-4o$=gpt-backend"),
				mustModelMapRule(t, "^claude-3-haiku.*=haiku-backend"),
			},
			inputModel:    "claude-3-haiku-20240307",
			expectedModel: "haiku-backend",
		},
		{
			name: "unmatched model passes through",
			rules: []ModelMapRule{
				mustModelMapRule(t, "^claude-.*=claude-backend"),
			},
			inputModel:    "gpt-4o",
			expectedModel: "gpt-4o",
		},
		{
			name:          "no rules passes through",
			inputModel:    "claude-3-haiku-20240307",
			expectedModel: "claude-3-haiku-20240307",
		},
		{
			name: "claude haiku has no implicit small-model mapping",
			rules: []ModelMapRule{
				mustModelMapRule(t, "^gpt-.*=gpt-backend"),
			},
			inputModel:    "claude-3-haiku-20240307",
			expectedModel: "claude-3-haiku-20240307",
		},
		{
			name: "go regexp matching",
			rules: []ModelMapRule{
				mustModelMapRule(t, "^(gpt|o)[^-]*-mini$=small-backend"),
			},
			inputModel:    "gpt4-mini",
			expectedModel: "small-backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := NewModelMapper(&Config{ModelMapRules: tt.rules})
			if got := mapper.MapModel(tt.inputModel); got != tt.expectedModel {
				t.Fatalf("MapModel(%q) = %q, want %q", tt.inputModel, got, tt.expectedModel)
			}
		})
	}
}

func TestParseModelMapSpec(t *testing.T) {
	tests := []struct {
		name      string
		spec      string
		wantRule  ModelMapRule
		wantError bool
	}{
		{
			name: "valid spec",
			spec: "^claude-.*=claude-opus-4-1",
			wantRule: ModelMapRule{
				Pattern: "^claude-.*",
				Model:   "claude-opus-4-1",
			},
		},
		{
			name: "model may contain equals",
			spec: "^alias$=backend=with-equals",
			wantRule: ModelMapRule{
				Pattern: "^alias$",
				Model:   "backend=with-equals",
			},
		},
		{name: "missing separator", spec: "^claude-.*", wantError: true},
		{name: "empty regex", spec: "=gpt-5", wantError: true},
		{name: "empty model", spec: "^gpt-.*=", wantError: true},
		{name: "invalid regex", spec: "^(bad=gpt-5", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, err := ParseModelMapSpec(tt.spec)
			if tt.wantError {
				if err == nil {
					t.Fatalf("ParseModelMapSpec(%q) succeeded, want error", tt.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseModelMapSpec(%q) error = %v", tt.spec, err)
			}
			if rule.Pattern != tt.wantRule.Pattern || rule.Model != tt.wantRule.Model {
				t.Fatalf("ParseModelMapSpec(%q) = {%q %q}, want {%q %q}",
					tt.spec, rule.Pattern, rule.Model, tt.wantRule.Pattern, tt.wantRule.Model)
			}
		})
	}
}

func mustModelMapRule(t *testing.T, spec string) ModelMapRule {
	t.Helper()
	rule, err := ParseModelMapSpec(spec)
	if err != nil {
		t.Fatalf("ParseModelMapSpec(%q): %v", spec, err)
	}
	return rule
}
