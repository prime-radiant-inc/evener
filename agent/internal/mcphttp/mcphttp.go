// Package mcphttp holds the HTTP client plumbing shared by the MCP connection
// manager (agent/internal/mcp) and the MCP reachability probe (agent/mcpprobe).
// Both must apply a server's configured HTTP headers to every request the same
// way, so they build their clients through ClientWithHeaders instead of each
// carrying a private copy of the wrapper and drifting apart.
package mcphttp

import (
	"net/http"
	"net/url"
)

// HeaderRoundTripper wraps an http.RoundTripper to inject Headers into a
// request before delegating to Base. When Host is non-empty, Headers are
// injected only into requests whose URL hostname matches it, so a redirect to
// another host cannot re-add credentials the http.Client would otherwise strip.
type HeaderRoundTripper struct {
	Base    http.RoundTripper
	Host    string // endpoint hostname that scopes injection; empty means every host
	Headers map[string]string
}

// RoundTrip implements http.RoundTripper. It injects Headers into a clone of
// req so the caller-owned request is never mutated, as the RoundTripper
// contract requires.
func (h *HeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if h.Host == "" || (req.URL != nil && req.URL.Hostname() == h.Host) {
		for k, v := range h.Headers {
			clone.Header.Set(k, v)
		}
	}
	return h.Base.RoundTrip(clone)
}

// CloseIdleConnections delegates to Base when it supports idle-connection
// cleanup, so http.Client.CloseIdleConnections keeps working through the
// wrapper instead of becoming a no-op.
func (h *HeaderRoundTripper) CloseIdleConnections() {
	if c, ok := h.Base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// ClientWithHeaders returns a copy of base (a fresh client when base is nil)
// whose transport injects headers into requests for the server identified by
// endpointURL. base is left untouched; when base's transport is nil the wrapper
// falls back to http.DefaultTransport.
//
// Injection is scoped to endpointURL's hostname so that a redirect to another
// host does not carry the configured headers: http.Client strips sensitive
// headers (Authorization, Cookie, ...) on a cross-host redirect, and an
// unscoped transport would immediately re-add them, leaking credentials to the
// redirect target.
func ClientWithHeaders(base *http.Client, endpointURL string, headers map[string]string) *http.Client {
	client := http.Client{}
	if base != nil {
		client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	var host string
	if u, err := url.Parse(endpointURL); err == nil {
		host = u.Hostname()
	}
	client.Transport = &HeaderRoundTripper{
		Base:    transport,
		Host:    host,
		Headers: headers,
	}
	return &client
}
