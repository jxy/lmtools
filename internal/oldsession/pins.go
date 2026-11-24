package oldsession

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/core"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// PinChange is metadata on a local user command. Ordinary user messages
// and provider tool results cannot create a change by imitating its text.
type PinChange struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Scope  string `json:"scope,omitempty"`
	Source string `json:"source,omitempty"`
	Start  int    `json:"start,omitempty"`
	End    int    `json:"end,omitempty"`
}

type StandingPin struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Text   string `json:"text"`
}

func PinDigest(pins []StandingPin) string {
	if pins == nil {
		return ""
	}
	data, _ := json.Marshal(pins)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// ActivePins returns the latest user change for each ID in chronological
// order. Only the supplied ancestors participate, including on a fork.
func ActivePins(messages []core.TypedMessage, refs []ContextRef) ([]StandingPin, error) {
	var pins []StandingPin
	seen := make(map[string]bool)
	var sources map[string]int
	for i := len(refs) - 1; i >= 0; i-- {
		change := refs[i].Pin
		if change == nil || seen[change.ID] {
			continue
		}
		if i >= len(messages) || messages[i].Role != "user" || change.ID == "" || (change.Scope != "" && change.Scope != "branch") {
			return nil, errors.New("invalid saved user pin event")
		}
		seen[change.ID] = true
		if pins == nil {
			pins = []StandingPin{}
		}
		if change.Action == "remove" {
			continue
		}
		if (change.Action != "add" && change.Action != "replace") || len(messages[i].Blocks) != 2 {
			return nil, fmt.Errorf("invalid saved pin %s", change.ID)
		}
		body, ok := messages[i].Blocks[1].(core.TextBlock)
		if !ok || len(body.Text) == 0 || change.Start < 0 || change.End-change.Start != len(body.Text) {
			return nil, fmt.Errorf("invalid saved text for pin %s", change.ID)
		}
		source := change.Source
		if source == "" {
			source = refs[i].Source + ".1"
			if change.Start != 0 {
				return nil, fmt.Errorf("invalid source span for pin %s", change.ID)
			}
		} else {
			if sources == nil {
				sources = make(map[string]int, len(refs))
				for j, r := range refs {
					sources[r.Source] = j
				}
			}
			id, block, err := pinRef(source)
			j, found := sources[id]
			if err != nil || !found || j >= i {
				return nil, fmt.Errorf("source of pin %s is outside its ancestry", change.ID)
			}
			original, _, err := pinBlock(messages[j], refs[j], block, []int{change.Start, change.End})
			if err != nil || original != body.Text {
				return nil, fmt.Errorf("source text of pin %s no longer matches", change.ID)
			}
		}
		pins = append(pins, StandingPin{change.ID, source, change.Start, change.End, body.Text})
	}
	slices.Reverse(pins)
	return pins, nil
}

// UserPinText resolves an original user text block in this branch, with
// optional half-open UTF-8 byte offsets. A tool results message can also
// carry a trailing text block written by the host.
func UserPinText(messages []core.TypedMessage, refs []ContextRef, source string, span []int) (string, int, error) {
	id, index, err := pinRef(source)
	if err != nil {
		return "", 0, err
	}
	for i, ref := range refs {
		if ref.Source == id && i < len(messages) {
			return pinBlock(messages[i], ref, index, span)
		}
	}
	return "", 0, errors.New("pin reference must identify original user text on this branch")
}

func pinRef(source string) (string, int, error) {
	id, block, ok := strings.Cut(source, ".")
	index, err := strconv.Atoi(block)
	if !ok || err != nil || index < 0 || strconv.Itoa(index) != block {
		return "", 0, errors.New("use the original user message.block reference")
	}
	return id, index, nil
}

func pinBlock(msg core.TypedMessage, ref ContextRef, index int, span []int) (string, int, error) {
	if msg.Role != "user" || index >= len(msg.Blocks) || ref.Pin != nil && (index != 1 || ref.Pin.Action == "remove") || slices.ContainsFunc(msg.Blocks, func(b core.Block) bool { _, ok := b.(core.ToolResultBlock); return ok }) {
		return "", 0, errors.New("pin reference must identify original user text")
	}
	b, ok := msg.Blocks[index].(core.TextBlock)
	if !ok {
		return "", 0, errors.New("pin reference must select a text block")
	}
	start, end := 0, len(b.Text)
	if len(span) != 0 {
		if len(span) != 2 {
			return "", 0, errors.New("use START END as byte offsets")
		}
		start, end = span[0], span[1]
	}
	if start < 0 || end <= start || end > len(b.Text) || !utf8.ValidString(b.Text[start:end]) {
		return "", 0, errors.New("pin span must select complete UTF-8 characters within the block")
	}
	return b.Text[start:end], start, nil
}

// PinEntry puts the change and its visible user message in one session commit.
// The second block holds the user's exact bytes and has an ordinary history ref.
func PinEntry(change PinChange, text string, active []StandingPin) (MessageEntry, error) {
	change.Scope = "branch"
	var header string
	switch change.Action {
	case "add":
		change.ID = newRevision()
		header = fmt.Sprintf("Standing user instruction %s (this branch until withdrawn):\n", change.ID)
	case "replace", "remove":
		if !slices.ContainsFunc(active, func(p StandingPin) bool { return p.ID == change.ID }) {
			return MessageEntry{}, fmt.Errorf("no active pin with ID %q", change.ID)
		}
		header = fmt.Sprintf("The user withdraws standing instruction %s.", change.ID)
		if change.Action == "replace" {
			header = fmt.Sprintf("The user replaces standing instruction %s with this instruction for the branch:\n", change.ID)
		}
	default:
		return MessageEntry{}, errors.New("unknown pin action")
	}
	blocks := []core.Block{core.TextBlock{Text: header}}
	if change.Action != "remove" {
		if strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
			return MessageEntry{}, errors.New("supply nonempty UTF-8 text for a pin")
		}
		change.End = change.Start + len(text)
		blocks = append(blocks, core.TextBlock{Text: text})
	}
	return MessageEntry{Message: Message{Role: core.RoleUser, Content: header + text, Timestamp: time.Now(), Pin: &change}, Blocks: blocks}, nil
}

func RenderPins(pins []StandingPin) string {
	if pins == nil {
		return ""
	}
	if len(pins) == 0 {
		return "Active user instructions\nThe user has withdrawn every previously marked instruction on this branch."
	}
	var b strings.Builder
	b.WriteString("Active user instructions\nThe user marked these instructions for this branch. Apply each within its stated scope. A later user request takes precedence within its scope.")
	for _, pin := range pins {
		fmt.Fprintf(&b, "\n\n[%s | branch | %s bytes %d..%d]\n%s", pin.ID, pin.Source, pin.Start, pin.End, pin.Text)
	}
	return b.String()
}
