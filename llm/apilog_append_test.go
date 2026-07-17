package llm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/identifier"
	apilog "primeradiant.com/serf/llm/apilog"
)

func TestSessionAPILoggerCanonicalPermissionsAndRouting(t *testing.T) {
	t.Run("logger creates private state and sessions directories", func(t *testing.T) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "new-state")
		logger, err := NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger: %v", err)
		}
		appendCanonicalTestRecords(t, logger, "sess-private")
		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		assertPathMode(t, stateDir, 0o700)
		assertPathMode(t, filepath.Join(stateDir, "sessions"), 0o700)
		apiPath := filepath.Join(stateDir, "sessions", "sess-private.api.jsonl")
		assertPathMode(t, apiPath, 0o600)
		assertCanonicalAttemptAndSettlement(t, apiPath)
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", "sess-private.api-raw.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("raw sibling exists: %v", err)
		}
	})

	t.Run("pre-existing shared directories retain exact modes", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "shared-state")
		if err := os.Mkdir(stateDir, 0o751); err != nil {
			t.Fatalf("mkdir state: %v", err)
		}
		if err := os.Chmod(stateDir, 0o751); err != nil {
			t.Fatalf("chmod state: %v", err)
		}
		sessionsDir := filepath.Join(stateDir, "sessions")
		if err := os.Mkdir(sessionsDir, 0o775); err != nil {
			t.Fatalf("mkdir sessions: %v", err)
		}
		if err := os.Chmod(sessionsDir, 0o775); err != nil {
			t.Fatalf("chmod sessions: %v", err)
		}
		logger, err := NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger: %v", err)
		}
		appendCanonicalTestRecords(t, logger, "sess-shared")
		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		assertPathMode(t, stateDir, 0o751)
		assertPathMode(t, sessionsDir, 0o775)
		assertPathMode(t, filepath.Join(sessionsDir, "sess-shared.api.jsonl"), 0o600)
	})

	t.Run("only missing sessions child becomes private", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "shared-state")
		if err := os.Mkdir(stateDir, 0o751); err != nil {
			t.Fatalf("mkdir state: %v", err)
		}
		if err := os.Chmod(stateDir, 0o751); err != nil {
			t.Fatalf("chmod state: %v", err)
		}
		logger, err := NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger: %v", err)
		}
		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		assertPathMode(t, stateDir, 0o751)
		assertPathMode(t, filepath.Join(stateDir, "sessions"), 0o700)
	})

	t.Run("existing permissive API file is repaired", func(t *testing.T) {
		stateDir := t.TempDir()
		sessionsDir := filepath.Join(stateDir, "sessions")
		if err := os.Mkdir(sessionsDir, 0o755); err != nil {
			t.Fatalf("mkdir sessions: %v", err)
		}
		path := filepath.Join(sessionsDir, "sess-repair.api.jsonl")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("create permissive API file: %v", err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod API file: %v", err)
		}
		logger, err := NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger: %v", err)
		}
		appendCanonicalTestRecords(t, logger, "sess-repair")
		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		assertPathMode(t, path, 0o600)
		assertPathMode(t, sessionsDir, 0o755)
	})
}

