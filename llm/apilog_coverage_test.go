package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestMarkAPILogErrorObservedNil covers the nil guard (lines 55-56).
func TestMarkAPILogErrorObservedNil(t *testing.T) {
	if got := markAPILogErrorObserved(nil); got != nil {
		t.Fatalf("markAPILogErrorObserved(nil) = %v, want nil", got)
	}
}

// TestNewAPILogOpenError covers the open error path in NewAPILogger (lines 80-81).
func TestNewAPILogOpenError(t *testing.T) {
	// A path under a regular file cannot be opened.
	regular := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewAPILogger(filepath.Join(regular, "api.jsonl"))
	if err == nil {
		t.Fatal("NewAPILogger under a regular file should error")
	}
}

// TestNewSessionAPILogDirError covers the ensurePrivateAPILogDirectory error
// in NewSessionAPILogger (lines 94-95).
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

// TestEnsurePrivateAPILogDirectoryStatError covers the non-NotExist stat error
// branch (lines 185-186).
func TestEnsurePrivateAPILogDirectoryStatError(t *testing.T) {
	// Create a regular file where the directory should be — Stat succeeds,
	// but we need a non-NotExist error. Use a path under a file to get a
	// different error from Stat.
	regular := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stat on a path under a regular file returns an error that is not NotExist
	// (on most systems it's ENOTDIR).
	err := ensurePrivateAPILogDirectory(filepath.Join(regular, "subdir"))
	if err == nil {
		t.Fatal("ensurePrivateAPILogDirectory under a file should error")
	}
}

// TestSessionFileWithErrorNilCache covers the nil-file cache path (lines 120-121).
// After a session file fails to open, the nil is cached, and subsequent calls
// return the "unavailable" error.
func TestSessionFileWithErrorNilCache(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	defer logger.Close()

	// Make the sessions directory read-only so opening a file fails.
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := os.Chmod(sessionsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(sessionsDir, 0o700)

	// First call fails to open the file and caches nil.
	_, firstErr := logger.sessionFileWithError("test-sess")
	if firstErr == nil {
		t.Fatal("sessionFileWithError should fail on read-only dir")
	}
	// Second call returns the cached nil as "unavailable".
	_, secondErr := logger.sessionFileWithError("test-sess")
	if secondErr == nil {
		t.Fatal("sessionFileWithError should return unavailable for cached nil")
	}
}

// TestReserveSessionNoSessionsDir covers the empty sessionsDir path (lines 147-148).
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

// TestReleaseSessionAfterClose covers the canonicalClosing path (lines 160-161).
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

// TestReleaseSessionEmptySessionsDir covers the empty sessionsDir path (lines 167-168).
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

// TestReleaseSessionNotFound covers the session-not-found path (lines 172-173).
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

// TestReleaseSessionNilFile covers the nil-file path (lines 176-177).
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
	// Now the session file is cached as nil.
	// Restore write permission for release.
	if err := os.Chmod(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := logger.ReleaseSession("fail-sess"); err != nil {
		t.Fatalf("ReleaseSession for nil-file session = %v, want nil", err)
	}
}

// TestRecoverCanonicalAPILogTailTruncateError covers the truncate error path
// (lines 199-200).
func TestRecoverCanonicalAPILogTailTruncateError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	first := standaloneCanonicalAttempt("ag_trunc_err", 1)
	writeCanonicalAttempt(t, path, first)
	complete, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read complete API log: %v", err)
	}
	appendAPILogCrashTail(t, path, []byte(`{"kind":"api_attempt"`))

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open API log: %v", err)
	}
	defer file.Close()

	// Open a read-only file descriptor to force Truncate to fail.
	// Actually, we need the file to be opened RDWR but Truncate to fail.
	// On most systems Truncate fails on a read-only fd, but our file is RDWR.
	// Instead, we can't easily force Truncate to fail. Skip if we can't.
	// Let's use a pipe or /dev/null — actually we can use os.Stdin.
	// A simpler approach: open the file read-only.
	roFile, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer roFile.Close()
	if err := recoverCanonicalAPILogTail(roFile, canonicalAPILogMaxLineBytes); err == nil {
		t.Fatal("recoverCanonicalAPILogTail on read-only file should error on truncate")
	}
	_ = complete // suppress unused warning
}

