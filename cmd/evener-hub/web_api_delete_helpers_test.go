package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestProjectDeletionOwnershipErrorLiveMessage(t *testing.T) {
	e := projectDeletionOwnershipError{Live: true}
	if e.Error() != "resumed live" {
		t.Fatalf("Live error should be 'resumed live', got %q", e.Error())
	}
}

func TestProjectDeletionOwnershipErrorWrappedMessage(t *testing.T) {
	inner := errors.New("some failure")
	e := projectDeletionOwnershipError{Err: inner}
	if e.Error() != "some failure" {
		t.Fatalf("wrapped error should delegate, got %q", e.Error())
	}
	if !errors.Is(e, inner) {
		t.Fatal("Unwrap should return the inner error")
	}
}

func TestProjectDeletionOwnershipErrorUnwrapNil(t *testing.T) {
	e := projectDeletionOwnershipError{Live: true}
	if e.Unwrap() != nil {
		t.Fatal("Unwrap on Live-only error should return nil")
	}
}

func TestProjectDeletionStateDirFromMap(t *testing.T) {
	s := &WebServer{}
	got := s.projectDeletionStateDir("proj", "thread1", map[string]string{"thread1": "/custom/state"})
	if got != "/custom/state" {
		t.Fatalf("expected /custom/state, got %q", got)
	}
}

func TestProjectDeletionStateDirFromConfig(t *testing.T) {
	s := &WebServer{cfg: hubcore.WebConfig{StateDir: "/data"}}
	got := s.projectDeletionStateDir("proj", "thread1", nil)
	if got != filepath.Join("/data", "projects", "proj") {
		t.Fatalf("expected /data/projects/proj, got %q", got)
	}
}

func TestProjectDeletionStateDirEmptyWhenNoConfig(t *testing.T) {
	s := &WebServer{}
	got := s.projectDeletionStateDir("proj", "thread1", nil)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRemoveProjectSessionDaemonLogNoRunDir(t *testing.T) {
	if err := removeProjectSessionDaemonLog("", "session-abc"); err != nil {
		t.Fatalf("empty runDir should be no-op, got %v", err)
	}
}

func TestRemoveProjectSessionDaemonLogMissingFile(t *testing.T) {
	dir := t.TempDir()
	if err := removeProjectSessionDaemonLog(dir, webTestSessionID); err != nil {
		t.Fatalf("missing daemon log should not error, got %v", err)
	}
}

func TestRemoveProjectSessionDaemonLogRemovesFile(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, daemonLogDirName)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, daemonLogName(webTestSessionID))
	if err := os.WriteFile(logPath, []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeProjectSessionDaemonLog(dir, webTestSessionID); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("daemon log should be removed")
	}
}

func TestRemoveProjectSessionRendezvousNoRunDir(t *testing.T) {
	if err := removeProjectSessionRendezvous("", "s1"); err != nil {
		t.Fatalf("empty runDir should be no-op, got %v", err)
	}
}

func TestRemoveProjectSessionRendezvousEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := removeProjectSessionRendezvous(dir, "s1"); err != nil {
		t.Fatalf("empty rendezvous dir should not error, got %v", err)
	}
}

func TestRemoveProjectSessionRendezvousRemovesEntry(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 12345, SessionID: "s1", ThreadID: "s1"}
	if _, err := rendezvous.Write(dir, entry); err != nil {
		t.Fatalf("rendezvous.Write: %v", err)
	}
	entries, err := rendezvous.List(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %v %v", entries, err)
	}
	if err := removeProjectSessionRendezvous(dir, "s1"); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	entries, err = rendezvous.List(dir)
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after removal, got %d", len(entries))
	}
}

func TestRemoveFlatProjectSessionArtifactsInvalidID(t *testing.T) {
	if err := removeFlatProjectSessionArtifacts("/tmp", "invalid session id"); err == nil {
		t.Fatal("invalid session ID should error")
	}
}

func TestRemoveFlatProjectSessionArtifactsMissingDir(t *testing.T) {
	if err := removeFlatProjectSessionArtifacts(filepath.Join(t.TempDir(), "nonexistent"), webTestSessionID); err != nil {
		t.Fatalf("missing dir should not error, got %v", err)
	}
}

func TestRemoveFlatProjectSessionArtifactsRemovesFlatFiles(t *testing.T) {
	dir := t.TempDir()
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, webTestSessionID+suffix), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, webTestSessionID+".api.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, webTestSessionID), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := removeFlatProjectSessionArtifacts(dir, webTestSessionID); err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, webTestSessionID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("flat artifact %s should be removed", suffix)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, webTestSessionID+".api.jsonl")); err != nil {
		t.Fatalf(".api.jsonl should NOT be removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, webTestSessionID)); err != nil {
		t.Fatalf("subdirectory should NOT be removed: %v", err)
	}
}

func TestAppendProjectDeleteLiveSkip(t *testing.T) {
	skipped := appendProjectDeleteLiveSkip(nil, "s1")
	if len(skipped) != 1 || skipped[0].ID != "s1" || skipped[0].Reason != "resumed live" {
		t.Fatalf("expected one skip with 'resumed live', got %+v", skipped)
	}
	skipped = appendProjectDeleteLiveSkip(skipped, "s2")
	if len(skipped) != 2 || skipped[1].ID != "s2" {
		t.Fatalf("expected two skips, got %+v", skipped)
	}
}

func TestScrubSessionDecisionsNoStores(t *testing.T) {
	s := &WebServer{}
	errs := s.scrubSessionDecisions("s1")
	if len(errs) != 0 {
		t.Fatalf("no stores should produce no errors, got %v", errs)
	}
}

func TestScrubSessionDecisionsArchiveError(t *testing.T) {
	dir := t.TempDir()
	archive := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	s := &WebServer{cfg: hubcore.WebConfig{Archive: archive}}
	errs := s.scrubSessionDecisions("s1")
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}
