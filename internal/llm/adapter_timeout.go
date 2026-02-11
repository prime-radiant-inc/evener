package llm

import (
	"context"
	"net"
	"net/http"
)

// ApplyAdapterTimeout creates a context with the appropriate deadline from AdapterTimeout.
// For non-streaming requests, it uses the Request timeout.
// For streaming requests, there is no overall deadline (stream_read is checked per-event).
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

// AdapterTransport returns an http.Transport with connect timeout derived from
// AdapterTimeout.Connect. Returns nil if at is nil or Connect is zero/negative.
func AdapterTransport(at *AdapterTimeout) *http.Transport {
	if at == nil || at.Connect <= 0 {
		return nil
	}
	dialer := &net.Dialer{Timeout: at.Connect}
	return &http.Transport{DialContext: dialer.DialContext}
}

// ClientWithConnectTimeout returns a copy of the given client with a transport
// that enforces the connect timeout from AdapterTimeout. If no connect timeout
// is configured, returns the original client unchanged.
func ClientWithConnectTimeout(client *http.Client, at *AdapterTimeout) *http.Client {
	t := AdapterTransport(at)
	if t == nil {
		return client
	}
	cp := *client
	cp.Transport = t
	return &cp
}
