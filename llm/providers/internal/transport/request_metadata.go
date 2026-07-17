package transport

import (
	"net/http"
	"net/http/httptrace"
	"sync"
)

type wireRequestMetadata struct {
	request *http.Request

	mu      sync.Mutex
	current http.Header
	last    http.Header

	traceObserved bool
	finished      bool
}

func newWireRequestMetadata(request *http.Request) *wireRequestMetadata {
	return &wireRequestMetadata{request: request}
}

func (m *wireRequestMetadata) trace(request *http.Request) *http.Request {
	trace := &httptrace.ClientTrace{
		WroteHeaderField: func(name string, values []string) {
			m.mu.Lock()
			m.traceObserved = true
			if m.current == nil {
				m.current = make(http.Header)
			}
			m.current[name] = append(m.current[name], values...)
			m.mu.Unlock()
		},
		WroteHeaders: func() {
			m.mu.Lock()
			m.traceObserved = true
			m.last = m.current.Clone()
			m.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			m.mu.Lock()
			m.traceObserved = true
			m.last = m.current.Clone()
			for name, values := range m.request.Trailer {
				m.last[name] = append([]string(nil), values...)
			}
			m.current = nil
			m.mu.Unlock()
		},
	}
	return request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
}

func (m *wireRequestMetadata) finishRoundTrip(transportErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.current) > 0 {
		m.last = m.current.Clone()
	}
	if !m.traceObserved && transportErr == nil {
		m.last = m.request.Header.Clone()
	}
	m.finished = true
}

func (m *wireRequestMetadata) snapshot() (http.Header, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.finished {
		return nil, false
	}
	return m.last.Clone(), true
}
