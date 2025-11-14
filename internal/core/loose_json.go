// Package core provides core functionality for parsing loose JSON.
package core

import (
	"strconv"
	"unicode/utf8"
)

// isSpaceByte reports whether a byte is ASCII whitespace.
func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// looseJSONParser encapsulates the state needed for parsing loose JSON.
// This allows the helper functions to be extracted while maintaining access to shared state.
type looseJSONParser struct {
	s string // The input string
	i int    // Current position in the string
}

// skipSpaces advances the parser position past any whitespace characters.
func (p *looseJSONParser) skipSpaces() {
	for p.i < len(p.s) && isSpaceByte(p.s[p.i]) {
		p.i++
	}
}

// parseString parses a string with the given delimiter (either ' or ").
// It handles escape sequences and returns the parsed string.
func (p *looseJSONParser) parseString(delim byte) (string, bool) {
	open := p.i // index of opening quote
	p.i++       // skip opening quote
	out := make([]byte, 0, 32)
	escaped := false

	for p.i < len(p.s) {
		ch := p.s[p.i]
		if escaped {
			// Handle known escapes; otherwise preserve backslash + char
			switch ch {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\\':
				out = append(out, '\\')
			case '"':
				out = append(out, '"')
			case '\'':
				out = append(out, '\'')
			default:
				out = append(out, '\\', ch)
			}
			escaped = false
			p.i++
			continue
		}
		if ch == '\\' {
			escaped = true
			p.i++
			continue
		}
		if ch == delim {
			// Count raw backslashes immediately preceding this delimiter
			cnt := 0
			for j := p.i - 1; j > open && p.s[j] == '\\'; j-- {
				cnt++
			}
			if cnt%2 == 1 { // escaped delimiter, keep it
				out = append(out, delim)
				p.i++
				continue
			}
			p.i++
			return string(out), true
		}
		out = append(out, ch)
		p.i++
	}

	return "", false
}

// parseNumber parses a numeric value (integer or float).
// It handles scientific notation and returns the parsed number as float64.
func (p *looseJSONParser) parseNumber() (interface{}, bool) {
	j := p.i
	if j < len(p.s) && (p.s[j] == '-' || p.s[j] == '+') {
		j++
	}
	hasDot := false
	for j < len(p.s) {
		c := p.s[j]
		if c >= '0' && c <= '9' {
			j++
			continue
		}
		if c == '.' && !hasDot {
			hasDot = true
			j++
			continue
		}
		if c == 'e' || c == 'E' {
			j++
			if j < len(p.s) && (p.s[j] == '+' || p.s[j] == '-') {
				j++
			}
			for j < len(p.s) && (p.s[j] >= '0' && p.s[j] <= '9') {
				j++
			}
			break
		}
		break
	}
	if j == p.i {
		return nil, false
	}
	numStr := p.s[p.i:j]
	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return nil, false
	}
	p.i = j
	return f, true
}

// parseLiteral parses Python/JSON literals: true, false, null, True, False, None.
// Returns the corresponding Go value (bool or nil).
func (p *looseJSONParser) parseLiteral() (interface{}, bool) {
	j := p.i
	for j < len(p.s) {
		c := p.s[j]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			j++
		} else {
			break
		}
	}
	if j == p.i {
		return nil, false
	}
	w := p.s[p.i:j]
	switch w {
	case "true", "True":
		p.i = j
		return true, true
	case "false", "False":
		p.i = j
		return false, true
	case "null", "None":
		p.i = j
		return nil, true
	default:
		return nil, false
	}
}

// parseArray parses a JSON array.
// Handles trailing commas and returns a slice of parsed values.
func (p *looseJSONParser) parseArray() ([]interface{}, bool) {
	if p.i >= len(p.s) || p.s[p.i] != '[' {
		return nil, false
	}
	p.i++
	p.skipSpaces()
	arr := make([]interface{}, 0)

	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return arr, true
	}
	for p.i < len(p.s) {
		p.skipSpaces()
		v, ok := p.parseValue()
		if !ok {
			return nil, false
		}
		arr = append(arr, v)
		p.skipSpaces()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			p.skipSpaces()
			if p.i < len(p.s) && p.s[p.i] == ']' {
				p.i++
				return arr, true // trailing comma
			}
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return arr, true
		}
		return nil, false
	}
	return nil, false
}

