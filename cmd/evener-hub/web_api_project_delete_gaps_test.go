package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/identifier"
)

// TestProjectDeletionOwnershipErrorLive covers the Live=true path in Error().
func TestProjectDeletionOwnershipErrorLive(t *testing.T) {
	e := projectDeletionOwnershipError{ThreadID: "s1", Live: true}
	if got := e.Error(); got != "resumed live" {
		t.Fatalf("expected 'resumed live', got %q", got)
	}
}

// TestProjectDeletionOwnershipErrorNonLive covers the Live=false path in
// Error().
func TestProjectDeletionOwnershipErrorNonLive(t *testing.T) {
	inner := errors.New("some error")
	e := projectDeletionOwnershipError{ThreadID: "s1", Err: inner}
	if got := e.Error(); got != "some error" {
		t.Fatalf("expected 'some error', got %q", got)
	}
}

// TestProjectDeletionOwnershipErrorUnwrap covers the Unwrap method.
func TestProjectDeletionOwnershipErrorUnwrap(t *testing.T) {
	inner := errors.New("inner error")
	e := projectDeletionOwnershipError{ThreadID: "s1", Err: inner}
	if !errors.Is(e, inner) {
		t.Fatal("Unwrap should return the inner error")
	}
}

// TestProjectDeletionOwnershipErrorUnwrapNilGaps covers the nil Err path.
func TestProjectDeletionOwnershipErrorUnwrapNilGaps(t *testing.T) {
	e := projectDeletionOwnershipError{ThreadID: "s1", Live: true}
	if e.Unwrap() != nil {
		t.Fatal("Unwrap with nil Err should return nil")
	}
}

// TestAppendProjectDeleteLiveSkipGaps covers the helper function.
func TestAppendProjectDeleteLiveSkipGaps(t *testing.T) {
	skipped := appendProjectDeleteLiveSkip(nil, "session-1")
	if len(skipped) != 1 || skipped[0].ID != "session-1" || skipped[0].Reason != "resumed live" {
		t.Fatalf("expected [{session-1, resumed live}], got %v", skipped)
	}
	// Append to existing
	skipped = appendProjectDeleteLiveSkip(skipped, "session-2")
	if len(skipped) != 2 {
		t.Fatalf("expected 2 skips, got %d", len(skipped))
	}
}

// TestRemoveProjectSessionRendezvousEmptyRunDir covers the empty runDir path.
func TestRemoveProjectSessionRendezvousEmptyRunDir(t *testing.T) {
	if err := removeProjectSessionRendezvous("", "s1"); err != nil {
		t.Fatalf("empty runDir should return nil, got %v", err)
	}
}

// TestRemoveProjectSessionRendezvousNonexistentDir covers the error path when
// the runDir doesn't exist.
func TestRemoveProjectSessionRendezvousNonexistentDir(t *testing.T) {
	dir := t.TempDir()
	// Create the rendezvous subdirectory so List doesn't fail
	rendezvousDir := filepath.Join(dir, "rendezvous")
	if err := os.MkdirAll(rendezvousDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := removeProjectSessionRendezvous(dir, "nonexistent-session")
	if err != nil {
		t.Fatalf("expected nil for empty rendezvous dir, got %v", err)
	}
}

// TestRemoveProjectSessionRendezvousNoMatchingEntries covers the path where
// no entries match the session ID.
func TestRemoveProjectSessionRendezvousNoMatchingEntries(t *testing.T) {
	dir := t.TempDir()
	// Create an empty rendezvous directory
	if err := os.MkdirAll(filepath.Join(dir, "rendezvous"), 0o755); err != nil {
		t.Fatal(err)
	}
	// rendezvous.List on an empty directory should return empty, no error
	err := removeProjectSessionRendezvous(dir, "nonexistent-session")
	if err != nil {
		t.Fatalf("expected nil for no matching entries, got %v", err)
	}
}

// TestRemoveProjectSessionDaemonLogEmptyRunDir covers the empty runDir path.
func TestRemoveProjectSessionDaemonLogEmptyRunDir(t *testing.T) {
	if err := removeProjectSessionDaemonLog("", "s1"); err != nil {
		t.Fatalf("empty runDir should return nil, got %v", err)
	}
}

// TestRemoveProjectSessionDaemonLogNonexistentFile covers the path where the
// log file doesn't exist (should return nil, not error).
func TestRemoveProjectSessionDaemonLogNonexistentFile(t *testing.T) {
	dir := t.TempDir()
	if err := removeProjectSessionDaemonLog(dir, "session-1"); err != nil {
		t.Fatalf("nonexistent log should return nil, got %v", err)
	}
}

// TestRemoveProjectSessionDaemonLogRemovesFileGaps covers the path where the log
// file exists and is removed.
func TestRemoveProjectSessionDaemonLogRemovesFileGaps(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, daemonLogDirName)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, daemonLogName("session-1"))
	if err := os.WriteFile(logPath, []byte("log data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeProjectSessionDaemonLog(dir, "session-1"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("log file should be removed, got %v", err)
	}
}

// TestRemoveFlatProjectSessionArtifactsInvalidSessionID covers the validation
// error path.
func TestRemoveFlatProjectSessionArtifactsInvalidSessionID(t *testing.T) {
	err := removeFlatProjectSessionArtifacts(t.TempDir(), "invalid session id with spaces")
	if err == nil {
		t.Fatal("invalid session ID should return error")
	}
}

// TestRemoveFlatProjectSessionArtifactsNonexistentDir covers the path where
// the directory doesn't exist (should return nil).
func TestRemoveFlatProjectSessionArtifactsNonexistentDir(t *testing.T) {
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	err = removeFlatProjectSessionArtifacts(filepath.Join(t.TempDir(), "nonexistent"), sessionID)
	if err != nil {
		t.Fatalf("nonexistent dir should return nil, got %v", err)
	}
}

// TestRemoveFlatProjectSessionArtifactsRemovesFiles covers the path where
// matching files are removed.
func TestRemoveFlatProjectSessionArtifactsRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	// Create a matching file (sessionID.transcript.jsonl)
	matchFile := filepath.Join(dir, sessionID+".transcript.jsonl")
	if err := os.WriteFile(matchFile, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a non-matching file
	otherFile := filepath.Join(dir, "other.jsonl")
	if err := os.WriteFile(otherFile, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create an api log file that should be preserved
	apiLog := filepath.Join(dir, sessionID+".api.jsonl")
	if err := os.WriteFile(apiLog, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeFlatProjectSessionArtifacts(dir, sessionID); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := os.Stat(matchFile); !os.IsNotExist(err) {
		t.Fatal("matching file should be removed")
	}
	if _, err := os.Stat(otherFile); os.IsNotExist(err) {
		t.Fatal("non-matching file should be preserved")
	}
	if _, err := os.Stat(apiLog); os.IsNotExist(err) {
		t.Fatal("api log file should be preserved")
	}
}

// TestRemoveFlatProjectSessionArtifactsPreservesDirs covers the path where
// directories are skipped.
func TestRemoveFlatProjectSessionArtifactsPreservesDirs(t *testing.T) {
	dir := t.TempDir()
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	// Create a directory with matching prefix
	subDir := filepath.Join(dir, sessionID+".something")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeFlatProjectSessionArtifacts(dir, sessionID); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	// Directory should be preserved (entry.IsDir() check)
	if _, err := os.Stat(subDir); os.IsNotExist(err) {
		t.Fatal("directory should be preserved")
	}
}
