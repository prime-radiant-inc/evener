package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm"
)

// servedEvents makes s a served session (a daemon's authoritative event
// consumer) and records its events.
type servedEvents struct {
	mu      sync.Mutex
	events  []events.SessionEvent
	drained chan struct{}
}

func serveFailClosedSession(s *Session) *servedEvents {
	served := &servedEvents{drained: make(chan struct{})}
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		served.mu.Lock()
		served.events = append(served.events, ev)
		served.mu.Unlock()
	}, func() { close(served.drained) })
	return served
}

// settle closes the session and returns every event it emitted.
func (e *servedEvents) settle(s *Session) []events.SessionEvent {
	s.Close()
	<-e.drained
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]events.SessionEvent(nil), e.events...)
}

func failClosedDiagnostics(evs []events.SessionEvent) int {
	count := 0
	for _, ev := range evs {
		if data, ok := ev.Data.(events.ErrorData); ok && ev.Kind == events.EventError && data.Cause != nil && data.Cause.Kind == failClosedCause {
			count++
		}
	}
	return count
}

func countKind(evs []events.SessionEvent, kind events.EventKind) int {
	count := 0
	for _, ev := range evs {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

func newFailClosedSession(t *testing.T, fault func(string) error) (*Session, *executionAdapter) {
	t.Helper()
	adapter := &executionAdapter{}
	client := llm.NewClient()
	client.Register(adapter)
	s := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		LLMRetryPolicy:   &llm.RetryPolicy{MaxRetries: 2},
		LLMSleep:         func(context.Context, time.Duration) error { return nil },
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, sessionInitFault: fault},
	}))
	return s, adapter
}

// A served session whose transcript could not be created refuses its input,
// with one diagnostic, however often input arrives.
func TestAServedSessionWithNoTranscriptFailsClosed(t *testing.T) {
	s, _ := newFailClosedSession(t, func(point string) error {
		if point == "new_transcript" {
			return errors.New("disk full")
		}
		return nil
	})
	served := serveFailClosedSession(s)
	for range 2 {
		if _, err := s.ProcessInput(context.Background(), "hello", nil); !errors.Is(err, errTranscriptFailedClosed) {
			t.Fatalf("input on a failed-closed session = %v, want the fail-closed refusal", err)
		}
	}
	evs := served.settle(s)
	if got := failClosedDiagnostics(evs); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1", got)
	}
	if countKind(evs, events.EventUserInput) != 0 {
		t.Fatal("a failed-closed session ran an input")
	}
}

// A communicate message whose entry is not recorded is never announced: the
// served session fails closed, and the running execution is interrupted.
func TestAnUnrecordedCommunicateFailsAServedSessionClosed(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	adapter.script(func(context.Context) (llm.Response, error) {
		// The writer stops recording before the round's communicate runs.
		if err := s.attachedTranscript().Close(); err != nil {
			t.Error(err)
		}
		return communicateResponse(true, "never delivered"), nil
	})
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err == nil {
		t.Fatal("the input completed after its communicate was not recorded")
	}
	if _, err := s.ProcessInput(context.Background(), "again", nil); !errors.Is(err, errTranscriptFailedClosed) {
		t.Fatalf("input after failing closed = %v, want the fail-closed refusal", err)
	}
	evs := served.settle(s)
	if got := countKind(evs, events.EventCommunicate); got != 0 {
		t.Fatalf("%d communicate events announced a message the transcript never recorded", got)
	}
	if got := failClosedDiagnostics(evs); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1", got)
	}
}

// Failing closed interrupts the execution that is running.
func TestFailingClosedInterruptsTheRunningExecution(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	inCall := make(chan struct{})
	adapter.script(func(ctx context.Context) (llm.Response, error) {
		close(inCall)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	})
	processed := make(chan error, 1)
	go func() {
		_, err := s.ProcessInput(context.Background(), "long", nil)
		processed <- err
	}()
	<-inCall
	s.failClosed(errors.New("writer poisoned"))
	if err := <-processed; err == nil {
		t.Fatal("the running execution completed after the session failed closed")
	}
	evs := served.settle(s)
	if got := failClosedDiagnostics(evs); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1", got)
	}
}

// An unserved session keeps today's behavior: the message is announced and
// no fail-closed diagnostic appears.
func TestAnUnservedSessionDeliversAnUnrecordedCommunicate(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	evs := drainEvents(s)
	adapter.script(func(context.Context) (llm.Response, error) {
		if err := s.attachedTranscript().Close(); err != nil {
			t.Error(err)
		}
		return communicateResponse(true, "delivered anyway"), nil
	})
	_, _ = s.ProcessInput(context.Background(), "talk", nil)
	s.Close()
	got := evs()
	if countKind(got, events.EventCommunicate) != 1 || failClosedDiagnostics(got) != 0 {
		t.Fatalf("communicate events %d, fail-closed diagnostics %d; want 1 and 0", countKind(got, events.EventCommunicate), failClosedDiagnostics(got))
	}
}
