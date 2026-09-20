package core

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func viewImageCall(id, args string) ToolCall {
	return ToolCall{ID: id, Name: ViewImageToolName, Args: json.RawMessage(args)}
}

func viewImagePathCall(t *testing.T, id string, args ViewImageArgs) ToolCall {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal view_image args: %v", err)
	}
	return ToolCall{ID: id, Name: ViewImageToolName, Args: encoded}
}

func newViewImageExecutor(t *testing.T, cfg RequestOptions, approver Approver) *Executor {
	t.Helper()
	cfg.ToolTimeout = 5 * time.Second
	executor, err := NewExecutor(cfg, nil, approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func singleResult(t *testing.T, results []ToolResult) ToolResult {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %#v", len(results), results)
	}
	return results[0]
}

func TestViewImageLoadsTheFileIntoTheResult(t *testing.T) {
	path := writeTestImage(t, "shot.png", pngBytes)
	executor := newViewImageExecutor(t, RequestOptions{ToolAutoApprove: true}, NewTestApprover(true))

	result := singleResult(t, executor.ExecuteParallel(context.Background(),
		[]ToolCall{viewImagePathCall(t, "img", ViewImageArgs{Path: path, Detail: "low"})}, TestToolUI{}))

	if result.Error != "" || result.NotRun {
		t.Fatalf("result = %#v, want a successful load", result)
	}
	want := ImageBlock{URL: ImageDataURL("image/png", pngBytes), Detail: "low", Name: "shot.png"}
	if len(result.Images) != 1 || result.Images[0] != want {
		t.Fatalf("Images = %#v, want %#v", result.Images, want)
	}
	if wantOutput := "Attached image " + DescribeImageBlock(want) + "."; result.Output != wantOutput {
		t.Fatalf("Output = %q, want %q", result.Output, wantOutput)
	}
	if strings.Contains(result.Output, "base64") {
		t.Fatal("the text half of the result carries the bytes")
	}
}

func TestViewImageAsksTheImageQuestionAndHonoursTheAnswer(t *testing.T) {
	path := writeTestImage(t, "shot.png", pngBytes)
	for _, approve := range []bool{true, false} {
		approver := NewTestApprover(approve)
		// No flags: the approver is present and nothing says not to ask.
		executor := newViewImageExecutor(t, RequestOptions{}, approver)

		result := singleResult(t, executor.ExecuteParallel(context.Background(),
			[]ToolCall{viewImagePathCall(t, "img", ViewImageArgs{Path: path})}, TestToolUI{}))

		if len(approver.ImageApprovalCalls) != 1 || approver.ImageApprovalCalls[0].Path != path {
			t.Fatalf("approve=%v: image prompts = %#v, want one for %q", approve, approver.ImageApprovalCalls, path)
		}
		if len(approver.ApprovalCalls) != 0 {
			t.Fatalf("approve=%v: the command question was asked for an image", approve)
		}
		if approve {
			if result.Error != "" || len(result.Images) != 1 {
				t.Fatalf("approved result = %#v, want the image", result)
			}
			continue
		}
		if !result.NotRun || result.Reason != "user denied permission" || result.Code != errors.ErrCodeNotApproved {
			t.Fatalf("denied result = %#v, want the user denial", result)
		}
		if len(result.Images) != 0 {
			t.Fatal("a denied image was still attached")
		}
	}
}

func TestViewImageIsDeniedUnderAWhitelistThatDoesNotNameIt(t *testing.T) {
	dir := t.TempDir()
	whitelist := filepath.Join(dir, "wl.txt")
	if err := os.WriteFile(whitelist, []byte(`["/bin/echo"]`+"\n"), 0o600); err != nil {
		t.Fatalf("write whitelist: %v", err)
	}
	path := writeTestImage(t, "shot.png", pngBytes)
	executor := newViewImageExecutor(t, RequestOptions{
		ToolWhitelist:      whitelist,
		ToolNonInteractive: true,
		ToolAutoApprove:    true,
	}, NewTestApprover(true))

	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		{ID: "echo", Name: UniversalCommandToolName, Args: json.RawMessage(`{"command":["/bin/echo","hi"]}`)},
		viewImagePathCall(t, "img", ViewImageArgs{Path: path}),
	}, nil)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Error != "" || results[0].Output != "hi\n" {
		t.Fatalf("whitelisted command result = %#v, want it to run", results[0])
	}

	image := results[1]
	if !image.NotRun || image.Code != errors.ErrCodeDeniedNotWhitelisted || image.Reason != "not in whitelist" {
		t.Fatalf("image result = %#v, want the not-whitelisted denial", image)
	}
	suggestion := suggestedImageRuleJSON(ViewImageArgs{Path: path})
	for _, want := range []string{"Whitelist file: " + whitelist, "To allow this image, either:", "Add " + suggestion + " to your whitelist file", "Run interactively without -tool-non-interactive"} {
		if !strings.Contains(image.Error, want) {
			t.Errorf("Error = %q, want it to mention %q", image.Error, want)
		}
	}
	// The printed rule admits the call it was printed for.
	granted, err := parseCommandRule(suggestion, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse suggested rule %s: %v", suggestion, err)
	}
	if !granted.matchesImage(ViewImageArgs{Path: path}) {
		t.Fatalf("suggested rule %s does not admit the denied call", suggestion)
	}
	if len(image.Images) != 0 {
		t.Fatal("a denied image was still attached")
	}
}

