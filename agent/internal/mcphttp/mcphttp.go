// Package mcphttp holds the HTTP plumbing the MCP connection manager
// (agent/internal/mcp) and the MCP reachability probe (agent/mcpprobe) share, so
// the two cannot drift apart:
//
//   - ClientWithHeaders applies a server's configured HTTP headers to every
//     request for that server's origin, and
//   - BuildTransport builds the http or sse client transport, through that same
//     client and with ErrEndpointRequired and ErrUnsupportedTransport for the two
//     ways a configuration cannot be turned into one.
//
// The stdio transport is deliberately not built here: the probe only checks that
// a configured command is on PATH, while the manager starts the process (see
// mcpprobe's package doc).
package mcphttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrEndpointRequired and ErrUnsupportedTransport identify BuildTransport's two
// refusals by identity rather than by message: an http or sse server with no
// endpoint URL, and a transport type this package does not build.
var (
	ErrEndpointRequired     = errors.New("transport requires a url")
	ErrUnsupportedTransport = errors.New("unknown MCP transport type")
)

// BuildTransport returns the SDK client transport for an http or sse server
// endpoint. It is the single construction the MCP connection manager
// (agent/internal/mcp) and the reachability probe (agent/mcpprobe) share, so
// their transport policy — which SDK transport a configured type means, the
// endpoint, whether an empty URL is refused — cannot drift apart (#2396).
//
// kind must be "http" or "sse". stdio is deliberately not built here: the two
// callers need different things from it, because the probe only checks that a
// configured command is on PATH while the manager starts the process inside the
// session sandbox (see mcpprobe's package doc for the probe's stated limits).
//
// client is the HTTP client the transport sends through, and each caller owns
// that decision: the probe passes the per-call client it pools and releases
// (probeHTTPClient, which keeps another component's CloseIdleConnections from
// breaking a handshake in flight, #1486), and the manager passes nil to keep
// the SDK's own client. A nil client with no headers leaves the transport on
// the SDK's client rather than installing an empty wrapper.
func BuildTransport(kind, endpointURL string, headers map[string]string, client *http.Client) (mcpsdk.Transport, error) {
	if len(headers) > 0 {
		client = ClientWithHeaders(client, endpointURL, headers)
	}
	switch kind {
	case "sse":
		if endpointURL == "" {
			return nil, fmt.Errorf("%s %w", kind, ErrEndpointRequired)
		}
		t := &mcpsdk.SSEClientTransport{Endpoint: endpointURL, HTTPClient: client}
		return t, nil

	case "http":
		if endpointURL == "" {
			return nil, fmt.Errorf("%s %w", kind, ErrEndpointRequired)
		}
		t := &mcpsdk.StreamableClientTransport{Endpoint: endpointURL, HTTPClient: client}
		return t, nil

	default:
		return nil, fmt.Errorf("%w %q", ErrUnsupportedTransport, kind)
	}
}

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
