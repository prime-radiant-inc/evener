// Package mcphttp holds the HTTP client plumbing shared by the MCP connection
// manager (agent/internal/mcp) and the MCP reachability probe (agent/mcpprobe).
// Both must apply a server's configured HTTP headers to every request the same
// way, so they build their clients through ClientWithHeaders instead of each
// carrying a private copy of the wrapper and drifting apart.
package mcphttp

import "net/http"

// HeaderRoundTripper wraps an http.RoundTripper to inject Headers into each
// request before delegating to Base.
type HeaderRoundTripper struct {
	Base    http.RoundTripper
	Headers map[string]string
}

// RoundTrip implements http.RoundTripper.
func (h *HeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}
	return h.Base.RoundTrip(req)
}

// ClientWithHeaders returns a copy of base (a fresh client when base is nil)
// whose transport injects headers into every request. base is left untouched;
// when base's transport is nil the wrapper falls back to http.DefaultTransport.
func ClientWithHeaders(base *http.Client, headers map[string]string) *http.Client {
	client := http.Client{}
	if base != nil {
		client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = &HeaderRoundTripper{
		Base:    transport,
		Headers: headers,
	}
	return &client
}