func TestSessionAPILoggerCanonicalRoutesPerSession(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	first := standaloneCanonicalAttempt("ag_session_a", 1)
	second := standaloneCanonicalAttempt("ag_session_b", 1)
	if err := logger.AppendAttempt(WithAPILogContext(context.Background(), "sess-a"), first); err != nil {
		t.Fatalf("append session A: %v", err)
	}
	if err := logger.AppendAttempt(WithAPILogContext(context.Background(), "sess-b"), second); err != nil {
		t.Fatalf("append session B: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertOnlyCanonicalAttempt(t, filepath.Join(stateDir, "sessions", "sess-a.api.jsonl"), first.AttemptID)
	assertOnlyCanonicalAttempt(t, filepath.Join(stateDir, "sessions", "sess-b.api.jsonl"), second.AttemptID)
}

func TestSessionAPILoggerCanonicalRoutesUnattributed(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	if err := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_unattributed", 1)); err != nil {
		t.Fatalf("append missing session: %v", err)
	}
	unsafeCtx := WithAPILogContext(context.Background(), "../evil")
	if err := logger.AppendAttempt(unsafeCtx, standaloneCanonicalAttempt("ag_unsafe", 1)); err != nil {
		t.Fatalf("append unsafe session: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := canonicalRecordCount(t, filepath.Join(stateDir, "sessions", "unattributed.api.jsonl")); got != 2 {
		t.Fatalf("unattributed record count = %d, want 2", got)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "evil.api.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("unsafe session escaped sessions directory: %v", err)
	}
}

func TestAPILoggerCanonicalPeriodicSync(t *testing.T) {
	t.Run("interval retains dirty records until close", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "api.jsonl")
		logger, err := NewAPILogger(path)
		if err != nil {
			t.Fatalf("NewAPILogger: %v", err)
		}
		logger.SyncInterval = time.Hour
		for index := 1; index <= 5; index++ {
			if err := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_periodic", index)); err != nil {
				t.Fatalf("AppendAttempt %d: %v", index, err)
			}
		}
		logger.mu.Lock()
		dirty := len(logger.dirty)
		logger.mu.Unlock()
		if dirty != 1 {
			t.Fatalf("dirty files = %d, want 1", dirty)
		}
		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := canonicalRecordCount(t, path); got != 5 {
			t.Fatalf("record count after close = %d, want 5", got)
		}
	})

	t.Run("zero interval syncs every append", func(t *testing.T) {
		logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
		if err != nil {
			t.Fatalf("NewAPILogger: %v", err)
		}
		if err := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_immediate", 1)); err != nil {
			t.Fatalf("AppendAttempt: %v", err)
		}
		logger.mu.Lock()
		dirty := len(logger.dirty)
		logger.mu.Unlock()
		if dirty != 0 {
			t.Fatalf("dirty files = %d, want 0", dirty)
		}
		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("expired interval syncs pending records", func(t *testing.T) {
		logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
		if err != nil {
			t.Fatalf("NewAPILogger: %v", err)
		}
		logger.SyncInterval = time.Hour
		if err := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_expired", 1)); err != nil {
			t.Fatalf("first AppendAttempt: %v", err)
		}
		logger.mu.Lock()
		logger.lastSync = time.Now().Add(-2 * time.Hour)
		logger.mu.Unlock()
		if err := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_expired", 2)); err != nil {
			t.Fatalf("second AppendAttempt: %v", err)
		}
		logger.mu.Lock()
		dirty := len(logger.dirty)
		logger.mu.Unlock()
		if dirty != 0 {
			t.Fatalf("dirty files = %d, want 0", dirty)
		}
		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

func TestStampEndpointURL(t *testing.T) {
	StampEndpointURL(nil, "https://example.invalid/api")
	resp := &Response{Raw: map[string]any{"other": 1}}
	StampEndpointURL(resp, "https://endpoint-user:endpoint-password@example.invalid/api?endpoint_token=endpoint-query#endpoint-fragment")
	if resp.Raw["endpoint_url"] != "https://example.invalid/api" || resp.Raw["other"] != 1 {
		t.Fatalf("stamped response raw metadata = %+v", resp.Raw)
	}

	invalid := &Response{}
	StampEndpointURL(invalid, "://not-a-valid-endpoint?endpoint_token=secret")
	if _, ok := invalid.Raw["endpoint_url"]; ok {
		t.Fatalf("invalid endpoint persisted in response metadata: %+v", invalid.Raw)
	}
}

func TestWithAPILogContextAttributesSession(t *testing.T) {
	ctx := WithAPILogContext(context.Background(), "sess-context")
	got, ok := getAPILogContext(ctx)
	if !ok || got.SessionID != "sess-context" {
		t.Fatalf("API log context = (%+v, %t), want session attribution", got, ok)
	}
}

func TestAPILoggerReopenRecoversPartialTailBeforeAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	first := standaloneCanonicalAttempt("ag_reopen_first", 1)
	writeCanonicalAttempt(t, path, first)
	appendAPILogCrashTail(t, path, []byte(`{"kind":"api_attempt","request":{"body":"partial`))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod permissive crash log: %v", err)
	}

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger after partial tail: %v", err)
	}
	second := standaloneCanonicalAttempt("ag_reopen_second", 1)
	if err := logger.AppendAttempt(context.Background(), second); err != nil {
		t.Fatalf("AppendAttempt after partial tail: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	assertPathMode(t, path, 0o600)
	assertCanonicalAttemptIDs(t, path, first.AttemptID, second.AttemptID)
}

func TestSessionAPILoggerReopenRecoversPartialTailBeforeAppend(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := "sess-reopen"
	path := filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
	if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	first := standaloneCanonicalAttempt("ag_session_reopen_first", 1)
	writeCanonicalAttempt(t, path, first)
	appendAPILogCrashTail(t, path, []byte(`{"kind":"attempt_group_settlement"`))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod permissive crash log: %v", err)
	}

	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	second := standaloneCanonicalAttempt("ag_session_reopen_second", 1)
	ctx := WithAPILogContext(context.Background(), sessionID)
	if err := logger.AppendAttempt(ctx, second); err != nil {
		t.Fatalf("AppendAttempt after partial tail: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	assertPathMode(t, path, 0o600)
	assertCanonicalAttemptIDs(t, path, first.AttemptID, second.AttemptID)
}

func TestAPILoggerReopenFailsClosedOnInvalidCompleteLine(t *testing.T) {
	tests := []struct {
		name string
		body func(t *testing.T, path string)
	}{
		{
			name: "corrupt",
			body: func(t *testing.T, path string) {
				t.Helper()
				writeCanonicalAttempt(t, path, standaloneCanonicalAttempt("ag_valid_prefix", 1))
				appendAPILogCrashTail(t, path, []byte("{broken}\n"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/single-file", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "api.jsonl")
			tt.body(t, path)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read invalid log: %v", err)
			}
			if logger, err := NewAPILogger(path); err == nil {
				_ = logger.Close()
				t.Fatal("NewAPILogger accepted an invalid complete line")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read rejected log: %v", err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("rejected log bytes changed:\n before: %q\n  after: %q", before, after)
			}
		})

		t.Run(tt.name+"/per-session", func(t *testing.T) {
			stateDir := t.TempDir()
			sessionID := "sess-invalid"
			path := filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("mkdir sessions: %v", err)
			}
			tt.body(t, path)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read invalid log: %v", err)
			}
			logger, err := NewSessionAPILogger(stateDir)
			if err != nil {
				t.Fatalf("NewSessionAPILogger: %v", err)
			}
			ctx := WithAPILogContext(context.Background(), sessionID)
			if err := logger.AppendAttempt(ctx, standaloneCanonicalAttempt("ag_rejected", 1)); err == nil {
				t.Fatal("AppendAttempt accepted an invalid complete line")
			}
			if err := logger.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read rejected log: %v", err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("rejected log bytes changed:\n before: %q\n  after: %q", before, after)
			}
		})
	}
}

