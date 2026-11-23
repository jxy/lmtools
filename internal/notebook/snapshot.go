package notebook

import (
	"context"
	"fmt"
	"lmtools/internal/mcp"
	"strings"
)

// Snapshot freezes the selected notebook view for one window. The full view
// is used by benchmarks and by sessions originally bound to that view.
func Snapshot(ctx context.Context, root, view string) (string, error) {
	if root == "" || view == "tools" {
		return "", nil
	}
	s := Store{Root: root}
	page, err := s.List(ctx, "", 0, 12)
	if err != nil {
		return "", err
	}
	if len(page.Notes) == 0 && page.InvalidCount == 0 && page.UnreadableCount == 0 {
		return "Project notebook at window start: empty.", nil
	}
	var b strings.Builder
	b.WriteString("Project notebook at window start. These entries are historical evidence.\n")
	for _, h := range page.Notes {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if view == "full" {
			current, body, err := s.Read(h.ID)
			if err != nil {
				page.skipNote(h.ID, err)
				continue
			}
			fmt.Fprintf(&b, "%s @%s\n", current.ID, current.Revision)
			part, next, err := mcp.SliceText(body, 0, 2048, 2048)
			if err != nil {
				return "", err
			}
			b.WriteString(part)
			if next > 0 {
				b.WriteString("\n[read the note for the rest]")
			}
			b.WriteByte('\n')
		} else {
			fmt.Fprintf(&b, "%s @%s — %s. %s\n", h.ID, h.Revision, h.Title, h.Summary)
		}
		if b.Len() >= 8192 {
			break
		}
	}
	if page.InvalidCount > 0 {
		fmt.Fprintf(&b, "%d malformed notes were skipped; list names them.\n", page.InvalidCount)
	}
	if page.UnreadableCount > 0 {
		fmt.Fprintf(&b, "%d unreadable notes were skipped; list names them.\n", page.UnreadableCount)
	}
	if b.Len() >= 8192 {
		part, _, err := mcp.SliceText(b.String(), 0, 8080, 8080)
		if err != nil {
			return "", err
		}
		return part + "\nMore entries or note text remain in the notebook.", nil
	}
	if !page.Exhausted {
		b.WriteString("More entries remain in the notebook.\n")
	}
	return b.String(), nil
}
