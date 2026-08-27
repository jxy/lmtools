package proxy

// Responses SSE relay
//
// Clients treat a Responses stream that ends without response.completed as a
// disconnect, so every stream this proxy relays has to reach a terminal event
// one way or another. The relay watches what the client actually sees and
// closes out a truncated, oversized, or failed upstream stream with a synthetic
// response.failed rather than silence.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"math"
	"net/http"
	"strings"
)

// relayResponsesSSE forwards an upstream Responses SSE stream to the client and
// guarantees the client sees a terminal event. Returns true when at least one
// event reached the client.
func relayResponsesSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, fallbackModel string, transform func(string) string, logName string) bool {
	relay := &responsesSSERelay{ctx: ctx, w: w, transform: transform, fallbackModel: fallbackModel}
	err := relay.run(body)
	if err != nil {
		_ = handleStreamError(ctx, nil, logName+" SSE", err)
	}
	if terminateErr := relay.terminate(err); terminateErr != nil && downstreamStreamIsLive(ctx) {
		logger.From(ctx).Errorf("Failed to write terminal %s stream event: %v", logName, terminateErr)
	}
	return relay.wroteAny
}

type responsesSSERelay struct {
	ctx       context.Context
	w         http.ResponseWriter
	transform func(string) string

	wroteAny    bool
	sawTerminal bool
	// sequence is the number the next Responses event would carry, tracking the
	// numbering the client has actually been reading rather than the records the
	// relay has walked past.
	sequence      int64
	responseID    string
	model         string
	fallbackModel string
	createdAt     int64
}

func (r *responsesSSERelay) run(body io.Reader) error {
	flusher, ok := r.w.(http.Flusher)
	if !ok {
		return fmt.Errorf("ResponseWriter does not support flushing")
	}
	err := consumeSSERecords(body, func(record sseRecord) error {
		if !downstreamStreamIsLive(r.ctx) {
			return r.ctx.Err()
		}
		data := record.data()
		if r.transform != nil {
			data = r.transform(data)
		}
		// Observe what the client sees, not what the upstream sent. On the mapped
		// path the transform rewrites the model back to the client's name, and a
		// synthetic failure event has to quote that same name rather than leak the
		// upstream one.
		stopAfterWrite := r.observe(record.event(), data)
		done := strings.TrimSpace(data) == OpenAIDoneMarker
		// Clients using the shared OpenAI SSE decoder stop at [DONE]. If the
		// upstream skipped the Responses terminal event, emit the failure while it
		// is still observable rather than appending it after the sentinel once the
		// record loop returns.
		if done && !r.sawTerminal {
			if err := r.emitSyntheticFailure(fmt.Errorf("upstream stream ended without a terminal response event")); err != nil {
				return err
			}
		}
		payload := record.withData(data)
		logClientStreamBytesIfUnhandled(r.ctx, r.w, []byte(payload))
		if _, err := io.WriteString(r.w, payload); err != nil {
			return err
		}
		r.wroteAny = true
		flusher.Flush()
		if stopAfterWrite || done {
			return errStopConsumingSSERecords
		}
		return nil
	})
	if errors.Is(err, errStopConsumingSSERecords) {
		return nil
	}
	return err
}

// observe records the response identity, the numbering the client is reading,
// and whether a terminal event went by. It reports terminal events so the
// caller can stop consuming immediately after forwarding that record.
func (r *responsesSSERelay) observe(sseEvent, data string) bool {
	sseTerminal := sseEvent == "error"
	if sseTerminal {
		r.sawTerminal = true
	}
	trimmed := strings.TrimSpace(data)
	if trimmed == "" || trimmed == OpenAIDoneMarker {
		return sseTerminal
	}
	var event struct {
		Type           string `json:"type"`
		SequenceNumber *int64 `json:"sequence_number"`
		Response       *struct {
			ID        string `json:"id"`
			Model     string `json:"model"`
			CreatedAt int64  `json:"created_at"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
		return sseTerminal
	}
	r.advanceSequence(event.SequenceNumber)
	if event.Response != nil {
		if r.responseID == "" {
			r.responseID = event.Response.ID
		}
		if r.model == "" {
			r.model = event.Response.Model
		}
		if r.createdAt == 0 {
			r.createdAt = event.Response.CreatedAt
		}
	}
	switch event.Type {
	case "response.completed", "response.failed", "response.incomplete", "error":
		r.sawTerminal = true
		return true
	}
	return sseTerminal
}

// advanceSequence moves the counter past an event the client has now seen. The
// upstream's own sequence_number wins where it has one, because a synthetic
// terminal event has to continue the numbering the client has been reading
// rather than a count of its own; a stream that starts numbering anywhere but
// zero would otherwise be answered in the wrong series. Comments, heartbeats,
// [DONE], and records that did not decode are not events and do not advance it
// at all: a client watching the field for loss reads a skipped number as an
// event it never received, which is the opposite of what a terminal event is
// for.
func (r *responsesSSERelay) advanceSequence(upstream *int64) {
	seen := r.sequence
	if upstream != nil && *upstream > seen {
		seen = *upstream
	}
	// Nothing follows MaxInt64, so stop there rather than wrapping. Stopping
	// still means moving: an upstream that numbered an event MaxInt64 has left
	// the client reading a number this relay has to at least match, and keeping
	// the count from before it would number the terminal event below an event
	// the client already has.
	r.sequence = seen
	if seen < math.MaxInt64 {
		r.sequence = seen + 1
	}
}

// terminate emits a synthetic response.failed when the upstream stream started
// but never reached a terminal event.
func (r *responsesSSERelay) terminate(streamErr error) error {
	if !downstreamStreamIsLive(r.ctx) || !r.wroteAny || r.sawTerminal {
		return nil
	}
	if streamErr == nil {
		streamErr = fmt.Errorf("upstream stream ended without a terminal response event")
	}
	return r.emitSyntheticFailure(streamErr)
}

// emitSyntheticFailure writes and records the terminal event as one operation.
// Recording it here prevents the post-loop termination check from duplicating
// a failure emitted ahead of [DONE].
func (r *responsesSSERelay) emitSyntheticFailure(streamErr error) error {
	if !downstreamStreamIsLive(r.ctx) {
		return nil
	}
	logger.From(r.ctx).Warnf("Responses stream ended without a terminal event; sending synthetic response.failed: %v", streamErr)

	model := r.model
	if model == "" {
		// [DONE], comments, and heartbeats can end a stream before any parsed
		// event supplies response metadata. The caller still knows the model the
		// client sent, which is the identity every synthetic event must expose.
		model = r.fallbackModel
	}
	payload := map[string]interface{}{
		"type":            "response.failed",
		"sequence_number": r.sequence,
		"response": map[string]interface{}{
			"id":         r.responseID,
			"object":     "response",
			"created_at": r.createdAt,
			"status":     "failed",
			"model":      model,
			"output":     []interface{}{},
			"error":      responsesStreamFailurePayload(streamErr),
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var event strings.Builder
	event.WriteString("event: response.failed\n")
	writeSSEDataLines(&event, string(encoded))
	event.WriteString("\n")
	logClientStreamBytesIfUnhandled(r.ctx, r.w, []byte(event.String()))
	if _, err := io.WriteString(r.w, event.String()); err != nil {
		return err
	}
	r.wroteAny = true
	r.sawTerminal = true
	if flusher, ok := r.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
