package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

// ModelMapRule maps a client-visible model regex to a backend model name.
type ModelMapRule struct {
	Pattern string
	Model   string

	regex *regexp.Regexp
}

// ParseModelMapSpec parses a command-line model map in REGEX=MODEL_NAME form.
func ParseModelMapSpec(spec string) (ModelMapRule, error) {
	idx := strings.Index(spec, "=")
	if idx < 0 {
		return ModelMapRule{}, fmt.Errorf("model map must use REGEX=MODEL_NAME")
	}

	pattern := strings.TrimSpace(spec[:idx])
	model := strings.TrimSpace(spec[idx+1:])
	if pattern == "" {
		return ModelMapRule{}, fmt.Errorf("model map regex cannot be empty")
	}
	if model == "" {
		return ModelMapRule{}, fmt.Errorf("model map backend model cannot be empty")
	}

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return ModelMapRule{}, fmt.Errorf("invalid model map regex %q: %w", pattern, err)
	}

	return ModelMapRule{Pattern: pattern, Model: model, regex: regex}, nil
}

// ValidateModelMapRules verifies all model map rules have usable regexes and models.
func ValidateModelMapRules(rules []ModelMapRule) error {
	for i, rule := range rules {
		if strings.TrimSpace(rule.Pattern) == "" {
			return fmt.Errorf("model map rule %d regex cannot be empty", i+1)
		}
		if strings.TrimSpace(rule.Model) == "" {
			return fmt.Errorf("model map rule %d backend model cannot be empty", i+1)
		}
		if _, err := rule.compiled(); err != nil {
			return fmt.Errorf("model map rule %d invalid regex %q: %w", i+1, rule.Pattern, err)
		}
	}
	return nil
}

func (r ModelMapRule) matches(model string) bool {
	regex, err := r.compiled()
	if err != nil {
		return false
	}
	return regex.MatchString(model)
}

func (r ModelMapRule) compiled() (*regexp.Regexp, error) {
	if r.regex != nil {
		return r.regex, nil
	}
	return regexp.Compile(r.Pattern)
}