func TestViewImageIsDeniedWhenNobodyCanApprove(t *testing.T) {
	path := writeTestImage(t, "shot.png", pngBytes)
	executor := newViewImageExecutor(t, RequestOptions{ToolNonInteractive: true}, NewTestApprover(true))

	result := singleResult(t, executor.ExecuteParallel(context.Background(),
		[]ToolCall{viewImagePathCall(t, "img", ViewImageArgs{Path: path})}, nil))

	if !result.NotRun || result.Code != errors.ErrCodeDeniedNonInteractive {
		t.Fatalf("result = %#v, want the approval-unavailable denial", result)
	}
	for _, want := range []string{"approval disabled by -tool-non-interactive", "Use -tool-auto-approve", "Add the image to a whitelist"} {
		if !strings.Contains(result.Error, want) {
			t.Errorf("Error = %q, want it to mention %q", result.Error, want)
		}
	}
	if strings.Contains(result.Error, "Add the command") {
		t.Fatalf("Error = %q names a command for an image", result.Error)
	}
}

func TestViewImageRejectsFilesItCannotSend(t *testing.T) {
	dir := t.TempDir()
	valid := writeTestImage(t, "ok.png", pngBytes)
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	empty := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		maxBytes int
		want     string
	}{
		{name: "missing", path: filepath.Join(dir, "missing.png"), want: "no such file"},
		{name: "symlink", path: link, want: "is a symbolic link"},
		{name: "directory", path: dir, want: "not a regular file"},
		{name: "not an image", path: writeTestImage(t, "shot.bmp", bmpBytes), want: "content is not " + SupportedImageMediaTypesText},
		{name: "empty", path: empty, want: "file is empty"},
		{name: "over the cap", path: valid, maxBytes: len(pngBytes) - 1, want: "larger than the 11 bytes limit; resize or compress the image with a command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := newViewImageExecutor(t, RequestOptions{ToolAutoApprove: true}, NewTestApprover(true))
			if tt.maxBytes > 0 {
				executor.maxImageBytes = tt.maxBytes
			}
			result := singleResult(t, executor.ExecuteParallel(context.Background(),
				[]ToolCall{viewImagePathCall(t, "img", ViewImageArgs{Path: tt.path})}, nil))
			if !result.NotRun || result.Code != errors.ErrCodeExecError {
				t.Fatalf("result = %#v, want a not-run load failure", result)
			}
			if !strings.Contains(result.Error, tt.want) {
				t.Fatalf("Error = %q, want it to contain %q", result.Error, tt.want)
			}
			if len(result.Images) != 0 {
				t.Fatal("a rejected file was still attached")
			}
		})
	}
}

func TestViewImageRejectsBadArguments(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{name: "empty object", args: `{}`, want: "requires a non-empty path"},
		{name: "blank path", args: `{"path":"  "}`, want: "requires a non-empty path"},
		{name: "unknown detail", args: `{"path":"x.png","detail":"ultra"}`, want: "detail must be one of: auto, low, high"},
		{name: "unknown field", args: `{"path":"x.png","paths":["y.png"]}`, want: "invalid view_image arguments"},
		{name: "not json", args: `nope`, want: "invalid view_image arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := newViewImageExecutor(t, RequestOptions{ToolAutoApprove: true}, NewTestApprover(true))
			result := singleResult(t, executor.ExecuteParallel(context.Background(),
				[]ToolCall{viewImageCall("img", tt.args)}, nil))
			if !result.NotRun || result.Code != errors.ErrCodeInvalidInput {
				t.Fatalf("result = %#v, want a not-run argument rejection", result)
			}
			if !strings.Contains(result.Error, tt.want) {
				t.Fatalf("Error = %q, want it to contain %q", result.Error, tt.want)
			}
		})
	}
}

func TestViewImageIsUnavailableOnTheLegacyArgoWire(t *testing.T) {
	cfg := RequestOptions{Provider: constants.ProviderArgo, ArgoLegacy: true, ToolAutoApprove: true}
	tools := GetBuiltinTools(cfg)
	if len(tools) != 1 || tools[0].Name != UniversalCommandToolName {
		t.Fatalf("legacy tools = %#v, want universal_command alone", tools)
	}

	path := writeTestImage(t, "shot.png", pngBytes)
	executor := newViewImageExecutor(t, cfg, NewTestApprover(true))
	result := singleResult(t, executor.ExecuteParallel(context.Background(),
		[]ToolCall{viewImagePathCall(t, "img", ViewImageArgs{Path: path})}, nil))
	if !result.NotRun || result.Error != "view_image is not available with -argo-legacy" || len(result.Images) != 0 {
		t.Fatalf("result = %#v, want the legacy refusal", result)
	}
}

func TestViewImageCancelledBeforeRunReadsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := newViewImageExecutor(t, RequestOptions{ToolAutoApprove: true}, NewTestApprover(true))

	// A path that does not exist: had the file been opened, the error would
	// say so instead.
	result := singleResult(t, executor.ExecuteParallel(ctx,
		[]ToolCall{viewImagePathCall(t, "img", ViewImageArgs{Path: filepath.Join(t.TempDir(), "missing.png")})}, nil))
	if !result.NotRun || result.Code != errors.ErrCodeCancelled || result.Reason != "execution cancelled" {
		t.Fatalf("result = %#v, want the pre-run cancellation", result)
	}
}

func TestGetBuiltinToolsNamesTheCapTheExecutorEnforces(t *testing.T) {
	tests := []struct {
		name      string
		cfg       RequestOptions
		wantLimit int
	}{
		{name: "anthropic", cfg: RequestOptions{Provider: constants.ProviderAnthropic}, wantLimit: constants.MaxAnthropicImageBytes},
		{name: "argo claude", cfg: RequestOptions{Provider: constants.ProviderArgo, Model: "claudeopus41"}, wantLimit: constants.MaxAnthropicImageBytes},
		{name: "argo gpt", cfg: RequestOptions{Provider: constants.ProviderArgo, Model: "gpt5"}, wantLimit: constants.MaxCLIImageBytes},
		{name: "openai", cfg: RequestOptions{Provider: constants.ProviderOpenAI}, wantLimit: constants.MaxCLIImageBytes},
		{name: "google", cfg: RequestOptions{Provider: constants.ProviderGoogle}, wantLimit: constants.MaxCLIImageBytes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToolImageByteLimit(tt.cfg); got != tt.wantLimit {
				t.Fatalf("ToolImageByteLimit() = %d, want %d", got, tt.wantLimit)
			}
			tools := GetBuiltinTools(tt.cfg)
			if len(tools) != 2 || tools[0].Name != UniversalCommandToolName || tools[1].Name != ViewImageToolName {
				t.Fatalf("tools = %#v, want universal_command then view_image", tools)
			}
			for _, want := range []string{FormatByteCount(tt.wantLimit), SupportedImageMediaTypesText} {
				if !strings.Contains(tools[1].Description, want) {
					t.Errorf("view_image description %q does not mention %q", tools[1].Description, want)
				}
			}
			schema, ok := tools[1].InputSchema.(map[string]interface{})
			if !ok {
				t.Fatalf("InputSchema type = %T", tools[1].InputSchema)
			}
			if required, _ := schema["required"].([]string); len(required) != 1 || required[0] != "path" {
				t.Fatalf("required = %#v, want [path]", schema["required"])
			}
			if executor := newViewImageExecutor(t, tt.cfg, nil); executor.maxImageBytes != tt.wantLimit {
				t.Fatalf("executor cap = %d, want %d", executor.maxImageBytes, tt.wantLimit)
			}
		})
	}
}

// A view_image call has no rule to match, so its decision is the one an
// unlisted command gets under the same flags. Enumerating the policy cells
// against decide itself keeps the two from drifting apart.
func TestDecideUnlistedIsTheUnlistedCommandDecision(t *testing.T) {
	policies := []approvalPolicy{
		{},
		{autoApprove: true},
		{canPrompt: true},
		{autoApprove: true, canPrompt: true},
		{whitelistConfigured: true},
		{whitelistConfigured: true, canPrompt: true},
		{whitelistConfigured: true, autoApprove: true},
		{whitelistConfigured: true, autoApprove: true, canPrompt: true},
		{whitelist: []commandRule{rule("ls")}, autoApprove: true},
		{whitelist: []commandRule{rule("ls")}, canPrompt: true},
		{nonInteractive: true, autoApprove: true},
	}
	for i, policy := range policies {
		want := policy.decide(UniversalCommandArgs{Command: []string{"unlisted-command"}})
		if got := policy.decideUnlisted(); got != want {
			t.Errorf("policy %d %+v: decideUnlisted() = %v, decide(unlisted command) = %v", i, policy, got, want)
		}
	}

	// The two cells that matter most, stated outright.
	if got := (approvalPolicy{whitelistConfigured: true, autoApprove: true}).decideUnlisted(); got != decisionDenyNotWhitelisted {
		t.Fatalf("whitelist without a prompt: %v, want not-whitelisted denial even with auto-approve", got)
	}
	if got := (approvalPolicy{canPrompt: true}).decideUnlisted(); got != decisionRequireApproval {
		t.Fatalf("prompt available: %v, want a prompt", got)
	}
}

func TestFormatByteCountNamesDecimalMegabytes(t *testing.T) {
	tests := map[int]string{
		5 * 1000 * 1000: "5MB",
		5 * 1024 * 1024: "5MiB",
		3 * 1000 * 1000: "3MB",
		1500:            "1500 bytes",
		2048:            "2KiB",
	}
	for size, want := range tests {
		if got := FormatByteCount(size); got != want {
			t.Errorf("FormatByteCount(%d) = %q, want %q", size, got, want)
		}
	}
}
