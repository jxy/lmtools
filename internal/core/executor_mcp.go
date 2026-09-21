package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/errors"
	"lmtools/internal/mcp"
	"strings"
	"time"
)

// preparedMCP is an MCP tool call that passed parsing and policy: the
// qualified name the model used and the server, tool, and arguments the
// call resolves to.
type preparedMCP struct {
	name string
	call MCPCall
}

// prepareMCPCall parses and policy-checks a call to an advertised MCP
// tool. The rules speak to the server and tool name alone, so nothing is
// sent before the decision.
func (e *Executor) prepareMCPCall(call ToolCall, tool mcp.QualifiedTool) (preparedExecution, ToolResult, bool) {
	result := ToolResult{ID: call.ID}

	args, err := parseMCPArguments(call)
	if err != nil {
		result.Error = err.Error()
		result.Code = errors.ErrCodeInvalidInput
		return preparedExecution{}, result, false
	}
	prepared := preparedExecution{
		id:  call.ID,
		mcp: &preparedMCP{name: call.Name, call: MCPCall{Server: tool.Server, Tool: tool.Tool.Name, Arguments: args}},
	}
	label := tool.Server + "/" + tool.Tool.Name

	switch e.policy.decideMCP(tool.Server, tool.Tool.Name) {
	case decisionAllow:
		return prepared, result, true
	case decisionRequireApproval:
		prepared.approvalRequired = true
		return prepared, result, true
	case decisionDenyBlacklist:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("MCP tool rejected: %s | Reason: blacklisted", label)
		}
		denyResult(&result, errors.ErrCodeDeniedBlacklist, "blacklisted")
		return prepared, result, false
	case decisionDenyNotWhitelisted:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("MCP tool rejected: %s | Reason: not in whitelist", label)
		}
		guidance := fmt.Sprintf(`To allow this tool, either:
  1. Add %s to your whitelist file and use -tool-whitelist <file>
  2. %s`, suggestedMCPRuleJSON(tool.Server, tool.Tool.Name), e.restoreApprovalGuidance())
		denyResult(&result, errors.ErrCodeDeniedNotWhitelisted, "not in whitelist",
			"Whitelist file: "+e.whitelistPath, guidance)
		return prepared, result, false
	case decisionDenyNonInteractive:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("MCP tool rejected: %s | Reason: approval unavailable", label)
		}
		e.denyApprovalUnavailable(&result, errors.ErrCodeDeniedNonInteractive, "tool")
		return prepared, result, false
	}

	result.Error = "unsupported approval decision"
	result.Code = errors.ErrCodeInvalidInput
	return preparedExecution{}, result, false
}

// parseMCPArguments accepts the JSON object a tools/call wants. No
// arguments is the empty object; anything that is not an object is the
// model's error, reported so it can be corrected.
func parseMCPArguments(call ToolCall) (json.RawMessage, error) {
	raw := strings.TrimSpace(string(call.Args))
	if raw == "" || raw == "null" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return nil, fmt.Errorf("invalid %s arguments: must be a JSON object: %v", call.Name, err)
	}
	return json.RawMessage(raw), nil
}

// executeMCPCall sends an approved call and turns what comes back into the
// result the model reads.
func (e *Executor) executeMCPCall(ctx context.Context, exec preparedExecution) ToolResult {
	result := ToolResult{ID: exec.id}
	label := exec.mcp.call.Server + "/" + exec.mcp.call.Tool
	if e.log != nil && e.log.IsDebugEnabled() {
		e.log.Debugf("Calling MCP tool %s | Arguments: %s", label, exec.mcp.call.Arguments)
	}

	start := time.Now()
	callResult, err := e.mcp.Call(ctx, exec.mcp.name, exec.mcp.call.Arguments)
	result.Elapsed = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		switch {
		case stdErrors.Is(err, context.DeadlineExceeded):
			result.Code = errors.ErrCodeTimeout
		case stdErrors.Is(err, context.Canceled):
			result.Code = errors.ErrCodeCancelled
		default:
			result.Code = errors.ErrCodeExecError
		}
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("MCP tool %s failed | Error: %s | Duration: %dms", label, result.Error, result.Elapsed)
		}
		return result
	}

	e.fillMCPResult(&result, callResult)
	if e.log != nil && e.log.IsDebugEnabled() {
		e.log.Debugf("MCP tool %s completed | Duration: %dms | Output size: %d bytes | Images: %d | Error: %v",
			label, result.Elapsed, len(result.Output), len(result.Images), result.Error != "")
	}
	return result
}

