package retry

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// checkStreamStart reads at most maxStreamStartBytes of a stream while it
// looks for the first event with content, and judges no line longer than
// streamStartLineBytes. The events that come before content are small;
// Azure's filter results, the largest seen, are about a kilobyte.
const (
	maxStreamStartBytes  = 256 << 10
	streamStartLineBytes = 64 << 10
)

// streamStartError is an error event that opened a 200 event stream, ahead
// of any content, standing for a status worth retrying. The provider found
// the failure after it had sent its headers, so only the stream shows it.
type streamStartError struct {
	method  string
	url     string
	status  int
	message string
}

func (e *streamStartError) Error() string {
	return fmt.Sprintf("%s %s -> error event before any content (as HTTP %d): %s", e.method, e.url, e.status, e.message)
}

// checkStreamStart reads a 200 event stream up to its first event with
// content, and returns the error event found ahead of it if the event
// stands for a status worth retrying; the caller then closes the body.
// Otherwise it puts back what it read, so the body still reads whole, and
// returns nil. It reads events as lmc's stream parser does: the data lines
// of an event are joined with newlines and judged at the blank line that
// ends the event, or where the stream ends, and comments and other fields
// are passed over. So are events that carry nothing yet: Chat chunks with
// no choices, as Azure's filter results come, or with no more than a role
// in their deltas; Anthropic message_start and ping; Responses
// response.created, response.in_progress, and response.queued.
func checkStreamStart(req *http.Request, resp *http.Response) *streamStartError {
	if resp.StatusCode != http.StatusOK || resp.Body == nil || !isEventStream(resp.Header) {
		return nil
	}
	body := resp.Body
	reader := bufio.NewReaderSize(body, streamStartLineBytes)
	var read bytes.Buffer
	var data []byte // the data lines of the event being read, joined
	var inEvent bool
	var readErr error
	for read.Len() < maxStreamStartBytes {
		line, err := reader.ReadSlice('\n')
		read.Write(line)
		if err == bufio.ErrBufferFull {
			break // a line this long is content
		}
		readErr = err
		line = bytes.TrimRight(line, "\r\n")
		if value, ok := dataField(line); ok {
			if inEvent {
				data = append(data, '\n')
			}
			data = append(data, value...)
			inEvent = true
		}
		if inEvent && (len(line) == 0 || readErr != nil) {
			kind, status, message := judgeStreamData(bytes.TrimSpace(data))
			if kind == eventError && retryableStatus(status) {
				return &streamStartError{method: req.Method, url: redactedURL(req.URL), status: status, message: message}
			}
			if kind != eventEmpty {
				break
			}
			data, inEvent = data[:0], false
		}
		if readErr != nil {
			break
		}
	}
	rest := io.Reader(reader)
	if readErr != nil {
		rest = errorReader{readErr}
	}
	resp.Body = replayedBody{Reader: io.MultiReader(bytes.NewReader(read.Bytes()), rest), Closer: body}
	return nil
}

// replayedBody reads what checkStreamStart took from a body and then the
// rest of it, and closes the body itself.
type replayedBody struct {
	io.Reader
	io.Closer
}

// errorReader gives the caller the error that ended checkStreamStart's
// reading, where the caller would have met it.
type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func isEventStream(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && mediaType == "text/event-stream"
}

// redactedURL leaves out the query, where Google's API key goes, and any
// user information.
func redactedURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	clean := *u
	clean.User = nil
	clean.RawQuery = ""
	clean.ForceQuery = false
	return clean.String()
}

// dataField returns the value of a data line as lmc's stream parser takes
// it: what follows "data:" and one space.
func dataField(line []byte) ([]byte, bool) {
	value, ok := bytes.CutPrefix(line, []byte("data:"))
	return bytes.TrimPrefix(value, []byte(" ")), ok
}

// streamEvent is what checkStreamStart makes of an event.
type streamEvent int

const (
	eventEmpty   streamEvent = iota // nothing yet: read on
	eventContent                    // content, the end, or data this check does not know
	eventError                      // an error
)

