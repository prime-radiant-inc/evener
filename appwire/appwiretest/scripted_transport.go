// Package appwiretest exposes test helpers for driving appwire.Client
// from external packages. The private memoryTransport in
// internal/appwire's own _test.go cannot be reused from cmd/serf-tui,
// so this package provides an equivalent with an exported API.
package appwiretest

import (
	"context"
	"errors"
	"sync"

	"primeradiant.com/serf/appwire"
)

// ScriptedTransport is a fake appwire.Transport whose Send calls are
// observable on the Sent() channel and whose Recv calls block until
// the test delivers a response or notification.
type ScriptedTransport struct {
	mu      sync.Mutex
	sent    chan appwire.Message
	inbound chan appwire.Message
	closed  bool
}

func NewScriptedTransport() *ScriptedTransport {
	return &ScriptedTransport{
		sent:    make(chan appwire.Message, 32),
		inbound: make(chan appwire.Message, 32),
	}
}

func (s *ScriptedTransport) Send(_ context.Context, msg appwire.Message) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("transport closed")
	}
	s.mu.Unlock()
	s.sent <- msg
	return nil
}

func (s *ScriptedTransport) Recv(ctx context.Context) (appwire.Message, error) {
	select {
	case msg, ok := <-s.inbound:
		if !ok {
			return appwire.Message{}, errors.New("transport closed")
		}
		return msg, nil
	case <-ctx.Done():
		return appwire.Message{}, ctx.Err()
	}
}

func (s *ScriptedTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.inbound)
	return nil
}

// Sent returns a receive-only channel of messages the client wrote.
// Tests read from this to observe outgoing requests and pick the
// correct ID for DeliverResponse.
func (s *ScriptedTransport) Sent() <-chan appwire.Message { return s.sent }

// DeliverResponse synthesizes a JSON-RPC response message for the
// given request ID and pushes it through the transport's Recv path.
func (s *ScriptedTransport) DeliverResponse(id appwire.ID, result any) {
	s.inbound <- appwire.Message{Response: &appwire.Response{ID: id, Result: result}}
}

// DeliverError synthesizes a JSON-RPC error response for the given
// request ID and pushes it through Recv.
func (s *ScriptedTransport) DeliverError(id appwire.ID, code int, message string) {
	s.inbound <- appwire.Message{Error: &appwire.ErrorResponse{ID: id, Error: appwire.WireError{Code: code, Message: message}}}
}

// DeliverNotification pushes a notification through Recv. The client's
// Start goroutine pumps it onto Notifications().
func (s *ScriptedTransport) DeliverNotification(n appwire.Notification) {
	s.inbound <- appwire.Message{Notification: &n}
}
