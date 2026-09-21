package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Transport names a server's binding.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// ServerConfig is one server from an mcpServers file after expansion and
// validation, plus what the run's flags say about it.
type ServerConfig struct {
	Name           string
	Type           string
	Command        string
	Args           []string
	Env            map[string]string
	Cwd            string
	URL            string
	Headers        map[string]string
	Timeout        time.Duration
	StartupTimeout time.Duration
	IncludeTools   []string
	ExcludeTools   []string
	Optional       bool
	// Selection is the run's narrowing of the server beyond its file. The
	// loader leaves it zero; the flag layer fills it in.
	Selection ToolSelection
}

// ToolSelection is what -tool-include and -tool-exclude say about one
// server's tools: Include keeps the named tools alone, empty keeping every
// one, and Exclude drops the named tools. Both apply after the file's
// includeTools and excludeTools.
type ToolSelection struct {
	Include []string
	Exclude []string
}

// Config is what a set of files configures: the servers, sorted by name so
// the tool list is the same on every run, and the warnings the loader
// noted on the way.
type Config struct {
	Servers  []ServerConfig
	Warnings []string
}

// serverFields are the keys a server object may carry. Anything else is
// reported and ignored so a file shared with another harness still loads.
var serverFields = map[string]bool{
	"type": true, "command": true, "args": true, "env": true, "cwd": true,
	"url": true, "httpUrl": true, "headers": true, "timeout": true, "startupTimeout": true,
	"includeTools": true, "excludeTools": true, "optional": true,
}

// LookupEnv answers variable references in a file; os.LookupEnv in
// production, a map in tests.
type LookupEnv func(name string) (string, bool)

// LoadConfigFiles reads every file and merges them. A server named in two
// files is an error rather than a silent override.
func LoadConfigFiles(paths []string, lookup LookupEnv) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var merged Config
	sources := make(map[string]string)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read %s: %w", path, err)
		}
		cfg, err := ParseConfig(data, lookup)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		for _, server := range cfg.Servers {
			if earlier, dup := sources[server.Name]; dup {
				return Config{}, fmt.Errorf("%s: server %q is already defined in %s", path, server.Name, earlier)
			}
			sources[server.Name] = path
		}
		merged.Servers = append(merged.Servers, cfg.Servers...)
		for _, warning := range cfg.Warnings {
			merged.Warnings = append(merged.Warnings, path+": "+warning)
		}
	}
	sort.Slice(merged.Servers, func(i, j int) bool { return merged.Servers[i].Name < merged.Servers[j].Name })
	return merged, nil
}

// ParseConfig reads one file's contents: an object with an mcpServers
// member mapping names to server objects. Other top level members are
// ignored without a word, because Gemini CLI keeps its mcpServers inside a
// settings file full of them.
func ParseConfig(data []byte, lookup LookupEnv) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var file struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return Config{}, fmt.Errorf("not a JSON object with mcpServers: %w", err)
	}
	if file.Servers == nil {
		return Config{}, fmt.Errorf("no mcpServers member")
	}
	names := make([]string, 0, len(file.Servers))
	for name := range file.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var cfg Config
	for _, name := range names {
		server, warnings, err := parseServer(name, file.Servers[name], lookup)
		if err != nil {
			return Config{}, fmt.Errorf("server %q: %w", name, err)
		}
		cfg.Servers = append(cfg.Servers, server)
		cfg.Warnings = append(cfg.Warnings, warnings...)
	}
	return cfg, nil
}