// judgeStreamData judges the data of one event; for an error it also
// returns the HTTP status the error stands for and its message.
func judgeStreamData(data []byte) (streamEvent, int, string) {
	if len(data) == 0 {
		return eventEmpty, 0, ""
	}
	var event struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
		Choices json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(data, &event) != nil {
		return eventContent, 0, "" // [DONE], or not an event this check knows
	}
	switch {
	case present(event.Error):
		// Argo, OpenAI Chat, Anthropic's error event, and Google.
		status, message := streamErrorStatus(event.Error, false)
		return eventError, status, message
	case event.Type == "error":
		// A Responses error event: the code and message sit beside the
		// event's type.
		status, message := streamErrorStatus(data, true)
		return eventError, status, message
	case event.Type == "response.failed":
		status, message := streamErrorStatus(event.Response.Error, false)
		return eventError, status, message
	}
	switch event.Type {
	case "message_start", "ping", "response.created", "response.in_progress", "response.queued":
		return eventEmpty, 0, ""
	}
	if present(event.Choices) && emptyChoices(event.Choices) {
		return eventEmpty, 0, ""
	}
	return eventContent, 0, ""
}

// emptyChoices reports whether the choices of a Chat chunk carry nothing
// yet: there are none, or each delta holds no more than a role and empty
// fields, and no choice has finished.
func emptyChoices(raw json.RawMessage) bool {
	var choices []struct {
		Delta        map[string]json.RawMessage `json:"delta"`
		FinishReason json.RawMessage            `json:"finish_reason"`
	}
	if json.Unmarshal(raw, &choices) != nil {
		return false
	}
	for _, choice := range choices {
		if present(choice.FinishReason) {
			return false
		}
		for key, value := range choice.Delta {
			if key != "role" && !emptyJSON(value) {
				return false
			}
		}
	}
	return true
}

func present(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func emptyJSON(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", `""`, "[]", "{}":
		return true
	}
	return false
}

// argoErrorCode finds the status Argo puts at the start of the message when
// it passes on its model backend's error: "Error code: 404 - {...}".
var argoErrorCode = regexp.MustCompile(`^Error code: (\d{3})\b`)

// streamErrorStatuses maps the type or code of an error event to the HTTP
// status it stands for, for the failures worth retrying. Argo names the
// Python exception it caught, lowercased here.
var streamErrorStatuses = map[string]int{
	"internal_error":      http.StatusInternalServerError, // Argo's own failure
	"apiconnectionerror":  http.StatusBadGateway,          // Argo could not reach its backend
	"apitimeouterror":     http.StatusGatewayTimeout,      // or heard nothing back in time
	"server_error":        http.StatusInternalServerError, // OpenAI
	"rate_limit_exceeded": http.StatusTooManyRequests,     // OpenAI
	"api_error":           http.StatusInternalServerError, // Anthropic
	"overloaded_error":    529,                            // Anthropic
	"rate_limit_error":    http.StatusTooManyRequests,     // Anthropic
}

// streamErrorStatus reads the error of an error event: the HTTP status it
// stands for, or 0 when it names none this check knows, and its message.
// A status in the message comes first, since Argo's code is internal_error
// whatever went wrong; then a numeric code, as Google sends; then the type;
// then a code string. ignoreType is for a Responses error event, whose type
// field names the event.
func streamErrorStatus(raw json.RawMessage, ignoreType bool) (int, string) {
	var fields struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		var message string
		if json.Unmarshal(raw, &message) == nil {
			return 0, message
		}
		return 0, string(raw)
	}
	if ignoreType {
		fields.Type = ""
	}
	var code string
	var number int
	if json.Unmarshal(fields.Code, &code) != nil {
		_ = json.Unmarshal(fields.Code, &number)
	}
	message := describeStreamError(fields.Message, fields.Type, code, number)
	if match := argoErrorCode.FindStringSubmatch(fields.Message); match != nil {
		status, _ := strconv.Atoi(match[1])
		return status, message
	}
	if number >= 100 && number <= 599 {
		return number, message
	}
	if fields.Type != "" {
		return streamErrorStatuses[strings.ToLower(fields.Type)], message
	}
	return streamErrorStatuses[strings.ToLower(code)], message
}

// describeStreamError writes an error event's message the way lmc's stream
// parsers do: "Streaming response failed (type: internal_error, code:
// internal_error)".
func describeStreamError(message, errType, code string, number int) string {
	var fields []string
	if errType != "" {
		fields = append(fields, "type: "+errType)
	}
	if code != "" {
		fields = append(fields, "code: "+code)
	} else if number != 0 {
		fields = append(fields, "code: "+strconv.Itoa(number))
	}
	message = strings.TrimSpace(message)
	switch {
	case len(fields) == 0 && message == "":
		return "no message"
	case len(fields) == 0:
		return message
	case message == "":
		return strings.Join(fields, ", ")
	}
	return message + " (" + strings.Join(fields, ", ") + ")"
}
