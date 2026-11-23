package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

const maxServiceFrameBytes = 1 << 20

// oversizedEnvelope recovers a top-level scalar ID while draining a rejected
// request. Params can precede the ID and can be arbitrarily large. Nested
// values are skipped, and at most one bounded scalar field is buffered.
type oversizedEnvelope struct {
	depth     int
	quoted    bool
	escaped   bool
	skipField bool
	done      bool
	field     []byte
	id        json.RawMessage
}

func (e *oversizedEnvelope) scan(part []byte) {
	for _, c := range part {
		if e.done {
			return
		}
		space := c == ' ' || c == '\t' || c == '\r'
		if e.depth == 0 {
			if !space {
				if c != '{' {
					e.done = true
					return
				}
				e.depth = 1
			}
			continue
		}
		if !e.quoted {
			if e.depth == 1 && (c == ',' || c == '}') {
				e.finishField()
				e.done = c == '}'
				continue
			}
			switch c {
			case '{', '[':
				e.depth++
				e.skipField = true
			case '}', ']':
				if e.depth == 1 {
					e.id = nil
					e.done = true
					return
				}
				e.depth--
			}
			if space {
				if len(e.field) == 0 || e.field[len(e.field)-1] == ' ' {
					continue
				}
				c = ' '
			}
		}
		if e.escaped {
			e.escaped = false
		} else if e.quoted && c == '\\' {
			e.escaped = true
		} else if c == '"' {
			e.quoted = !e.quoted
		}
		if len(e.field) == maxServiceFrameBytes {
			e.skipField = true
		}
		if !e.skipField {
			e.field = append(e.field, c)
		}
	}
}

func (e *oversizedEnvelope) finishField() {
	if !e.skipField {
		key, value, ok := bytes.Cut(e.field, []byte(":"))
		var name string
		if ok && json.Unmarshal(key, &name) == nil && name == "id" {
			value = bytes.TrimSpace(value)
			if e.id == nil && len(value) > 0 && (value[0] == '"' || value[0] == '-' || value[0] >= '0' && value[0] <= '9') && json.Valid(value) {
				e.id = bytes.Clone(value)
			} else {
				e.id = json.RawMessage("null")
			}
		}
	}
	e.field = e.field[:0]
	e.skipField = false
}

// readServiceFrame consumes one line. An oversized frame returns only its
// request ID, or JSON null when the ID could not be identified.
func readServiceFrame(r *bufio.Reader) (line []byte, oversizedID json.RawMessage, err error) {
	var rejected *oversizedEnvelope
	for {
		part, more, err := r.ReadLine()
		if err == io.EOF && (len(line) > 0 || rejected != nil) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if rejected == nil && len(part) > maxServiceFrameBytes-len(line) {
			rejected = &oversizedEnvelope{}
			rejected.scan(line)
			line = nil
		}
		if rejected != nil {
			rejected.scan(part)
		} else {
			line = append(line, part...)
		}
		if !more {
			break
		}
	}
	if rejected != nil {
		if rejected.id == nil {
			return nil, json.RawMessage("null"), nil
		}
		return nil, rejected.id, nil
	}
	return line, nil, nil
}
