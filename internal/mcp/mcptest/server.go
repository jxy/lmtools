// Package mcptest is a fake MCP server for tests. One Scenario drives it
// over stdio, as a subprocess the test binary re-executes itself into, and
// over HTTP, as an httptest handler; it speaks the modern era, the legacy
// era, or plays a legacy server that ignores the era probe, and records
// every message it receives so a test can assert what the client sent.
package mcptest

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/mcp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Eras a scenario can play.
const (
	// EraModern answers server/discover and refuses initialize.
	EraModern = "modern"
	// EraLegacy answers server/discover with method-not-found and wants the
	// initialize handshake.
	EraLegacy = "legacy"
	// EraSilent never answers server/discover and wants the handshake,
	// which is how the client's probe timeout is exercised.
	EraSilent = "silent"
)

// EnvScenario is the environment variable the subprocess reads its
// scenario from. RunFromEnv serves when it is set.
const EnvScenario = "LMC_MCP_FAKE_SCENARIO"

// Scenario is everything a fake server does. It is JSON so it can cross a
// process boundary in the environment.
type Scenario struct {
	Era               string                        `json:"era,omitempty"`
	SupportedVersions []string                      `json:"supportedVersions,omitempty"`
	ServerInfo        mcp.Implementation            `json:"serverInfo"`
	Instructions      string                        `json:"instructions,omitempty"`
	Tools             []mcp.Tool                    `json:"tools"`
	Results           map[string]mcp.CallToolResult `json:"results,omitempty"`
	Errors            map[string]*mcp.RPCError      `json:"errors,omitempty"`
	PageSize          int                           `json:"pageSize,omitempty"`
	// Progress is how many progress notifications precede a call result.
	Progress int `json:"progress,omitempty"`
	// ServerRequests are methods a legacy server asks the client before
	// answering a call, such as ping or roots/list.
	ServerRequests []string `json:"serverRequests,omitempty"`
	CallDelayMs    int64    `json:"callDelayMs,omitempty"`
	// CrashOnCall exits the process when a call arrives.
	CrashOnCall bool `json:"crashOnCall,omitempty"`
	// IgnoreStdinClose keeps the subprocess alive after its stdin closes,
	// and IgnoreSIGTERM past the first signal, to exercise the shutdown
	// sequence.
	IgnoreStdinClose bool     `json:"ignoreStdinClose,omitempty"`
	IgnoreSIGTERM    bool     `json:"ignoreSigterm,omitempty"`
	StderrLines      []string `json:"stderrLines,omitempty"`
	// PIDPath, for the subprocess, is a file that receives its process ID
	// once it starts.
	PIDPath string `json:"pidPath,omitempty"`
	// StallAfter, for the subprocess, is a method after whose answer the
	// server stops reading its stdin and stays alive, the way a server
	// that has stopped reading does.
	StallAfter string `json:"stallAfter,omitempty"`
	// SpawnDescendant, for the subprocess, is a file that receives the
	// process ID of a sleep the server starts in its own process group and
	// leaves running when it exits, the way a launcher such as npx can.
	// With IgnoreSIGTERM, the sleep inherits the ignored SIGTERM.
	SpawnDescendant string `json:"spawnDescendant,omitempty"`
	// RecordPath, for the subprocess, is a file that receives one JSON
	// line per message received.
	RecordPath string `json:"recordPath,omitempty"`

	// HTTP only.
	SessionIDs bool `json:"sessionIds,omitempty"`
	// ExpireSessionAfter answers 404 to the request after this many on a
	// session, once, so the client has to initialize again.
	ExpireSessionAfter int  `json:"expireSessionAfter,omitempty"`
	StreamResponses    bool `json:"streamResponses,omitempty"`
	// CheckHeaders enforces the modern routing headers.
	CheckHeaders bool `json:"checkHeaders,omitempty"`
	// AuthToken, when set, is the bearer token every request must carry.
	AuthToken string `json:"authToken,omitempty"`
	// DiscoverStatus, when set, is the HTTP status a legacy server answers
	// the probe with, and DiscoverBody the plain text body.
	DiscoverStatus int    `json:"discoverStatus,omitempty"`
	DiscoverBody   string `json:"discoverBody,omitempty"`
}

