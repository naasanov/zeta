package provider

import (
	"net/http"
	"time"
)

// keepAliveHTTPClient returns an *http.Client tuned to hold warm keep-alive connections.
func keepAliveHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}}
}
