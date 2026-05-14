package core

import "errors"

type fatalStreamError struct {
	err error
}

func newFatalStreamError(err error) error {
	if err == nil {
		return nil
	}
	return fatalStreamError{err: err}
}

func (e fatalStreamError) Error() string {
	return e.err.Error()
}

func (e fatalStreamError) Unwrap() error {
	return e.err
}

func isFatalStreamError(err error) bool {
	var fatal fatalStreamError
	return errors.As(err, &fatal)
}
