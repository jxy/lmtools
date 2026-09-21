package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMessageKinds(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want messageKind
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"ping"}`, kindRequest},
		{`{"jsonrpc":"2.0","id":"a","method":"ping","params":{}}`, kindRequest},
		{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, kindNotification},
		{`{"jsonrpc":"2.0","id":null,"method":"x"}`, kindNotification},
		{`{"jsonrpc":"2.0","id":1,"result":{}}`, kindResponse},
		{`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"no"}}`, kindResponse},
		{`{"jsonrpc":"2.0","id":1}`, kindInvalid},
		{`{"jsonrpc":"2.0"}`, kindInvalid},
	} {
		var msg message
		if err := json.Unmarshal([]byte(tt.raw), &msg); err != nil {
			t.Fatalf("%s: %v", tt.raw, err)
		}
		if got := msg.kind(); got != tt.want {
			t.Errorf("%s: kind = %v, want %v", tt.raw, got, tt.want)
		}
	}
	if _, err := decodeMessage([]byte(`{"jsonrpc":"1.0","id":1,"method":"x"}`)); err == nil {
		t.Error("a jsonrpc 1.0 message decoded")
	}
	if _, err := decodeMessage([]byte(`{"jsonrpc":"2.0","id":1}`)); err == nil {
		t.Error("an invalid message decoded")
	}
}

func TestIDKeyAndErrors(t *testing.T) {
	if idKey(json.RawMessage(` 12 `)) != "12" || idKey(json.RawMessage(`"12"`)) != `"12"` {
		t.Fatal("idKey did not canonicalise")
	}
	if idKey(json.RawMessage(`12`)) == idKey(json.RawMessage(`"12"`)) {
		t.Fatal("a string id and a number id collided")
	}
	rpcErr := &RPCError{Code: CodeUnsupportedProtocolVersion, Message: "Unsupported", Data: json.RawMessage(`{"supported":["2026-07-28"],"requested":"x"}`)}
	if !rpcErr.IsModern() || strings.Join(rpcErr.SupportedVersions(), ",") != "2026-07-28" {
		t.Fatalf("modern error not recognised: %+v", rpcErr)
	}
	if (&RPCError{Code: CodeMethodNotFound}).IsModern() {
		t.Fatal("method not found counted as modern")
	}
	if rpcErr.Error() != "Unsupported (JSON-RPC error -32022)" {
		t.Fatalf("Error() = %q", rpcErr.Error())
	}

	req, err := newRequest(7, "tools/call", map[string]interface{}{"text": "a\nb"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := encodeMessage(req)
	if err != nil || strings.Contains(string(data), "\n") {
		t.Fatalf("encoded message %q has a newline (%v)", data, err)
	}
}

func TestReadSSE(t *testing.T) {
	input := ": keep-alive\r\n\r\nevent: message\r\ndata: {\"a\":1}\r\n\r\ndata: line one\ndata: line two\nid: 3\nretry: 100\n\ndata: tail"
	var events []sseEvent
	err := readSSE(bufio.NewReader(strings.NewReader(input)), 1<<10, func(event sseEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("readSSE() error = %v", err)
	}
	want := []sseEvent{{event: "message", data: `{"a":1}`}, {data: "line one\nline two"}, {data: "tail"}}
	if len(events) != len(want) {
		t.Fatalf("events = %+v, want %+v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
		}
	}

	stopped := 0
	err = readSSE(bufio.NewReader(strings.NewReader("data: 1\n\ndata: 2\n\n")), 1<<10, func(sseEvent) error {
		stopped++
		return errStopSSE
	})
	if err != nil || stopped != 1 {
		t.Fatalf("stop: err = %v, events seen = %d", err, stopped)
	}

	failed := errors.New("callback failed")
	err = readSSE(bufio.NewReader(strings.NewReader("data: 1\n\n")), 1<<10, func(sseEvent) error { return failed })
	if !errors.Is(err, failed) {
		t.Fatalf("callback error = %v", err)
	}

	err = readSSE(bufio.NewReader(strings.NewReader("data: "+strings.Repeat("x", 100)+"\n\n")), 32, func(sseEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exceeds 32 bytes") {
		t.Fatalf("long line error = %v", err)
	}
}

func TestReadLine(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("ab\r\ncd\nef"), 16)
	var lines []string
	for {
		line, err := readLine(reader, 1<<10)
		if err != nil {
			break
		}
		lines = append(lines, string(line))
	}
	if strings.Join(lines, "|") != "ab|cd|ef" {
		t.Fatalf("lines = %q", lines)
	}
	long := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 64)+"\n"), 16)
	line, err := readLine(long, 1<<10)
	if err != nil || len(line) != 64 {
		t.Fatalf("a line longer than the buffer: %d bytes, %v", len(line), err)
	}
}

func TestToolHeaderParams(t *testing.T) {
	for _, tt := range []struct {
		name    string
		schema  string
		want    []string
		wantErr string
	}{
		{name: "none", schema: `{"type":"object","properties":{"a":{"type":"string"}}}`},
		{name: "top level", schema: `{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"},"n":{"type":"integer","x-mcp-header":"N"},"b":{"type":"boolean","x-mcp-header":"B"}}}`, want: []string{"B:b", "N:n", "Region:region"}},
		{name: "nested properties", schema: `{"type":"object","properties":{"outer":{"type":"object","properties":{"inner":{"type":"string","x-mcp-header":"Inner"}}}}}`, want: []string{"Inner:outer.inner"}},
		{name: "under items", schema: `{"type":"object","properties":{"list":{"type":"array","items":{"type":"string","x-mcp-header":"Item"}}}}`, wantErr: "not on a property reachable"},
		{name: "under anyOf", schema: `{"type":"object","properties":{"x":{"anyOf":[{"type":"string","x-mcp-header":"X"}]}}}`, wantErr: "not on a property reachable"},
		{name: "number type", schema: `{"type":"object","properties":{"x":{"type":"number","x-mcp-header":"X"}}}`, wantErr: "not a string, integer, or boolean"},
		{name: "duplicate ignoring case", schema: `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"Key"},"b":{"type":"string","x-mcp-header":"key"}}}`, wantErr: "used twice"},
		{name: "bad token", schema: `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"Re gion"}}}`, wantErr: "not a valid header name"},
		{name: "empty", schema: `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":""}}}`, wantErr: "non-empty string"},
		{name: "not an object", schema: `[1]`, wantErr: "not an object"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			params, err := toolHeaderParams(json.RawMessage(tt.schema))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			var got []string
			for _, param := range params {
				got = append(got, param.name+":"+strings.Join(param.path, "."))
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("params = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHeaderValuesAndEncoding(t *testing.T) {
	params := []headerParam{{name: "Region", path: []string{"region"}}, {name: "N", path: []string{"n"}}, {name: "B", path: []string{"b"}}, {name: "Deep", path: []string{"outer", "inner"}}}
	values, err := headerValuesFor(params, json.RawMessage(`{"region":"us","n":42,"b":false,"outer":{"inner":" padded "},"skip":null}`))
	if err != nil {
		t.Fatalf("headerValuesFor() error = %v", err)
	}
	got := map[string]string{}
	for _, value := range values {
		got[value.Name] = value.Value
	}
	want := map[string]string{"Mcp-Param-Region": "us", "Mcp-Param-N": "42", "Mcp-Param-B": "false", "Mcp-Param-Deep": "=?base64?IHBhZGRlZCA=?="}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("values = %v", got)
	}

	values, err = headerValuesFor(params, json.RawMessage(`{"n":null}`))
	if err != nil || len(values) != 0 {
		t.Fatalf("null and absent arguments produced %v, %v", values, err)
	}
	if _, err := headerValuesFor(params, json.RawMessage(`{"n":1.5}`)); err == nil {
		t.Fatal("a fractional integer was accepted")
	}
	if _, err := headerValuesFor(params, json.RawMessage(`{"region":["x"]}`)); err == nil {
		t.Fatal("an array was accepted for a string header")
	}

	for value, want := range map[string]string{
		"us-west1":           "us-west1",
		"Hello, 世界":          "=?base64?SGVsbG8sIOS4lueVjA==?=",
		" padded ":           "=?base64?IHBhZGRlZCA=?=",
		"line1\nline2":       "=?base64?bGluZTEKbGluZTI=?=",
		"=?base64?literal?=": "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?=",
		"":                   "",
	} {
		if got := EncodeHeaderValue(value); got != want {
			t.Errorf("EncodeHeaderValue(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestQualifiedName(t *testing.T) {
	if got := QualifiedName("github", "list_issues"); got != "mcp__github__list_issues" {
		t.Fatalf("plain name = %s", got)
	}
	if got := QualifiedName("my.server", "admin.tools.list"); got != "mcp__my_server__admin_tools_list" {
		t.Fatalf("dotted name = %s", got)
	}
	long := QualifiedName("server", strings.Repeat("tool", 30))
	if len(long) != MaxToolNameLength || !strings.HasPrefix(long, "mcp__server__tooltool") {
		t.Fatalf("long name = %s (%d)", long, len(long))
	}
	if long != QualifiedName("server", strings.Repeat("tool", 30)) {
		t.Fatal("long names are not stable")
	}
	other := QualifiedName("server", strings.Repeat("tool", 30)+"x")
	if other == long {
		t.Fatal("two long names collided")
	}
	for _, name := range []string{long, other} {
		for _, r := range name {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
				t.Fatalf("name %s carries %q", name, r)
			}
		}
	}
}

func TestSchemaValidation(t *testing.T) {
	if err := validateToolSchema(json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"#/$defs/x"}}}`)); err != nil {
		t.Fatalf("local ref rejected: %v", err)
	}
	if err := validateToolSchema(json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"https://example.test/x.json"}}}`)); err == nil {
		t.Fatal("remote ref accepted")
	}
	if err := validateToolSchema(json.RawMessage(`{"type":"object","properties":{"x":{"anyOf":[{"$ref":"file:///etc/passwd"}]}}}`)); err == nil {
		t.Fatal("nested remote ref accepted")
	}
	if err := validateToolSchema(json.RawMessage(`"string"`)); err == nil {
		t.Fatal("a non-object schema accepted")
	}
	if err := validateToolSchema(nil); err != nil {
		t.Fatalf("an absent schema rejected: %v", err)
	}
}

// serverFields is the list the loader warns against and serverJSON the
// shape it decodes; a field in one and not the other is either silently
// ignored or warned about while being read.
func TestKnownServerFieldsMatchTheFileShape(t *testing.T) {
	typ := reflect.TypeOf(serverJSON{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if !serverFields[name] {
			t.Errorf("serverJSON decodes %q but serverFields does not know it", name)
		}
	}
	if len(serverFields) != typ.NumField() {
		t.Errorf("serverFields has %d names, serverJSON %d fields", len(serverFields), typ.NumField())
	}
}