func TestRecoverCanonicalAPILogTailFailsClosedOnOversizedCompleteLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	const maxLineBytes = 64
	data := append([]byte(strings.Repeat("x", maxLineBytes+1)), '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write oversized API log: %v", err)
	}
	before := append([]byte(nil), data...)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open oversized API log: %v", err)
	}
	defer file.Close()
	if err := recoverCanonicalAPILogTail(file, maxLineBytes); err == nil {
		t.Fatal("recovery accepted an oversized complete line")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rejected oversized API log: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("rejected oversized API-log bytes changed:\n before: %q\n  after: %q", before, after)
	}
}

func TestAPILoggerCloseWaitsForAdmittedCanonicalAppendAndRejectsLateAppend(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldSync := apiLogFileSync
	t.Cleanup(func() { apiLogFileSync = oldSync })
	syncEntered := make(chan struct{})
	releaseSync := make(chan struct{})
	var blockOnce sync.Once
	apiLogFileSync = func(file *os.File) error {
		blockOnce.Do(func() {
			close(syncEntered)
			<-releaseSync
		})
		return file.Sync()
	}

	appendDone := make(chan error, 1)
	go func() {
		appendDone <- logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_close", 1))
	}()
	<-syncEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- logger.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before admitted append/sync: %v", err)
	default:
	}
	close(releaseSync)
	if err := <-appendDone; err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}

	failures := make(chan APILogFailure, 1)
	logger.SetFailureObserver(func(failure APILogFailure) { failures <- failure })
	late := standaloneCanonicalAttempt("ag_close", 2)
	if err := logger.AppendAttempt(context.Background(), late); err == nil {
		t.Fatal("late AppendAttempt after Close succeeded")
	}
	select {
	case failure := <-failures:
		if failure.Operation != "append_attempt" || failure.AttemptGroupID != late.AttemptGroupID || failure.AttemptID != late.AttemptID {
			t.Fatalf("late-append failure = %+v", failure)
		}
	default:
		t.Fatal("late AppendAttempt was not reported to the failure observer")
	}
}

