package suggest

import (
	"errors"
	"fmt"

	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// NoticeFor builds a classifier for the notice channel, closing over the
// user-configured brand and its key env var, since a provider.Error only
// knows its adapter, not what the user typed into config.toml.
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
