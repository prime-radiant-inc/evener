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

func TestAPILoggerRejectsInvalidCanonicalRecordBeforeWrite(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	oldWrite := apiLogFileWrite
	oldSync := apiLogFileSync
	t.Cleanup(func() {
		apiLogFileWrite = oldWrite
		apiLogFileSync = oldSync
	})
	writes := 0
	syncs := 0
	apiLogFileWrite = func(file *os.File, data []byte) (int, error) {
		writes++
		return oldWrite(file, data)
	}
	apiLogFileSync = func(file *os.File) error {
		syncs++
		return oldSync(file)
	}

	record := standaloneCanonicalAttempt("ag_invalid_record", 1)
	record.Outcome = apilog.AttemptOutcomeClass("invented")
	ctx := WithAPILogContext(context.Background(), "sess-invalid-record")
	if err := logger.AppendAttempt(ctx, record); err == nil {
		t.Fatal("AppendAttempt accepted an invalid canonical outcome")
	}
	if writes != 0 || syncs != 0 {
		t.Fatalf("invalid record reached storage: writes=%d syncs=%d", writes, syncs)
	}
	path := filepath.Join(stateDir, "sessions", "sess-invalid-record.api.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid record opened a session leaf: %v", err)
	}
}

func TestAPILoggerCanonicalSyncsEveryAppendBeforeSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldSync := apiLogFileSync
	t.Cleanup(func() { apiLogFileSync = oldSync })
	syncCount := 0
	apiLogFileSync = func(file *os.File) error {
		syncCount++
		return oldSync(file)
	}
	for index := 1; index <= 2; index++ {
		if err := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_sync_each", index)); err != nil {
			t.Fatalf("AppendAttempt %d: %v", index, err)
		}
		if syncCount != index {
			t.Fatalf("sync calls after append %d = %d, want %d", index, syncCount, index)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if syncCount != 2 {
		t.Fatalf("Close added a deferred sync: got %d total calls, want 2", syncCount)
	}
	if got := canonicalRecordCount(t, path); got != 2 {
		t.Fatalf("record count after close = %d, want 2", got)
	}
}