func TestAPILoggerCanonicalSyncFailureObserved(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	syncErr := errors.New("sync failed")
	oldSync := apiLogFileSync
	apiLogFileSync = func(*os.File) error { return syncErr }
	t.Cleanup(func() { apiLogFileSync = oldSync })
	failures := make(chan APILogFailure, 1)
	logger.SetFailureObserver(func(failure APILogFailure) { failures <- failure })
	group := NewAPIAttemptGroup("ag_sync_failure")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), logger)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	select {
	case failure := <-failures:
		if failure.Operation != "append_attempt" || failure.AttemptGroupID != group.ID || failure.AttemptID == "" || !errors.Is(failure.Err, syncErr) {
			t.Fatalf("sync failure = %+v", failure)
		}
	default:
		t.Fatal("sync failure was not reported")
	}
	apiLogFileSync = oldSync
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestAPILoggerCanonicalSyncFailureOwnedByAffectedSessionGroup(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	logger.SyncInterval = time.Hour
	var failures []APILogFailure
	logger.SetFailureObserver(func(failure APILogFailure) { failures = append(failures, failure) })
	syncErr := errors.New("session A sync failed")
	oldSync := apiLogFileSync
	apiLogFileSync = func(file *os.File) error {
		if strings.HasSuffix(file.Name(), "sess-a.api.jsonl") {
			return syncErr
		}
		return oldSync(file)
	}
	t.Cleanup(func() { apiLogFileSync = oldSync })

	completeCanonicalGroup(t, logger, "sess-a", "ag_session_a_sync", time.Unix(1_700_000_000, 0).UTC())
	logger.mu.Lock()
	logger.lastSync = time.Now().Add(-2 * time.Hour)
	logger.mu.Unlock()
	completeCanonicalGroup(t, logger, "sess-b", "ag_session_b_sync", time.Unix(1_700_000_001, 0).UTC())

	apiLogFileSync = oldSync
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(failures) != 2 {
		t.Fatalf("sync failure observer calls = %d, want attempt and settlement for session A: %+v", len(failures), failures)
	}
	for _, failure := range failures {
		if failure.SessionID != "sess-a" || failure.AttemptGroupID != "ag_session_a_sync" || !errors.Is(failure.Err, syncErr) {
			t.Fatalf("sync failure attributed to unaffected group: %+v", failure)
		}
	}
	if failures[0].Operation != "append_attempt" || failures[0].AttemptID == "" {
		t.Fatalf("attempt sync failure identity = %+v", failures[0])
	}
	if failures[1].Operation != "append_settlement" || failures[1].AttemptID != "" {
		t.Fatalf("settlement sync failure identity = %+v", failures[1])
	}

	settlementA := readOnlyCanonicalSettlement(t, filepath.Join(stateDir, "sessions", "sess-a.api.jsonl"))
	if settlementA.ForensicIncomplete {
		t.Fatalf("session A append-only settlement was rewritten: %+v", settlementA)
	}
	settlementB := readOnlyCanonicalSettlement(t, filepath.Join(stateDir, "sessions", "sess-b.api.jsonl"))
	if settlementB.ForensicIncomplete {
		t.Fatalf("session B settlement = %+v, want complete forensics", settlementB)
	}
}

func TestAPILoggerCloseObservesPendingCanonicalSyncFailureIdentity(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	logger.SyncInterval = time.Hour
	var failures []APILogFailure
	logger.SetFailureObserver(func(failure APILogFailure) { failures = append(failures, failure) })
	group := NewAPIAttemptGroup("ag_close_sync")
	ctx := WithAPILogContext(context.Background(), "sess-close-sync")
	ctx = WithAPIAttemptSink(WithAPIAttemptGroup(ctx, group), logger)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))

	syncErr := errors.New("close sync failed")
	oldSync := apiLogFileSync
	apiLogFileSync = func(file *os.File) error {
		if strings.HasSuffix(file.Name(), "sess-close-sync.api.jsonl") {
			return syncErr
		}
		return oldSync(file)
	}
	t.Cleanup(func() { apiLogFileSync = oldSync })
	if err := logger.Close(); !errors.Is(err, syncErr) {
		t.Fatalf("Close error = %v, want %v", err, syncErr)
	}
	if len(failures) != 1 {
		t.Fatalf("failure observer calls = %d, want 1: %+v", len(failures), failures)
	}
	failure := failures[0]
	if failure.Operation != "append_attempt" || failure.SessionID != "sess-close-sync" || failure.AttemptGroupID != group.ID || failure.AttemptID == "" || !errors.Is(failure.Err, syncErr) {
		t.Fatalf("close sync failure identity = %+v", failure)
	}
}

