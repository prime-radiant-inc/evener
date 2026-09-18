// Package mcphttp holds the HTTP client plumbing shared by the MCP connection
// manager (agent/internal/mcp) and the MCP reachability probe (agent/mcpprobe).
// Both must apply a server's configured HTTP headers to every request the same
// way, so they build their clients through ClientWithHeaders instead of each
// carrying a private copy of the wrapper and drifting apart.
package mcphttp

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// HeaderRoundTripper wraps an http.RoundTripper to inject Headers into a
// request before delegating to Base. Headers are injected only into requests
// whose normalized origin (scheme, case-insensitive host, effective port)
// matches Origin, so a redirect to a different origin cannot re-add credentials
// the http.Client would otherwise withhold. The zero Origin injects nothing: an
// unscoped wrapper would leak to every origin, so callers must supply a
// concrete one.
type HeaderRoundTripper struct {
	Base    http.RoundTripper
	Origin  string // normalized scheme://host:port that scopes injection; empty injects nothing
	Headers map[string]string
}

// RoundTrip implements http.RoundTripper. It injects Headers into a clone of
// req so the caller-owned request is never mutated, as the RoundTripper
// contract requires.
func (h *HeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if h.Origin != "" && originOf(req.URL) == h.Origin {
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

// originOf returns u's normalized origin — lowercased scheme and hostname plus
// the effective port (the scheme default when u omits one) — joined with
// net.JoinHostPort so an IPv6 host is bracketed unambiguously. It returns ""
// when u has no scheme or hostname, which callers treat as "injects nothing".
func originOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if scheme == "" || host == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return ""
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// ClientWithHeaders returns a copy of base (a fresh client when base is nil)
// whose transport injects headers into requests for the server identified by
// endpointURL. base is left untouched; when base's transport is nil the wrapper
// falls back to http.DefaultTransport.
//
// Injection is scoped to endpointURL's normalized origin (scheme,
// case-insensitive host, effective port) so a redirect to a different origin
// does not carry the configured headers: http.Client withholds sensitive
// headers (Authorization, Cookie, ...) on a cross-host redirect, and an
// unscoped transport would immediately re-add them, leaking credentials to the
// redirect target. An unparseable or hostless endpointURL yields an origin of
// "", which injects nothing, so a bad URL fails closed rather than leaking to
// every origin.
func ClientWithHeaders(base *http.Client, endpointURL string, headers map[string]string) *http.Client {
	client := http.Client{}
	if base != nil {
		client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	u, err := url.Parse(endpointURL)
	if err != nil {
		u = nil
	}
	client.Transport = &HeaderRoundTripper{
		Base:    transport,
		Origin:  originOf(u),
		Headers: headers,
	}
	return &client
}
