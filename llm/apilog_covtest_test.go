package llm

import (
	"context"
	"errors"
	"testing"

	apilog "primeradiant.com/evener/llm/apilog"
)

func TestCovObservedAPILogErrorAPILogFailureWasObserved(t *testing.T) {
	cause := errors.New("fail")
	err := markAPILogErrorObserved(cause)
	if err.Error() != cause.Error() {
		t.Fatalf("observed API-log error = %q, want %q", err, cause)
	}
	var observed apiLogObservedFailure
	if !errors.As(err, &observed) {
		t.Fatalf("markAPILogErrorObserved result should satisfy apiLogObservedFailure: %T", err)
	}
	observed.apiLogFailureWasObserved()
}

func TestCovPumpClosingChannelReturn(t *testing.T) {
	events := make(chan StreamEvent)
	closing := make(chan struct{})
	close(closing)
	stream := &apiAttemptSettlementStream{
		inner:   &pumpTestStream{events: events},
		ctx:     context.Background(),
		group:   NewAPIAttemptGroup("ag_pump_close"),
		out:     make(chan StreamEvent),
		done:    make(chan struct{}),
		closing: closing,
	}

	stream.pump()

	if _, ok := <-stream.done; ok {
		t.Fatal("pump done channel remained open")
	}
	if _, ok := <-stream.out; ok {
		t.Fatal("pump output channel remained open")
	}
}

func TestCovPumpStreamEventError(t *testing.T) {
	providerErr := errors.New("provider error")
	events := make(chan StreamEvent, 1)
	events <- StreamEvent{Type: StreamEventError, Err: providerErr}
	close(events)

	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_pump_error")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	stream := &apiAttemptSettlementStream{
		inner:   &pumpTestStream{events: events},
		ctx:     ctx,
		group:   group,
		out:     make(chan StreamEvent, 1),
		done:    make(chan struct{}),
		closing: make(chan struct{}),
	}

	stream.pump()

	gotEvent, ok := <-stream.out
	if !ok {
		t.Fatal("pump emitted no provider error event")
	}
	if gotEvent.Type != StreamEventError || !errors.Is(gotEvent.Err, providerErr) {
		t.Fatalf("pump event = %#v, want StreamEventError carrying provider error", gotEvent)
	}
	if _, ok := <-stream.out; ok {
		t.Fatal("pump output channel remained open after input closed")
	}
	attempts, settlements, _ := sink.snapshot()
	if len(attempts) != 0 {
		t.Fatalf("attempt count = %d, want 0", len(attempts))
	}
	if len(settlements) != 1 || settlements[0].AttemptGroupID != group.ID || settlements[0].FinalAttemptCount != 0 || settlements[0].Outcome != apilog.AttemptTransportFail {
		t.Fatalf("settlements = %+v, want one zero-attempt transport failure for %q", settlements, group.ID)
	}
}

func TestCovPumpClosingDuringForward(t *testing.T) {
	events := make(chan StreamEvent)
	closing := make(chan struct{})
	stream := &apiAttemptSettlementStream{
		inner:   &pumpTestStream{events: events},
		ctx:     context.Background(),
		group:   NewAPIAttemptGroup("ag_pump_forward_close"),
		out:     make(chan StreamEvent),
		done:    make(chan struct{}),
		closing: closing,
	}
	go stream.pump()

	events <- StreamEvent{Type: StreamEventTextDelta, Delta: "not forwarded"}
	close(closing)
	<-stream.done

	if event, ok := <-stream.out; ok {
		t.Fatalf("pump forwarded event after closing: %#v", event)
	}
}

type pumpTestStream struct {
	events <-chan StreamEvent
}

func (s *pumpTestStream) Events() <-chan StreamEvent { return s.events }
func (s *pumpTestStream) Close() error               { return nil }
