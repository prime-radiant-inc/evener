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
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
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
	return newFailClosedSessionWithClock(t, fault, nil)
}

// newFailClosedSessionWithClock is newFailClosedSession with an injectable
// clock, set at construction: the durability-retry tests need the session's
// own clock to be the fake one from the start, since a background timer that
// captured clock.Real() before a later `s.clock = clk` swap would park on the
// wrong clock and desync a test's BlockUntil/Advance driving. A nil clk keeps
// the ordinary real-clock default.
func newFailClosedSessionWithClock(t *testing.T, fault func(string) error, clk clock.Clock) (*Session, *executionAdapter) {
	t.Helper()
	adapter := &executionAdapter{}
	client := llm.NewClient()
	client.Register(adapter)
	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		LLMRetryPolicy:   &llm.RetryPolicy{MaxRetries: 2},
		LLMSleep:         func(context.Context, time.Duration) error { return nil },
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, sessionInitFault: fault},
	}
	if clk != nil {
		cfg.clock = clk
	}
	s := newSession(t, withClient(client), withConfig(cfg))
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
// fails closed, and the unrecorded message is not announced either --
// history would otherwise show a message the transcript never has.
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
	evs := served.settle(s)
	if got := failClosedDiagnostics(evs); got != 0 {
		t.Fatalf("%d fail-closed diagnostics for a closed transcript, want 0", got)
	}
	if got := countKind(evs, events.EventCommunicate); got != 0 {
		t.Fatalf("%d communicate events announced a message the closed writer never recorded", got)
	}
}

// A served session with no transcript defensively fails closed if
// deliverCommunicate is ever reached directly, even though admission (the
// create-failure check in failClosedOnUnhealthyTranscript) already refuses
// every input before any turn runs, so this path is not reachable through
// ProcessInput today (TestAServedSessionWithNoTranscriptFailsClosed). Pinned
// so deliverCommunicate stays correct on its own, independent of its callers.
func TestDeliverCommunicateFailsClosedWithNoTranscript(t *testing.T) {
	s, _ := newFailClosedSession(t, func(point string) error {
		if point == "new_transcript" {
			return errors.New("disk full")
		}
		return nil
	})
	served := serveFailClosedSession(s)
	if s.attachedTranscript() != nil {
		t.Fatal("setup: expected no writer")
	}
	if err := s.deliverCommunicate(events.CommunicateData{CallID: "comm-no-writer", EndTurn: true, Message: "lost"}); !errors.Is(err, errTranscriptFailedClosed) {
		t.Fatalf("deliverCommunicate with no writer = %v, want the fail-closed refusal", err)
	}
	evs := served.settle(s)
	if got := countKind(evs, events.EventCommunicate); got != 0 {
		t.Fatalf("%d communicate events announced with no writer to record them", got)
	}
	if got := failClosedDiagnostics(evs); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1", got)
	}
}

// A COMMUNICATE that reaches delivery after the session already failed
// closed -- the running execution was cancelled but the tool call was
// mid-flight -- is refused immediately, without a second attempt to record
// or a second diagnostic.
func TestDeliverCommunicateAfterAlreadyFailedClosedDoesNotReannounce(t *testing.T) {
	s, _ := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	s.failClosed(errors.New("earlier cause"))
	if err := s.deliverCommunicate(events.CommunicateData{CallID: "comm-late", EndTurn: true, Message: "too late"}); !errors.Is(err, errTranscriptFailedClosed) {
		t.Fatalf("deliverCommunicate on an already failed-closed session = %v, want the fail-closed refusal", err)
	}
	evs := served.settle(s)
	if got := countKind(evs, events.EventCommunicate); got != 0 {
		t.Fatalf("%d communicate events announced after the session failed closed", got)
	}
	if got := failClosedDiagnostics(evs); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1 (no re-announce)", got)
	}
}

