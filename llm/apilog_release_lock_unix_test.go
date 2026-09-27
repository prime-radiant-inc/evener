//go:build darwin || linux

package llm

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func duplicateAPILogDescription(t *testing.T, file *os.File, description string) int {
	t.Helper()
	dup, err := unix.Dup(int(file.Fd()))
	if err != nil {
		t.Fatalf("duplicate %s description: %v", description, err)
	}
	t.Cleanup(func() { _ = unix.Close(dup) })
	return dup
}

func duplicateSessionAPILogDescription(t *testing.T, logger *APILogger, sessionID string) int {
	t.Helper()
	logger.mu.Lock()
	file := logger.sessionFiles[sessionLogBaseName(sessionID)]
	if file == nil {
		logger.mu.Unlock()
		t.Fatalf("session API log %q was not reserved", sessionID)
	}
	logger.mu.Unlock()
	return duplicateAPILogDescription(t, file, "session API-log")
}

func duplicateSingleAPILogDescription(t *testing.T, logger *APILogger) int {
	t.Helper()
	logger.mu.Lock()
	if logger.file == nil {
		logger.mu.Unlock()
		t.Fatal("single-file API logger has no file")
	}
	file := logger.file
	logger.mu.Unlock()
	return duplicateAPILogDescription(t, file, "single-file API-log")
}

func assertAPILogDescriptionLive(t *testing.T, fd int) {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		t.Fatalf("shared API-log description is not live: %v", err)
	}
}

func assertSessionReservationBlocked(t *testing.T, stateDir, sessionID string) {
	t.Helper()
	contender, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger contender: %v", err)
	}
	defer contender.Close() //nolint:errcheck
	if err := contender.ReserveSession(sessionID); !errors.Is(err, ErrAPILogTargetLocked) {
		t.Fatalf("contender ReserveSession = %v, want ErrAPILogTargetLocked", err)
	}
}

func TestReleaseSessionSharedOpenDescription(t *testing.T) {
	stateDir := t.TempDir()
	owner, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger owner: %v", err)
	}
	defer owner.Close() //nolint:errcheck
	const sessionID = "shared-release-session"
	if err := owner.ReserveSession(sessionID); err != nil {
		t.Fatalf("owner ReserveSession: %v", err)
	}
	dup := duplicateSessionAPILogDescription(t, owner, sessionID)
	assertSessionReservationBlocked(t, stateDir, sessionID)
	// Retry after the failed contender's cleanup; the owner must still hold the lock.
	assertSessionReservationBlocked(t, stateDir, sessionID)

	if err := owner.ReleaseSession(sessionID); err != nil {
		t.Fatalf("owner ReleaseSession: %v", err)
	}
	assertAPILogDescriptionLive(t, dup)

	contender, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger after release: %v", err)
	}
	defer contender.Close() //nolint:errcheck
	if err := contender.ReserveSession(sessionID); err != nil {
		t.Fatalf("contender ReserveSession after release with duplicate open: %v", err)
	}
}

func TestCloseRoutedSharedOpenDescription(t *testing.T) {
	stateDir := t.TempDir()
	owner, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger owner: %v", err)
	}
	const sessionID = "shared-routed-close"
	if err := owner.ReserveSession(sessionID); err != nil {
		t.Fatalf("owner ReserveSession: %v", err)
	}
	dup := duplicateSessionAPILogDescription(t, owner, sessionID)
	assertSessionReservationBlocked(t, stateDir, sessionID)
	// Retry after the failed contender's cleanup; the owner must still hold the lock.
	assertSessionReservationBlocked(t, stateDir, sessionID)

	if err := owner.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	assertAPILogDescriptionLive(t, dup)

	contender, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger after routed Close: %v", err)
	}
	defer contender.Close() //nolint:errcheck
	if err := contender.ReserveSession(sessionID); err != nil {
		t.Fatalf("contender ReserveSession after routed Close with duplicate open: %v", err)
	}
}

func TestCloseSingleFileSharedOpenDescription(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	owner, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger owner: %v", err)
	}
	dup := duplicateSingleAPILogDescription(t, owner)

	if _, err := NewAPILogger(path); !errors.Is(err, ErrAPILogTargetLocked) {
		t.Fatalf("first single-file contender = %v, want ErrAPILogTargetLocked", err)
	}
	// Retry after the failed contender's cleanup; the owner must still hold the lock.
	if _, err := NewAPILogger(path); !errors.Is(err, ErrAPILogTargetLocked) {
		t.Fatalf("second single-file contender = %v, want ErrAPILogTargetLocked", err)
	}

	if err := owner.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	assertAPILogDescriptionLive(t, dup)

	contender, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger after single-file Close with duplicate open: %v", err)
	}
	t.Cleanup(func() { _ = contender.Close() })
}

func TestOpenRecoveryFailureSharedOpenDescription(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	writeCanonicalAttempt(t, path, standaloneCanonicalAttempt("shared-recovery-failure", 1))
	complete, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read complete API log: %v", err)
	}
	appendAPILogCrashTail(t, path, []byte(`{"kind":"api_attempt"`))

	syncErr := errors.New("shared recovery sync failed")
	oldSync := apiLogFileSync
	var original *os.File
	dup := -1
	apiLogFileSync = func(file *os.File) error {
		original = file
		var err error
		dup, err = unix.Dup(int(file.Fd()))
		if err != nil {
			return err
		}
		return syncErr
	}
	defer func() { apiLogFileSync = oldSync }()

	file, err := openPrivateAPILogFile(path)
	if file != nil {
		_ = file.Close()
		t.Fatal("openPrivateAPILogFile returned a file after recovery sync failure")
	}
	if !errors.Is(err, syncErr) {
		t.Fatalf("openPrivateAPILogFile error = %v, want sync failure", err)
	}
	if original == nil {
		t.Fatal("recovery sync seam did not observe the opened file")
	}
	if _, statErr := original.Stat(); statErr == nil {
		t.Fatal("original API-log descriptor remained open after recovery failure")
	}
	if dup < 0 {
		t.Fatal("recovery sync seam did not duplicate the locked description")
	}
	defer unix.Close(dup) //nolint:errcheck
	assertAPILogDescriptionLive(t, dup)

	recovered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered API log: %v", err)
	}
	if !bytes.Equal(recovered, complete) {
		t.Fatalf("recovered API log = %q, want the complete prefix %q", recovered, complete)
	}

	contender, err := openPrivateAPILogFile(path)
	if err != nil {
		t.Fatalf("independent contender after recovery failure with duplicate open: %v", err)
	}
	t.Cleanup(func() { _ = contender.Close() })
}