func TestAPILoggerCanonicalAppendUsesOneNewlineTerminatedWrite(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldWrite := apiLogFileWrite
	oldSync := apiLogFileSync
	t.Cleanup(func() {
		apiLogFileWrite = oldWrite
		apiLogFileSync = oldSync
	})
	var writes [][]byte
	apiLogFileWrite = func(file *os.File, data []byte) (int, error) {
		writes = append(writes, append([]byte(nil), data...))
		return oldWrite(file, data)
	}
	syncCount := 0
	apiLogFileSync = func(file *os.File) error {
		syncCount++
		return oldSync(file)
	}

	if err := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_one_write", 1)); err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}
	if len(writes) != 1 {
		t.Fatalf("write calls = %d, want exactly 1", len(writes))
	}
	if !bytes.HasSuffix(writes[0], []byte{'\n'}) || bytes.Count(writes[0], []byte{'\n'}) != 1 {
		t.Fatalf("append bytes are not one newline-terminated record: %q", writes[0])
	}
	if _, err := apilog.DecodeRecord(bytes.TrimSuffix(writes[0], []byte{'\n'})); err != nil {
		t.Fatalf("written record is not canonical: %v", err)
	}
	if syncCount != 1 {
		t.Fatalf("sync calls = %d, want 1", syncCount)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestAPILoggerCanonicalShortWriteIsSticky(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldWrite := apiLogFileWrite
	oldSync := apiLogFileSync
	t.Cleanup(func() {
		apiLogFileWrite = oldWrite
		apiLogFileSync = oldSync
		_ = logger.Close()
	})
	writeCount := 0
	apiLogFileWrite = func(_ *os.File, data []byte) (int, error) {
		writeCount++
		return len(data) - 1, nil
	}
	syncCount := 0
	apiLogFileSync = func(*os.File) error {
		syncCount++
		return nil
	}
	var failures []APILogFailure
	logger.SetFailureObserver(func(failure APILogFailure) { failures = append(failures, failure) })

	first := standaloneCanonicalAttempt("ag_short_write", 1)
	assertDetachedAPILogError(t, logger.AppendAttempt(context.Background(), first), io.ErrShortWrite)
	apiLogFileWrite = oldWrite
	assertDetachedAPILogError(t, logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_after_short_write", 1)), io.ErrShortWrite)
	if writeCount != 1 || syncCount != 0 {
		t.Fatalf("filesystem calls after short write = writes:%d syncs:%d, want 1 and 0", writeCount, syncCount)
	}
	if len(failures) != 1 {
		t.Fatalf("short-write observations = %d, want 1: %+v", len(failures), failures)
	}
	assertDetachedAPILogError(t, logger.Close(), io.ErrShortWrite)
}

func TestAPILoggerCanonicalWriteFailureIsSticky(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldWrite := apiLogFileWrite
	t.Cleanup(func() {
		apiLogFileWrite = oldWrite
		_ = logger.Close()
	})
	writeErr := errors.New("write failed")
	writeCount := 0
	apiLogFileWrite = func(*os.File, []byte) (int, error) {
		writeCount++
		return 0, writeErr
	}
	var failures []APILogFailure
	logger.SetFailureObserver(func(failure APILogFailure) { failures = append(failures, failure) })

	assertDetachedAPILogError(t, logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_write_failure", 1)), writeErr)
	apiLogFileWrite = oldWrite
	assertDetachedAPILogError(t, logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_after_write_failure", 1)), writeErr)
	if writeCount != 1 {
		t.Fatalf("write calls after quarantine = %d, want 1", writeCount)
	}
	if len(failures) != 1 {
		t.Fatalf("write-failure observations = %d, want 1: %+v", len(failures), failures)
	}
	assertDetachedAPILogError(t, logger.Close(), writeErr)
}

func TestSessionAPILoggerReserveExistingRouteHonorsStickyQuarantineWithoutOpening(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	const sessionID = "sess-existing-route"
	if err := logger.ReserveSession(sessionID); err != nil {
		t.Fatalf("ReserveSession: %v", err)
	}
	syncErr := quarantineSessionLogger(t, logger, sessionID)

	oldOpen := apiLogOpenFile
	openCalls := 0
	apiLogOpenFile = func(path string) (*os.File, error) {
		openCalls++
		return oldOpen(path)
	}
	t.Cleanup(func() { apiLogOpenFile = oldOpen })
	assertDetachedAPILogError(t, logger.ReserveSession(sessionID), syncErr)
	if openCalls != 0 {
		t.Fatalf("ReserveSession existing route opened %d files after quarantine", openCalls)
	}
	assertStickyClose(t, logger, syncErr)
}

func TestSessionAPILoggerReserveNewRouteHonorsStickyQuarantineWithoutOpening(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	syncErr := quarantineSessionLogger(t, logger, "sess-failed-route")

	oldOpen := apiLogOpenFile
	openCalls := 0
	apiLogOpenFile = func(string) (*os.File, error) {
		openCalls++
		return nil, errors.New("unexpected open after quarantine")
	}
	t.Cleanup(func() { apiLogOpenFile = oldOpen })
	assertDetachedAPILogError(t, logger.ReserveSession("sess-new-route"), syncErr)
	if openCalls != 0 {
		t.Fatalf("ReserveSession new route opened %d files after quarantine", openCalls)
	}
	assertStickyClose(t, logger, syncErr)
}

func TestAPILoggerFailureObserverCanCloseSynchronously(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	syncErr := errors.New("observer close sync failure")
	oldSync := apiLogFileSync
	apiLogFileSync = func(*os.File) error { return syncErr }
	t.Cleanup(func() { apiLogFileSync = oldSync })

	observerCalls := 0
	var observerCloseErr error
	logger.SetFailureObserver(func(APILogFailure) {
		observerCalls++
		observerCloseErr = logger.Close()
	})
	appendErr := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_observer_close", 1))
	assertDetachedAPILogError(t, appendErr, syncErr)
	if observerCalls != 1 {
		t.Fatalf("observer calls = %d, want 1", observerCalls)
	}
	assertDetachedAPILogError(t, observerCloseErr, syncErr)
	if err := logger.ReserveSession("late-session"); !errors.Is(err, errAPILoggerClosed) {
		t.Fatalf("ReserveSession after observer Close = %v, want logger closed", err)
	}
	assertDetachedAPILogError(t, logger.Close(), syncErr)
}

func quarantineSessionLogger(t *testing.T, logger *APILogger, sessionID string) error {
	t.Helper()
	syncErr := errors.New("session logger sync failure")
	oldSync := apiLogFileSync
	apiLogFileSync = func(*os.File) error { return syncErr }
	t.Cleanup(func() { apiLogFileSync = oldSync })
	err := logger.AppendAttempt(WithAPILogContext(context.Background(), sessionID), standaloneCanonicalAttempt("ag_quarantine_reserve", 1))
	assertDetachedAPILogError(t, err, syncErr)
	apiLogFileSync = oldSync
	return syncErr
}

func assertStickyClose(t *testing.T, logger *APILogger, stickyErr error) {
	t.Helper()
	var firstText string
	for call := 1; call <= 2; call++ {
		err := logger.Close()
		assertDetachedAPILogError(t, err, stickyErr)
		if call == 1 {
			firstText = err.Error()
		} else if err.Error() != firstText {
			t.Fatalf("Close call %d = %q, want sticky %q", call, err, firstText)
		}
	}
}

func assertDetachedAPILogError(t *testing.T, got, raw error) {
	t.Helper()
	if got == nil {
		t.Fatalf("API-log operation succeeded; want detached failure for %v", raw)
	}
	if errors.Is(got, raw) || errors.Unwrap(got) != nil {
		t.Fatalf("API-log failure retained raw error graph: %v", got)
	}
}

func TestAPILoggerFailureDoesNotChangeProviderResult(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldSync := apiLogFileSync
	syncErr := errors.New("sync failed")
	apiLogFileSync = func(*os.File) error { return syncErr }
	t.Cleanup(func() {
		apiLogFileSync = oldSync
		_ = logger.Close()
	})
	want := Response{ID: "provider-response", Message: Assistant("provider result")}
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	got, gotErr := logger.WrapComplete(func(ctx context.Context, _ Request) (Response, error) {
		BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
		return want, nil
	})(context.Background(), Request{})
	if gotErr != nil || got.ID != want.ID || got.Text() != want.Text() {
		t.Fatalf("provider result after logging failure = (%+v, %v), want (%+v, nil)", got, gotErr, want)
	}
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
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod invalid log: %v", err)
			}
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
			assertPathMode(t, path, 0o644)
		})

		t.Run(tt.name+"/per-session", func(t *testing.T) {
			stateDir := t.TempDir()
			sessionID := "sess-invalid"
			path := filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("mkdir sessions: %v", err)
			}
			tt.body(t, path)
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod invalid log: %v", err)
			}
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
			assertPathMode(t, path, 0o644)
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
	var failures []APILogFailure
	logger.SetFailureObserver(func(failure APILogFailure) { failures = append(failures, failure) })
	group := NewAPIAttemptGroup("ag_sync_failure")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), logger)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	if len(failures) != 1 {
		t.Fatalf("sync failure observations = %d, want 1: %+v", len(failures), failures)
	}
	failure := failures[0]
	if failure.Operation != "append_attempt" || failure.AttemptGroupID != group.ID || failure.AttemptID == "" {
		t.Fatalf("sync failure = %+v", failure)
	}
	if errors.Is(failure.Err, syncErr) || errors.Unwrap(failure.Err) != nil {
		t.Fatalf("sync failure retained raw error graph: %+v", failure)
	}

	apiLogFileSync = oldSync
	second := standaloneCanonicalAttempt("ag_after_sync_failure", 1)
	secondErr := logger.AppendAttempt(context.Background(), second)
	if secondErr == nil {
		t.Fatal("AppendAttempt after sync failure succeeded")
	}
	if errors.Is(secondErr, syncErr) || errors.Unwrap(secondErr) != nil {
		t.Fatalf("sticky append retained raw error graph: %v", secondErr)
	}
	if len(failures) != 1 {
		t.Fatalf("sticky failure produced duplicate observations: %+v", failures)
	}
	closeErr := logger.Close()
	if closeErr == nil {
		t.Fatal("Close after sync failure succeeded")
	}
	if errors.Is(closeErr, syncErr) || errors.Unwrap(closeErr) != nil {
		t.Fatalf("sticky Close retained raw error graph: %v", closeErr)
	}
}

