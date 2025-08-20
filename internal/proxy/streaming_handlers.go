package proxy

import (
	"context"
	"net/http"
	"time"
)

// StreamingHandlers contains all streaming-related methods extracted from Server
type StreamingHandlers struct {
	server *Server
}

// NewStreamingHandlers creates a new StreamingHandlers instance
func NewStreamingHandlers(server *Server) *StreamingHandlers {
	return &StreamingHandlers{server: server}
}

// HandleStreamingRequest processes a streaming request
func (h *StreamingHandlers) HandleStreamingRequest(w http.ResponseWriter, r *http.Request, anthReq *AnthropicRequest, provider, originalModel, mappedModel string) {
	h.server.handleStreamingRequest(w, r, anthReq, provider, originalModel, mappedModel)
}

// StreamFromOpenAI handles streaming from OpenAI provider
func (h *StreamingHandlers) StreamFromOpenAI(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	return h.server.streamFromOpenAI(ctx, anthReq, handler)
}

// StreamFromGoogle handles streaming from Google AI provider
func (h *StreamingHandlers) StreamFromGoogle(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	return h.server.streamFromGoogle(ctx, anthReq, handler)
}

// StreamFromArgo handles streaming from Argo provider
func (h *StreamingHandlers) StreamFromArgo(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	return h.server.streamFromArgo(ctx, anthReq, handler)
}

// StreamFromArgoWithPings handles streaming from Argo with ping keep-alive
func (h *StreamingHandlers) StreamFromArgoWithPings(ctx context.Context, req *http.Request, handler *AnthropicStreamHandler, pingInterval time.Duration) error {
	return h.server.streamFromArgoWithPings(ctx, req, handler, pingInterval)
}

// StreamFromAnthropic handles streaming from Anthropic provider
func (h *StreamingHandlers) StreamFromAnthropic(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	return h.server.streamFromAnthropic(ctx, anthReq, handler)
}

// SimulateStreamingFromArgo simulates streaming for non-streaming Argo responses
func (h *StreamingHandlers) SimulateStreamingFromArgo(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	return h.server.simulateStreamingFromArgo(ctx, anthReq, handler)
}

// WaitForArgoResponseWithPings waits for Argo response with periodic pings
func (h *StreamingHandlers) WaitForArgoResponseWithPings(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) (*ArgoChatResponse, error) {
	return h.server.waitForArgoResponseWithPings(ctx, anthReq, handler, pingInterval)
}

// StreamArgoResponseContent streams the content of an Argo response
func (h *StreamingHandlers) StreamArgoResponseContent(ctx context.Context, anthResp *AnthropicResponse, handler *AnthropicStreamHandler) error {
	return h.server.streamArgoResponseContent(ctx, anthResp, handler)
}

// StreamTextBlock streams a text content block
func (h *StreamingHandlers) StreamTextBlock(content string, handler *AnthropicStreamHandler) error {
	return h.server.streamTextBlock(content, handler)
}

// StreamToolBlock streams a tool use content block
func (h *StreamingHandlers) StreamToolBlock(ctx context.Context, block AnthropicContentBlock, blockIndex int, handler *AnthropicStreamHandler) error {
	return h.server.streamToolBlock(ctx, block, blockIndex, handler)
}

// SimulateStreamingFromArgoWithInterval simulates streaming with a specific interval
func (h *StreamingHandlers) SimulateStreamingFromArgoWithInterval(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) error {
	return h.server.simulateStreamingFromArgoWithInterval(ctx, anthReq, handler, pingInterval)
}

// SplitTextForStreaming splits text into chunks for streaming
func SplitTextForStreaming(text string, targetChunkSize int) []string {
	return splitTextForStreaming(text, targetChunkSize)
}
