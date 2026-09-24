package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultClientInfo is how lmc introduces itself to a server.
var DefaultClientInfo = Implementation{Name: "lmc", Version: "1.0"}

// DefaultStartupTimeout bounds the launch, the era probe, the handshake, and
// the first tool listing of one server.
const DefaultStartupTimeout = 30 * time.Second

// maxToolListPages bounds tools/list pagination against a server that never
// stops handing out cursors.
const maxToolListPages = 100

type era int

const (
	eraUnknown era = iota
	eraModern
	eraLegacy
)

func (e era) String() string {
	switch e {
	case eraModern:
		return "modern"
	case eraLegacy:
		return "legacy"
	default:
		return "unknown"
	}
}

// DialOptions configure a connection.
type DialOptions struct {
	ClientInfo Implementation
	// Log receives debug lines. Warn receives what the operator should
	// see: a stdio server that exited on its own, and cleanup its shutdown
	// could not confirm. A nil Warn sends those to Log.
	Log            Logf
	Warn           Logf
	HTTPClient     *http.Client
	StartupTimeout time.Duration
}

// Client is one connected server: its transport, the era and protocol
// version negotiated with it, and what it said about itself.
type Client struct {
	cfg        ServerConfig
	clientInfo Implementation
	log        Logf
	warn       Logf
	transport  transport
	nextID     atomic.Int64

	mu           sync.Mutex
	era          era
	version      string
	serverInfo   Implementation
	instructions string
	// dialed is set once Dial succeeds, and exited once the transport
	// reports an exit the client did not cause, with exitErr its status.
	dialed  bool
	exited  bool
	exitErr error
}

// Dial starts or connects to one server and settles which era it speaks.
func Dial(ctx context.Context, cfg ServerConfig, opts DialOptions) (*Client, error) {
	warn := opts.Warn
	if warn == nil {
		warn = opts.Log
	}
	c := &Client{cfg: cfg, clientInfo: opts.ClientInfo, log: serverLogf(opts.Log, cfg.Name), warn: serverLogf(warn, cfg.Name)}
	if c.clientInfo.Name == "" {
		c.clientInfo = DefaultClientInfo
	}
	handler := peerHandler{onNotification: c.onNotification, onRequest: c.onRequest, onExit: c.serverExited}

	switch cfg.Type {
	case TransportStdio:
		t, err := startStdio(cfg, handler, c.log, c.warn)
		if err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", cfg.Name, err)
		}
		c.transport = t
	case TransportHTTP:
		c.transport = newHTTPTransport(cfg, opts.HTTPClient, handler, c.log)
	default:
		return nil, fmt.Errorf("mcp server %q: unknown transport %q", cfg.Name, cfg.Type)
	}

	startup := opts.StartupTimeout
	if cfg.StartupTimeout > 0 {
		startup = cfg.StartupTimeout
	}
	if startup <= 0 {
		startup = DefaultStartupTimeout
	}
	if err := c.negotiate(ctx, startup); err != nil {
		_ = c.transport.close()
		return nil, fmt.Errorf("mcp server %q: %w", cfg.Name, err)
	}
	c.mu.Lock()
	c.dialed = true
	exited, exitErr := c.exited, c.exitErr
	c.mu.Unlock()
	if exited {
		c.reportExit(exitErr)
	}
	return c, nil
}

// serverExited hears from the transport that the server exited when the
// client had not shut it down. The exit is reported once, and only after
// Dial succeeded: an exit while dialing fails Dial, which reports it.
func (c *Client) serverExited(err error) {
	c.mu.Lock()
	c.exited, c.exitErr = true, err
	dialed := c.dialed
	c.mu.Unlock()
	if dialed {
		c.reportExit(err)
	}
}

// reportExit tells the operator that the server is gone. Its tools stay
// advertised, because the tool list and the system prompt are settled when
// the run starts, and a call to one returns an error result.
func (c *Client) reportExit(err error) {
	if err != nil {
		c.warn("server exited: %v; its tools stay listed and calls to them fail", err)
		return
	}
	c.warn("server exited; its tools stay listed and calls to them fail")
}

// Name is the configured server name.
func (c *Client) Name() string { return c.cfg.Name }

// Version is the negotiated protocol revision.
func (c *Client) Version() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Era reports "modern" or "legacy".
func (c *Client) Era() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.era.String()
}

// ServerInfo is what the server said it is; display only, as the
// specification says.
func (c *Client) ServerInfo() Implementation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverInfo
}

// Instructions are the server's guidance for the model, if it gave any.
func (c *Client) Instructions() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instructions
}

// Close releases the transport: shuts a stdio server down, ends a legacy
// HTTP session.
func (c *Client) Close() error {
	return c.transport.close()
}

