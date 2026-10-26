// Package httpx builds the shared outbound HTTP client used by every external
// integration, so timeouts, connection pooling, and transport tuning live in
// one place instead of being re-invented per service.
package httpx

import (
	"net"
	"net/http"
	"time"
)

func NewClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

// WithTimeout returns a shallow copy of c using the given per-request timeout.
// Used by callers that need a shorter deadline than the shared default.
func WithTimeout(c *http.Client, timeout time.Duration) *http.Client {
	if c == nil {
		return NewClient(timeout)
	}
	clone := *c
	clone.Timeout = timeout
	return &clone
}
