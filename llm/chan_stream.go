package llm

import (
	"context"
	"sync"
)

type chanStream struct {
	events   chan StreamEvent
	cancel   context.CancelFunc
	once     sync.Once
	sendOnce sync.Once
	closing  chan struct{}
	done     chan struct{}
}

// ChanStream is a channel-backed stream of StreamEvents with a single producer
// goroutine and a single consumer. The consumer reads from Events, and can stop
// the producer cooperatively via Close.
type ChanStream struct{ chanStream }

// NewChanStream returns a ChanStream with a buffered event channel. cancel, if
// non-nil, is invoked by Close to stop the underlying producer.
func NewChanStream(cancel context.CancelFunc) *ChanStream {
	return &ChanStream{chanStream{
		events:  make(chan StreamEvent, 128),
		cancel:  cancel,
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}}
}

// Events returns the receive-only channel of stream events for the consumer.
func (s *ChanStream) Events() <-chan StreamEvent { return s.events }

// Close stops the stream cooperatively: on the first call it invokes the cancel
// function (if any) and signals senders to stop blocking and drop events, then
// blocks until the producer has finished via CloseSend. Subsequent calls only
// wait for that completion. It always returns nil.
func (s *ChanStream) Close() error {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		// Signal senders to stop blocking and drop events.
		close(s.closing)
	})
	<-s.done
	return nil
}

// CloseSend closes the event channel and marks the stream as finished. Provider adapters
// should call this exactly once when the underlying stream finishes.
func (s *ChanStream) CloseSend() {
	s.sendOnce.Do(func() {
		close(s.done)
		close(s.events)
	})
}

// Send publishes a stream event from the stream's single producer goroutine.
//
// Concurrency contract: a ChanStream has exactly one producer. Every Send and the
// single CloseSend run on that one goroutine, in order, so a Send is never
// concurrent with — nor reached after — CloseSend's close(s.events); there is no
// send-on-closed-channel hazard. We deliberately do not recover here: if the
// single-producer contract is ever violated, a stray Send racing CloseSend faults
// loudly (a panic, or a race the detector flags) instead of being silently masked.
// The consumer runs on another goroutine and stops the producer cooperatively via
// Close, which cancels and closes s.closing but never s.events; Send observes that
// here and drops the event.
func (s *ChanStream) Send(ev StreamEvent) {
	select {
	case <-s.done:
		return
	case <-s.closing:
		return
	default:
	}
	select {
	case s.events <- ev:
	case <-s.closing:
	case <-s.done:
	}
}
