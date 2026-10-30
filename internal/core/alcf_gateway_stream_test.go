package core

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestALCFGatewayErrorChunkFailsTheStream(t *testing.T) {
	for _, tt := range []struct {
		capture   string
		responses bool
		want      string
	}{
		{"openai-gateway-error.stream.txt", false, "Unknown message role: developer"},
		{"openai-responses-gateway-error.stream.txt", true, "'input_text' is not a valid ChunkTypes"},
	} {
		t.Run(tt.capture, func(t *testing.T) {
			stream := readOpenAIChatFixture(t, "response-openai-vllm-reasoning", tt.capture)
			output := &recordingResponseOutput{}
			response, err := HandleResponseWithOptions(context.Background(), RequestOptions{
				Provider: "openai", Model: "mistral-large-4-nvfp4", OpenAIResponses: tt.responses, StreamChat: true,
			}, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(stream))},
				&MockLogger{logDir: t.TempDir()}, &MockNotifier{}, ResponseParseOptions{Output: output})
			if err == nil || !strings.Contains(err.Error(), "upstream stream error: Error: Unexpected error calling stream API") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want the gateway's message", err)
			}
			if strings.Contains(err.Error(), "[ERROR]") || response.Text != "" {
				t.Fatalf("response = %+v, error = %v; the error became an answer", response, err)
			}
			for _, event := range output.events {
				if event.Type == ResponseStreamTextDelta {
					t.Fatalf("the error was shown as an answer: %q", event.Text)
				}
			}
		})
	}
}

func TestALCFGatewayErrorNeedsTheIDAndTheMarker(t *testing.T) {
	for _, data := range []string{
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"[ERROR] quoted by the model"}}]}`,
		`{"id":"chatcmpl-api-error","choices":[{"index":0,"delta":{"content":"an answer"}}]}`,
		`{"id":"chatcmpl-api-error","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"[ERROR] parts"}]}}]}`,
	} {
		if err := alcfGatewayStreamError([]byte(data)); err != nil {
			t.Fatalf("alcfGatewayStreamError(%s) = %v, want nil", data, err)
		}
	}
}

func TestOpenAIResponsesStreamWithoutResponsesEventsFails(t *testing.T) {
	for _, tt := range []struct {
		name   string
		events []string
		want   string
	}{
		{"done alone", []string{`[DONE]`}, "ended without a Responses event"},
		{"chat chunk", []string{`{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`, `[DONE]`}, "Chat Completions chunk on a Responses stream"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := NewOpenAIResponsesStreamState()
			var err error
			for _, event := range tt.events {
				if _, _, _, err = state.ParseLine("data: " + event); err != nil {
					break
				}
			}
			if !isFatalStreamError(err) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if state.IncompleteReason() == "" {
				t.Fatal("the stream counts as complete")
			}
		})
	}
}
