package mcptest

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/mcp"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Recorded is one HTTP request the handler saw.
type Recorded struct {
	Method  string
	Headers http.Header
	Message *Message
}

// Handler is the Streamable HTTP binding of the fake server.
type Handler struct {
	s *server

	mu       sync.Mutex
	requests []Recorded
	sessions map[string]int
	expired  bool
	deletes  int
}

// NewHandler builds a handler for one scenario.
func NewHandler(scn Scenario) *Handler {
	return &Handler{s: newServer(scn), sessions: make(map[string]int)}
}

// Requests lists every HTTP request in order.
func (h *Handler) Requests() []Recorded {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Recorded(nil), h.requests...)
}

// Messages lists every JSON-RPC message received, in order.
func (h *Handler) Messages() []*Message {
	return h.s.Received()
}

// Deletes counts the session terminations received.
func (h *Handler) Deletes() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deletes
}

func (h *Handler) record(r *http.Request, msg *Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, Recorded{Method: r.Method, Headers: r.Header.Clone(), Message: msg})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scn := h.s.scn
	if scn.AuthToken != "" && r.Header.Get("Authorization") != "Bearer "+scn.AuthToken {
		h.record(r, nil)
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://example.test/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		h.record(r, nil)
		if !scn.SessionIDs {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		h.mu.Lock()
		h.deletes++
		delete(h.sessions, r.Header.Get("Mcp-Session-Id"))
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		h.record(r, nil)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		h.record(r, nil)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.record(r, &msg)
	h.s.recordMessage(&msg)

	switch {
	case msg.IsResponse():
		h.s.deliverResponse(&msg)
		w.WriteHeader(http.StatusAccepted)
		return
	case len(msg.ID) == 0:
		h.s.handleNotification(&msg)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if h.s.modern() {
		if rpcErr := h.checkModernHeaders(r, &msg); rpcErr != nil {
			h.writeJSON(w, http.StatusBadRequest, &Message{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr})
			return
		}
		if msg.Method == "initialize" {
			h.writeJSON(w, http.StatusNotFound, h.s.handleModern(&msg)[0].msg)
			return
		}
		replies := h.s.handleModern(&msg)
		if msg.ProtocolVersion() != "" && !contains(scn.SupportedVersions, msg.ProtocolVersion()) {
			h.writeJSON(w, http.StatusBadRequest, replies[0].msg)
			return
		}
		h.writeReplies(w, replies)
		return
	}

	// Legacy era.
	session := ""
	if msg.Method == "server/discover" && scn.DiscoverStatus != 0 {
		w.WriteHeader(scn.DiscoverStatus)
		_, _ = io.WriteString(w, scn.DiscoverBody)
		return
	}
	if scn.SessionIDs {
		if msg.Method == "initialize" {
			session = newSessionID()
			h.mu.Lock()
			h.sessions[session] = 0
			h.mu.Unlock()
			w.Header().Set("Mcp-Session-Id", session)
		} else {
			session = r.Header.Get("Mcp-Session-Id")
			if session == "" && msg.Method != "server/discover" {
				h.writeJSON(w, http.StatusBadRequest, h.s.errorResponse(msg.ID, -32000, "Mcp-Session-Id is required", nil))
				return
			}
			h.mu.Lock()
			count, known := h.sessions[session]
			if known {
				h.sessions[session] = count + 1
				if scn.ExpireSessionAfter > 0 && count+1 > scn.ExpireSessionAfter && !h.expired {
					h.expired = true
					delete(h.sessions, session)
					known = false
				}
			}
			h.mu.Unlock()
			if !known && msg.Method != "server/discover" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
	}
	replies := h.s.handleLegacy(&msg, session)
	if len(replies) == 0 {
		// The silent scenario: hold the request open until the client gives
		// up, the way a server that never answers would.
		<-r.Context().Done()
		return
	}
	if replies[0].msg.Error != nil && replies[0].msg.Error.Code == mcp.CodeMethodNotFound && msg.Method == "server/discover" {
		// A legacy server answers an unknown method with a JSON-RPC error in
		// a 200 body, which is not a modern error and so means legacy.
		h.writeJSON(w, http.StatusOK, replies[0].msg)
		return
	}
	h.writeReplies(w, replies)
}

// checkModernHeaders enforces the routing headers when the scenario asks.
func (h *Handler) checkModernHeaders(r *http.Request, msg *Message) *mcp.RPCError {
	if !h.s.scn.CheckHeaders {
		return nil
	}
	mismatch := func(text string) *mcp.RPCError {
		return &mcp.RPCError{Code: mcp.CodeHeaderMismatch, Message: "Header mismatch: " + text}
	}
	if got := r.Header.Get("MCP-Protocol-Version"); got != msg.ProtocolVersion() {
		return mismatch(fmt.Sprintf("MCP-Protocol-Version %q does not match body %q", got, msg.ProtocolVersion()))
	}
	if got := r.Header.Get("Mcp-Method"); got != msg.Method {
		return mismatch(fmt.Sprintf("Mcp-Method %q does not match body %q", got, msg.Method))
	}
	if msg.Method == "tools/call" {
		name, _ := msg.ParamsMap()["name"].(string)
		if got := decodeHeaderValue(r.Header.Get("Mcp-Name")); got != name {
			return mismatch(fmt.Sprintf("Mcp-Name %q does not match body %q", got, name))
		}
		for header, want := range h.s.expectedParamHeaders(msg) {
			if got := r.Header.Get(header); got != want {
				return mismatch(fmt.Sprintf("%s is %q, want %q", header, got, want))
			}
		}
	}
	return nil
}

func decodeHeaderValue(value string) string {
	if strings.HasPrefix(value, "=?base64?") && strings.HasSuffix(value, "?=") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(value, "=?base64?"), "?="))
		if err == nil {
			return string(decoded)
		}
	}
	return value
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, msg *Message) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(msg)
}

// writeReplies answers a request with one JSON object when the reply is a
// single response, and with an SSE stream when notifications or server
// requests precede it or the scenario streams everything.
func (h *Handler) writeReplies(w http.ResponseWriter, replies []reply) {
	if len(replies) == 1 && replies[0].wait == nil && !h.s.scn.StreamResponses {
		h.writeJSON(w, http.StatusOK, replies[0].msg)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = io.WriteString(w, ": keep-alive\n\n")
	flush()
	for _, r := range replies {
		data, _ := json.Marshal(r.msg)
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flush()
		if r.wait != nil {
			select {
			case <-r.wait:
			case <-time.After(5 * time.Second):
			}
			h.s.forgetRequest(r.msg.ID)
		}
	}
}

func newSessionID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}