// negotiate applies the backward compatibility rules of the transport
// pages. The probe is a modern server/discover. A DiscoverResult or a
// modern error means a modern server, whose version is then settled from
// what it lists; anything else, including silence, means a legacy server,
// which gets the initialize handshake. The fallback is not keyed to one
// error code, because legacy servers answer an unknown method however they
// like. The probe and the handshake each get the startup timeout, since a
// legacy server that never answers the probe has spent the first one.
func (c *Client) negotiate(ctx context.Context, startup time.Duration) error {
	c.setEra(eraModern, ModernVersions[0])
	probeCtx, cancelProbe := context.WithTimeout(ctx, startup)
	raw, err := c.roundTrip(probeCtx, "server/discover", nil, requestOptions{protocolVersion: ModernVersions[0], modern: true})
	cancelProbe()
	if err == nil {
		var result DiscoverResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return fmt.Errorf("server/discover result: %w", err)
		}
		version, ok := newestCommon(ModernVersions, result.SupportedVersions)
		if !ok {
			return fmt.Errorf("server supports protocol versions %v; this client speaks %v", result.SupportedVersions, ModernVersions)
		}
		c.mu.Lock()
		c.version = version
		c.instructions = result.Instructions
		if info, ok := result.Meta[metaServerInfo]; ok {
			_ = json.Unmarshal(info, &c.serverInfo)
		}
		c.mu.Unlock()
		c.log("modern server, protocol %s", version)
		return nil
	}

	if rpcErr := modernError(err); rpcErr != nil {
		if rpcErr.Code != CodeUnsupportedProtocolVersion {
			return fmt.Errorf("server/discover: %w", rpcErr)
		}
		version, ok := newestCommon(ModernVersions, rpcErr.SupportedVersions())
		if !ok {
			return fmt.Errorf("server supports protocol versions %v; this client speaks %v", rpcErr.SupportedVersions(), ModernVersions)
		}
		c.setEra(eraModern, version)
		c.log("modern server, protocol %s after version retry", version)
		return nil
	}

	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.Status == http.StatusUnauthorized {
		return statusErr
	}
	if ctx.Err() != nil {
		return fmt.Errorf("server did not answer the era probe: %w", ctx.Err())
	}
	c.log("legacy server: probe answered %v", err)
	initCtx, cancelInit := context.WithTimeout(ctx, startup)
	defer cancelInit()
	return c.initialize(initCtx)
}

// modernError returns the JSON-RPC error inside err when it is one of the
// codes only a modern server sends, whether it arrived in a 200 body or in
// the body of a 4xx.
func modernError(err error) *RPCError {
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) && rpcErr.IsModern() {
		return rpcErr
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.RPC != nil && statusErr.RPC.IsModern() {
		return statusErr.RPC
	}
	return nil
}

// initialize is the legacy handshake: initialize, then the initialized
// notification once the server has answered with a version this client
// speaks.
func (c *Client) initialize(ctx context.Context) error {
	c.setEra(eraLegacy, "")
	params := map[string]interface{}{
		"protocolVersion": LegacyVersions[0],
		"capabilities":    map[string]interface{}{},
		"clientInfo":      c.clientInfo,
	}
	raw, err := c.roundTrip(ctx, "initialize", params, requestOptions{})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var result InitializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("initialize result: %w", err)
	}
	if !contains(LegacyVersions, result.ProtocolVersion) {
		return fmt.Errorf("server answered initialize with protocol version %q; this client speaks %v", result.ProtocolVersion, LegacyVersions)
	}
	c.mu.Lock()
	c.version = result.ProtocolVersion
	c.serverInfo = result.ServerInfo
	c.instructions = result.Instructions
	c.mu.Unlock()

	note, err := newNotification("notifications/initialized", nil)
	if err != nil {
		return err
	}
	if err := c.transport.notify(ctx, note, requestOptions{protocolVersion: result.ProtocolVersion}); err != nil {
		return fmt.Errorf("notifications/initialized: %w", err)
	}
	c.log("legacy server, protocol %s", result.ProtocolVersion)
	return nil
}

func (c *Client) setEra(e era, version string) {
	c.mu.Lock()
	c.era = e
	c.version = version
	c.mu.Unlock()
}

// newestCommon picks the newest version both lists hold; ours is ordered
// newest first.
func newestCommon(ours, theirs []string) (string, bool) {
	for _, version := range ours {
		if contains(theirs, version) {
			return version, true
		}
	}
	return "", false
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// ListTools fetches every page of the server's tools.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	cursor := ""
	for page := 0; page < maxToolListPages; page++ {
		params := map[string]interface{}{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params, "", nil)
		if err != nil {
			return nil, err
		}
		var result ListToolsResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("mcp server %q: tools/list result: %w", c.cfg.Name, err)
		}
		if result.ResultType != "" && result.ResultType != ResultTypeComplete {
			return nil, fmt.Errorf("mcp server %q: tools/list answered with result type %q", c.cfg.Name, result.ResultType)
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			return tools, nil
		}
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("mcp server %q: tools/list did not end after %d pages", c.cfg.Name, maxToolListPages)
}

