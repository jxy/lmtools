package tools

import (
	"encoding/json"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"testing"
)

var viewImagePNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func viewImageCall(t *testing.T, id string, args core.ViewImageArgs) core.ToolCall {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal view_image args: %v", err)
	}
	return core.ToolCall{ID: id, Name: core.ViewImageToolName, Args: encoded}
}

func TestCLIToolUIShowCallRendersViewImage(t *testing.T) {
	notifier := newFormatTestNotifier()
	ui := NewCLIToolUI(notifier)

	ui.ShowCall(0, 2, viewImageCall(t, "img", core.ViewImageArgs{Path: "plots/out.png", Detail: "high"}), nil)
	ui.ShowCall(1, 2, viewImageCall(t, "plain", core.ViewImageArgs{Path: "shot.png"}), nil)

	want := "\n>>> Tools requested: 2\n[1/2] View image: \"plots/out.png\"\n      Detail: high\n\n[2/2] View image: \"shot.png\"\n"
	if got := notifier.out.String(); got != want {
		t.Fatalf("ShowCall() output = %q, want %q", got, want)
	}
}

func TestCLIToolUIShowCallFallsBackForAnUndecodableViewImage(t *testing.T) {
	notifier := newFormatTestNotifier()
	ui := NewCLIToolUI(notifier)

	ui.ShowCall(0, 1, core.ToolCall{ID: "img", Name: core.ViewImageToolName, Args: json.RawMessage(`{}`)}, nil)

	want := "\n>>> Tools requested: 1\n[1/1] Tool: view_image\n      Arguments: {}\n"
	if got := notifier.out.String(); got != want {
		t.Fatalf("ShowCall() output = %q, want %q", got, want)
	}
}

// Every outcome a view_image call can have, driven through the real executor
// and this package's real renderer, the way the command outcomes are.
func TestViewImageOutcomesRenderThroughTheCLI(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(image, viewImagePNG, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("not an image\n"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	attached := `
>>> Tools requested: 1
[1/1] View image: "<TMP>/shot.png"

>>> Running 1 command...

>>> Results:
[1/1] Completed in NNms
      Output:
Attached image shot.png (image/png, 12 bytes).

`
	scenarios := []executorScenario{
		{
			name:     "auto-approved",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{viewImageCall(t, "a", core.ViewImageArgs{Path: image})},
			want:     attached,
		},
		{
			name:     "approved at the prompt",
			cfg:      core.RequestOptions{},
			approver: core.NewTestApprover(true),
			calls:    []core.ToolCall{viewImageCall(t, "a", core.ViewImageArgs{Path: image})},
			want:     attached,
		},
		{
			name:     "denied at the prompt",
			cfg:      core.RequestOptions{},
			approver: core.NewTestApprover(false),
			calls:    []core.ToolCall{viewImageCall(t, "a", core.ViewImageArgs{Path: image, Detail: "low"})},
			want: `
>>> Tools requested: 1
[1/1] View image: "<TMP>/shot.png"
      Detail: low

>>> No commands will be run.

>>> Results:
[1/1] Not run: user denied permission

`,
		},
		{
			name:     "not an image",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{viewImageCall(t, "a", core.ViewImageArgs{Path: notes})},
			want: `
>>> Tools requested: 1
[1/1] View image: "<TMP>/notes.txt"

>>> No commands will be run.

>>> Results:
[1/1] Not run: read image "<TMP>/notes.txt": content is not PNG, JPEG, GIF, or WebP

`,
		},
		{
			name:     "nobody to ask",
			cfg:      core.RequestOptions{ToolNonInteractive: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{viewImageCall(t, "a", core.ViewImageArgs{Path: image})},
			want: `
>>> Tools requested: 1
[1/1] View image: "<TMP>/shot.png"

>>> No commands will be run.

>>> Results:
[1/1] Not run: approval disabled by -tool-non-interactive
      Hint: Allow via one of:
              - Run interactively without -tool-non-interactive
              - Use -tool-auto-approve

`,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			if got := renderExecutorBatch(t, scenario, dir); got != scenario.want {
				t.Fatalf("transcript mismatch\n--- got ---\n%s--- want ---\n%s", got, scenario.want)
			}
		})
	}
}
