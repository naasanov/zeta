package provider

import (
	"net/http"
	"time"
)

// keepAliveHTTPClient returns an *http.Client tuned to hold warm keep-alive
// connections, so the daemon never pays TLS/TCP setup cost on the hot
// per-keystroke path (design §4 "warm connections"). Construct one at adapter
// startup and reuse it for every request — a fresh client per request would
// defeat the entire point.
//
// This is a shared *opt-in* helper, not a requirement of the Provider
// contract: an adapter built on an SDK that manages its own HTTP client (see
// anthropic.go, which lets anthropic-sdk-go own the connection pool) ignores
// it entirely. It exists only because the two adapters that DO hand-build a
// client — openai.go and codestral.go — otherwise duplicated this exact tuning.
func keepAliveHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}}
}