// CallTool invokes one tool. args is the JSON object of arguments and
// headers the Mcp-Param values its schema asked for.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage, headers []headerValue) (*CallToolResult, error) {
	params := map[string]interface{}{"name": name}
	if len(args) > 0 {
		params["arguments"] = args
	} else {
		params["arguments"] = map[string]interface{}{}
	}
	raw, err := c.call(ctx, "tools/call", params, name, headers)
	if err != nil {
		return nil, err
	}
	var result CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp server %q: tools/call result: %w", c.cfg.Name, err)
	}
	if result.ResultType != "" && result.ResultType != ResultTypeComplete && result.ResultType != ResultTypeInputRequired {
		return nil, fmt.Errorf("mcp server %q: tools/call answered with result type %q", c.cfg.Name, result.ResultType)
	}
	return &result, nil
}

// call sends one request in the negotiated era: modern requests carry the
// reserved _meta fields and the routing headers, legacy requests the
// negotiated version header. A legacy HTTP session the server has expired
// is started again once and the request retried.
func (c *Client) call(ctx context.Context, method string, params map[string]interface{}, name string, headers []headerValue) (json.RawMessage, error) {
	c.mu.Lock()
	era, version := c.era, c.version
	c.mu.Unlock()

	opts := requestOptions{protocolVersion: version, modern: era == eraModern, name: name, params: headers}
	raw, err := c.roundTrip(ctx, method, params, opts)
	if err == nil {
		return raw, nil
	}
	if era == eraLegacy && sessionExpired(err) {
		c.log("session expired; initializing again")
		if initErr := c.initialize(ctx); initErr != nil {
			return nil, fmt.Errorf("mcp server %q: %w", c.cfg.Name, initErr)
		}
		c.mu.Lock()
		opts.protocolVersion = c.version
		c.mu.Unlock()
		raw, err = c.roundTrip(ctx, method, params, opts)
		if err == nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("mcp server %q: %s: %w", c.cfg.Name, method, err)
}

// sessionExpired recognises the 404 a legacy server sends for a session it
// has forgotten.
func sessionExpired(err error) bool {
	var statusErr *httpStatusError
	return errors.As(err, &statusErr) && statusErr.Status == http.StatusNotFound
}

// roundTrip builds and sends one request and returns its result, or the
// server's error as an RPCError.
func (c *Client) roundTrip(ctx context.Context, method string, params map[string]interface{}, opts requestOptions) (json.RawMessage, error) {
	if opts.modern {
		if params == nil {
			params = map[string]interface{}{}
		}
		params["_meta"] = map[string]interface{}{
			metaProtocolVersion:    opts.protocolVersion,
			metaClientInfo:         c.clientInfo,
			metaClientCapabilities: map[string]interface{}{},
		}
	}
	var body interface{}
	if params != nil {
		body = params
	}
	req, err := newRequest(c.nextID.Add(1), method, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.transport.roundTrip(ctx, req, opts)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// onNotification logs what the server volunteers. Log messages and
// progress are the two a client is likely to see; both are debug output.
func (c *Client) onNotification(msg *message) {
	switch msg.Method {
	case "notifications/message":
		var params struct {
			Level  string          `json:"level"`
			Logger string          `json:"logger"`
			Data   json.RawMessage `json:"data"`
		}
		if json.Unmarshal(msg.Params, &params) == nil {
			c.log("log %s %s: %s", params.Level, params.Logger, compactJSON(params.Data))
			return
		}
	case "notifications/progress":
		c.log("progress: %s", compactJSON(msg.Params))
		return
	}
	c.log("notification %s: %s", msg.Method, compactJSON(msg.Params))
}

// onRequest answers a legacy server's requests. A ping gets its empty
// result; roots, sampling, and elicitation are capabilities this client
// never declared, so they get the method-not-found error the specification
// reserves for them.
func (c *Client) onRequest(msg *message) *message {
	if msg.Method == "ping" {
		reply, err := newResultResponse(msg.ID, map[string]interface{}{})
		if err == nil {
			return reply
		}
	}
	c.log("refusing server request %s", msg.Method)
	return newErrorResponse(msg.ID, CodeMethodNotFound, fmt.Sprintf("lmc does not support the %s request", msg.Method))
}

func compactJSON(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 512 {
		return text[:512] + "..."
	}
	return text
}
