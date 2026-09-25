package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
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

// entryRefusingFs is the real filesystem whose files refuse, writing
// nothing, every write of an entry of kind: that append records nothing and
// leaves the writer usable, and every other entry records as usual.
type entryRefusingFs struct {
	afero.Fs
	kind schema.TurnKind
}

func (fs entryRefusingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return entryRefusingFile{File: f, marker: []byte(`"kind":"` + string(fs.kind) + `"`)}, nil
}

type entryRefusingFile struct {
	afero.File
	marker []byte
}

func (f entryRefusingFile) Write(p []byte) (int, error) {
	if bytes.Contains(p, f.marker) {
		return 0, errors.New("injected write failure")
	}
	return f.File.Write(p)
}

// toolResultsTearingFs is the real filesystem whose files write only half of
// a TOOL_RESULTS entry and then fail: the partial line poisons the writer.
type toolResultsTearingFs struct{ afero.Fs }

func (fs toolResultsTearingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return toolResultsTearingFile{File: f}, nil
}

type toolResultsTearingFile struct{ afero.File }

func (f toolResultsTearingFile) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"kind":"TOOL_RESULTS"`)) {
		n, _ := f.File.Write(p[:len(p)/2])
		return n, errors.New("injected torn write")
	}
	return f.File.Write(p)
}

// A real append that tears its line poisons the writer: a served session
// fails closed, once, and refuses what comes next.
func TestAPoisonedWriterFailsAServedSessionClosed(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	w, _, err := transcript.OpenWriterForSessionWithFS(toolResultsTearingFs{Fs: afero.NewOsFs()}, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	previous := s.transcript
	s.transcript = w
	s.mu.Unlock()
	t.Cleanup(func() { _ = previous.Close() })
	adapter.script(func(context.Context) (llm.Response, error) {
		return communicateResponse(false, "working"), nil
	})
	if _, err := s.ProcessInput(context.Background(), "tear", nil); err == nil {
		t.Fatal("the input completed on a poisoned transcript")
	}
	if !w.Poisoned() {
		t.Fatal("setup: the torn write did not poison the writer")
	}
	if _, err := s.ProcessInput(context.Background(), "again", nil); !errors.Is(err, errTranscriptFailedClosed) {
		t.Fatalf("input after the poisoning = %v, want the fail-closed refusal", err)
	}
	if got := failClosedDiagnostics(served.settle(s)); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1", got)
	}
}

// refuseCommunicateEntries swaps s's writer for one on the same file whose
// COMMUNICATE appends fail.
func refuseCommunicateEntries(t *testing.T, s *Session) {
	t.Helper()
	refuseEntries(t, s, schema.TurnCommunicate)
}

// refuseEntries swaps s's writer for one on the same file whose appends of
// kind fail.
func refuseEntries(t *testing.T, s *Session, kind schema.TurnKind) {
	t.Helper()
	w, _, err := transcript.OpenWriterForSessionWithFS(entryRefusingFs{Fs: afero.NewOsFs(), kind: kind}, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	previous := s.transcript
	s.transcript = w
	s.mu.Unlock()
	t.Cleanup(func() { _ = previous.Close() })
}

// A served session with no transcript refuses a client turn at its claim, and
// shows its one diagnostic there too: the claim path runs no turn loop.
func TestAFailedClosedSessionAnnouncesAtAClientClaim(t *testing.T) {
	s, _ := newFailClosedSession(t, func(point string) error {
		if point == "new_transcript" {
			return errors.New("disk full")
		}
		return nil
	})
	served := serveFailClosedSession(s)
	s.SetClientMutationStartWakeFunc(func() {})
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-on-a-dead-transcript", ExpectedInstanceID: s.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ProcessClientMutationStart(context.Background(), nil); !errors.Is(err, errTranscriptFailedClosed) {
		t.Fatalf("claim on a failed-closed session = %v, want the fail-closed refusal", err)
	}
	if got := failClosedDiagnostics(served.settle(s)); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1", got)
	}
}

// A communicate message whose entry is not recorded is never announced: the
// served session fails closed, and the running execution is interrupted.
func TestAnUnrecordedCommunicateFailsAServedSessionClosed(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	refuseCommunicateEntries(t, s)
	adapter.script(func(context.Context) (llm.Response, error) {
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

// A communicate that lands after the session closed its transcript (a turn
// finishing while the session shuts down) is not a writer failure: nothing
// fails closed.
func TestACommunicateAfterTheTranscriptClosedDoesNotFailClosed(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	adapter.script(func(context.Context) (llm.Response, error) {
		if err := s.attachedTranscript().Close(); err != nil {
			t.Error(err)
		}
		return communicateResponse(true, "after close"), nil
	})
	_, _ = s.ProcessInput(context.Background(), "talk", nil)
	if got := failClosedDiagnostics(served.settle(s)); got != 0 {
		t.Fatalf("%d fail-closed diagnostics for a closed transcript, want 0", got)
	}
}

// An unserved session keeps today's behavior: the message is announced and
// no fail-closed diagnostic appears.
func TestAnUnservedSessionDeliversAnUnrecordedCommunicate(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	evs := drainEvents(s)
	refuseCommunicateEntries(t, s)
	adapter.script(func(context.Context) (llm.Response, error) {
		return communicateResponse(true, "delivered anyway"), nil
	})
	_, _ = s.ProcessInput(context.Background(), "talk", nil)
	s.Close()
	got := evs()
	if countKind(got, events.EventCommunicate) != 1 || failClosedDiagnostics(got) != 0 {
		t.Fatalf("communicate events %d, fail-closed diagnostics %d; want 1 and 0", countKind(got, events.EventCommunicate), failClosedDiagnostics(got))
	}
}
