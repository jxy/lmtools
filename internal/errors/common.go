package errors

import "errors"

// Common errors
var (
	ErrInterrupted  = errors.New("operation interrupted")
	ErrNoInput      = errors.New("no input provided")
	ErrInvalidInput = errors.New("invalid input")
)
