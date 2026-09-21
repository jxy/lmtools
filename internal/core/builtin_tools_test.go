package core

import (
	"lmtools/internal/prompts"
	"strings"
	"testing"
)

func TestBuiltinUniversalCommandIncludesStdioFields(t *testing.T) {
	tools := GetBuiltinUniversalCommandTool()
	if len(tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(tools))
	}
	if tools[0].Description == "" {
		t.Fatal("tool description is empty")
	}

	schema, ok := tools[0].InputSchema.(map[string]interface{})
	if !ok {
		t.Fatalf("input schema = %#v, want object", tools[0].InputSchema)
	}
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties = %#v, want object", schema["properties"])
	}
	wantTypes := map[string]string{
		"command":     "array",
		"environ":     "object",
		"workdir":     "string",
		"stdin":       "string",
		"stdin_file":  "string",
		"stdout_file": "string",
		"stderr_file": "string",
		"timeout":     "integer",
	}
	for name, wantType := range wantTypes {
		property, ok := properties[name].(map[string]interface{})
		if !ok {
			t.Fatalf("property %q = %#v, want object", name, properties[name])
		}
		if property["type"] != wantType {
			t.Errorf("property %q type = %#v, want %q", name, property["type"], wantType)
		}
		// A missing key yields a nil interface, and nil == "" is false, so the
		// comparison has to go through a type assertion or a property with no
		// description at all passes.
		description, ok := property["description"].(string)
		if !ok || description == "" {
			t.Errorf("property %q description = %#v, want a non-empty string", name, property["description"])
		}
	}

	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "command" {
		t.Fatalf("required = %#v, want command only", schema["required"])
	}
}

func TestGetBuiltinToolsHonoursTheExclusions(t *testing.T) {
	names := func(cfg RequestOptions) string {
		var out []string
		for _, tool := range GetBuiltinTools(cfg) {
			out = append(out, tool.Name)
		}
		return strings.Join(out, ",")
	}
	for _, tt := range []struct {
		name string
		cfg  RequestOptions
		want string
	}{
		{name: "nothing excluded", cfg: RequestOptions{}, want: "universal_command,view_image"},
		{name: "image excluded", cfg: RequestOptions{ExcludedTools: []string{ViewImageToolName}}, want: "universal_command"},
		{name: "command excluded", cfg: RequestOptions{ExcludedTools: []string{UniversalCommandToolName}}, want: "view_image"},
		{name: "both excluded", cfg: RequestOptions{ExcludedTools: BuiltinToolNames}, want: ""},
		{name: "legacy wire and command excluded", cfg: RequestOptions{ArgoLegacy: true, ExcludedTools: []string{UniversalCommandToolName}}, want: ""},
	} {
		if got := names(tt.cfg); got != tt.want {
			t.Errorf("%s: tools = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestToolSystemPromptForDropsTheCommandRulesWithTheCommand(t *testing.T) {
	if got := ToolSystemPromptFor(nil); got != prompts.ToolSystemPrompt {
		t.Fatalf("prompt without exclusions = %q", got)
	}
	if got := ToolSystemPromptFor([]string{ViewImageToolName}); got != prompts.ToolSystemPrompt {
		t.Fatalf("prompt without the image tool = %q", got)
	}
	got := ToolSystemPromptFor([]string{UniversalCommandToolName})
	if got != prompts.ToolSystemPromptWithoutCommand {
		t.Fatalf("prompt without the command tool = %q", got)
	}
	if strings.Contains(got, UniversalCommandToolName) {
		t.Fatalf("the prompt still names the withheld tool: %q", got)
	}
}
