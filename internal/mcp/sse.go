package mcp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// sseEvent is one Server-Sent Event: its type, when named, and its data
// lines joined by newlines.
type sseEvent struct {
	event string
	data  string
}

// errStopSSE is returned by an event callback to stop reading without an
// error, once the message it waited for has arrived.
var errStopSSE = errors.New("stop reading events")

// readSSE feeds each event of a text/event-stream body to onEvent until the
// stream ends, the callback asks to stop, or a line exceeds maxLine.
// Comment lines, which servers send as keep-alives, carry no event. The id
// and retry fields are read past: neither era this client speaks resumes a
// stream.
func readSSE(r *bufio.Reader, maxLine int, onEvent func(sseEvent) error) error {
	var current sseEvent
	var dataLines []string
	pending := false

	flush := func() error {
		if !pending {
			return nil
		}
		current.data = strings.Join(dataLines, "\n")
		event := current
		current = sseEvent{}
		dataLines = dataLines[:0]
		pending = false
		return onEvent(event)
	}

	for {
		line, err := readLine(r, maxLine)
		if err != nil {
			if err == io.EOF {
				if flushErr := flush(); flushErr != nil && flushErr != errStopSSE {
					return flushErr
				}
				return nil
			}
			return err
		}
		if len(line) == 0 {
			if err := flush(); err != nil {
				if err == errStopSSE {
					return nil
				}
				return err
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value := splitSSEField(line)
		switch field {
		case "event":
			current.event = value
			pending = true
		case "data":
			dataLines = append(dataLines, value)
			pending = true
		}
	}
}

// splitSSEField separates "field: value", dropping the one optional space
// after the colon as the SSE grammar says.
func splitSSEField(line []byte) (string, string) {
	field, value, found := bytes.Cut(line, []byte(":"))
	if !found {
		return string(line), ""
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return string(field), string(value)
}

// readLine returns the next line without its terminator, tolerating CRLF.
// A final unterminated line is returned with a nil error and the following
// call reports io.EOF. A line longer than maxLine is an error rather than a
// silent split, because the caller parses each line as one message.
func readLine(r *bufio.Reader, maxLine int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLine {
			return nil, fmt.Errorf("line exceeds %d bytes", maxLine)
		}
		if err == nil {
			return trimLineEnding(line), nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(line) > 0 {
			return trimLineEnding(line), nil
		}
		return nil, err
	}
}

func trimLineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
}