func TestAPILoggerOrdinaryMiddlewareCreatesAndSettlesCanonicalGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	want := Response{ID: "canonical-response", Model: "model-a", Provider: "primary", Message: Assistant("ok"), Finish: FinishReason{Reason: FinishReasonStop}}
	got, err := logger.WrapComplete(func(ctx context.Context, _ Request) (Response, error) {
		BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
		return want, nil
	})(context.Background(), Request{Model: "model-a", Provider: "primary"})
	if err != nil || got.ID != want.ID {
		t.Fatalf("WrapComplete = (%+v, %v)", got, err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertCanonicalAttemptAndSettlement(t, path)
}

func TestAPILoggerOrdinaryMiddlewareSettlesZeroAttemptFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	wantErr := errors.New("request rejected before transport")
	_, gotErr := logger.WrapComplete(func(context.Context, Request) (Response, error) {
		return Response{}, wantErr
	})(context.Background(), Request{})
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("WrapComplete error = %v, want %v", gotErr, wantErr)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	settlement := readOnlyCanonicalSettlement(t, path)
	if settlement.FinalAttemptID != "" || settlement.FinalAttemptCount != 0 || settlement.Outcome != apilog.AttemptTransportFail {
		t.Fatalf("zero-attempt settlement = %+v", settlement)
	}
}

func TestAPILoggerStreamSettlesOnlyAfterTerminalEventAndAttemptAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	inner := &controlledAPILogStream{events: make(chan StreamEvent, 1)}
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	var callCtx context.Context
	stream, err := logger.WrapStream(func(ctx context.Context, _ Request) (Stream, error) {
		callCtx = ctx
		return inner, nil
	})(context.Background(), Request{})
	if err != nil {
		t.Fatalf("WrapStream: %v", err)
	}
	if got := canonicalRecordCount(t, path); got != 0 {
		t.Fatalf("records after stream handle return = %d, want 0", got)
	}

	attempt := BeginAPIAttempt(callCtx, testAPIAttemptMeta(startedAt))
	attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	inner.events <- StreamEvent{Type: StreamEventFinish}
	close(inner.events)
	for range stream.Events() {
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("stream Close: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("logger Close: %v", err)
	}
	assertCanonicalAttemptAndSettlement(t, path)
	settlement := readOnlyCanonicalSettlement(t, path)
	if settlement.FinalAttemptID == "" || settlement.FinalAttemptCount != 1 || settlement.Outcome != apilog.AttemptSuccess {
		t.Fatalf("stream settlement = %+v", settlement)
	}
}

type controlledAPILogStream struct {
	events chan StreamEvent
}

func (s *controlledAPILogStream) Events() <-chan StreamEvent { return s.events }
func (s *controlledAPILogStream) Close() error               { return nil }

func canonicalRecordCount(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	count := 0
	for {
		_, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return count
		}
		if err != nil {
			t.Fatalf("decode canonical API log: %v", err)
		}
		count++
	}
}

func writeCanonicalAttempt(t *testing.T, path string, attempt apilog.APIAttemptRecord) {
	t.Helper()
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	if err := logger.AppendAttempt(context.Background(), attempt); err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func appendAPILogCrashTail(t *testing.T, path string, tail []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open API log for crash tail: %v", err)
	}
	if _, err := file.Write(tail); err != nil {
		_ = file.Close()
		t.Fatalf("append API-log crash tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close API log crash tail: %v", err)
	}
}

func assertCanonicalAttemptIDs(t *testing.T, path string, want ...string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer file.Close()
	decoder := apilog.NewDecoder(file, 128<<20)
	for index, wantID := range want {
		record, err := decoder.Next()
		if err != nil {
			t.Fatalf("decode canonical API log record %d: %v", index+1, err)
		}
		attempt, ok := record.(apilog.APIAttemptRecord)
		if !ok || attempt.AttemptID != wantID {
			t.Fatalf("canonical API log record %d = %+v, want attempt %q", index+1, record, wantID)
		}
	}
	if record, err := decoder.Next(); record != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("canonical API log tail = (%T, %v), want clean EOF", record, err)
	}
}