// Message is the JSON-RPC envelope as the fake sees it.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcp.RPCError   `json:"error,omitempty"`
}

// IsResponse reports whether a message answers one of the server's own
// requests.
func (m *Message) IsResponse() bool {
	return m.Method == "" && len(m.ID) > 0
}

// ParamsMap decodes the params object.
func (m *Message) ParamsMap() map[string]interface{} {
	var params map[string]interface{}
	_ = json.Unmarshal(m.Params, &params)
	return params
}

// Meta returns the _meta object of the params.
func (m *Message) Meta() map[string]interface{} {
	meta, _ := m.ParamsMap()["_meta"].(map[string]interface{})
	return meta
}

// ProtocolVersion is the version the request declares in _meta.
func (m *Message) ProtocolVersion() string {
	version, _ := m.Meta()["io.modelcontextprotocol/protocolVersion"].(string)
	return version
}

// server is the transport independent half: it decides what a message
// gets back. Per session state is the legacy initialized flag.
type server struct {
	scn Scenario

	mu          sync.Mutex
	nextID      int64
	pending     map[string]chan *Message
	cancelled   map[string]chan struct{}
	record      []*Message
	recordFile  *os.File
	initialized map[string]bool
}

func newServer(scn Scenario) *server {
	if scn.Era == "" {
		scn.Era = EraModern
	}
	if len(scn.SupportedVersions) == 0 {
		scn.SupportedVersions = []string{mcp.ProtocolVersion20260728}
	}
	if scn.ServerInfo.Name == "" {
		scn.ServerInfo = mcp.Implementation{Name: "fake", Version: "0"}
	}
	s := &server{
		scn:         scn,
		pending:     make(map[string]chan *Message),
		cancelled:   make(map[string]chan struct{}),
		initialized: make(map[string]bool),
	}
	if scn.RecordPath != "" {
		file, err := os.OpenFile(scn.RecordPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			s.recordFile = file
		}
	}
	return s
}

func (s *server) modern() bool { return s.scn.Era == EraModern }

func (s *server) recordMessage(msg *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record = append(s.record, msg)
	if s.recordFile != nil {
		if data, err := json.Marshal(msg); err == nil {
			_, _ = s.recordFile.Write(append(data, '\n'))
		}
	}
}

// Received lists every message the server has seen, in order.
func (s *server) Received() []*Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Message(nil), s.record...)
}

// deliverResponse hands a client's answer to the server request waiting
// for it.
func (s *server) deliverResponse(msg *Message) {
	s.mu.Lock()
	ch, ok := s.pending[idKey(msg.ID)]
	s.mu.Unlock()
	if ok {
		ch <- msg
	}
}

func idKey(raw json.RawMessage) string {
	return strings.TrimSpace(string(raw))
}

// reply is one message the server emits in answer to a request, in order.
// Notifications and server requests come before the final response; a
// server request carries wait, the channel its reply arrives on.
type reply struct {
	msg  *Message
	wait chan *Message
}

func (s *server) response(id json.RawMessage, result interface{}) *Message {
	encoded, _ := json.Marshal(result)
	return &Message{JSONRPC: "2.0", ID: id, Result: encoded}
}

func (s *server) errorResponse(id json.RawMessage, code int, text string, data interface{}) *Message {
	rpcErr := &mcp.RPCError{Code: code, Message: text}
	if data != nil {
		rpcErr.Data, _ = json.Marshal(data)
	}
	return &Message{JSONRPC: "2.0", ID: id, Error: rpcErr}
}

func (s *server) notification(method string, params interface{}) *Message {
	encoded, _ := json.Marshal(params)
	return &Message{JSONRPC: "2.0", Method: method, Params: encoded}
}

func (s *server) serverRequest(method string) (*Message, chan *Message) {
	s.mu.Lock()
	s.nextID++
	id := json.RawMessage(strconv.FormatInt(s.nextID, 10))
	ch := make(chan *Message, 1)
	s.pending[idKey(id)] = ch
	s.mu.Unlock()
	return &Message{JSONRPC: "2.0", ID: id, Method: method, Params: json.RawMessage(`{}`)}, ch
}

