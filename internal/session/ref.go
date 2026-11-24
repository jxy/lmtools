package session

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// identityPrefix starts every session identity, so an identity cannot be
// mistaken for a session number.
const identityPrefix = "ses_"

// Ref names a session, or a message in a session. A session is named by its
// number, or by its identity until Locate maps that to the number. A Ref
// that names no message stays a session reference until a command decides
// what it means: resuming and showing take the session's newest message,
// and deleting takes the whole session.
type Ref struct {
	Session string
	Message string
}

// ParseRef reads a reference written as SESSION or SESSION/MESSAGE, where
// SESSION is a session number or identity.
func ParseRef(text string) (Ref, error) {
	text = strings.TrimSpace(text)
	session, message, named := strings.Cut(text, "/")
	if !validID(session) && !validIdentity(session) {
		return Ref{}, fmt.Errorf("%q is not a session reference: use a session number such as 0001 or an identity such as %s…, optionally followed by /MESSAGE", text, identityPrefix)
	}
	if named && !validID(message) {
		return Ref{}, fmt.Errorf("%q does not name a message: a message ID is a hexadecimal number of at least four digits, such as 000c", text)
	}
	return Ref{Session: session, Message: message}, nil
}

// String writes r the way ParseRef reads it.
func (r Ref) String() string {
	if r.Message == "" {
		return r.Session
	}
	return r.Session + "/" + r.Message
}

// At returns the reference to message id in r's session.
func (r Ref) At(id string) Ref {
	return Ref{Session: r.Session, Message: id}
}

// formatID writes a session number or message ID: lowercase hexadecimal
// with at least four digits.
func formatID(n uint64) string {
	return fmt.Sprintf("%04x", n)
}

// following returns the number or ID after n, and false when n is the
// last one formatID can spell.
func following(n uint64) (uint64, bool) {
	return n + 1, n < math.MaxUint64
}

// countPast returns the count past n: the number after it, or n itself
// when n is the last, so a count that ran out stays at the last number,
// which nothing takes.
func countPast(n uint64) uint64 {
	if next, ok := following(n); ok {
		return next
	}
	return n
}

// parseID reads a session number or message ID in the one spelling formatID
// gives it.
func parseID(id string) (uint64, bool) {
	if len(id) < 4 || len(id) > 16 {
		return 0, false
	}
	n, err := strconv.ParseUint(id, 16, 64)
	if err != nil || formatID(n) != id {
		return 0, false
	}
	return n, true
}

func validID(id string) bool {
	_, ok := parseID(id)
	return ok
}

func validIdentity(s string) bool {
	rest, ok := strings.CutPrefix(s, identityPrefix)
	if !ok || len(rest) != 32 {
		return false
	}
	for _, c := range rest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
