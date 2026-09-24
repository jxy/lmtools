package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HostOptions configure a Host.
type HostOptions struct {
	ClientInfo Implementation
	// Log receives debug lines; Warn receives what the operator should see:
	// a skipped optional server, a dropped tool. Either may be nil.
	Log  Logf
	Warn Logf
	// CallTimeout bounds a tools/call on a server that sets no timeout of
	// its own; zero leaves the caller's context in charge.
	CallTimeout time.Duration
	// StartupTimeout is the default a server's startupTimeout overrides.
	StartupTimeout time.Duration
	HTTPClient     *http.Client
}

// QualifiedTool is one advertised tool: the name the model sees, the server
// it belongs to, and the server's own definition.
type QualifiedTool struct {
	Name   string
	Server string
	Tool   Tool
	// headers are the Mcp-Param annotations the schema carried, valid by
	// construction.
	headers []headerParam
}

// ServerInstructions are one server's guidance for the model.
type ServerInstructions struct {
	Server       string
	Instructions string
}

// ServerStatus describes one connected server for display.
type ServerStatus struct {
	Name      string
	Transport string
	Protocol  string
	Era       string
	ToolCount int
}

// Host is every configured server together: it connects them, lists and
// qualifies their tools, and routes a call by the qualified name.
type Host struct {
	opts    HostOptions
	clients []*Client
	tools   []QualifiedTool
	byName  map[string]int
}

// Connect dials every server in order and lists its tools. A server that
// cannot be reached fails the whole host unless it is optional, in which
// case it is skipped with a warning; the one-shot CLI is more predictable
// when a configured server is either there or the run stops.
func Connect(ctx context.Context, servers []ServerConfig, opts HostOptions) (*Host, error) {
	h := &Host{opts: opts, byName: make(map[string]int)}
	for _, cfg := range servers {
		if err := h.connectOne(ctx, cfg); err != nil {
			if cfg.Optional {
				h.warnf("skipping optional MCP server %q: %v", cfg.Name, err)
				continue
			}
			h.Close()
			return nil, err
		}
	}
	return h, nil
}

func (h *Host) connectOne(ctx context.Context, cfg ServerConfig) error {
	client, err := Dial(ctx, cfg, DialOptions{
		ClientInfo:     h.opts.ClientInfo,
		Log:            h.opts.Log,
		Warn:           h.opts.Warn,
		HTTPClient:     h.opts.HTTPClient,
		StartupTimeout: h.opts.StartupTimeout,
	})
	if err != nil {
		return err
	}

	startup := cfg.StartupTimeout
	if startup <= 0 {
		startup = h.opts.StartupTimeout
	}
	if startup <= 0 {
		startup = DefaultStartupTimeout
	}
	listCtx, cancel := context.WithTimeout(ctx, startup)
	tools, err := client.ListTools(listCtx)
	cancel()
	if err != nil {
		_ = client.Close()
		return err
	}

	h.clients = append(h.clients, client)
	for _, tool := range filterTools(cfg, tools, h.warnf) {
		h.addTool(cfg, tool)
	}
	return nil
}

// addTool qualifies one tool and checks its schema. A tool that cannot be
// advertised is dropped with a warning naming why, so one bad definition
// does not take its server's other tools with it.
func (h *Host) addTool(cfg ServerConfig, tool Tool) {
	if tool.Name == "" {
		h.warnf("MCP server %q: dropping a tool with no name", cfg.Name)
		return
	}
	if err := validateToolSchema(tool.InputSchema); err != nil {
		h.warnf("MCP server %q: dropping tool %q: %v", cfg.Name, tool.Name, err)
		return
	}
	var headers []headerParam
	if cfg.Type == TransportHTTP {
		params, err := toolHeaderParams(tool.InputSchema)
		if err != nil {
			h.warnf("MCP server %q: dropping tool %q: %v", cfg.Name, tool.Name, err)
			return
		}
		headers = params
	}
	name := QualifiedName(cfg.Name, tool.Name)
	if existing, dup := h.byName[name]; dup {
		other := h.tools[existing]
		h.warnf("MCP server %q: dropping tool %q: its name %s collides with tool %q of server %q",
			cfg.Name, tool.Name, name, other.Tool.Name, other.Server)
		return
	}
	h.byName[name] = len(h.tools)
	h.tools = append(h.tools, QualifiedTool{Name: name, Server: cfg.Name, Tool: tool, headers: headers})
}

