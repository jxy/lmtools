// Package limitio provides size-limited I/O operations for DoS prevention.
//
// This package centralizes all size-limited reads across the codebase,
// ensuring consistent enforcement of size limits defined in the constants
// package. It prevents memory exhaustion attacks by limiting:
//
//   - Request bodies (DefaultMaxRequestBodySize: 10MB)
//   - Response bodies (DefaultMaxResponseBodySize: 20MB)
//   - Error responses (MaxErrorResponseSize: 10KB)
//   - CLI input (MaxCLIInputSize: 10MB)
//
// All functions return ErrTooLarge when the size limit is exceeded,
// providing consistent error messages across the application.
package limitio

import (
	"fmt"
	"io"
)

// ErrTooLarge creates a consistent error message for size limit violations.
// The kind parameter describes what exceeded the limit (e.g., "request body", "response").
func ErrTooLarge(kind string, maxSize int64) error {
	return fmt.Errorf("%s exceeds maximum size of %d bytes", kind, maxSize)
}

// ReadLimited reads from an io.Reader with a size limit.
// Returns ErrTooLarge if the data exceeds maxSize.
// This is the single source of truth for size-limited reading across the codebase.
func ReadLimited(r io.Reader, maxSize int64) ([]byte, error) {
	return ReadLimitedWithKind(r, maxSize, "data")
}

// ReadLimitedWithKind reads from an io.Reader with a size limit and a custom kind for errors.
// The kind parameter is used in error messages to identify what data exceeded the limit.
func ReadLimitedWithKind(r io.Reader, maxSize int64, kind string) ([]byte, error) {
	limitedReader := io.LimitReader(r, maxSize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	if int64(len(data)) > maxSize {
		return nil, ErrTooLarge(kind, maxSize)
	}
	return data, nil
}
