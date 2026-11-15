package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/transcriptindex"
)

// These leaf-worker fixtures model the constructor-owned phase explicitly;
// ordinary bare Sessions keep their zero-value, unfenced behavior.
func newStartupFrontierPhase(t *testing.T, constructing bool) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		id: identifier.MustNewSessionID(), stateDir: t.TempDir(),
		events:     make(chan events.SessionEvent, 256),
		sessionCtx: ctx, cancelFunc: cancel, frontierStartupPending: constructing,
	}
	w, err := transcript.NewWriter(transcriptPath(s.stateDir, s.id), transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatal(err)
	}
	s.attachTranscript(w, nil)
	t.Cleanup(func() {
		s.abortFrontierStartup()
		_ = w.Close()
	})
	return s
}

func recordStartupFrontier(t *testing.T, s *Session) {
	t.Helper()
	for _, id := range []string{"turn_m601", "turn_m602"} {
		s.beginExecution(id)
		rec, err := s.attachedTranscript().Record(schema.Turn{Kind: schema.TurnCheckpoint}, transcript.RecordOptions{Place: transcript.PlaceSession})
		if err != nil || !rec.Recorded || rec.Turn.TurnKind != schema.TurnSpanExecution {
			t.Fatalf("startup record=%+v err=%v", rec, err)
		}
		s.completeExecution(schema.TurnCompleted)
	}
	if got := s.LatestExecutionFrontier(); got != (events.ExecutionFrontier{TurnID: "turn_m602", Sequence: 2}) {
		t.Fatalf("held startup commits did not stamp frontier: %+v", got)
	}
}

func joinStartupFrontier(t *testing.T, s *Session) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.frontierEmitterWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-tripwireCtx(t).Done():
		t.Fatal("pre-start emitter did not finish")
	}
	s.executionFrontierMu.Lock()
	defer s.executionFrontierMu.Unlock()
	if !s.frontierStopped || len(s.frontierPending) != 0 {
		t.Fatalf("pre-start cleanup retained worker state: stopped=%v pending=%v", s.frontierStopped, s.frontierPending)
	}
}

func TestExecutionFrontierStartupCleanup(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"constructor-failure", "close", "cancel", "start-not-delivered", "start-after-close"} {
		t.Run(action, func(t *testing.T) {
			s := newStartupFrontierPhase(t, true)
			recordStartupFrontier(t, s)
			switch action {
			case "constructor-failure":
				s.abortFrontierStartup()
			case "close":
				s.closeEventStream(tripwireCtx(t))
			case "start-after-close":
				s.closeEventStream(tripwireCtx(t))
				s.descendantEvent = func(ev events.SessionEvent) { t.Errorf("closed stream forwarded event: %+v", ev) }
				s.emitSessionStartEnvelope(events.SessionStartData{CurrentWork: &events.CurrentWorkSeedData{}}, nil)
			case "cancel":
				s.cancelFunc()
			case "start-not-delivered":
				// A best-effort stream with no consumer can reject the start.
				for range cap(s.events) {
					s.events <- events.SessionEvent{Kind: events.EventPromptLoaded}
				}
				s.emitSessionStartEnvelope(events.SessionStartData{CurrentWork: &events.CurrentWorkSeedData{}}, nil)
			}
			joinStartupFrontier(t, s)
			for {
				select {
				case ev, open := <-s.Events():
					if !open {
						return
					}
					if ev.Kind == events.EventExecutionFirstRecorded || ev.Kind == events.EventSessionStart {
						t.Fatalf("pre-start cleanup published event: %+v", ev)
					}
				default:
					return
				}
			}
		})
	}
}