func TestAPILoggerCanonicalSyncFailureQuarantinesAllSessionRoutes(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
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
	completeCanonicalGroup(t, logger, "sess-b", "ag_session_b_sync", time.Unix(1_700_000_001, 0).UTC())

	apiLogFileSync = oldSync
	assertDetachedAPILogError(t, logger.Close(), syncErr)
	if len(failures) != 1 {
		t.Fatalf("sync failure observer calls = %d, want one session A failure: %+v", len(failures), failures)
	}
	for _, failure := range failures {
		if failure.SessionID != "sess-a" || failure.AttemptGroupID != "ag_session_a_sync" {
			t.Fatalf("sync failure attributed to unaffected group: %+v", failure)
		}
		if errors.Is(failure.Err, syncErr) || errors.Unwrap(failure.Err) != nil {
			t.Fatalf("sync failure retained raw error graph: %+v", failure)
		}
	}
	if failures[0].Operation != "append_attempt" || failures[0].AttemptID == "" {
		t.Fatalf("attempt sync failure identity = %+v", failures[0])
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", "sess-b.api.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("quarantined logger accessed session B target: %v", err)
	}
}

func TestAPILoggerCloseDoesNotResyncAcceptedRecords(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldSync := apiLogFileSync
	syncCount := 0
	apiLogFileSync = func(file *os.File) error {
		syncCount++
		return oldSync(file)
	}
	t.Cleanup(func() { apiLogFileSync = oldSync })
	if err := logger.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_close_no_sync", 1)); err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}
	if syncCount != 1 {
		t.Fatalf("sync calls after append = %d, want 1", syncCount)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if syncCount != 1 {
		t.Fatalf("Close added a sync: got %d total calls, want 1", syncCount)
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
			StatusCode: apiLogTestInt(http.StatusOK),
			Body:       apilog.EncodeBody([]byte(`{"output":"hello"}`)),
		},
		Outcome: apilog.AttemptSuccess,
	}
}

func apiLogTestInt(value int) *int { return &value }

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
