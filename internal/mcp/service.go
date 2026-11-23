package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"unicode/utf8"
)

// Service is a local tool implementation that can also be served over stdio.
type Service interface {
	Definitions() []Tool
	CallTool(context.Context, string, json.RawMessage) (any, error)
}

func ObjectTool(name, description string, properties map[string]any, required ...string) Tool {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	} // Schemas are constructed from program constants.
	return Tool{Name: name, Description: description, InputSchema: raw}
}

func Arguments(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	if bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("arguments must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected data after arguments")
	}
	return nil
}

// TextBoundary backs up only when a valid UTF-8 character spans a byte offset.
func TextBoundary(s string, at int) int {
	if at > 0 && at < len(s) && !utf8.RuneStart(s[at]) {
		// Only a well-formed rune spanning the cut can move it backwards.
		// Raw command output can contain any byte, including a whole buffer
		// of continuation bytes. Each malformed byte is its own decoding error.
		for start := at - 1; start >= 0 && at-start < utf8.UTFMax; start-- {
			if utf8.RuneStart(s[start]) {
				_, width := utf8.DecodeRuneInString(s[start:])
				if start+width > at {
					return start
				}
				break
			}
		}
	}
	return at
}

// SliceText returns UTF-8 text and the next byte offset, or zero at EOF.
func SliceText(text string, offset, limit, maximum int) (string, int, error) {
	if offset < 0 || offset > len(text) || limit < 0 || TextBoundary(text, offset) != offset {
		return "", 0, errors.New("invalid text offset or limit")
	}
	if limit == 0 {
		limit = maximum
	}
	end := offset + min(limit, maximum, len(text)-offset)
	end = TextBoundary(text, end)
	if end == offset && end < len(text) {
		return "", 0, errors.New("limit too small for the next character")
	}
	part := text[offset:end]
	if !utf8.ValidString(part) {
		part = string([]rune(part))
	}
	if end == len(text) {
		end = 0
	}
	return part, end, nil
}

func TextResult(value any, err error) *CallToolResult {
	result := &CallToolResult{ResultType: ResultTypeComplete}
	var text string
	if err == nil {
		var raw []byte
		raw, err = json.Marshal(value)
		text = string(raw)
	}
	if err != nil {
		result.IsError = true
		text = err.Error()
	}
	result.Content = []Content{{Type: "text", Text: text}}
	return result
}

func ServeStdio(ctx context.Context, in io.Reader, out io.Writer, info Implementation, instructions string, service Service) (err error) {
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if closer, ok := in.(io.ReadCloser); ok {
		// Unblock a pending frame read on cancellation or an asynchronous write error.
		end := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer end()
	}
	reader := bufio.NewReaderSize(in, 8192)
	enc := json.NewEncoder(out)
	var writes, requests sync.Mutex
	var workers sync.WaitGroup
	active := make(map[string]context.CancelFunc)
	slots := make(chan struct{}, 64)
	defer func() {
		if err != nil {
			stop(err)
		}
		workers.Wait()
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
	}()
	reply := func(resp *message) {
		writes.Lock()
		defer writes.Unlock()
		if ctx.Err() == nil {
			if err := enc.Encode(resp); err != nil {
				stop(fmt.Errorf("write MCP reply: %w", err))
			}
		}
	}
	legacy := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, oversizedID, err := readServiceFrame(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var req message
		var resp *message
		switch {
		case oversizedID != nil:
			resp = newErrorResponse(oversizedID, CodeInvalidRequest, fmt.Sprintf("request exceeds %d bytes", maxServiceFrameBytes))
		case json.Unmarshal(line, &req) != nil:
			resp = newErrorResponse(json.RawMessage("null"), CodeParseError, "invalid JSON")
		case req.JSONRPC != "2.0" || req.Method == "":
			resp = newErrorResponse(json.RawMessage("null"), CodeInvalidRequest, "expected a JSON-RPC request")
		case req.kind() == kindNotification:
			if req.Method == "notifications/cancelled" {
				var notice struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				if json.Unmarshal(req.Params, &notice) == nil {
					requests.Lock()
					if cancel := active[idKey(notice.RequestID)]; cancel != nil {
						cancel()
					}
					requests.Unlock()
				}
			}
			continue
		default:
			var p struct {
				Protocol  string                     `json:"protocolVersion"`
				Meta      map[string]json.RawMessage `json:"_meta"`
				Name      string                     `json:"name"`
				Arguments json.RawMessage            `json:"arguments"`
			}
			if len(req.Params) > 0 && json.Unmarshal(req.Params, &p) != nil {
				resp = newErrorResponse(req.ID, CodeInvalidParams, "invalid parameters")
				break
			}
			var revision string
			_ = json.Unmarshal(p.Meta[metaProtocolVersion], &revision)
			var value any
			caps := map[string]any{"tools": map[string]any{}}
			switch {
			case req.Method == "initialize":
				v := p.Protocol
				if !slices.Contains(LegacyVersions, v) {
					v = LegacyVersions[0]
				}
				legacy = true
				value = map[string]any{"protocolVersion": v, "capabilities": caps, "serverInfo": info, "instructions": instructions}
			case revision != "" && !slices.Contains(ModernVersions, revision):
				resp = newErrorResponse(req.ID, CodeUnsupportedProtocolVersion, "unsupported protocol revision")
				resp.Error.Data, _ = json.Marshal(map[string]any{"supported": ModernVersions, "requested": revision})
			case req.Method == "server/discover" && revision != "":
				value = map[string]any{"resultType": "complete", "supportedVersions": ModernVersions, "capabilities": caps, "instructions": instructions, "_meta": map[string]any{metaServerInfo: info}}
			case req.Method == "ping":
				value = map[string]any{}
			case !legacy && revision == "":
				resp = newErrorResponse(req.ID, CodeInvalidRequest, "initialize or provide protocol metadata")
			case req.Method == "tools/list":
				list := ListToolsResult{Tools: service.Definitions()}
				if revision != "" {
					list.ResultType = ResultTypeComplete
				}
				value = list
			case req.Method == "tools/call":
				known := slices.ContainsFunc(service.Definitions(), func(t Tool) bool { return t.Name == p.Name })
				if !known {
					resp = newErrorResponse(req.ID, CodeInvalidParams, "unknown tool")
					break
				}
				key := idKey(req.ID)
				requests.Lock()
				if active[key] != nil {
					requests.Unlock()
					resp = newErrorResponse(req.ID, CodeInvalidRequest, "request ID is already in use")
					break
				}
				// Bound outstanding work while keeping the reader available for cancellation.
				if len(slots) == cap(slots) {
					requests.Unlock()
					resp = newErrorResponse(req.ID, CodeInternalError, "too many concurrent tool calls")
					break
				}
				slots <- struct{}{} // Only the frame reader takes slots.
				callCtx, cancel := context.WithCancel(ctx)
				active[key] = cancel
				requests.Unlock()
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer cancel()
					defer func() { <-slots }()
					result := TextResult(service.CallTool(callCtx, p.Name, p.Arguments))
					if revision == "" {
						result.ResultType = ""
					}
					requests.Lock()
					delete(active, key)
					cancelled := callCtx.Err() != nil
					requests.Unlock()
					if cancelled {
						return
					}
					msg, err := newResultResponse(req.ID, result)
					if err != nil {
						stop(err)
						return
					}
					reply(msg)
				}()
				continue
			default:
				resp = newErrorResponse(req.ID, CodeMethodNotFound, "method not found")
			}
			if resp == nil {
				var err error
				resp, err = newResultResponse(req.ID, value)
				if err != nil {
					return err
				}
			}
		}
		reply(resp)
	}
}
