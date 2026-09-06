package suggest

import (
	"errors"
	"fmt"

	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// NoticeFor builds a classifier for the notice channel, closing over the
// brand the user configured (e.g. "groq") and the env var its key comes
// from, since a provider.Error only knows its adapter (e.g. "openai") which
// is not what the user typed into config.toml.
//
// The returned func surfaces only the kinds a user can act on (auth,
// bad_request). Everything else, including the recoverable ErrRateLimited,
// returns ok == false.
func NoticeFor(brand, keyEnv string) func(error) (text, kind string, ok bool) {
	return func(err error) (text, kind string, ok bool) {
		var perr *provider.Error
		if !errors.As(err, &perr) {
			return "", "", false
		}

		status := ""
		if perr.HTTPStatus != 0 {
			status = fmt.Sprintf(" (%d)", perr.HTTPStatus)
		}

		target := ""
		switch perr.Kind {
		case provider.ErrAuth:
			if brand != "" {
				target = " for " + brand
			}
			hint := "check your API key"
			if keyEnv != "" {
				hint = "check $" + keyEnv
			}
			return fmt.Sprintf("auth failed%s%s: %s", status, target, hint), "auth", true
		case provider.ErrBadRequest:
			if brand != "" {
				target = " by " + brand
			}
			return fmt.Sprintf("request rejected%s%s: check your model name in config.toml", status, target), "bad_request", true
		default:
			return "", "", false
		}
	}
}
