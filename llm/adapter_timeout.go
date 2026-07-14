package llm

import (
	"context"
	"net"
	"net/http"
)

// ApplyAdapterTimeout creates a context with the appropriate deadline from AdapterTimeout.
// For non-streaming requests, it uses the Request timeout for the whole call.
// Streaming requests have no overall context deadline: Request is applied while
// waiting for HTTP response headers, and StreamRead is checked per SSE line.
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

// AdapterTransport returns a configured clone of http.DefaultTransport.
// Connect bounds dialing and Request bounds the wait for response headers.
// Returns nil when neither transport timeout is configured.
func AdapterTransport(at *AdapterTimeout) *http.Transport {
	return configuredAdapterTransport(http.DefaultTransport.(*http.Transport), at)
}

func configuredAdapterTransport(base *http.Transport, at *AdapterTimeout) *http.Transport {
	if at == nil || (at.Connect <= 0 && at.Request <= 0) {
		return nil
	}
	transport := base.Clone()
	if at.Connect > 0 {
		dialer := &net.Dialer{Timeout: at.Connect}
		transport.DialContext = dialer.DialContext
	}
	if at.Request > 0 {
		transport.ResponseHeaderTimeout = at.Request
	}
	return transport
}

// ClientWithAdapterTimeout returns a copy of client configured with adapter
// transport timeouts. Standard transports are cloned; opaque RoundTrippers are
// preserved and remain responsible for their own timeout behavior.
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