func completeCanonicalGroup(t *testing.T, logger *APILogger, sessionID, groupID string, startedAt time.Time) {
	t.Helper()
	group := NewAPIAttemptGroup(groupID)
	ctx := WithAPILogContext(context.Background(), sessionID)
	ctx = WithAPIAttemptSink(WithAPIAttemptGroup(ctx, group), logger)
	BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	group.Settle(ctx, apilog.AttemptSuccess)
}

func appendCanonicalTestRecords(t *testing.T, logger *APILogger, sessionID string) {
	t.Helper()
	ctx := WithAPILogContext(context.Background(), sessionID)
	attempt := standaloneCanonicalAttempt("ag_permissions", 1)
	if err := logger.AppendAttempt(ctx, attempt); err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}
	if err := logger.AppendSettlement(ctx, apilog.APIAttemptGroupSettlement{
		Kind:              "attempt_group_settlement",
		SchemaVersion:     1,
		AttemptGroupID:    attempt.AttemptGroupID,
		FinalAttemptID:    attempt.AttemptID,
		FinalAttemptCount: 1,
		Outcome:           apilog.AttemptSuccess,
		SettledAt:         time.Unix(1_700_000_001, 0).UTC(),
	}); err != nil {
		t.Fatalf("AppendSettlement: %v", err)
	}
}

func standaloneCanonicalAttempt(groupID string, index int) apilog.APIAttemptRecord {
	return apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   groupID,
		AttemptIndex:     index,
		Timestamp:        time.Unix(1_700_000_000, 0).UTC(),
		LatencyMS:        1,
		ProviderInstance: "primary",
		RequestModel:     "model-a",
		Request: apilog.APIAttemptRequest{
			Method:   http.MethodPost,
			Endpoint: "https://provider.invalid/v1/responses",
			Body:     apilog.EncodeBody([]byte(`{"input":"hello"}`)),
		},
		Response: &apilog.APIAttemptResponse{
			StatusCode: http.StatusOK,
			Body:       apilog.EncodeBody([]byte(`{"output":"hello"}`)),
		},
		Outcome: apilog.AttemptSuccess,
	}
}

func assertPathMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}

func assertCanonicalAttemptAndSettlement(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	if record, err := decoder.Next(); err != nil {
		t.Fatalf("decode canonical attempt: %v", err)
	} else if _, ok := record.(apilog.APIAttemptRecord); !ok {
		t.Fatalf("first canonical record = %T, want APIAttemptRecord", record)
	}
	if record, err := decoder.Next(); err != nil {
		t.Fatalf("decode canonical settlement: %v", err)
	} else if _, ok := record.(apilog.APIAttemptGroupSettlement); !ok {
		t.Fatalf("second canonical record = %T, want APIAttemptGroupSettlement", record)
	}
	if record, err := decoder.Next(); !errors.Is(err, io.EOF) || record != nil {
		t.Fatalf("canonical tail = (%T, %v), want clean EOF", record, err)
	}
}

func assertOnlyCanonicalAttempt(t *testing.T, path, wantAttemptID string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	record, err := decoder.Next()
	if err != nil {
		t.Fatalf("decode canonical attempt: %v", err)
	}
	attempt, ok := record.(apilog.APIAttemptRecord)
	if !ok || attempt.AttemptID != wantAttemptID {
		t.Fatalf("canonical attempt = %+v, want ID %q", record, wantAttemptID)
	}
	if record, err := decoder.Next(); !errors.Is(err, io.EOF) || record != nil {
		t.Fatalf("canonical tail = (%T, %v), want clean EOF", record, err)
	}
}

func readOnlyCanonicalSettlement(t *testing.T, path string) apilog.APIAttemptGroupSettlement {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	var settlements []apilog.APIAttemptGroupSettlement
	for {
		record, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode canonical API log: %v", err)
		}
		if settlement, ok := record.(apilog.APIAttemptGroupSettlement); ok {
			settlements = append(settlements, settlement)
		}
	}
	if len(settlements) != 1 {
		t.Fatalf("settlement count in %s = %d, want 1", path, len(settlements))
	}
	return settlements[0]
}