// trackExecutionCancel's cleanup only clears its own tracked cancel: a
// finishing execution overlapping a newer one must not clear the newer
// execution's cancel out from under it, or a later failClosed could not
// interrupt the run still in flight.
func TestTrackExecutionCancelDoesNotClearANewerExecutionsCancel(t *testing.T) {
	s, _ := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	var firstCancelled, secondCancelled bool
	stopFirst := s.trackExecutionCancel(func() { firstCancelled = true })
	stopSecond := s.trackExecutionCancel(func() { secondCancelled = true })
	stopFirst() // the first execution finishes while the second still runs
	s.failClosed(errors.New("writer poisoned"))
	if firstCancelled {
		t.Fatal("the finished execution's own cancel ran")
	}
	if !secondCancelled {
		t.Fatal("failClosed did not cancel the newer execution still tracked")
	}
	stopSecond()
	served.settle(s)
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

// A completion entry that is not recorded fails a served session closed, as
// an unrecorded COMMUNICATE does: a turn's terminal status is never lost.
func TestAnUnrecordedCompletionFailsAServedSessionClosed(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	refuseEntries(t, s, schema.TurnCompletion)
	adapter.script(func(context.Context) (llm.Response, error) {
		return communicateResponse(true, "done"), nil
	})
	_, _ = s.ProcessInput(context.Background(), "finish", nil)
	if _, err := s.ProcessInput(context.Background(), "again", nil); !errors.Is(err, errTranscriptFailedClosed) {
		t.Fatalf("input after an unrecorded completion = %v, want the fail-closed refusal", err)
	}
	if got := failClosedDiagnostics(served.settle(s)); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1", got)
	}
}

// Any other entry whose append rolls back cleanly leaves the writer usable
// and the served session running: the caller's own error handling applies
// (an unrecorded USER_INPUT fails its input), and nothing fails closed. Only
// COMMUNICATE and completions, and a poisoned, missing or never-created
// writer, fail a served session closed.
func TestACleanRollbackOfAnOrdinaryEntryKeepsTheSessionRunning(t *testing.T) {
	s, _ := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	refuseEntries(t, s, schema.TurnUserInput)
	for _, input := range []string{"first", "second"} {
		_, err := s.ProcessInput(context.Background(), input, nil)
		if err == nil || errors.Is(err, errTranscriptFailedClosed) {
			t.Fatalf("input %q with its USER_INPUT rolled back = %v, want the append's own error", input, err)
		}
	}
	if s.attachedTranscript().Poisoned() {
		t.Fatal("a clean rollback poisoned the writer")
	}
	if got := failClosedDiagnostics(served.settle(s)); got != 0 {
		t.Fatalf("%d fail-closed diagnostics, want 0", got)
	}
}

// syncTrackingFs is the real filesystem whose files report, for each entry
// kind, whether a line of that kind was fsynced before the next write.
type syncTrackingFs struct {
	afero.Fs
	mu     *sync.Mutex
	last   *string
	synced map[string]bool
}

func (fs syncTrackingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return syncTrackingFile{File: f, fs: fs}, nil
}

type syncTrackingFile struct {
	afero.File
	fs syncTrackingFs
}

func (f syncTrackingFile) Write(p []byte) (int, error) {
	f.fs.mu.Lock()
	*f.fs.last = ""
	for _, kind := range []schema.TurnKind{schema.TurnCommunicate, schema.TurnCompletion} {
		if bytes.Contains(p, []byte(`"kind":"`+string(kind)+`"`)) {
			*f.fs.last = string(kind)
		}
	}
	f.fs.mu.Unlock()
	return f.File.Write(p)
}

func (f syncTrackingFile) Sync() error {
	f.fs.mu.Lock()
	if *f.fs.last != "" {
		f.fs.synced[*f.fs.last] = true
	}
	f.fs.mu.Unlock()
	return f.File.Sync()
}

// COMMUNICATE and completion entries go through the synced door: each is
// fsynced as it is recorded, so a delivered message or a terminal status
// survives a crash. The writer's buffered door is held to an hour here, so
// only a synced write fsyncs.
func TestCommunicateAndCompletionEntriesAreSynced(t *testing.T) {
	s, adapter := newFailClosedSession(t, nil)
	served := serveFailClosedSession(s)
	tracking := syncTrackingFs{Fs: afero.NewOsFs(), mu: &sync.Mutex{}, last: new(string), synced: map[string]bool{}}
	w, _, err := transcript.OpenWriterForSessionWithFS(tracking, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	w.SyncInterval = time.Hour
	s.mu.Lock()
	previous := s.transcript
	s.transcript = w
	s.mu.Unlock()
	t.Cleanup(func() { _ = previous.Close() })
	adapter.script(func(context.Context) (llm.Response, error) {
		return communicateResponse(true, "synced"), nil
	})
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
		t.Fatal(err)
	}
	served.settle(s)
	tracking.mu.Lock()
	defer tracking.mu.Unlock()
	for _, kind := range []schema.TurnKind{schema.TurnCommunicate, schema.TurnCompletion} {
		if !tracking.synced[string(kind)] {
			t.Errorf("a %s entry was not fsynced as it was recorded", kind)
		}
	}
}