func (s *server) forgetRequest(id json.RawMessage) {
	s.mu.Lock()
	delete(s.pending, idKey(id))
	s.mu.Unlock()
}

// handleNotification records client notifications. A cancellation wakes
// the call it names.
func (s *server) handleNotification(msg *Message) {
	if msg.Method != "notifications/cancelled" {
		return
	}
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(msg.Params, &params) != nil {
		return
	}
	s.mu.Lock()
	ch, ok := s.cancelled[idKey(params.RequestID)]
	s.mu.Unlock()
	if ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// handleRequest computes the replies to one request. session names the
// legacy session the request belongs to; the stdio transport uses one.
// A nil final message means the server chose not to answer.
func (s *server) handleRequest(msg *Message, session string) []reply {
	if s.modern() {
		return s.handleModern(msg)
	}
	return s.handleLegacy(msg, session)
}

func (s *server) handleModern(msg *Message) []reply {
	if msg.Method == "initialize" {
		return single(s.errorResponse(msg.ID, mcp.CodeMethodNotFound,
			"initialize is not a method of this server; it speaks protocol version "+s.scn.SupportedVersions[0], nil))
	}
	version := msg.ProtocolVersion()
	if version == "" {
		return single(s.errorResponse(msg.ID, mcp.CodeInvalidParams, "_meta.io.modelcontextprotocol/protocolVersion is required", nil))
	}
	if !contains(s.scn.SupportedVersions, version) {
		return single(s.errorResponse(msg.ID, mcp.CodeUnsupportedProtocolVersion, "Unsupported protocol version",
			map[string]interface{}{"supported": s.scn.SupportedVersions, "requested": version}))
	}
	switch msg.Method {
	case "server/discover":
		return single(s.response(msg.ID, map[string]interface{}{
			"resultType":        "complete",
			"supportedVersions": s.scn.SupportedVersions,
			"capabilities":      map[string]interface{}{"tools": map[string]interface{}{}},
			"instructions":      s.scn.Instructions,
			"_meta":             map[string]interface{}{"io.modelcontextprotocol/serverInfo": s.scn.ServerInfo},
			"ttlMs":             60000,
			"cacheScope":        "public",
		}))
	case "tools/list":
		return single(s.response(msg.ID, s.toolsPage(msg, true)))
	case "tools/call":
		return s.callReplies(msg, true)
	}
	return single(s.errorResponse(msg.ID, mcp.CodeMethodNotFound, "Method not found: "+msg.Method, nil))
}

func (s *server) handleLegacy(msg *Message, session string) []reply {
	switch msg.Method {
	case "server/discover":
		if s.scn.Era == EraSilent {
			return nil
		}
		return single(s.errorResponse(msg.ID, mcp.CodeMethodNotFound, "Method not found: server/discover", nil))
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		version := params.ProtocolVersion
		if !contains(mcp.LegacyVersions, version) {
			version = mcp.LegacyVersions[0]
		}
		s.mu.Lock()
		s.initialized[session] = true
		s.mu.Unlock()
		return single(s.response(msg.ID, map[string]interface{}{
			"protocolVersion": version,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{"listChanged": false}},
			"serverInfo":      s.scn.ServerInfo,
			"instructions":    s.scn.Instructions,
		}))
	case "ping":
		return single(s.response(msg.ID, map[string]interface{}{}))
	}
	s.mu.Lock()
	initialized := s.initialized[session]
	s.mu.Unlock()
	if !initialized {
		return single(s.errorResponse(msg.ID, -32000, "Server not initialized", nil))
	}
	switch msg.Method {
	case "tools/list":
		return single(s.response(msg.ID, s.toolsPage(msg, false)))
	case "tools/call":
		return s.callReplies(msg, false)
	}
	return single(s.errorResponse(msg.ID, mcp.CodeMethodNotFound, "Method not found: "+msg.Method, nil))
}

func single(msg *Message) []reply {
	return []reply{{msg: msg}}
}