// filterTools applies the file's includeTools and excludeTools, then the
// run's -tool-include and -tool-exclude names for the server. A name in any
// list that the server does not offer is reported, since it is most likely
// a typo that leaves a tool exposed or hidden by accident.
func filterTools(cfg ServerConfig, tools []Tool, warnf Logf) []Tool {
	offered := make(map[string]bool, len(tools))
	for _, tool := range tools {
		offered[tool.Name] = true
	}
	kept := tools
	for _, list := range []struct {
		field   string
		names   []string
		include bool
	}{
		{"includeTools", cfg.IncludeTools, true},
		{"excludeTools", cfg.ExcludeTools, false},
		{"-tool-include", cfg.Selection.Include, true},
		{"-tool-exclude", cfg.Selection.Exclude, false},
	} {
		if len(list.names) == 0 {
			continue
		}
		named := make(map[string]bool, len(list.names))
		for _, name := range list.names {
			named[name] = true
			if !offered[name] {
				warnf("MCP server %q: %s names %q, which the server does not offer", cfg.Name, list.field, name)
			}
		}
		next := make([]Tool, 0, len(kept))
		for _, tool := range kept {
			if named[tool.Name] == list.include {
				next = append(next, tool)
			}
		}
		kept = next
	}
	return kept
}

// validateToolSchema refuses what no provider can take or the
// specification says not to follow: a schema that is not an object, and a
// $ref that points off the document, which the client must not fetch.
func validateToolSchema(schema json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	var root interface{}
	if err := json.Unmarshal(schema, &root); err != nil {
		return fmt.Errorf("input schema is not JSON: %w", err)
	}
	object, ok := root.(map[string]interface{})
	if !ok {
		return fmt.Errorf("input schema is not an object")
	}
	return walkSchema(object, nil, true, 0, func(node map[string]interface{}, _ []string, _ bool) error {
		ref, ok := node["$ref"].(string)
		if !ok {
			return nil
		}
		if !strings.HasPrefix(ref, "#") {
			return fmt.Errorf("input schema has a $ref to %q, which is outside the schema", ref)
		}
		return nil
	})
}

func (h *Host) warnf(format string, args ...interface{}) {
	if h.opts.Warn != nil {
		h.opts.Warn(format, args...)
	}
}

// Tools lists the advertised tools in server, then listing, order.
func (h *Host) Tools() []QualifiedTool {
	return append([]QualifiedTool(nil), h.tools...)
}

// Lookup finds an advertised tool by its qualified name.
func (h *Host) Lookup(name string) (QualifiedTool, bool) {
	index, ok := h.byName[name]
	if !ok {
		return QualifiedTool{}, false
	}
	return h.tools[index], true
}

// Instructions lists the servers that gave guidance, in server order.
func (h *Host) Instructions() []ServerInstructions {
	var out []ServerInstructions
	for _, client := range h.clients {
		if text := strings.TrimSpace(client.Instructions()); text != "" {
			out = append(out, ServerInstructions{Server: client.Name(), Instructions: text})
		}
	}
	return out
}

// Servers describes the connected servers.
func (h *Host) Servers() []ServerStatus {
	out := make([]ServerStatus, 0, len(h.clients))
	for _, client := range h.clients {
		count := 0
		for _, tool := range h.tools {
			if tool.Server == client.Name() {
				count++
			}
		}
		out = append(out, ServerStatus{
			Name:      client.Name(),
			Transport: client.cfg.Type,
			Protocol:  client.Version(),
			Era:       client.Era(),
			ToolCount: count,
		})
	}
	return out
}

// Call invokes an advertised tool under the server's timeout, or the host's
// default, or the caller's context alone when neither is set.
func (h *Host) Call(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	tool, ok := h.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("no MCP tool is advertised as %q", name)
	}
	client := h.client(tool.Server)
	if client == nil {
		return nil, fmt.Errorf("MCP server %q is not connected", tool.Server)
	}
	headers, err := headerValuesFor(tool.headers, args)
	if err != nil {
		return nil, fmt.Errorf("MCP tool %s: %w", name, err)
	}
	timeout := client.cfg.Timeout
	if timeout <= 0 {
		timeout = h.opts.CallTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return client.CallTool(ctx, tool.Tool.Name, args, headers)
}

func (h *Host) client(server string) *Client {
	for _, client := range h.clients {
		if client.Name() == server {
			return client
		}
	}
	return nil
}

// Close shuts every server down, all at once, since a stdio server's
// shutdown takes at least its grace after SIGTERM.
func (h *Host) Close() {
	var wg sync.WaitGroup
	for _, client := range h.clients {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			_ = client.Close()
		}(client)
	}
	wg.Wait()
	h.clients = nil
}
