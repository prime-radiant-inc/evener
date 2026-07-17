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
	written chan struct{}
	once    sync.Once
}

func newWireRequestMetadata(request *http.Request) *wireRequestMetadata {
	return &wireRequestMetadata{request: request, written: make(chan struct{})}
}

func (m *wireRequestMetadata) trace(request *http.Request) *http.Request {
	trace := &httptrace.ClientTrace{
		WroteHeaderField: func(name string, values []string) {
			m.mu.Lock()
			if m.current == nil {
				m.current = make(http.Header)
			}
			m.current[name] = append(m.current[name], values...)
			m.mu.Unlock()
		},
		WroteHeaders: func() {
			m.mu.Lock()
			m.last = m.current.Clone()
			m.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			m.mu.Lock()
			m.last = m.current.Clone()
			for name, values := range m.request.Trailer {
				m.last[name] = append([]string(nil), values...)
			}
			m.current = nil
			m.mu.Unlock()
			m.once.Do(func() { close(m.written) })
		},
	}
	return request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
}

func (m *wireRequestMetadata) snapshot() (http.Header, bool) {
	m.mu.Lock()
	hasWrittenHeaders := len(m.last) > 0
	m.mu.Unlock()
	if !hasWrittenHeaders {
		return nil, false
	}
	<-m.written
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last.Clone(), true
}
