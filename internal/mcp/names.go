package mcp

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

const (
	// QualifiedPrefix opens every MCP tool name the model sees, the
	// convention Claude Code and Codex share.
	QualifiedPrefix = "mcp__"
	qualifiedJoin   = "__"
	// MaxToolNameLength is the tightest provider limit on a tool name:
	// OpenAI and Gemini stop at 64 characters, Anthropic at 128.
	MaxToolNameLength = 64
	hashSuffixLength  = 8
)

// QualifiedName is the name a server's tool is advertised under:
// mcp__<server>__<tool>, with every character outside [A-Za-z0-9_-]
// replaced by an underscore, since MCP allows dots and providers do not.
// A name past the provider limit keeps its head and ends in a hash of the
// unsanitised name, so it stays stable across runs and distinct from a
// sibling that shares the head.
func QualifiedName(server, tool string) string {
	raw := QualifiedPrefix + server + qualifiedJoin + tool
	name := sanitizeName(raw)
	if len(name) <= MaxToolNameLength {
		return name
	}
	sum := sha1.Sum([]byte(raw))
	suffix := "_" + hex.EncodeToString(sum[:])[:hashSuffixLength]
	return name[:MaxToolNameLength-len(suffix)] + suffix
}

func sanitizeName(raw string) string {
	var out strings.Builder
	out.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}
	}
	return out.String()
}
