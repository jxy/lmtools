package notebook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/mcp"
	"os"
	"strings"
	"unicode/utf8"
)

const Instructions = "Use the project notebook for durable decisions, their reasons, corrections and source references. Look up a topic before changing it. Use the current revision when replacing it. Keep the summary to one short line. When a note has sufficient evidence, continue the task; an exhausted search is a completed lookup."

var definitions = func() []mcp.Tool {
	text := map[string]string{"type": "string"}
	num := map[string]string{"type": "integer"}
	return []mcp.Tool{
		mcp.ObjectTool("list", "List a page of note IDs, titles, summaries and revisions.", map[string]any{"offset": num, "limit": num}),
		mcp.ObjectTool("search", "Search note titles and bodies by words. Returns a page of matching note headers.", map[string]any{"query": text, "offset": num, "limit": num}, "query"),
		mcp.ObjectTool("read", "Read a note by ID. Offset and length are UTF-8 bytes. Supply revision when continuing a partial read.", map[string]any{"id": text, "offset": num, "length": num, "revision": text}, "id"),
		mcp.ObjectTool("put", "Create or replace one complete note. Use expected_revision 'new' to create, otherwise use the current revision. Include original source references and retain applicable corrections.", map[string]any{"id": text, "title": text, "summary": text, "body": text, "sources": map[string]any{"type": "array", "items": text}, "expected_revision": text}, "id", "title", "summary", "body", "sources", "expected_revision"),
	}
}()

func (s Store) Definitions() []mcp.Tool { return definitions }

const maxReadBytes = 8192

type notePage struct {
	Header
	Markdown string `json:"markdown"`
	Next     int    `json:"next_offset,omitempty"`
	Invalid  string `json:"invalid,omitempty"`
	Base64   string `json:"base64,omitempty"`
}

// An explicit read exposes the original bytes and revision needed to repair
// a file that the normal catalog skipped. Writes still validate the replacement.
func (s Store) repairPage(ctx context.Context, id, revision string, offset, length int, reason error) (notePage, error) {
	p := notePage{Header: Header{ID: id}, Invalid: reason.Error()}
	if length < 0 {
		return p, errors.New("invalid note length")
	}
	if length == 0 || length > maxReadBytes {
		length = maxReadBytes
	}
	// Read past the page boundary just far enough to recognize a split rune.
	digest, data, size, err := s.rawPage(ctx, id, offset, length+utf8.UTFMax)
	if err != nil {
		return p, err
	}
	if revision != "" && revision != digest {
		return p, fmt.Errorf("note %q changed; restart the read", id)
	}
	p.Revision = digest
	n := mcp.TextBoundary(string(data), min(length, len(data)))
	if n == 0 || !utf8.Valid(data[:n]) {
		n = min(length, len(data))
		p.Base64 = base64.StdEncoding.EncodeToString(data[:n])
	} else {
		p.Markdown = string(data[:n])
	}
	if int64(offset+n) < size {
		p.Next = offset + n
	}
	return p, nil
}

func (s Store) CallTool(ctx context.Context, name string, args json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch name {
	case "list", "search":
		var p struct {
			Query         string
			Offset, Limit int
		}
		if err := mcp.Arguments(args, &p); err != nil {
			return nil, err
		}
		if name == "search" && strings.TrimSpace(p.Query) == "" {
			return nil, fmt.Errorf("search requires a query")
		}
		return s.List(ctx, p.Query, p.Offset, p.Limit)
	case "read":
		var p struct {
			ID, Revision   string
			Offset, Length int
		}
		if err := mcp.Arguments(args, &p); err != nil {
			return nil, err
		}
		h, text, err := s.Read(p.ID)
		if errors.Is(err, errInvalidNote) {
			return s.repairPage(ctx, p.ID, p.Revision, p.Offset, p.Length, err)
		}
		if err != nil {
			return nil, err
		}
		if p.Revision != "" && p.Revision != h.Revision {
			return nil, fmt.Errorf("note %q changed; restart the read", p.ID)
		}
		body, next, err := mcp.SliceText(text, p.Offset, p.Length, maxReadBytes)
		if err != nil {
			return nil, err
		}
		return notePage{Header: h, Markdown: body, Next: next}, nil
	case "put":
		var e Edit
		if err := mcp.Arguments(args, &e); err != nil {
			return nil, err
		}
		return s.put(ctx, e, (*os.File).Sync)
	default:
		return nil, fmt.Errorf("unknown notebook tool %q", name)
	}
}
