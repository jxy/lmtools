//go:build integration

package main

import (
	"encoding/json"
	"testing"
)

// A Gemini thought signature that comes with a tool call goes back with that
// call in the tool round's request, from the session lmc saved the call in.
// The backend here sends it in extra_content alone, with a plain call ID, as
// a backend other than Argo can.
func TestCLISendsGeminiThoughtSignatureBackWithItsToolCall(t *testing.T) {
	const signature = "c2lnbmF0dXJlIGZyb20gZ2VtaW5p"
	call := `{"index":0,"id":"call_plain_1","type":"function","function":{"name":"universal_command","arguments":"{\"command\":[\"/usr/bin/printf\",\"TOOL_OK\"]}"},"extra_content":{"google":{"thought_signature":"` + signature + `"}}}`
	toolCall := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[` + call + `]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	answer := `data: {"choices":[{"index":0,"delta":{"content":"DONE_OK"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	server, requests := completionServer(t, completionReply{[]byte(toolCall), true}, completionReply{[]byte(answer), true})
	stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), false, true, "-tool", "-tool-auto-approve"), "run the tool")
	if err != nil || stdout != "DONE_OK" {
		t.Fatalf("stdout=%q stderr=%s err=%v", stdout, stderr, err)
	}
	sent := requests()
	if len(sent) != 2 {
		t.Fatalf("requests = %d, want the turn's and the tool round's", len(sent))
	}
	var round struct {
		Messages []struct {
			ToolCalls []struct {
				ID           string `json:"id"`
				ExtraContent struct {
					Google struct {
						ThoughtSignature string `json:"thought_signature"`
					} `json:"google"`
				} `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(sent[1]), &round); err != nil {
		t.Fatalf("tool round request: %v\n%s", err, sent[1])
	}
	for _, message := range round.Messages {
		for _, call := range message.ToolCalls {
			if call.ID == "call_plain_1" && call.ExtraContent.Google.ThoughtSignature == signature {
				return
			}
		}
	}
	t.Fatalf("tool round request does not send the signature back with call_plain_1:\n%s", sent[1])
}
