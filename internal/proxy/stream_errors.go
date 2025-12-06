package proxy

// Parse-level Streaming Error Handling
//
// This file handles errors during stream parsing and processing.
//
// Use this file for:
//   - Classifying errors during stream parsing (recoverable vs fatal)
//   - Handling JSON syntax/type errors in streaming responses
//   - Notifying clients of stream processing errors via StreamErrorEmitter
//
// For HTTP response formatting (sending errors to clients),
// see errors.go.
//
// For HTTP-level provider error handling (non-200 responses from upstream),
// see provider_errors.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/logger"
)

// Streaming errors use two-tier handling: HTTP-level errors (HandleStreamingError)
// and parse-level errors (handleStreamError) for recoverable vs fatal conditions.

// StreamErrorEmitter is implemented by handlers that can send error events to clients.
// Both AnthropicStreamHandler and OpenAIStreamWriter implement this interface.
type StreamErrorEmitter interface {
	SendStreamError(msg string) error
}

// handleStreamError processes errors that occur during stream parsing.
//
// Error classification:
//   - Recoverable (returns nil): io.EOF (normal termination),
//     io.ErrUnexpectedEOF (connection issues), JSON syntax/type errors
//   - Fatal (returns error): All other errors
//
// For recoverable errors, the function logs at appropriate severity and
// returns nil to allow continued processing. For fatal errors, it logs
// at ERROR level and optionally notifies the client via the emitter.
//
// Callers should NOT additionally log or wrap errors returned by this
// function - all logging is handled internally.
func handleStreamError(ctx context.Context, emitter StreamErrorEmitter, parser string, err error) error {
	log := logger.From(ctx)

	// Define what's recoverable
	switch {
	case err == io.EOF:
		return nil // EOF is normal termination

	case err == io.ErrUnexpectedEOF:
		log.Warnf("%s: unexpected EOF in stream", parser)
		return nil // Try to recover

	case isJSONSyntaxError(err):
		log.Warnf("%s: malformed JSON chunk (skipping): %v", parser, err)
		return nil // Skip bad chunk, continue

	default:
		log.Errorf("%s parse error (fatal): %v", parser, err)
		if emitter != nil {
			if sendErr := emitter.SendStreamError(fmt.Sprintf("Stream processing error: %v", err)); sendErr != nil {
				log.Errorf("%s: failed to send error event: %v", parser, sendErr)
			}
		}
		return err // Fatal error
	}
}

// isJSONSyntaxError checks if an error is a JSON syntax or type error
func isJSONSyntaxError(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr)
}