// retainedBarrierFs makes the next appended entry whose line contains marker
// a retained record: its own fsync fails and the rollback that would take it
// back out fails too, so the whole line stays in the file. Every
// EstablishDurability barrier call that follows -- the one recordSynced makes
// inline, and any later retry -- fails as long as remaining > 0, decrementing
// it each time; once remaining reaches 0 a barrier call succeeds.
type retainedBarrierFs struct {
	afero.Fs
	mu         sync.Mutex
	marker     []byte
	pending    bool // the marked line's own sync (and then rollback) still owed
	rollback   bool // the marked line's rollback still owed, after its sync failed
	armed      bool // the marked line is now retained; barrier calls start counting
	remaining  int
	barrierErr error
}

func (fs *retainedBarrierFs) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	return &retainedBarrierFile{File: f, fs: fs}, nil
}

type retainedBarrierFile struct {
	afero.File
	fs *retainedBarrierFs
}

func (file *retainedBarrierFile) Write(p []byte) (int, error) {
	file.fs.mu.Lock()
	if bytes.Contains(p, file.fs.marker) {
		file.fs.pending = true
	}
	file.fs.mu.Unlock()
	return file.File.Write(p)
}

func (file *retainedBarrierFile) Sync() error {
	file.fs.mu.Lock()
	if file.fs.pending {
		file.fs.pending = false
		file.fs.rollback = true
		file.fs.mu.Unlock()
		return errors.New("injected marked-entry sync failure")
	}
	if file.fs.armed && file.fs.remaining > 0 {
		file.fs.remaining--
		err := file.fs.barrierErr
		file.fs.mu.Unlock()
		return err
	}
	file.fs.mu.Unlock()
	return file.File.Sync()
}

func (file *retainedBarrierFile) Truncate(size int64) error {
	file.fs.mu.Lock()
	if file.fs.rollback {
		file.fs.rollback = false
		file.fs.armed = true
		file.fs.mu.Unlock()
		return errors.New("injected marked-entry rollback failure")
	}
	file.fs.mu.Unlock()
	return file.File.Truncate(size)
}

// attachRetainedBarrierWrite swaps s's writer for one whose next write of an
// entry of kind is retained (its own sync and rollback fail), then fails
// EstablishDurability barrierFailures times before a barrier call succeeds.
func attachRetainedBarrierWrite(t *testing.T, s *Session, kind schema.TurnKind, barrierFailures int) {
	t.Helper()
	fs := &retainedBarrierFs{Fs: afero.NewOsFs(), marker: []byte(`"kind":"` + string(kind) + `"`), remaining: barrierFailures, barrierErr: errors.New("injected barrier failure")}
	w, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	w.SyncInterval = time.Hour
	s.mu.Lock()
	previous := s.transcript
	s.transcript = w
	s.mu.Unlock()
	t.Cleanup(func() { _ = previous.Close() })
}

// driveDurabilityRetries advances clk once per pending retry attempt, up to
// attempts times, waiting for the retry goroutine to park on the clock before
// each advance -- deterministic, no wall-clock sleep.
// driveDurabilityRetries advances clk once per pending retry attempt, up to
// attempts times, waiting for the retry goroutine to park on the clock before
// each advance -- deterministic, no wall-clock sleep. baseline is the number
// of unrelated waiters already parked on clk (a session's own background
// timers, such as its worktree-lane sweep) before the retry goroutine adds
// its own, so BlockUntil waits for baseline+1 rather than being satisfied by
// those unrelated waiters alone.
func driveDurabilityRetries(clk *agenttest.FakeClock, baseline, attempts int) {
	for range attempts {
		clk.BlockUntil(baseline + 1)
		clk.Advance(durabilityRetryMaxDelay)
	}
}