func TestExecutionFrontierStartupDescendantPublication(t *testing.T) {
	t.Parallel()
	s := newStartupFrontierPhase(t, true)
	forwarded := make(chan events.SessionEvent, 8)
	s.descendantEvent = func(ev events.SessionEvent) {
		if ev.Kind == events.EventSessionStart || ev.Kind == events.EventExecutionFirstRecorded {
			forwarded <- ev
		}
	}
	for range cap(s.events) {
		s.events <- events.SessionEvent{Kind: events.EventPromptLoaded}
	}
	if _, _, delivered := s.sendEventContext(context.Background(), events.PromptLoadedData{}, nil); delivered {
		t.Fatal("channel-only delivery reported success for a full local channel")
	}
	recordStartupFrontier(t, s)
	s.emitSessionStartEnvelope(events.SessionStartData{CurrentWork: &events.CurrentWorkSeedData{}}, nil)
	var first events.SessionEvent
	select {
	case first = <-forwarded:
	case <-tripwireCtx(t).Done():
		t.Fatal("descendant callback did not receive SessionStart")
	}
	if first.Kind != events.EventSessionStart {
		t.Fatalf("descendant callback did not introduce session first: %+v", first)
	}
	s.executionFrontierMu.Lock()
	stopped := s.frontierStopped
	s.executionFrontierMu.Unlock()
	if stopped {
		t.Fatal("SessionStart reached the descendant callback but permanently stopped frontier publication")
	}
	await := func(want string) {
		t.Helper()
		select {
		case ev := <-forwarded:
			data, ok := ev.Data.(events.ExecutionFirstRecordedData)
			if ev.Kind != events.EventExecutionFirstRecorded || !ok || data.TurnID != want {
				t.Fatalf("forwarded event=%+v want first record for %s", ev, want)
			}
		case <-tripwireCtx(t).Done():
			t.Fatalf("descendant did not receive first record for %s", want)
		}
	}
	await("turn_m601")
	await("turn_m602")
	s.beginExecution("turn_m603")
	rec, err := s.attachedTranscript().Record(schema.Turn{Kind: schema.TurnCheckpoint}, transcript.RecordOptions{Place: transcript.PlaceSession})
	if err != nil || !rec.Recorded || rec.Turn.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("runtime record=%+v err=%v", rec, err)
	}
	await("turn_m603")
	s.closeEventStream(tripwireCtx(t))
	select {
	case ev := <-forwarded:
		t.Fatalf("duplicate forwarded event after worker joined: %+v", ev)
	default:
	}
}

func TestExecutionFrontierStartupReleasePrecedesFanout(t *testing.T) {
	t.Parallel()
	s := newStartupFrontierPhase(t, true)
	jm, err := newJobManagerNoSync(s.stateDir, s.id, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.jobManager = jm
	t.Cleanup(func() { _ = jm.closeStoreOnly() })
	jm.mu.Lock()
	started := make(chan struct{})
	go func() {
		s.emitSessionStartEnvelope(events.SessionStartData{CurrentWork: &events.CurrentWorkSeedData{}}, nil)
		close(started)
	}()
	defer func() {
		jm.mu.Unlock()
		select {
		case <-started:
		case <-tripwireCtx(t).Done():
			t.Error("SessionStart did not finish after fan-out was released")
		}
	}()
	select {
	case ev := <-s.Events():
		if ev.Kind != events.EventSessionStart {
			t.Fatalf("event before runtime commit=%+v, want SessionStart", ev)
		}
	case <-tripwireCtx(t).Done():
		t.Fatal("SessionStart did not publish before job fan-out")
	}
	// Start has entered the stream, but its emitter cannot return through the
	// job-manager lock. A new commit must already have runtime publication rules.
	s.beginExecution("turn_m604")
	rec, err := s.attachedTranscript().Record(schema.Turn{Kind: schema.TurnCheckpoint}, transcript.RecordOptions{Place: transcript.PlaceSession})
	if err != nil || !rec.Recorded || rec.Turn.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("runtime record=%+v err=%v", rec, err)
	}
	select {
	case ev := <-s.Events():
		data, ok := ev.Data.(events.ExecutionFirstRecordedData)
		if ev.Kind != events.EventExecutionFirstRecorded || !ok || data.TurnID != "turn_m604" {
			t.Fatalf("post-start signal=%+v", ev)
		}
	case <-tripwireCtx(t).Done():
		t.Fatal("runtime first-record publication waited behind SessionStart fan-out")
	}
}

func TestExecutionFrontierBareSessionPublishesWithoutStart(t *testing.T) {
	t.Parallel()
	s := newStartupFrontierPhase(t, false)
	s.beginExecution("turn_m605")
	rec, err := s.attachedTranscript().Record(schema.Turn{Kind: schema.TurnCheckpoint}, transcript.RecordOptions{Place: transcript.PlaceSession})
	if err != nil || !rec.Recorded || rec.Turn.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("bare-session record=%+v err=%v", rec, err)
	}
	select {
	case ev := <-s.Events():
		data, ok := ev.Data.(events.ExecutionFirstRecordedData)
		if ev.Kind != events.EventExecutionFirstRecorded || !ok || data.TurnID != "turn_m605" {
			t.Fatalf("bare-session signal=%+v", ev)
		}
	case <-tripwireCtx(t).Done():
		t.Fatal("zero-phase Session incorrectly waited for SessionStart")
	}
}

