package llm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	apilog "primeradiant.com/evener/llm/apilog"
)

func TestMarkAPILogErrorObservedNil(t *testing.T) {
	if got := markAPILogErrorObserved(nil); got != nil {
		t.Fatalf("markAPILogErrorObserved(nil) = %v, want nil", got)
	}
}

// NewAPILogger must refuse a target whose directory cannot exist because a
// regular file occupies a path component: the error is the directory check's
// own path error, and no logger comes back.
func TestNewAPILoggerRejectsAnUnusableDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("a file, not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	logger, err := NewAPILogger(filepath.Join(blocker, "sub", "api.jsonl"))
	if err == nil || logger != nil {
		t.Fatalf("NewAPILogger under a file path = (%v, %v), want an error and no logger", logger, err)
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Fatalf("err = %v, want the directory check's path error", err)
	}
}

func TestNewSessionAPILogDirError(t *testing.T) {
	// A sessions dir under a regular file cannot be created.
	regular := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewSessionAPILogger(regular)
	if err == nil {
		t.Fatal("NewSessionAPILogger under a regular file should error")
	}
}

// A session file that failed to open for a reason that will not clear on its
// own stays failed: later calls report it unavailable without reopening.
func TestSessionFileWithErrorNilCache(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	defer logger.Close()

	openErr := errors.New("permission denied")
	opens := 0
	oldOpen := apiLogOpenFile
	apiLogOpenFile = func(string) (*os.File, error) {
		opens++
		return nil, openErr
	}
	defer func() { apiLogOpenFile = oldOpen }()

	if _, err := logger.sessionFileWithError("test-sess"); !errors.Is(err, openErr) {
		t.Fatalf("first sessionFileWithError = %v, want the open failure", err)
	}
	if _, err := logger.sessionFileWithError("test-sess"); err == nil {
		t.Fatal("sessionFileWithError after a cached failure = nil, want unavailable")
	}
	if opens != 1 {
		t.Fatalf("open attempts = %d, want 1 (the failure is cached)", opens)
	}
}

func TestReserveSessionNoSessionsDir(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()
	if err := logger.ReserveSession("sess"); err == nil {
		t.Fatal("ReserveSession on non-session logger should error")
	}
}

func TestReleaseSessionAfterClose(t *testing.T) {
	logger, err := NewSessionAPILogger(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	if err := logger.ReserveSession("sess-a"); err != nil {
		t.Fatalf("ReserveSession: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := logger.ReleaseSession("sess-a"); !errors.Is(err, errAPILoggerClosed) {
		t.Fatalf("ReleaseSession after Close = %v, want errAPILoggerClosed", err)
	}
}

func TestReleaseSessionEmptySessionsDir(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()
	if err := logger.ReleaseSession("sess"); err != nil {
		t.Fatalf("ReleaseSession on non-session logger = %v, want nil", err)
	}
}

func TestReleaseSessionNotFound(t *testing.T) {
	logger, err := NewSessionAPILogger(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	defer logger.Close()
	if err := logger.ReleaseSession("never-reserved"); err != nil {
		t.Fatalf("ReleaseSession for unreserved session = %v, want nil", err)
	}
}

func TestReleaseSessionNilFile(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	defer logger.Close()

	// Make sessions dir read-only so the file open fails and nil is cached.
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := os.Chmod(sessionsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(sessionsDir, 0o700)

	_ = logger.ReserveSession("fail-sess")
	if err := os.Chmod(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := logger.ReleaseSession("fail-sess"); err != nil {
		t.Fatalf("ReleaseSession for nil-file session = %v, want nil", err)
	}
}

func TestRecoverCanonicalAPILogTailTruncateError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	writeCanonicalAttempt(t, path, standaloneCanonicalAttempt("ag_trunc_err", 1))
	appendAPILogCrashTail(t, path, []byte(`{"kind":"api_attempt"`))

	// A read-only descriptor cannot be truncated.
	roFile, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer roFile.Close()
	if err := recoverCanonicalAPILogTail(roFile, canonicalAPILogMaxLineBytes); err == nil {
		t.Fatal("recoverCanonicalAPILogTail on read-only file should error on truncate")
	}
}

// A stream that never starts still ends the logical call it owns: a failed
// setup settles the group as a transport failure, and a provider that hands
// back no stream settles it as a success.
func TestWrapStreamSettlesAnOwnedGroupWithoutAStream(t *testing.T) {
	streamErr := errors.New("stream setup failed")
	for _, tc := range []struct {
		name    string
		err     error
		outcome apilog.AttemptOutcomeClass
	}{
		{"setup fails", streamErr, apilog.AttemptTransportFail},
		{"no stream", nil, apilog.AttemptSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "api.jsonl")
			logger, err := NewAPILogger(path)
			if err != nil {
				t.Fatalf("NewAPILogger: %v", err)
			}
			wrapped := logger.WrapStream(func(context.Context, Request) (Stream, error) {
				return nil, tc.err
			})
			stream, err := wrapped(context.Background(), Request{})
			if stream != nil || !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
				t.Fatalf("WrapStream = (%v, %v), want (nil, %v)", stream, err, tc.err)
			}
			if err := logger.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			var settlements []apilog.APIAttemptGroupSettlement
			for _, record := range readLogicalGroupRecords(t, path) {
				if s, ok := record.(apilog.APIAttemptGroupSettlement); ok {
					settlements = append(settlements, s)
				}
			}
			if len(settlements) != 1 || settlements[0].Outcome != tc.outcome || settlements[0].FinalAttemptCount != 0 {
				t.Fatalf("settlements = %+v, want one zero-attempt %s settlement", settlements, tc.outcome)
			}
		})
	}
}

func TestObserveClosedAppendCoordinatorManaged(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	if cerr := logger.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	// Credential material in the context marks the append as coordinator
	// managed, so the closed-logger error passes through unobserved.
	ctx := withAPILogCredentialMaterial(context.Background(), APILogCredentialMaterial{})
	failure := APILogFailure{Operation: "append_attempt", Err: errAPILoggerClosed}
	got := logger.observeClosedAppend(ctx, failure)
	if !errors.Is(got, errAPILoggerClosed) {
		t.Fatalf("observeClosedAppend with coordinatorManaged = %v, want errAPILoggerClosed", got)
	}
}

func TestAppendCanonicalRecordNoDestination(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	logger.mu.Lock()
	file := logger.file
	logger.file = nil
	logger.mu.Unlock()
	defer file.Close()
	err = logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_no_dest", 1))
	if err == nil {
		t.Fatal("AppendAttempt with no destination should error")
	}
	_ = logger.Close()
}

func TestCloseFileCloseError(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	file := logger.file
	defer file.Close()

	closeErr := errors.New("close failed")
	oldClose := apiLogFileClose
	apiLogFileClose = func(*os.File) error { return closeErr }
	defer func() { apiLogFileClose = oldClose }()

	if err := logger.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close with a file close error = %v, want that error", err)
	}
}

func TestSettlementStreamPumpStopsWhenClosing(t *testing.T) {
	closing := make(chan struct{})
	close(closing)
	stream := &apiAttemptSettlementStream{
		inner:   &pumpTestStream{events: make(chan StreamEvent)},
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

func TestSettlementStreamPumpSettlesAnErrorEvent(t *testing.T) {
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

func TestSettlementStreamPumpDropsAnEventWhileClosing(t *testing.T) {
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