// A completion (or COMMUNICATE) entry whose fsync and rollback both fail is
// retained: adopted and announced immediately. When the durability barrier
// fails a few times and then succeeds, the background retry settles the debt
// without ever failing the served session closed, and the entry appears
// exactly once in history.
func TestRetainedCompletionSettlesAfterRetryingTheBarrier(t *testing.T) {
	clk := agenttest.NewFakeClock()
	s, adapter := newFailClosedSessionWithClock(t, nil, clk)
	served := serveFailClosedSession(s)
	attachRetainedBarrierWrite(t, s, schema.TurnCompletion, 2)
	adapter.script(func(context.Context) (llm.Response, error) {
		return communicateResponse(true, "settles"), nil
	})
	baseline := clk.BlockedCount()
	if _, err := s.ProcessInput(context.Background(), "finish", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	driveDurabilityRetries(clk, baseline, 2)
	s.sendersWG.Wait() // the retry goroutine settles or exhausts before this returns
	if _, err := s.ProcessInput(context.Background(), "again", nil); err != nil {
		t.Fatalf("input after the completion settled = %v, want no fail-closed refusal", err)
	}
	evs := served.settle(s)
	if got := failClosedDiagnostics(evs); got != 0 {
		t.Fatalf("%d fail-closed diagnostics, want 0: the barrier eventually settled", got)
	}
	// TURN_COMPLETION entries are transcript-only and never enter in-memory
	// history (sessionHistory), so read the raw file to confirm the retained
	// entry was adopted, not re-appended, once the barrier settled: one
	// completion per ProcessInput call (two turns ran here), each at its own
	// ordinal.
	var ordinals []int
	completions := 0
	for _, entry := range decodeTranscriptEntries(t, afero.NewOsFs(), s.TranscriptPath()) {
		if entry.Turn.Kind == schema.TurnCompletion {
			completions++
			ordinals = append(ordinals, entry.Seq)
		}
	}
	if completions != 2 {
		t.Fatalf("%d completion entries on disk, want 2 (one per turn)", completions)
	}
	if ordinals[0] == ordinals[1] {
		t.Fatalf("both completion entries at seq %d: the retained one was re-appended", ordinals[0])
	}
}

// When the durability barrier never succeeds, the retry budget exhausts and a
// served session fails closed: a completion's terminal status must never be
// left undurable forever.
func TestRetainedCompletionFailsAServedSessionClosedAfterExhaustingRetries(t *testing.T) {
	clk := agenttest.NewFakeClock()
	s, adapter := newFailClosedSessionWithClock(t, nil, clk)
	served := serveFailClosedSession(s)
	attachRetainedBarrierWrite(t, s, schema.TurnCompletion, durabilityRetryAttempts+1)
	adapter.script(func(context.Context) (llm.Response, error) {
		return communicateResponse(true, "never settles"), nil
	})
	baseline := clk.BlockedCount()
	if _, err := s.ProcessInput(context.Background(), "finish", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	driveDurabilityRetries(clk, baseline, durabilityRetryAttempts)
	s.sendersWG.Wait() // the retry goroutine fails closed (or not) before this returns
	if _, err := s.ProcessInput(context.Background(), "again", nil); !errors.Is(err, errTranscriptFailedClosed) {
		t.Fatalf("input after exhausted retries = %v, want the fail-closed refusal", err)
	}
	if got := failClosedDiagnostics(served.settle(s)); got != 1 {
		t.Fatalf("%d fail-closed diagnostics, want 1", got)
	}
}

// An unserved session keeps warn-and-continue even when the durability
// barrier never succeeds: the retry runs, exhausts, and the session keeps
// running -- only a served session's fail-closed rule applies. Marked at the
// completion entry (not COMMUNICATE): once durability is never established,
// w.lastSync never advances, so every later synced write in the SAME turn
// (the completion that follows a COMMUNICATE) would also retry its own
// barrier, tangling this test's attempt count with a second retry goroutine;
// the completion is the turn's last synced write, so marking it keeps this
// test to the one retry loop the assertions below drive.
func TestRetainedCompletionOnAnUnservedSessionKeepsRunningAfterExhaustingRetries(t *testing.T) {
	clk := agenttest.NewFakeClock()
	s, adapter := newFailClosedSessionWithClock(t, nil, clk)
	evs := drainEvents(s)
	attachRetainedBarrierWrite(t, s, schema.TurnCompletion, durabilityRetryAttempts+1)
	adapter.script(func(context.Context) (llm.Response, error) {
		return communicateResponse(true, "delivered anyway"), nil
	})
	baseline := clk.BlockedCount()
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	driveDurabilityRetries(clk, baseline, durabilityRetryAttempts)
	s.sendersWG.Wait() // the retry goroutine exhausts (or not) before this returns
	if _, err := s.ProcessInput(context.Background(), "again", nil); err != nil {
		t.Fatalf("input on an unserved session after exhausted retries = %v, want it to keep running", err)
	}
	s.Close()
	got := evs()
	if countKind(got, events.EventCommunicate) < 1 {
		t.Fatal("an unserved session's communicate was not announced")
	}
	if failClosedDiagnostics(got) != 0 {
		t.Fatal("an unserved session failed closed after exhausting durability retries")
	}
}