// parseObject parses a JSON object.
// Handles trailing commas and various quote styles for keys.
func (p *looseJSONParser) parseObject() (map[string]interface{}, bool) {
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return nil, false
	}
	p.i++
	p.skipSpaces()
	obj := make(map[string]interface{})

	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return obj, true
	}
	for p.i < len(p.s) {
		p.skipSpaces()
		if p.i >= len(p.s) {
			return nil, false
		}
		var key string
		switch p.s[p.i] {
		case '\'':
			k, ok := p.parseString('\'')
			if !ok {
				return nil, false
			}
			key = k
		case '"':
			k, ok := p.parseString('"')
			if !ok {
				return nil, false
			}
			key = k
		case '\\':
			if p.i+1 < len(p.s) && (p.s[p.i+1] == '\'' || p.s[p.i+1] == '"') {
				p.i++
				delim := p.s[p.i]
				k, ok := p.parseString(delim)
				if !ok {
					return nil, false
				}
				key = k
				break
			}
			fallthrough
		default:
			return nil, false
		}

		p.skipSpaces()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, false
		}
		p.i++
		p.skipSpaces()

		val, ok := p.parseValue()
		if !ok {
			return nil, false
		}
		obj[key] = val
		p.skipSpaces()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			p.skipSpaces()
			if p.i < len(p.s) && p.s[p.i] == '}' {
				p.i++
				return obj, true // trailing comma
			}
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			return obj, true
		}
		// Unexpected character while parsing object
		return nil, false
	}
	return nil, false
}

// parseValue parses any JSON value: object, array, string, number, or literal.
// This is the main dispatch function that determines the value type and calls the appropriate parser.
func (p *looseJSONParser) parseValue() (interface{}, bool) {
	p.skipSpaces()
	if p.i >= len(p.s) {
		return nil, false
	}
	if p.s[p.i] == '\\' && p.i+1 < len(p.s) && (p.s[p.i+1] == '\'' || p.s[p.i+1] == '"') {
		p.i++
		delim := p.s[p.i]
		str, ok := p.parseString(delim)
		if !ok {
			return nil, false
		}
		return str, true
	}
	ch := p.s[p.i]

	switch ch {
	case '{':
		m, ok := p.parseObject()
		if !ok {
			return nil, false
		}
		return m, true
	case '[':
		a, ok := p.parseArray()
		if !ok {
			return nil, false
		}
		return a, true
	case '\'':
		str, ok := p.parseString('\'')
		if !ok {
			return nil, false
		}
		return str, true
	case '"':
		str, ok := p.parseString('"')
		if !ok {
			return nil, false
		}
		return str, true
	default:
		if (ch >= '0' && ch <= '9') || ch == '-' || ch == '+' || ch == '.' {
			return p.parseNumber()
		}
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
			return p.parseLiteral()
		}
		return nil, false
	}
}

// parseLooseJSONObjectAt parses a Python/JSON-like object starting at absolute index 'start'
// within the original string 's'. It returns the parsed map, the index of the closing '}'
// (inclusive), and ok=true on success.
//
// This parser handles:
// - Single-quoted strings (Python-style)
// - Double-quoted strings (JSON-style)
// - Python literals: True, False, None (in addition to true, false, null)
// - Trailing commas in objects and arrays
// - Escape sequences in strings
// - Scientific notation in numbers
//
// The parser does not require that parsing consumes the entire tail of the string;
// it stops exactly at the end of the object.
func parseLooseJSONObjectAt(s string, start int) (map[string]interface{}, int, bool) {
	// Validate that the input string is valid UTF-8
	if !utf8.ValidString(s) {
		return nil, -1, false
	}

	// Create a parser instance with the input string and starting position
	parser := &looseJSONParser{
		s: s,
		i: start,
	}

	parser.skipSpaces()
	if parser.i >= len(s) || s[parser.i] != '{' {
		return nil, -1, false
	}
	obj, ok := parser.parseObject()
	if !ok {
		return nil, -1, false
	}
	// The object just closed; parser.i points right after the closing '}'. Return end index.
	return obj, parser.i - 1, true
}
