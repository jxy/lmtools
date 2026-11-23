// Package workingcontext constructs the model's view of a saved conversation.
package workingcontext

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"lmtools/internal/session"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed history.txt
var historyInstructions string

//go:embed phase_hint.txt
var phaseHintInstructions string

type Record struct {
	Ref     string `json:"ref"`
	Turn    int    `json:"message"`
	Kind    string `json:"kind"`
	At      string `json:"at,omitempty"`
	Tool    string `json:"tool,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Capture string `json:"capture_note,omitempty"`
	Text    string `json:"-"`
}

// IsHistoryTool recognizes the local service's qualified tool names in saved records.
func IsHistoryTool(name string) bool {
	tool, ok := strings.CutPrefix(name, mcp.QualifiedPrefix+"history__")
	return ok && slices.ContainsFunc(historyDefs, func(t mcp.Tool) bool { return t.Name == tool })
}

func Records(msg core.TypedMessage, ref string, index int) []Record {
	if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
		return nil
	}
	var records []Record
	var capture string
	for _, v := range msg.Blocks {
		if b, ok := v.(core.TextBlock); ok && strings.HasPrefix(b.Text, "Note: Output for tool '") {
			capture = b.Text[:mcp.TextBoundary(b.Text, min(len(b.Text), 512))]
		}
	}
	for i, v := range msg.Blocks {
		r := Record{Ref: fmt.Sprintf("%s.%d", ref, i), Turn: index + 1, Kind: msg.Role}
		switch b := v.(type) {
		case core.TextBlock:
			r.Text = b.Text
		case core.ToolUseBlock:
			if IsHistoryTool(b.Name) {
				continue
			}
			r.Kind = "call"
			r.Tool = b.Name
			r.Text = string(b.Input)
			if b.InputString != "" {
				r.Text = b.InputString
			}
		case core.ToolResultBlock:
			if IsHistoryTool(b.Name) {
				continue
			}
			r.Kind = "result"
			r.Tool = b.Name
			r.Text = b.Content
			r.IsError = b.IsError
			r.Capture = capture
			if len(b.Images) > 0 {
				r.Capture = strings.TrimSpace(fmt.Sprintf("%s\nAttached images: %d. History reads return the captured text.", capture, len(b.Images)))
			}
		case core.ImageBlock:
			r.Text = fmt.Sprintf("[attached image %q]", b.Name)
		default:
			continue
		}
		records = append(records, r)
	}
	return records
}

type History struct {
	mu sync.RWMutex
	historyView
	phaseHints  bool
	unavailable bool
}

// Each call keeps the branch snapshot it started with while Bind installs the next.
type historyView struct {
	messages []core.TypedMessage
	refs     []session.ContextRef
	archived int
	head     *session.Session
}

// Keep the advertised interface stable if a REPL switches to a model
// whose capacity is unknown. Its receipt must accurately describe the action.
func (h *History) SetCompactionAvailable(available bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unavailable = !available
}

func (h *History) Bind(messages []core.TypedMessage, refs []session.ContextRef, archived int, head *session.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = messages
	h.refs = refs
	h.archived = archived
	h.head = nil
	if head != nil && head.Head != nil {
		ref := *head.Head
		h.head = &session.Session{Path: head.Path, Head: &ref}
	}
}

// A tool receipt crosses process boundaries. Only a successful result for
// the named phase request can cause an early cut on a later user turn.
func phaseRequest(messages []core.TypedMessage) bool {
	const name = "mcp__history__request_compaction"
	for i := 1; i < len(messages); i++ {
		if messages[i-1].Role != "assistant" {
			continue
		}
		for _, item := range messages[i-1].Blocks {
			call, ok := item.(core.ToolUseBlock)
			if !ok || call.Name != name {
				continue
			}
			for _, item := range messages[i].Blocks {
				result, ok := item.(core.ToolResultBlock)
				if ok && result.ToolUseID == call.ID && !result.IsError {
					return true
				}
			}
		}
	}
	return false
}

var historyDefs = func() []mcp.Tool {
	s := map[string]string{"type": "string"}
	n := map[string]string{"type": "integer"}
	return []mcp.Tool{
		mcp.ObjectTool("search", "Find a literal string on this branch across all prior windows. By default skip the current user question and history-tool traffic. Use scope all to include the current question, archived for the compacted prefix. Narrow by kind or tool. An exhausted exact match search is definitive for the reported scope.", map[string]any{"query": s, "kind": s, "tool": s, "scope": s, "offset": n, "limit": n}, "query"),
		mcp.ObjectTool("read", "Read original text at a source ref and byte offset. Use the offset from search. Results give the next offset for long blocks.", map[string]any{"ref": s, "offset": n, "limit": n}, "ref"),
		mcp.ObjectTool("request_compaction", "Request a new context window after a completed phase. The host checks the budget and the tool batch before the cut. Continue normally after the receipt.", map[string]any{}),
	}
}()

func (h *History) Definitions() []mcp.Tool {
	if h.phaseHints {
		return historyDefs
	}
	return historyDefs[:2]
}

type hit struct {
	Record
	Offset  int    `json:"offset"`
	Snippet string `json:"snippet"`
}
type hits struct {
	Status    string `json:"status"`
	Hits      []hit  `json:"hits"`
	Messages  int    `json:"covered_messages"`
	Archived  int    `json:"archived_messages"`
	Exhausted bool   `json:"exhausted"`
	Next      int    `json:"next_offset,omitempty"`
}
type query struct {
	Query, Kind, Tool, Scope string
	Offset, Limit            int
}

// A literal has at most four bytes per rune after Unicode case folding.
// Overlap adjacent chunks by that bound so a match can cross a cancellation check.
func findHistoryMatch(ctx context.Context, re *regexp.Regexp, text string, overlap int) ([]int, error) {
	const step = 64 << 10
	for start := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := mcp.TextBoundary(text, start+min(len(text)-start, step))
		until := mcp.TextBoundary(text, end+min(len(text)-end, overlap))
		if at := re.FindStringIndex(text[start:until]); at != nil {
			at[0] += start
			at[1] += start
			return at, nil
		}
		if until == len(text) {
			return nil, nil
		}
		start = end
	}
}

func (h *historyView) search(ctx context.Context, p query) (hits, error) {
	result := hits{Hits: []hit{}, Messages: len(h.refs), Archived: h.archived, Exhausted: true, Status: "no matching original record in this scope"}
	if strings.TrimSpace(p.Query) == "" || len(p.Query) > 512 || p.Offset < 0 || p.Limit < 0 {
		return result, fmt.Errorf("supply a literal query of 1–512 bytes and nonnegative pagination")
	}
	if p.Scope != "" && p.Scope != "all" && p.Scope != "archived" {
		return result, fmt.Errorf("scope must be all or archived")
	}
	if p.Scope == "archived" {
		result.Messages = h.archived
	}
	skipUser := -1
	if p.Scope == "" {
		for i := len(h.refs) - 1; i >= 0; i-- {
			m := h.messages[i]
			if m.Role == "assistant" && !hasCall(m) {
				break
			}
			if m.Role == "user" && !resultMessage(m) {
				skipUser = i
				break
			}
		}
	}
	visibleBlocks := 0
	if p.Limit == 0 {
		p.Limit = 5
	}
	p.Limit = min(p.Limit, 12)
	re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(p.Query))
	if err != nil {
		return result, err
	}
	overlap := utf8.RuneCountInString(p.Query) * utf8.UTFMax
	seen := 0
	for i, ref := range h.refs[:result.Messages] {
		if i == skipUser {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		for _, r := range Records(h.messages[i], ref.Source, i) {
			if !ref.At.IsZero() {
				r.At = ref.At.UTC().Format(time.RFC3339Nano)
			}
			visibleBlocks++
			if p.Kind != "" && r.Kind != p.Kind || p.Tool != "" && !strings.EqualFold(r.Tool, p.Tool) && !strings.HasSuffix(r.Tool, "__"+p.Tool) {
				continue
			}
			at := 0
			for at <= len(r.Text) {
				pos, err := findHistoryMatch(ctx, re, r.Text[at:], overlap)
				if err != nil {
					return result, err
				}
				if pos == nil {
					if at != 0 || !re.MatchString(r.Tool) {
						break
					}
					pos = []int{0, len(r.Text) + 1} // one hit on the tool label
				}
				begin := at + pos[0]
				if seen >= p.Offset {
					if len(result.Hits) >= p.Limit {
						result.Next = seen
						result.Exhausted = false
						return result, nil
					}
					start := mcp.TextBoundary(r.Text, max(0, begin-64))
					text, _, err := mcp.SliceText(r.Text, start, 280, 280)
					if err != nil {
						return result, err
					}
					result.Hits = append(result.Hits, hit{r, start, text})
					result.Status = "matched"
				}
				seen++
				at += pos[1]
			}
		}
	}
	if visibleBlocks == 0 {
		result.Status = "empty history scope; no earlier evidence is available"
	}
	return result, nil
}

func (h *historyView) read(ctx context.Context, ref string, offset, limit int) (any, error) {
	source, block, ok := strings.Cut(ref, ".")
	b, err := strconv.Atoi(block)
	if !ok || err != nil || b < 0 {
		return nil, fmt.Errorf("invalid source ref")
	}
	for i, r := range h.refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.Source != source {
			continue
		}
		for _, rec := range Records(h.messages[i], source, i) {
			if !r.At.IsZero() {
				rec.At = r.At.UTC().Format(time.RFC3339Nano)
			}
			if rec.Ref != ref {
				continue
			}
			text, next, err := mcp.SliceText(rec.Text, offset, limit, 4096)
			if err != nil {
				return nil, err
			}
			return struct {
				Record
				Text string `json:"text"`
				Next int    `json:"next_offset,omitempty"`
			}{rec, text, next}, nil
		}
	}
	return nil, fmt.Errorf("source ref is outside the available branch or no longer exists")
}

func (h *History) CallTool(ctx context.Context, name string, args json.RawMessage) (any, error) {
	h.mu.RLock()
	view, hints, unavailable := h.historyView, h.phaseHints, h.unavailable
	h.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "request_compaction" && hints {
		if err := mcp.Arguments(args, &struct{}{}); err != nil {
			return nil, err
		}
		if unavailable {
			return nil, fmt.Errorf("compaction is unavailable with the current model and window settings")
		}
		return "Checkpoint requested at the next eligible boundary. Continue the task.", nil
	}
	if err := session.CheckContextHead(ctx, view.head); err != nil {
		return nil, err
	}
	switch name {
	case "search":
		var p query
		if err := mcp.Arguments(args, &p); err != nil {
			return nil, err
		}
		return view.search(ctx, p)
	case "read":
		var p struct {
			Ref           string
			Offset, Limit int
		}
		if err := mcp.Arguments(args, &p); err != nil {
			return nil, err
		}
		return view.read(ctx, p.Ref, p.Offset, p.Limit)
	default:
		return nil, fmt.Errorf("unknown history tool %q", name)
	}
}