// toolsPage answers tools/list with pagination when the scenario sets a
// page size; the cursor is the index of the next tool.
func (s *server) toolsPage(msg *Message, modern bool) map[string]interface{} {
	start := 0
	if cursor, ok := msg.ParamsMap()["cursor"].(string); ok && cursor != "" {
		start, _ = strconv.Atoi(cursor)
	}
	end := len(s.scn.Tools)
	if s.scn.PageSize > 0 && start+s.scn.PageSize < end {
		end = start + s.scn.PageSize
	}
	tools := s.scn.Tools[start:end]
	if tools == nil {
		tools = []mcp.Tool{}
	}
	result := map[string]interface{}{"tools": tools}
	if end < len(s.scn.Tools) {
		result["nextCursor"] = strconv.Itoa(end)
	}
	if modern {
		result["resultType"] = "complete"
		result["ttlMs"] = 60000
		result["cacheScope"] = "public"
	}
	return result
}

// callReplies answers tools/call: progress notifications, then the
// legacy server's own requests, then the delay, then the result, unless a
// cancellation arrives during the delay.
func (s *server) callReplies(msg *Message, modern bool) []reply {
	if s.scn.CrashOnCall {
		os.Exit(3)
	}
	name, _ := msg.ParamsMap()["name"].(string)
	var replies []reply
	for i := 0; i < s.scn.Progress; i++ {
		replies = append(replies, reply{msg: s.notification("notifications/progress", map[string]interface{}{
			"progressToken": "call", "progress": i + 1, "total": s.scn.Progress,
		})})
	}
	if !modern {
		for _, method := range s.scn.ServerRequests {
			request, wait := s.serverRequest(method)
			replies = append(replies, reply{msg: request, wait: wait})
		}
	}
	if s.scn.CallDelayMs > 0 {
		cancelled := make(chan struct{}, 1)
		s.mu.Lock()
		s.cancelled[idKey(msg.ID)] = cancelled
		s.mu.Unlock()
		select {
		case <-time.After(time.Duration(s.scn.CallDelayMs) * time.Millisecond):
		case <-cancelled:
			s.mu.Lock()
			delete(s.cancelled, idKey(msg.ID))
			s.mu.Unlock()
			return replies
		}
		s.mu.Lock()
		delete(s.cancelled, idKey(msg.ID))
		s.mu.Unlock()
	}
	if rpcErr, ok := s.scn.Errors[name]; ok {
		return append(replies, reply{msg: &Message{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr}})
	}
	if !s.hasTool(name) {
		return append(replies, reply{msg: s.errorResponse(msg.ID, mcp.CodeInvalidParams, "Unknown tool: "+name, nil)})
	}
	result, ok := s.scn.Results[name]
	if !ok {
		result = mcp.CallToolResult{Content: []mcp.Content{{Type: "text", Text: "ok"}}}
	}
	if modern && result.ResultType == "" {
		result.ResultType = "complete"
	}
	if result.Content == nil {
		result.Content = []mcp.Content{}
	}
	return append(replies, reply{msg: s.response(msg.ID, result)})
}

func (s *server) hasTool(name string) bool {
	for _, tool := range s.scn.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// expectedParamHeaders computes the Mcp-Param headers a modern call must
// carry, from the x-mcp-header annotations of the named tool's schema, so
// the HTTP handler can check the client's.
func (s *server) expectedParamHeaders(msg *Message) map[string]string {
	name, _ := msg.ParamsMap()["name"].(string)
	args, _ := msg.ParamsMap()["arguments"].(map[string]interface{})
	expected := make(map[string]string)
	for _, tool := range s.scn.Tools {
		if tool.Name != name {
			continue
		}
		var schema map[string]interface{}
		_ = json.Unmarshal(tool.InputSchema, &schema)
		properties, _ := schema["properties"].(map[string]interface{})
		for property, raw := range properties {
			node, _ := raw.(map[string]interface{})
			header, _ := node["x-mcp-header"].(string)
			if header == "" {
				continue
			}
			value, present := args[property]
			if !present || value == nil {
				continue
			}
			expected[strings.ToLower("Mcp-Param-"+header)] = mcp.EncodeHeaderValue(fmt.Sprint(value))
		}
	}
	return expected
}