// TestRecoverCanonicalAPILogTailSyncError covers the sync error path
// (lines 202-203).
func TestRecoverCanonicalAPILogTailSyncError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	first := standaloneCanonicalAttempt("ag_sync_err", 1)
	writeCanonicalAttempt(t, path, first)
	appendAPILogCrashTail(t, path, []byte(`{"kind":"api_attempt"`))

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open API log: %v", err)
	}
	defer file.Close()

	syncErr := errors.New("sync failed")
	oldSync := apiLogFileSync
	apiLogFileSync = func(*os.File) error { return syncErr }
	defer func() { apiLogFileSync = oldSync }()

	if err := recoverCanonicalAPILogTail(file, canonicalAPILogMaxLineBytes); err == nil {
		t.Fatal("recoverCanonicalAPILogTail with sync error should return error")
	}
}

// TestWrapStreamErrorWithOwnedSettlement covers the error path where
// the stream setup fails and the logger owns settlement (lines 227-228).
func TestWrapStreamErrorWithOwnedSettlement(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	streamErr := errors.New("stream setup failed")
	wrapped := logger.WrapStream(func(ctx context.Context, _ Request) (Stream, error) {
		return nil, streamErr
	})
	stream, err := wrapped(context.Background(), Request{})
	if err == nil || stream != nil {
		t.Fatalf("WrapStream with error = (%v, %v), want (nil, error)", stream, err)
	}
}

// TestWrapStreamNilStreamWithOwnedSettlement covers the nil-stream path where
// the logger owns settlement (lines 232-235).
func TestWrapStreamNilStreamWithOwnedSettlement(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	wrapped := logger.WrapStream(func(ctx context.Context, _ Request) (Stream, error) {
		return nil, nil
	})
	stream, err := wrapped(context.Background(), Request{})
	if err != nil || stream != nil {
		t.Fatalf("WrapStream with nil stream = (%v, %v), want (nil, nil)", stream, err)
	}
}

// TestObserveClosedAppendCoordinatorManaged covers the coordinatorManaged
// branch in observeClosedAppend (lines 367-368).
func TestObserveClosedAppendCoordinatorManaged(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	if cerr := logger.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	// Provide credential material in context so observeClosedAppend takes the
	// coordinatorManaged early return.
	ctx := withAPILogCredentialMaterial(context.Background(), APILogCredentialMaterial{})
	failure := APILogFailure{Operation: "append_attempt", Err: errAPILoggerClosed}
	got := logger.observeClosedAppend(ctx, failure)
	if !errors.Is(got, errAPILoggerClosed) {
		t.Fatalf("observeClosedAppend with coordinatorManaged = %v, want errAPILoggerClosed", got)
	}
}

// TestAppendCanonicalRecordNoDestination covers the no-destination path
// (lines 407-409). This happens when a logger has no file and no sessionsDir.
func TestAppendCanonicalRecordNoDestination(t *testing.T) {
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	// Close the logger to remove the file destination.
	if cerr := logger.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	// Re-open would fail, but we can manually construct a logger with no file.
	// Actually, after Close the file is nil and sessionsDir is empty, so
	// admitCanonicalAppend will return errAPILoggerClosed first.
	// We need a logger that admits appends but has no file. Let's use
	// a fresh logger and manually nil its file.
	logger2, err := NewAPILogger(filepath.Join(t.TempDir(), "api2.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	logger2.mu.Lock()
	logger2.file = nil
	logger2.mu.Unlock()
	err = logger2.AppendAttempt(context.Background(), standaloneCanonicalAttempt("ag_no_dest", 1))
	if err == nil {
		t.Fatal("AppendAttempt with no destination should error")
	}
	_ = logger2.Close()
}

// TestCloseFileCloseError covers the file close error path in Close
// (lines 466-467).
func TestCloseFileCloseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}

	closeErr := errors.New("close failed")
	oldClose := apiLogFileClose
	apiLogFileClose = func(f *os.File) error { return closeErr }
	defer func() { apiLogFileClose = oldClose }()

	if err := logger.Close(); err == nil {
		t.Fatal("Close with file close error should return error")
	}
}

// TestStampEndpointURLNilResponse is a placeholder — StampEndpointURL
// with nil is already covered, but we add a test for the nil response case
// to be safe.
func TestStampEndpointURLNilResponse(t *testing.T) {
	StampEndpointURL(nil, "https://example.invalid/api", APILogCredentialMaterial{})
	// No panic.
}

// newChanStreamWithEvents creates a ChanStream that yields the given events
// then closes.
func newChanStreamWithEvents(events []StreamEvent) Stream {
	s := NewChanStream(func() {})
	go func() {
		for _, ev := range events {
			s.Send(ev)
		}
		s.CloseSend()
	}()
	return s
}
