package core

import (
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/errors"
	"os"
	"strings"
	"syscall"
)

// preparedImage is a view_image call whose file has been read and sniffed:
// the arguments as the model gave them, and the block that the approval
// prompt describes and the result carries. Reading before asking is what
// makes the question honest — the type and size shown are those of the bytes
// that will be sent — and a swap of the file after the answer changes
// nothing, because the answer was about these bytes.
type preparedImage struct {
	args  ViewImageArgs
	block ImageBlock
}

// result is the whole of executing an approved view_image call. There is no
// process to run: the text tells the model what arrived, and the block is
// what arrived.
func (p *preparedImage) result(id string) ToolResult {
	return ToolResult{
		ID:     id,
		Output: viewImageOutput(p.block),
		Images: []ImageBlock{p.block},
	}
}

// viewImageOutput is the text half of an image result, the same on every
// wire. On Anthropic, Responses, and Google the image sits in the same result;
// on Chat Completions it follows in the next user message, and "attached"
// reads correctly for both.
func viewImageOutput(block ImageBlock) string {
	return "Attached image " + DescribeImageBlock(block) + "."
}

// prepareViewImage reads the file a view_image call names and decides, under
// the same policy an unlisted command gets, whether the bytes may be sent.
func (e *Executor) prepareViewImage(call ToolCall) (preparedExecution, ToolResult, bool) {
	result := ToolResult{ID: call.ID}

	if e.imageUnavailable != "" {
		result.Error = e.imageUnavailable
		result.Code = errors.ErrCodeInvalidInput
		return preparedExecution{}, result, false
	}

	args, err := parseViewImageArgs(call)
	if err != nil {
		result.Error = err.Error()
		result.Code = errors.ErrCodeInvalidInput
		return preparedExecution{}, result, false
	}

	block, err := LoadToolImage(args.Path, e.maxImageBytes)
	if err != nil {
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Image rejected: %s | Reason: %v", args.Path, err)
		}
		result.Error = viewImageLoadError(err)
		result.Code = errors.ErrCodeExecError
		return preparedExecution{}, result, false
	}
	block.Detail = args.Detail
	prepared := preparedExecution{id: call.ID, image: &preparedImage{args: args, block: block}}

	switch e.policy.decideUnlisted() {
	case decisionAllow:
		return prepared, result, true
	case decisionRequireApproval:
		prepared.approvalRequired = true
		return prepared, result, true
	case decisionDenyNotWhitelisted:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Image rejected: %s | Reason: not in whitelist", args.Path)
		}
		denyResult(&result, errors.ErrCodeDeniedNotWhitelisted, "not in whitelist",
			"Whitelist file: "+e.whitelistPath,
			ViewImageToolName+" cannot be granted by a whitelist rule; "+e.restoreApprovalGuidance())
		return preparedExecution{}, result, false
	case decisionDenyNonInteractive:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Image rejected: %s | Reason: approval unavailable", args.Path)
		}
		e.denyApprovalUnavailable(&result, errors.ErrCodeDeniedNonInteractive, false)
		return preparedExecution{}, result, false
	}

	result.Error = "unsupported approval decision"
	result.Code = errors.ErrCodeInvalidInput
	return preparedExecution{}, result, false
}

// parseViewImageArgs decodes a view_image call. Unknown fields are refused
// so a misspelled argument fails loudly rather than being ignored, which is
// the safer reading for a call that discloses a file.
func parseViewImageArgs(call ToolCall) (ViewImageArgs, error) {
	var args ViewImageArgs
	decoder := json.NewDecoder(strings.NewReader(string(call.Args)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return ViewImageArgs{}, fmt.Errorf("invalid %s arguments: %v", ViewImageToolName, err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return ViewImageArgs{}, fmt.Errorf("%s requires a non-empty path", ViewImageToolName)
	}
	args.Detail = strings.ToLower(strings.TrimSpace(args.Detail))
	switch args.Detail {
	case "", "auto", "low", "high":
	default:
		return ViewImageArgs{}, fmt.Errorf("%s detail must be one of: auto, low, high", ViewImageToolName)
	}
	return args, nil
}

// viewImageLoadError is the text the model reads for a file that could not
// be sent. Only the size case gets advice, because it is the one the model
// can fix from where it stands: a missing or non-image file is the model's
// path to reconsider, a large one is a command away from fitting.
func viewImageLoadError(err error) string {
	var tooLarge ImageTooLargeError
	if stdErrors.As(err, &tooLarge) {
		return err.Error() + "; resize or compress the image with a command and try again"
	}
	return err.Error()
}

// LoadToolImage reads a file for a view_image result. It is LoadImageFile
// under the rules a redirection is opened by: Lstat refuses a symlink so the
// path shown at the approval prompt is the file that is read, O_NOFOLLOW keeps
// a swap between the check and the open from changing that, and the
// descriptor's own Stat inside ReadImageFile answers for what was opened.
func LoadToolImage(path string, maxBytes int) (ImageBlock, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ImageBlock{}, fmt.Errorf("read image %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ImageBlock{}, fmt.Errorf("read image %q: is a symbolic link; name the file it points to", path)
	}
	if !info.Mode().IsRegular() {
		return ImageBlock{}, fmt.Errorf("read image %q: not a regular file", path)
	}

	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ImageBlock{}, fmt.Errorf("read image %q: %w", path, err)
	}
	defer file.Close()

	return ReadImageFile(file, path, maxBytes)
}
