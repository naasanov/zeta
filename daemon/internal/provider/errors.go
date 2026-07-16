package provider

import "fmt"

// ErrKind classifies a provider failure into the buckets metrics/logging
// care about, independent of which adapter produced it.
type ErrKind string

const (
	ErrUnknown     ErrKind = "unknown"
	ErrRateLimited ErrKind = "rate_limited" // 429
	ErrAuth        ErrKind = "auth"         // 401, 403
	ErrBadRequest  ErrKind = "bad_request"  // other 4xx
	ErrServer      ErrKind = "server"       // 5xx
	ErrCanceled    ErrKind = "canceled"     // ctx.Err() != nil
	ErrTransport   ErrKind = "transport"    // network, pre-response
)

// Error wraps a provider failure with its classification. Adapters return
// *Error on every failure path so internal/suggest can populate the
// "request" event's error_type field via errors.As without caring which
// adapter produced the error.
type Error struct {
	Kind       ErrKind
	HTTPStatus int
	Provider   string
	Err        error
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider(%s): %s: %v", e.Provider, e.Kind, e.Err)
}

// Unwrap exposes the underlying error so errors.Is(err, context.Canceled)
// keeps working through the wrapper (TestComplete_Cancellation relies on
// this).
func (e *Error) Unwrap() error {
	return e.Err
}

// ClassifyHTTP maps an HTTP status code to an ErrKind. Shared by all
// adapters so rate-limit/auth/server-error bucketing is consistent across
// providers.
func ClassifyHTTP(status int) ErrKind {
	switch {
	case status == 429:
		return ErrRateLimited
	case status == 401 || status == 403:
		return ErrAuth
	case status >= 400 && status < 500:
		return ErrBadRequest
	case status >= 500:
		return ErrServer
	default:
		return ErrUnknown
	}
}