// Admit the second input behind a genuinely recovered and reclaimed start, then
// leave both failure transactions for the next real constructor to record.
func startupFailureSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	original := newQueuePersistTestSession(t, dir)
	t.Cleanup(original.Close)
	first, err := original.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "startup-first", Input: []appwire.InputItem{{Type: "text", Text: "startup-input-one"}}})
	if err != nil {
		t.Fatal(err)
	}
	original.Close()
	recovered := restoreQueuePersistTestSession(t, dir, original.ID())
	t.Cleanup(recovered.Close)
	if _, claimed, err := recovered.claimClientMutationStart(); err != nil || !claimed {
		t.Fatalf("claim recovered start: claimed=%v err=%v", claimed, err)
	}
	second, err := recovered.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "startup-second", Input: []appwire.InputItem{{Type: "text", Text: "startup-input-two"}}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Turn.ID == second.Turn.ID {
		t.Fatal("follow-up did not reserve an independent execution")
	}
	for _, id := range []string{"startup-first", "startup-second"} {
		if err := recovered.beginClientMutationFailure(id, errors.New("startup-fault")); err != nil {
			t.Fatal(err)
		}
	}
	recovered.Close()
	return recovered
}

func TestExecutionFrontierStartupRecoveryPublication(t *testing.T) {
	t.Parallel()
	original := startupFailureSession(t)
	meta, err := schema.LoadSessionMeta(original.stateDir, original.ID())
	if err != nil {
		t.Fatal(err)
	}
	var sample func() events.ExecutionFrontier
	var mu sync.Mutex
	var observed []events.SessionEvent
	var committed []string
	cfg := RestoreSessionConfig{StateDir: original.stateDir, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}
	cfg.spawn.parentSessionID = "startup-owner"
	cfg.spawn.descendantPublication = func(string) TranscriptPublicationHooks {
		return TranscriptPublicationHooks{RegisterFrontier: func(fn func() events.ExecutionFrontier) { sample = fn }}
	}
	cfg.spawn.descendantEvent = func(ev events.SessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, ev)
	}
	cfg.OnRestoredTranscript = func(_ transcript.Header, entries []transcript.Entry, opened bool) {
		if !opened || sample == nil {
			t.Fatal("constructor did not open and register its transcript")
		}
		for _, entry := range entries {
			if entry.Turn.OriginalOrdinal == nil && entry.Turn.TurnKind == schema.TurnSpanExecution {
				committed = append(committed, entry.Turn.TurnID)
			}
		}
		if len(committed) != 2 {
			t.Fatalf("recovery recorded %d executions, want two", len(committed))
		}
		want := events.ExecutionFrontier{TurnID: committed[1], Sequence: 2}
		if got := sample(); got != want {
			t.Fatalf("pre-start committed frontier=%+v want=%+v", got, want)
		}
		index, err := transcriptindex.Open(original.TranscriptPath(), filepath.Join(t.TempDir(), "index"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = index.Close() }()
		window, err := index.Latest(40)
		if err != nil || window.LatestExecutionTurnID != want.TurnID {
			t.Fatalf("pre-start index frontier=%q want=%q err=%v", window.LatestExecutionTurnID, want.TurnID, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(observed) != 0 {
			t.Errorf("construction published before SessionStart: %v", observed)
		}
	}
	restored, err := RestoreSessionFromMetaWithConfig(original.client, original.profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	restored.Close() // The real close joins the worker, no scheduling or flush guess.
	mu.Lock()
	defer mu.Unlock()
	if len(observed) == 0 || observed[0].Kind != events.EventSessionStart {
		t.Errorf("first event did not introduce session: %v", observed)
	}
	var published []string
	for _, ev := range observed {
		if ev.Kind == events.EventExecutionFirstRecorded {
			data, ok := ev.Data.(events.ExecutionFirstRecordedData)
			if !ok {
				t.Fatalf("first-record event lost typed payload: %T", ev.Data)
			}
			published = append(published, data.TurnID)
		}
	}
	if !slices.Equal(published, committed) {
		t.Fatalf("published executions=%v committed=%v, want ordered and exactly once", published, committed)
	}
}
