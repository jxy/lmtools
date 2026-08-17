package proxy

// Client Request Size Limits
//
// The proxy buffers request bodies on converted paths, so it needs an upper
// bound. That bound exists to cap memory, not to police the provider: the
// default matches OpenAI's documented 512MB payload limit so a request the
// upstream would accept is never rejected here first. Requests over the limit
// get HTTP 413 with a `request_too_large` identity, matching the contract
// Anthropic documents, plus the observed size and the flag that raises it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/limitio"
	"lmtools/internal/logger"
	"math"
	"net/http"
	"time"
)

const (
	// rejectedRequestDrainLimit bounds how much of an oversized request body the
	// proxy reads and discards after flushing the rejection. Closing a connection
	// with unread bytes still in flight can make the peer see a reset instead of
	// the 413, so a bounded prefix of the upload is absorbed first.
	rejectedRequestDrainLimit = 64 * 1024 * 1024

	// rejectedRequestDrainTimeout bounds how long that drain may take.
	rejectedRequestDrainTimeout = 5 * time.Second
)

// requestTooLargeError reports a client request body over the proxy's own limit.
type requestTooLargeError struct {
	// Limit is the configured maximum in bytes.
	Limit int64
	// Size is the body size in bytes, or -1 when the client sent no Content-Length.
	Size int64
}

func (e *requestTooLargeError) Error() string {
	var message string
	if e.Size > 0 {
		message = fmt.Sprintf("request body is %s, over the %s limit", humanByteSize(e.Size), humanByteSize(e.Limit))
	} else {
		message = fmt.Sprintf("request body exceeds the %s limit", humanByteSize(e.Limit))
		return fmt.Sprintf("%s (increase -max-request-body-size; the value is in whole MB)", message)
	}
	if suggested := suggestedRequestBodySizeMB(e.Size); suggested > 0 {
		return fmt.Sprintf("%s (raise it with -max-request-body-size %d)", message, suggested)
	}
	return fmt.Sprintf("%s (no whole-MB -max-request-body-size value can admit this length)", message)
}

func newRequestTooLargeError(limit, size int64) *requestTooLargeError {
	if size <= 0 {
		size = -1
	}
	return &requestTooLargeError{Limit: limit, Size: size}
}

// asRequestTooLarge reports whether err was caused by an oversized client body.
func asRequestTooLarge(err error) (*requestTooLargeError, bool) {
	var tooLarge *requestTooLargeError
	if errors.As(err, &tooLarge) {
		return tooLarge, true
	}
	return nil, false
}

// isBodySizeLimitError matches both size-limit sources: the proxy's own
// limitio check and net/http's MaxBytesReader in the middleware.
func isBodySizeLimitError(err error) bool {
	var sizeLimit *limitio.SizeLimitError
	var maxBytes *http.MaxBytesError
	return errors.As(err, &sizeLimit) || errors.As(err, &maxBytes)
}

// suggestedRequestBodySizeMB proposes a -max-request-body-size value that would
// admit the rejected request, rounded up to a whole MB. It returns zero when no
// int64 byte limit produced by the whole-MB flag can represent the target.
func suggestedRequestBodySizeMB(size int64) int64 {
	const mb = 1024 * 1024
	const maxFlagMB = math.MaxInt64 / mb
	const maxFlagBytes = maxFlagMB * mb

	if size <= 0 || size > maxFlagBytes {
		return 0
	}

	// Quotient-and-remainder ceiling division avoids size+mb-1 overflowing
	// for client-controlled Content-Length values near math.MaxInt64.
	suggested := size / mb
	if size%mb != 0 {
		suggested++
	}
	return suggested
}

func humanByteSize(size int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
	)
	switch {
	case size >= mb:
		return fmt.Sprintf("%.1fMB", float64(size)/mb)
	case size >= kb:
		return fmt.Sprintf("%.1fKB", float64(size)/kb)
	default:
		return fmt.Sprintf("%dB", size)
	}
}

func effectiveMaxRequestBodySize(config *Config) int64 {
	if config == nil || config.MaxRequestBodySize <= 0 {
		return constants.DefaultMaxRequestBodySize
	}
	return config.MaxRequestBodySize
}

