package session

import (
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	identity := identityPrefix + strings.Repeat("0a", 16)
	for text, want := range map[string]Ref{
		"0001":                  {Session: "0001"},
		" 0002 ":                {Session: "0002"},
		"0001/000c":             {Session: "0001", Message: "000c"},
		"10000/1000a":           {Session: "10000", Message: "1000a"},
		identity:                {Session: identity},
		identity + "/0010":      {Session: identity, Message: "0010"},
		"fffff/0001":            {Session: "fffff", Message: "0001"},
		"0001/" + "000000000c1": {},
	} {
		got, err := ParseRef(text)
		if want == (Ref{}) {
			if err == nil {
				t.Errorf("ParseRef(%q) = %v, want an error", text, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %v, %v; want %v", text, got, err, want)
		}
		if strings.TrimSpace(text) != got.String() {
			t.Errorf("%v writes as %q, want %q", got, got.String(), strings.TrimSpace(text))
		}
	}
}

// Each number has one spelling, and references in the format the store
// replaced are not references.
func TestParseRefRefusesOtherSpellings(t *testing.T) {
	for _, text := range []string{
		"", "1", "001", "00001", "000A", "0001/", "0001/12", "0001/000C",
		"0001/0004.s.0001", "0001/0004.s.0001/0005", "0001/000c/0001",
		"ses_abc", identityPrefix + strings.Repeat("A", 32), "/0001",
	} {
		if ref, err := ParseRef(text); err == nil {
			t.Errorf("ParseRef(%q) = %v, want an error", text, ref)
		}
	}
}