// serverJSON is the on-disk shape of one server. Durations are integers of
// milliseconds, the unit every harness uses.
type serverJSON struct {
	Type           string            `json:"type"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	Cwd            string            `json:"cwd"`
	URL            string            `json:"url"`
	HTTPURL        string            `json:"httpUrl"`
	Headers        map[string]string `json:"headers"`
	Timeout        *int64            `json:"timeout"`
	StartupTimeout *int64            `json:"startupTimeout"`
	IncludeTools   []string          `json:"includeTools"`
	ExcludeTools   []string          `json:"excludeTools"`
	Optional       bool              `json:"optional"`
}

func parseServer(name string, raw json.RawMessage, lookup LookupEnv) (ServerConfig, []string, error) {
	if err := validateServerName(name); err != nil {
		return ServerConfig{}, nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ServerConfig{}, nil, fmt.Errorf("not a JSON object: %w", err)
	}
	var warnings []string
	unknown := make([]string, 0)
	for key := range fields {
		if !serverFields[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		warnings = append(warnings, fmt.Sprintf("server %q: ignoring unknown field %q", name, key))
	}

	var object serverJSON
	if err := json.Unmarshal(raw, &object); err != nil {
		return ServerConfig{}, nil, fmt.Errorf("field has the wrong type: %w", err)
	}

	expand := func(field, value string) (string, error) {
		expanded, err := ExpandEnv(value, lookup)
		if err != nil {
			return "", fmt.Errorf("%s: %w", field, err)
		}
		return expanded, nil
	}

	server := ServerConfig{Name: name, Optional: object.Optional}
	var err error
	if server.Command, err = expand("command", object.Command); err != nil {
		return ServerConfig{}, nil, err
	}
	for i, arg := range object.Args {
		expanded, err := expand(fmt.Sprintf("args[%d]", i), arg)
		if err != nil {
			return ServerConfig{}, nil, err
		}
		server.Args = append(server.Args, expanded)
	}
	if len(object.Env) > 0 {
		server.Env = make(map[string]string, len(object.Env))
		for key, value := range object.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") {
				return ServerConfig{}, nil, fmt.Errorf("env: invalid variable name %q", key)
			}
			if server.Env[key], err = expand("env."+key, value); err != nil {
				return ServerConfig{}, nil, err
			}
		}
	}
	if server.Cwd, err = expand("cwd", object.Cwd); err != nil {
		return ServerConfig{}, nil, err
	}
	rawURL := object.URL
	if rawURL == "" {
		rawURL = object.HTTPURL
	}
	if server.URL, err = expand("url", rawURL); err != nil {
		return ServerConfig{}, nil, err
	}
	if len(object.Headers) > 0 {
		server.Headers = make(map[string]string, len(object.Headers))
		for key, value := range object.Headers {
			if !isToken(key) {
				return ServerConfig{}, nil, fmt.Errorf("headers: invalid header name %q", key)
			}
			if server.Headers[key], err = expand("headers."+key, value); err != nil {
				return ServerConfig{}, nil, err
			}
		}
	}
	if server.Timeout, err = millis("timeout", object.Timeout); err != nil {
		return ServerConfig{}, nil, err
	}
	if server.StartupTimeout, err = millis("startupTimeout", object.StartupTimeout); err != nil {
		return ServerConfig{}, nil, err
	}
	server.IncludeTools = object.IncludeTools
	server.ExcludeTools = object.ExcludeTools

	if server.Type, err = resolveTransport(object.Type, server.Command, server.URL); err != nil {
		return ServerConfig{}, nil, err
	}
	if server.Type == TransportHTTP {
		parsed, err := url.Parse(server.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return ServerConfig{}, nil, fmt.Errorf("url %q is not an http or https URL", server.URL)
		}
	}
	return server, warnings, nil
}

func validateServerName(name string) error {
	if name == "" {
		return fmt.Errorf("empty server name")
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("server name %q contains whitespace or a control character", name)
		}
	}
	return nil
}

// resolveTransport settles the binding from the type field or, when it is
// absent, from which of command and url the server carries. The old
// HTTP+SSE transport is refused by name: it is deprecated, and a server
// that only speaks it cannot be reached by this client.
func resolveTransport(typ, command, rawURL string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "", "stdio", "http", "streamable-http", "streamable_http", "streamablehttp":
	case "sse":
		return "", fmt.Errorf("type \"sse\" is the deprecated HTTP+SSE transport, which this client does not speak; use a Streamable HTTP endpoint")
	default:
		return "", fmt.Errorf("unknown type %q: use \"stdio\" or \"http\"", typ)
	}
	wantsStdio := strings.EqualFold(strings.TrimSpace(typ), "stdio")
	wantsHTTP := typ != "" && !wantsStdio
	switch {
	case command != "" && rawURL != "":
		return "", fmt.Errorf("both command and url are set; a server is either a stdio subprocess or an HTTP endpoint")
	case command != "":
		if wantsHTTP {
			return "", fmt.Errorf("type %q needs url, but command is set", typ)
		}
		return TransportStdio, nil
	case rawURL != "":
		if wantsStdio {
			return "", fmt.Errorf("type \"stdio\" needs command, but url is set")
		}
		return TransportHTTP, nil
	default:
		return "", fmt.Errorf("neither command nor url is set")
	}
}

func millis(field string, value *int64) (time.Duration, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 {
		return 0, fmt.Errorf("%s must not be negative", field)
	}
	return time.Duration(*value) * time.Millisecond, nil
}

// ExpandEnv replaces ${VAR} and ${VAR:-default} references. A variable that
// is unset and has no default is an error, because an empty Authorization
// header or an empty argument fails later in a way that names nothing.
func ExpandEnv(value string, lookup LookupEnv) (string, error) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	var out strings.Builder
	rest := value
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			out.WriteString(rest)
			return out.String(), nil
		}
		out.WriteString(rest[:start])
		end := strings.Index(rest[start:], "}")
		if end < 0 {
			return "", fmt.Errorf("unterminated variable reference in %q", value)
		}
		ref := rest[start+2 : start+end]
		rest = rest[start+end+1:]

		name, fallback, hasFallback := strings.Cut(ref, ":-")
		if name == "" || strings.ContainsAny(name, " \t") {
			return "", fmt.Errorf("invalid variable reference ${%s}", ref)
		}
		if resolved, ok := lookup(name); ok {
			out.WriteString(resolved)
			continue
		}
		if !hasFallback {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		out.WriteString(fallback)
	}
}
