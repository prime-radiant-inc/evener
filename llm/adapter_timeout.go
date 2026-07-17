package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"

	apilog "primeradiant.com/serf/llm/apilog"
)

// APITimeoutSource identifies the layer that owned a provider-attempt timeout.
type APITimeoutSource string

const (
	// APITimeoutNone indicates that no owned timeout was observed.
	APITimeoutNone APITimeoutSource = ""
	// APITimeoutAdapter identifies an adapter-owned overall deadline.
	APITimeoutAdapter APITimeoutSource = "adapter_deadline"
	// APITimeoutResponseHeader identifies the standard transport's response-header deadline.
	APITimeoutResponseHeader APITimeoutSource = "response_header_timeout"
	// APITimeoutSSERead identifies the streaming decoder's per-read deadline.
	APITimeoutSSERead APITimeoutSource = "sse_read_timeout"
	// APITimeoutTransport identifies a timeout owned by the caller's HTTP transport.
	APITimeoutTransport APITimeoutSource = "transport_timeout"
)

// APIAttemptContextOwnership keeps caller cancellation distinct from timeout
// policy owned by an adapter or its transport/stream reader.
type APIAttemptContextOwnership struct {
	Parent        context.Context
	Attempt       context.Context
	TimeoutSource APITimeoutSource
}

// APITimeoutSourceForTransport identifies timeout ownership from the actual
// contexts and transport error. Configuring a timeout alone is not evidence
// that an unrelated transport failure came from that timeout.
func APITimeoutSourceForTransport(parent, attempt context.Context, transportErr error) APITimeoutSource {
	if transportErr == nil || (parent != nil && parent.Err() != nil) {
		return APITimeoutNone
	}
	var responseHeaderTimeout *responseHeaderTimeoutError
	if errors.As(transportErr, &responseHeaderTimeout) {
		return APITimeoutResponseHeader
	}
	if !errorIsTimeout(transportErr) {
		return APITimeoutNone
	}
	if attempt != nil && errors.Is(attempt.Err(), context.DeadlineExceeded) {
		return APITimeoutAdapter
	}
	return APITimeoutTransport
}

// APITimeoutSourceForAttempt resolves timeout ownership from errors actually
// observed while sending or consuming one response. It does not infer a
// timeout merely because a policy or deadline was configured.
func APITimeoutSourceForAttempt(parent, attempt context.Context, current APITimeoutSource, evidence ...error) APITimeoutSource {
	if current != APITimeoutNone || (parent != nil && parent.Err() != nil) {
		return current
	}
	hasTimeoutEvidence := false
	for _, err := range evidence {
		if errorIsTimeout(err) {
			hasTimeoutEvidence = true
			break
		}
	}
	if !hasTimeoutEvidence {
		return APITimeoutNone
	}
	if attempt != nil && errors.Is(attempt.Err(), context.DeadlineExceeded) {
		return APITimeoutAdapter
	}
	return APITimeoutTransport
}

func errorIsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

// ClassifyAPIAttemptOutcome records attempt evidence without changing the
// provider's existing error or retryability decisions.
func ClassifyAPIAttemptOutcome(owner APIAttemptContextOwnership, statusCode int, decodeErr, transportErr error) apilog.AttemptOutcomeClass {
	if owner.Parent != nil && owner.Parent.Err() != nil {
		return apilog.AttemptCallerCancel
	}
	if owner.Attempt != nil && errors.Is(owner.Attempt.Err(), context.Canceled) &&
		(errorIsCancellation(decodeErr) || errorIsCancellation(transportErr)) {
		return apilog.AttemptCallerCancel
	}
	if attemptOwnedTimeout(owner, decodeErr, transportErr) {
		return apilog.AttemptProviderTimeout
	}
	if transportErr != nil {
		return apilog.AttemptTransportFail
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return apilog.AttemptProviderReject
	}
	if decodeErr != nil {
		return apilog.AttemptDecodeFail
	}
	return apilog.AttemptSuccess
}

func errorIsCancellation(err error) bool {
	return err != nil && errors.Is(err, context.Canceled)
}

func attemptOwnedTimeout(owner APIAttemptContextOwnership, decodeErr, transportErr error) bool {
	switch owner.TimeoutSource {
	case APITimeoutAdapter:
		return owner.Attempt != nil && errors.Is(owner.Attempt.Err(), context.DeadlineExceeded)
	case APITimeoutResponseHeader:
		return transportErr != nil
	case APITimeoutSSERead:
		return decodeErr != nil || transportErr != nil
	case APITimeoutTransport:
		return true
	default:
		return false
	}
}

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
		if dialContext == nil && transport.Dial == nil { //nolint:staticcheck // Preserve a caller-supplied legacy Dial hook rather than overriding it.
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

// responseHeaderTimeoutTransport identifies the ambiguous timeout after a
// request was fully written but before response headers arrived. The standard
// transport owns the timer; this wrapper records the completed write phase so
// retry policy can classify the bounded timeout as retryable despite the risk
// that the provider already accepted the request.
type responseHeaderTimeoutTransport struct {
	base *http.Transport
}

func (t *responseHeaderTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var wroteRequest atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wroteRequest.Store(true)
			}
		},
	}
	ctx := httptrace.WithClientTrace(req.Context(), trace)
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err == nil || !wroteRequest.Load() || req.Context().Err() != nil {
		return resp, err
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return nil, newResponseHeaderTimeoutError("", err.Error(), err)
	}
	return resp, err
}

func (t *responseHeaderTimeoutTransport) CloseIdleConnections() {
	t.base.CloseIdleConnections()
}

// APILogTransportUsesStandardCompression reports whether the wrapped standard
// transport would automatically request and decode gzip responses. The API
// transport layer uses this to preserve raw response bytes while restoring the
// same decoded response semantics for provider adapters.
func (t *responseHeaderTimeoutTransport) APILogTransportUsesStandardCompression() bool {
	return t != nil && t.base != nil && !t.base.DisableCompression
}

// ClientWithAdapterTimeout returns a copy of client configured with adapter
// transport timeouts. Standard transports are cloned; opaque RoundTrippers and
// caller client policy, including Timeout, remain authoritative.
func ClientWithAdapterTimeout(client *http.Client, at *AdapterTimeout) *http.Client {
	if at == nil || (at.Connect <= 0 && at.Request <= 0) {
		return client
	}

	clientCopy := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return &clientCopy
	}
	configured := configuredAdapterTransport(transport, at)
	clientCopy.Transport = configured
	if at.Request > 0 {
		clientCopy.Transport = &responseHeaderTimeoutTransport{base: configured}
	}
	return &clientCopy
}

// StreamReadSSEOptions returns ParseSSE options for the StreamRead timeout.
// If at is nil or StreamRead is zero/negative, returns nil (no options).
func StreamReadSSEOptions(at *AdapterTimeout) []SSEOption {
	if at == nil || at.StreamRead <= 0 {
		return nil
	}
	return []SSEOption{WithStreamReadTimeout(at.StreamRead)}
}