// fillMCPResult maps a tools/call result onto a ToolResult: text items
// joined, images as blocks under the provider cap, everything the wire
// cannot carry as a one line note, structured content as text when no text
// came, the server's error flag as the model facing error, and the output
// cap with its truncation mark.
func (e *Executor) fillMCPResult(result *ToolResult, callResult *mcp.CallToolResult) {
	if callResult.InputRequired() {
		result.Error = "the MCP server asked for more input (elicitation, sampling, or roots), which lmc does not provide; the call cannot be completed as made"
		result.Code = errors.ErrCodeExecError
		return
	}

	var parts []string
	sawText := false
	for _, item := range callResult.Content {
		switch item.Type {
		case "text":
			parts = append(parts, item.Text)
			sawText = true
		case "image":
			if block, note := e.mcpImage(item.MimeType, item.Data); note != "" {
				parts = append(parts, note)
			} else {
				result.Images = append(result.Images, block)
			}
		case "audio":
			parts = append(parts, fmt.Sprintf("[audio omitted: %s, %s]", item.MimeType, FormatByteCount(base64DecodedSize(item.Data))))
		case "resource_link":
			parts = append(parts, mcpResourceLinkNote(item))
		case "resource":
			if item.Resource == nil {
				parts = append(parts, "[resource omitted: no contents]")
				continue
			}
			parts = append(parts, e.mcpEmbeddedResource(result, *item.Resource)...)
			if item.Resource.Text != "" {
				sawText = true
			}
		default:
			parts = append(parts, fmt.Sprintf("[%s content omitted]", item.Type))
		}
	}
	if !sawText && len(callResult.StructuredContent) > 0 {
		parts = append(parts, string(callResult.StructuredContent))
	}

	text := strings.Join(parts, "\n")
	if int64(len(text)) > e.maxOutputSize {
		text = text[:e.maxOutputSize]
		result.Truncated = true
		result.TruncatedTo = int(e.maxOutputSize)
	}
	if callResult.IsError {
		result.Error = text
		if result.Error == "" {
			result.Error = "the MCP tool reported an error without a message"
		}
		result.Code = errors.ErrCodeExecError
		return
	}
	result.Output = text
}

// mcpImage turns an image item into a block, or into the note that takes
// its place when the wire cannot carry it: the legacy Argo wire, an
// unsupported type, undecodable data, or a size past the provider cap.
func (e *Executor) mcpImage(mimeType, data string) (ImageBlock, string) {
	size := base64DecodedSize(data)
	if e.imageUnavailable != "" {
		return ImageBlock{}, fmt.Sprintf("[image omitted: %s, %s; %s]", mimeType, FormatByteCount(size), e.imageUnavailable)
	}
	if size > e.maxImageBytes {
		return ImageBlock{}, fmt.Sprintf("[image omitted: %s, %s is larger than the %s limit]",
			mimeType, FormatByteCount(size), FormatByteCount(e.maxImageBytes))
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return ImageBlock{}, fmt.Sprintf("[image omitted: %s, data is not valid base64]", mimeType)
	}
	detected, ok := DetectImageMediaType(decoded)
	if !ok {
		return ImageBlock{}, fmt.Sprintf("[image omitted: %s is not %s]", mimeType, SupportedImageMediaTypesText)
	}
	return ImageBlock{URL: ImageDataURL(detected, decoded)}, ""
}

// mcpResourceLinkNote is the one line a resource link becomes: the model
// cannot fetch it, so it learns the URI and what the server said about it.
func mcpResourceLinkNote(item mcp.Content) string {
	var details []string
	if item.Name != "" {
		details = append(details, item.Name)
	}
	if item.MimeType != "" {
		details = append(details, item.MimeType)
	}
	note := "[resource: " + item.URI
	if len(details) > 0 {
		note += " (" + strings.Join(details, ", ") + ")"
	}
	note += "]"
	if item.Description != "" {
		note += " " + item.Description
	}
	return note
}

// mcpEmbeddedResource renders an embedded resource: its text as text, an
// image blob as an image, and any other blob as a note.
func (e *Executor) mcpEmbeddedResource(result *ToolResult, resource mcp.ResourceContents) []string {
	if resource.Text != "" {
		return []string{fmt.Sprintf("[resource: %s]", resource.URI), resource.Text}
	}
	if resource.Blob == "" {
		return []string{fmt.Sprintf("[resource: %s (empty)]", resource.URI)}
	}
	if strings.HasPrefix(resource.MimeType, "image/") {
		block, note := e.mcpImage(resource.MimeType, resource.Blob)
		if note != "" {
			return []string{fmt.Sprintf("[resource: %s] %s", resource.URI, note)}
		}
		block.Name = resource.URI
		result.Images = append(result.Images, block)
		return []string{fmt.Sprintf("[resource: %s attached as an image]", resource.URI)}
	}
	return []string{fmt.Sprintf("[resource omitted: %s, %s, %s of binary data]",
		resource.URI, resource.MimeType, FormatByteCount(base64DecodedSize(resource.Blob)))}
}
