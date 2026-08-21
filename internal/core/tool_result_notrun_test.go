package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

// ToolResult is written to a session's .tools.json and read back on resume, so
// a field added to it has to be additive in both directions: a result stamped
// by this binary must survive the round trip, and a session written before the
// field existed must still load with every other field intact.
func TestToolResultNotRunSurvivesTheSessionRoundTrip(t *testing.T) {
	stamped := ToolResult{
		ID:     "call-1",
		Error:  "denied: not in whitelist",
		Code:   "DENIED_NOT_WHITELISTED",
		NotRun: true,
		Reason: "not in whitelist",
		Hints:  []string{"Whitelist file: /tmp/wl.txt"},
	}
	encoded, err := json.Marshal(ToolInteraction{Results: []ToolResult{stamped}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ToolInteraction
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Results) != 1 || !reflect.DeepEqual(decoded.Results[0], stamped) {
		t.Fatalf("round trip = %#v, want %#v", decoded.Results, stamped)
	}

	// A result that ran carries no key at all, so a reader that predates the
	// field sees the same bytes it always did.
	ran, err := json.Marshal(ToolResult{ID: "call-2", Output: "hi", Elapsed: 4})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(ran, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := keys["not_run"]; present {
		t.Fatalf("a result that ran serialized not_run: %s", ran)
	}

	// A session written before the field existed loads as a result that ran,
	// which is what every stored result was recorded as.
	var legacy ToolResult
	if err := json.Unmarshal([]byte(`{"tool_call_id":"old","error":"denied: blacklisted","code":"DENIED_BLACKLIST","elapsed_ms":0}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacy.NotRun {
		t.Fatalf("legacy result decoded with NotRun set: %#v", legacy)
	}
	if legacy.Error != "denied: blacklisted" || legacy.Code != "DENIED_BLACKLIST" {
		t.Fatalf("legacy result lost fields: %#v", legacy)
	}
}

// NotRun is for the operator's transcript. The model reads Error, and adding a
// display field must not change the block the provider is sent.
func TestNotRunDoesNotChangeTheModelVisibleBlock(t *testing.T) {
	result := ToolResult{ID: "call-1", Error: "denied: blacklisted", Code: "DENIED_BLACKLIST"}
	ran := ToolResultBlockFromResult(result, "universal_command")
	result.NotRun = true
	if notRun := ToolResultBlockFromResult(result, "universal_command"); !reflect.DeepEqual(notRun, ran) {
		t.Fatalf("model-visible block changed with NotRun: %#v, want %#v", notRun, ran)
	}
}
