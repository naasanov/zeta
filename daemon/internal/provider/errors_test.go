package provider

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyHTTP(t *testing.T) {
	tests := []struct {
		status int
		want   ErrKind
	}{
		{400, ErrBadRequest},
		{401, ErrAuth},
		{403, ErrAuth},
		{404, ErrBadRequest},
		{429, ErrRateLimited},
		{500, ErrServer},
		{503, ErrServer},
	}
	for _, tt := range tests {
		if got := ClassifyHTTP(tt.status); got != tt.want {
			t.Errorf("ClassifyHTTP(%d) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// TestError_UnwrapPreservesErrorsIs asserts errors.Is(err, context.Canceled)
// works through the *Error wrapper, matching TestComplete_Cancellation's
// expectation in openai_test.go.
func TestError_UnwrapPreservesErrorsIs(t *testing.T) {
	err := &Error{Kind: ErrCanceled, Provider: "openai", Err: context.Canceled}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false, want true")
	}
}
