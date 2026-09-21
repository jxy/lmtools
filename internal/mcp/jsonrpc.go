package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// message is the JSON-RPC 2.0 envelope. One struct carries requests,
// notifications, and responses; kind tells them apart the way the
// specification does, by which fields are present.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type messageKind int

const (
	kindInvalid messageKind = iota
	kindRequest
	kindNotification
	kindResponse
)

// kind classifies a decoded message. A request has a method and an id, a
// notification a method and no id, a response an id and no method.
func (m *message) kind() messageKind {
	hasID := len(m.ID) > 0 && !bytes.Equal(m.ID, []byte("null"))
	switch {
	case m.Method != "" && hasID:
		return kindRequest
	case m.Method != "":
		return kindNotification
	case hasID && (m.Result != nil || m.Error != nil):
		return kindResponse
	default:
		return kindInvalid
	}
}

// RPCError is the error member of a JSON-RPC response, returned to callers
// as a Go error so a server's refusal reads the way the server spelled it.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("%s (JSON-RPC error %d)", e.Message, e.Code)
}

// IsModern reports whether the code is one the 2026-07-28 revision defines.
// The backward compatibility rules key era detection on exactly these: any
// other error from a probe means a legacy server.
func (e *RPCError) IsModern() bool {
	switch e.Code {
	case CodeHeaderMismatch, CodeMissingRequiredClientCapability, CodeUnsupportedProtocolVersion:
		return true
	}
	return false
}

// SupportedVersions reads the versions an UnsupportedProtocolVersion error
// lists in its data.
func (e *RPCError) SupportedVersions() []string {
	if len(e.Data) == 0 {
		return nil
	}
	var data struct {
		Supported []string `json:"supported"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil {
		return nil
	}
	return data.Supported
}

func newRequest(id int64, method string, params interface{}) (*message, error) {
	msg := &message{
		JSONRPC: "2.0",
		ID:      json.RawMessage(strconv.FormatInt(id, 10)),
		Method:  method,
	}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("encode %s params: %w", method, err)
		}
		msg.Params = encoded
	}
	return msg, nil
}

func newNotification(method string, params interface{}) (*message, error) {
	msg := &message{JSONRPC: "2.0", Method: method}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("encode %s params: %w", method, err)
		}
		msg.Params = encoded
	}
	return msg, nil
}

func newResultResponse(id json.RawMessage, result interface{}) (*message, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	return &message{JSONRPC: "2.0", ID: id, Result: encoded}, nil
}

func newErrorResponse(id json.RawMessage, code int, text string) *message {
	return &message{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: text}}
}

// idKey is the map key a request id is matched by. Ids are compacted so a
// server that reformats them still matches; a string id and a number id
// with the same digits stay distinct, as JSON-RPC says they are.
func idKey(raw json.RawMessage) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return string(raw)
	}
	return compact.String()
}

// encodeMessage renders one message on one line. json.Marshal escapes the
// newlines inside strings, which is what the stdio framing needs.
func encodeMessage(msg *message) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("encode message: %w", err)
	}
	return data, nil
}

func decodeMessage(data []byte) (*message, error) {
	var msg message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}
	if msg.JSONRPC != "2.0" {
		return nil, fmt.Errorf("decode message: jsonrpc is %q, want \"2.0\"", msg.JSONRPC)
	}
	if msg.kind() == kindInvalid {
		return nil, fmt.Errorf("decode message: neither a request, a notification, nor a response")
	}
	return &msg, nil
}
