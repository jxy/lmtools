// Package limitio provides size-limited I/O operations for DoS prevention.
//
// This package centralizes all size-limited reads across the codebase,
// ensuring consistent enforcement of size limits defined in the constants
// package. It prevents memory exhaustion attacks by limiting:
//
//   - Request bodies (DefaultMaxRequestBodySize: 512MB)
//   - Response bodies (DefaultMaxResponseBodySize: 20MB)
//   - Error responses (MaxErrorResponseSize: 10KB)
//   - CLI input (MaxCLIInputSize: 10MB)
//
// All functions return ErrTooLarge when the size limit is exceeded,
// providing consistent error messages across the application.
package limitio

import (
	"errors"
	"fmt"
	"io"
	"math"
)

// SizeLimitError reports data that exceeded a configured size limit.
// Callers can detect it with errors.As to map the failure onto a protocol
// specific status, such as HTTP 413.
type SizeLimitError struct {
	// Kind describes what exceeded the limit (e.g. "request body").
	Kind string
	// MaxSize is the limit that was exceeded, in bytes.
	MaxSize int64
}

func (e *SizeLimitError) Error() string {
	return fmt.Sprintf("%s exceeds maximum size of %d bytes", e.Kind, e.MaxSize)
}

// ReadLimited reads from an io.Reader with a size limit.
// Returns *SizeLimitError if the data exceeds maxSize.
// This is the single source of truth for size-limited reading across the codebase.
func ReadLimited(r io.Reader, maxSize int64) ([]byte, error) {
	return ReadLimitedWithKind(r, maxSize, "data")
}

// ReadLimitedWithKind reads from an io.Reader with a size limit and a custom kind for errors.
// The kind parameter is used in error messages to identify what data exceeded the limit.
func ReadLimitedWithKind(r io.Reader, maxSize int64, kind string) ([]byte, error) {
	return ReadLimitedWithSizeHint(r, maxSize, kind, -1)
}

// initialReadBytes is the most this package reserves before a single byte has
// arrived. A body that declares less than this reserves only what it declares,
// so the ordinary small request still lands on one exact allocation.
const initialReadBytes = 64 * 1024

// readGrowthFactor bounds how far ahead of the delivered bytes the buffer may
// run. Reserving a client-declared Content-Length up front turned the size limit
// from a memory bound into a memory instruction: a request declaring 32MB and
// then stalling pinned 32MB per connection for the whole read timeout, having
// sent nothing. Growing by a fixed factor instead keeps the buffer proportional
// to what the client has actually delivered, so pinning memory costs an attacker
// the bandwidth to fill it.
const readGrowthFactor = 4

// ReadLimitedWithSizeHint is ReadLimitedWithKind with an expected size, such as
// a request's Content-Length. A negative hint means the size is unknown; zero
// means the input is expected to be empty. The hint is a ceiling on growth, not
// a reservation: the buffer still starts small and grows as bytes arrive, but it
// snaps to the declared size on the way past, so an honest body ends in a buffer
// sized to it rather than the next power up.
func ReadLimitedWithSizeHint(r io.Reader, maxSize int64, kind string, sizeHint int64) ([]byte, error) {
	if maxSize < 0 {
		return nil, fmt.Errorf("maximum %s size must not be negative: %d", kind, maxSize)
	}

	// One byte past the limit is all it takes to know the body is over it.
	ceiling := maxSize
	if ceiling < math.MaxInt64 {
		ceiling++
	}
	declared := int64(-1)
	if sizeHint >= 0 {
		declared = min(sizeHint, maxSize)
	}

	buf := make([]byte, 0, initialReadCapacity(declared, ceiling))
	for {
		if len(buf) == cap(buf) {
			if int64(cap(buf)) >= ceiling {
				// Filling a buffer sized one past the limit is the over-limit proof.
				return nil, &SizeLimitError{Kind: kind, MaxSize: maxSize}
			}
			grown := make([]byte, len(buf), nextReadCapacity(int64(cap(buf)), declared, ceiling))
			copy(grown, buf)
			buf = grown
		}
		n, err := r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err != nil {
			if errors.Is(err, io.EOF) {
				if int64(len(buf)) > maxSize {
					return nil, &SizeLimitError{Kind: kind, MaxSize: maxSize}
				}
				return buf, nil
			}
			return nil, fmt.Errorf("read %s: %w", kind, err)
		}
	}
}

// initialReadCapacity reserves the declared size, plus the sentinel byte, when
// the client declared less than the floor. Ordinary requests are far below it,
// so the common case is still a single allocation of exactly the right size.
func initialReadCapacity(declared, ceiling int64) int64 {
	initial := int64(initialReadBytes)
	if declared >= 0 && declared < initial {
		initial = declared + 1
	}
	return min(initial, ceiling)
}

// nextReadCapacity grows geometrically, snapping to the declared size — and the
// sentinel byte after it — the first time growth reaches it, so a body that ends
// exactly where it said it would does not pay for one more step. A client that
// declares less than it sends does not get to stall the read there: once the
// buffer is past the declared size and still full, growth resumes toward the
// ceiling.
func nextReadCapacity(current, declared, ceiling int64) int64 {
	next := ceiling
	if current <= ceiling/readGrowthFactor {
		next = max(current*readGrowthFactor, initialReadBytes)
	}
	if declared >= 0 && current <= declared && next >= declared {
		next = declared
		if declared < ceiling {
			next++
		}
	}
	return min(next, ceiling)
}
