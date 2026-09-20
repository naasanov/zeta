package provider

import "fmt"

// ErrKind classifies a provider failure, independent of which adapter produced it.
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

// Adapters return *Error on every failure path, so callers can classify via errors.As.
type Error struct {
	Kind       ErrKind
	HTTPStatus int
	Provider   string
	Err        error
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider(%s): %s: %v", e.Provider, e.Kind, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

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
