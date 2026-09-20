package suggest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// TestNoticeFor covers every provider.ErrKind and pins the surfaced vs
// not-surfaced verdict; ErrRateLimited must stay not-ok since treating a
// recoverable rate limit as a notice would be a regression.
func TestNoticeFor(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantOK   bool
		wantKind string
	}{
		{
			name:     "auth",
			err:      &provider.Error{Kind: provider.ErrAuth, HTTPStatus: 401, Provider: "codestral"},
			wantOK:   true,
			wantKind: "auth",
		},
		{
			name:     "bad_request",
			err:      &provider.Error{Kind: provider.ErrBadRequest, HTTPStatus: 404, Provider: "codestral"},
			wantOK:   true,
			wantKind: "bad_request",
		},
		{
			name:   "rate_limited never surfaces (recoverable by design)",
			err:    &provider.Error{Kind: provider.ErrRateLimited, HTTPStatus: 429, Provider: "groq"},
			wantOK: false,
		},
		{
			name:   "server error does not surface",
			err:    &provider.Error{Kind: provider.ErrServer, HTTPStatus: 500, Provider: "anthropic"},
			wantOK: false,
		},
		{
			name:   "transport error does not surface",
			err:    &provider.Error{Kind: provider.ErrTransport, Provider: "openai"},
			wantOK: false,
		},
		{
			name:   "canceled error does not surface",
			err:    &provider.Error{Kind: provider.ErrCanceled, Provider: "openai"},
			wantOK: false,
		},
		{
			name:   "unknown error does not surface",
			err:    &provider.Error{Kind: provider.ErrUnknown, Provider: "openai"},
			wantOK: false,
		},
		{
			name:   "plain error is not a *provider.Error",
			err:    errors.New("boom"),
			wantOK: false,
		},
		{
			name:     "wrapped *provider.Error still unwraps via errors.As",
			err:      fmt.Errorf("suggest: %w", &provider.Error{Kind: provider.ErrAuth, HTTPStatus: 403, Provider: "anthropic"}),
			wantOK:   true,
			wantKind: "auth",
		},
	}

	classify := NoticeFor("codestral", "ZSH_AUTOPILOT_CODESTRAL_KEY")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, kind, ok := classify(c.err)
			if ok != c.wantOK {
				t.Fatalf("NoticeFor() ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				if text != "" || kind != "" {
					t.Errorf("not-ok result should be zero-valued, got text=%q kind=%q", text, kind)
				}
				return
			}
			if kind != c.wantKind {
				t.Errorf("kind = %q, want %q", kind, c.wantKind)
			}
			if text == "" {
				t.Error("text should be non-empty when ok")
			}
		})
	}
}

func TestNoticeFor_OmitsZeroStatus(t *testing.T) {
	text, _, ok := NoticeFor("codestral", "ZSH_AUTOPILOT_CODESTRAL_KEY")(&provider.Error{Kind: provider.ErrAuth, Provider: "codestral"})
	if !ok {
		t.Fatal("NoticeFor() ok = false, want true")
	}
	if want := "auth failed for codestral: check $ZSH_AUTOPILOT_CODESTRAL_KEY"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

// TestNoticeFor_BrandNotAdapter is the regression guard: a groq-branded
// profile riding the openai adapter must never leak "openai" into the
// notice, and must name "groq" instead.
func TestNoticeFor_BrandNotAdapter(t *testing.T) {
	text, _, ok := NoticeFor("groq", "ZSH_AUTOPILOT_GROQ_KEY")(&provider.Error{Kind: provider.ErrAuth, HTTPStatus: 401, Provider: "openai"})
	if !ok {
		t.Fatal("NoticeFor() ok = false, want true")
	}
	if want := "auth failed (401) for groq: check $ZSH_AUTOPILOT_GROQ_KEY"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if strings.Contains(text, "openai") {
		t.Errorf("text = %q, must not mention the adapter name %q", text, "openai")
	}
}

func TestNoticeFor_EmptyKeyEnvFallsBack(t *testing.T) {
	text, _, ok := NoticeFor("ollama", "")(&provider.Error{Kind: provider.ErrAuth, HTTPStatus: 401, Provider: "openai"})
	if !ok {
		t.Fatal("NoticeFor() ok = false, want true")
	}
	if want := "auth failed (401) for ollama: check your API key"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func TestNoticeFor_EmptyBrandOmitsClause(t *testing.T) {
	authText, _, ok := NoticeFor("", "ZSH_AUTOPILOT_GROQ_KEY")(&provider.Error{Kind: provider.ErrAuth, HTTPStatus: 401, Provider: "openai"})
	if !ok {
		t.Fatal("NoticeFor() ok = false, want true")
	}
	if want := "auth failed (401): check $ZSH_AUTOPILOT_GROQ_KEY"; authText != want {
		t.Errorf("text = %q, want %q", authText, want)
	}

	badReqText, _, ok := NoticeFor("", "")(&provider.Error{Kind: provider.ErrBadRequest, HTTPStatus: 404, Provider: "openai"})
	if !ok {
		t.Fatal("NoticeFor() ok = false, want true")
	}
	if want := "request rejected (404): check your model name in config.toml"; badReqText != want {
		t.Errorf("text = %q, want %q", badReqText, want)
	}
}
