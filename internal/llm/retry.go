package llm

import "errors"

// RetryableError marks a request failure that another attempt can plausibly
// survive: rate limits, server errors, and transport errors. Protocol errors
// (bad request, auth, policy) are never wrapped.
type RetryableError struct{ Err error }

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// Retryable reports whether err was classified as worth another attempt.
func Retryable(err error) bool {
	var r *RetryableError
	return errors.As(err, &r)
}
