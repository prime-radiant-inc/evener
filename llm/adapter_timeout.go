package llm

import (
	"context"
	"net"
	"net/http"
)

// ApplyAdapterTimeout creates a context with the appropriate deadline from AdapterTimeout.
// For non-streaming requests, it uses the Request timeout for the whole call.
// Streaming requests get no additional overall context deadline: Request is
// applied while waiting for HTTP response headers, and StreamRead is checked per
// SSE line. Caller-supplied context and HTTP client policies remain authoritative.
// If at is nil, returns the context unchanged.
func ApplyAdapterTimeout(ctx context.Context, at *AdapterTimeout, streaming bool) (context.Context, context.CancelFunc) {
	if at == nil {
		return ctx, func() {}
	}
	if !streaming && at.Request > 0 {
		return context.WithTimeout(ctx, at.Request)
	}
	return ctx, func() {}
}

// AdapterTransport returns a configured clone of http.DefaultTransport. Connect
// bounds context-aware dialing without replacing caller hooks, and Request bounds
// the wait for response headers. Context-free Dial and DialTLS remain
// caller-authoritative. Returns nil when neither timeout is configured.
func AdapterTransport(at *AdapterTimeout) *http.Transport {
	return configuredAdapterTransport(http.DefaultTransport.(*http.Transport), at)
}

func configuredAdapterTransport(base *http.Transport, at *AdapterTimeout) *http.Transport {
	if at == nil || (at.Connect <= 0 && at.Request <= 0) {
		return nil
	}
	transport := base.Clone()
	if at.Connect > 0 {
		connectTimeout := at.Connect
		dialContext := transport.DialContext
		if dialContext == nil && transport.Dial == nil {
			dialContext = (&net.Dialer{}).DialContext
		}
		if dialContext != nil {
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				ctx, cancel := context.WithTimeout(ctx, connectTimeout)
				defer cancel()
				return dialContext(ctx, network, address)
			}
		}

		if dialTLSContext := transport.DialTLSContext; dialTLSContext != nil {
			transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				ctx, cancel := context.WithTimeout(ctx, connectTimeout)
				defer cancel()
				return dialTLSContext(ctx, network, address)
			}
		}
		// Dial and DialTLS have no contexts to bound safely without goroutines that
		// may leak. Preserve them unchanged as caller-authoritative transport policy.
	}
	if at.Request > 0 {
		transport.ResponseHeaderTimeout = at.Request
	}
	return transport
}

// ClientWithAdapterTimeout returns a copy of client configured with adapter
// transport timeouts. Standard transports are cloned; opaque RoundTrippers and
// caller client policy, including Timeout, remain authoritative.
func ClientWithAdapterTimeout(client *http.Client, at *AdapterTimeout) *http.Client {
	if at == nil || (at.Connect <= 0 && at.Request <= 0) {
		return client
	}

	copy := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return &copy
	}
	copy.Transport = configuredAdapterTransport(transport, at)
	return &copy
}

// StreamReadSSEOptions returns ParseSSE options for the StreamRead timeout.
// If at is nil or StreamRead is zero/negative, returns nil (no options).
func StreamReadSSEOptions(at *AdapterTimeout) []SSEOption {
	if at == nil || at.StreamRead <= 0 {
		return nil
	}
	return []SSEOption{WithStreamReadTimeout(at.StreamRead)}
}