// drainRejectedRequestBody absorbs the rest of a request the proxy refused so
// the client can read the response instead of a connection reset. Both new
// socket bytes and the wait are bounded; a client that keeps sending past either
// one gets its connection closed. Bytes net/http already buffered at the cap are
// still emitted to DEBUG before the unread tail is abandoned.
//
// The rejection goes out before the drain, not after it. A 413 is small enough
// to sit in net/http's response buffer until the handler returns, and the
// handler returns on the far side of this drain, so the order decides whether
// the client can read the answer while it is still deciding what to send: one
// already mid-upload can stop as soon as it sees the status instead of filling
// the drain, and one waiting on Expect: 100-continue is not left waiting for
// permission that was refused before this middleware ever saw the body.
//
// Writing the header is itself a read, which is why the deadline and the
// takeover both precede the flush. net/http absorbs an unread body from inside
// the header write, so a deadline installed after the flush bounds every read
// but the one that already blocked, and a drain run from in there is the one
// thing the ordering above exists to prevent — it holds the rejection back, by
// up to a stalled client's worth of waiting, and answers to a 256KB cap and no
// log rather than to the limits here. EnableFullDuplex is the standing
// declaration that this handler reads the request while it writes the response,
// which is precisely the sequence below, and it leaves this function the only
// drain. The deadline is absolute, so it caps the whole sequence rather than
// each read within it.
//
// The flush only reaches the wire because the caller has put the unwrapped body
// back first; see ProxyMiddleware.ServeHTTP for why that is not optional.
func drainRejectedRequestBody(ctx context.Context, w http.ResponseWriter, body io.ReadCloser, wireBody *wireRequestBodyLogger) {
	drainRejectedRequestBodyWithLimit(ctx, w, body, wireBody, rejectedRequestDrainLimit)
}

// drainRejectedRequestBodyWithLimit is the drain implementation with an
// injectable byte ceiling so boundary behavior can be tested without a 64MB
// fixture.
func drainRejectedRequestBodyWithLimit(ctx context.Context, w http.ResponseWriter, body io.ReadCloser, wireBody *wireRequestBodyLogger, limit int64) {
	if body == nil {
		return
	}
	log := logger.From(ctx)
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(rejectedRequestDrainTimeout)); err != nil {
		log.Debugf("Could not bound the drain of the rejected request body: %v", err)
	}
	if err := rc.EnableFullDuplex(); err != nil {
		log.Debugf("Could not take over the drain of the rejected request body: %v", err)
	}
	if err := rc.Flush(); err != nil {
		log.Debugf("Could not flush the rejection before draining: %v", err)
	}
	destination := io.Writer(io.Discard)
	if wireBody != nil {
		// DEBUG's wire capture is not a new read limit. It receives exactly the
		// bytes this already-bounded drain observes and no bytes beyond it.
		destination = wireBody
	}
	drained, err := io.Copy(destination, io.LimitReader(body, limit))
	complete := err == nil && drained < limit
	if !complete {
		// net/http closes the original request body after ServeHTTP returns. For
		// HTTP/1, that Close may read up to 256KB in an attempt to make the
		// connection reusable. The response already promises Connection: close,
		// so those reads cannot help framing and, in DEBUG mode, would bypass the
		// wire logger above.
		//
		// Expire the connection read deadline, then read through our logger once
		// more. This records bytes net/http had already buffered before the
		// deadline took effect, but the expired deadline prevents another socket
		// read. Closing the now-unbuffered body cannot silently consume a tail,
		// and marks it closed before net/http performs its own cleanup.
		if deadlineErr := rc.SetReadDeadline(time.Now().Add(-time.Second)); deadlineErr != nil {
			log.Debugf("Could not stop reading the incomplete rejected request body: %v", deadlineErr)
			if err == nil {
				err = deadlineErr
			}
		} else {
			buffered, bufferedErr := io.Copy(destination, body)
			drained += buffered
			if bufferedErr == nil {
				complete = true
			} else if err == nil {
				err = bufferedErr
			}
		}
		if closeErr := body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	if drained > 0 || err != nil {
		log.Debugf("Drained %d bytes of rejected request body (complete: %t, err: %v)", drained, complete, err)
	}
}
