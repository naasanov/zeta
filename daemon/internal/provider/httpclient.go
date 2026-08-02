package provider

import (
	"net/http"
	"time"
)

// keepAliveHTTPClient returns an *http.Client tuned to hold warm keep-alive
// connections, so the daemon never pays TLS/TCP setup cost on the hot
// per-keystroke path (design §4). Construct one at adapter startup and reuse
// it for every request. Opt-in, not part of the Provider contract: an
// adapter whose SDK owns its own client (anthropic.go) ignores it.
func keepAliveHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}}
}
