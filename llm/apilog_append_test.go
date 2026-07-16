package llm

import (
	"context"
	"encoding/json"
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
	if err := logger.AppendAttempt(WithAPILogContext(context.Background(), "sess-a", 1), first); err != nil {
		t.Fatalf("append session A: %v", err)
	}
	if err := logger.AppendAttempt(WithAPILogContext(context.Background(), "sess-b", 1), second); err != nil {
		t.Fatalf("append session B: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertOnlyCanonicalAttempt(t, filepath.Join(stateDir, "sessions", "sess-a.api.jsonl"), first.AttemptID)
	assertOnlyCanonicalAttempt(t, filepath.Join(stateDir, "sessions", "sess-b.api.jsonl"), second.AttemptID)
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
	ctx := WithAPILogContext(context.Background(), "sess-close-sync", 1)
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

func TestAPILoggerOrdinaryMiddlewareStillWritesOnlyLegacyFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	want := Response{ID: "legacy-response", Model: "model-a", Provider: "primary", Message: Assistant("ok"), Finish: FinishReason{Reason: FinishReasonStop}}
	got, err := logger.WrapComplete(func(context.Context, Request) (Response, error) { return want, nil })(context.Background(), Request{Model: "model-a", Provider: "primary"})
	if err != nil || got.ID != want.ID {
		t.Fatalf("WrapComplete = (%+v, %v)", got, err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read legacy API log: %v", err)
	}
	line := []byte(strings.TrimSuffix(string(data), "\n"))
	var envelope struct {
		Kind    string          `json:"kind"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		t.Fatalf("decode legacy entry: %v", err)
	}
	if envelope.Kind != "" || len(envelope.Request) == 0 {
		t.Fatalf("ordinary middleware emitted non-legacy envelope: %s", line)
	}
	if record, err := apilog.DecodeRecord(line); err == nil {
		t.Fatalf("ordinary middleware emitted canonical record %T", record)
	}
}

func completeCanonicalGroup(t *testing.T, logger *APILogger, sessionID, groupID string, startedAt time.Time) {
	t.Helper()
	group := NewAPIAttemptGroup(groupID)
	ctx := WithAPILogContext(context.Background(), sessionID, 1)
	ctx = WithAPIAttemptSink(WithAPIAttemptGroup(ctx, group), logger)
	BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	group.Settle(ctx, apilog.AttemptSuccess)
}

func appendCanonicalTestRecords(t *testing.T, logger *APILogger, sessionID string) {
	t.Helper()
	ctx := WithAPILogContext(context.Background(), sessionID, 1)
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
