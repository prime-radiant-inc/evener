package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenDaemonLogNoRunDir covers the empty-runDir guard.
func TestOpenDaemonLogNoRunDir(t *testing.T) {
	if _, err := openDaemonLog("", "session-1"); err == nil {
		t.Fatalf("openDaemonLog with empty runDir should error")
	}
}

// TestOpenDaemonLogMkdirAllFailure covers the MkdirAll error path.
func TestOpenDaemonLogMkdirAllFailure(t *testing.T) {
	// A path where a file blocks directory creation.
	tmp := t.TempDir()
	conflict := filepath.Join(tmp, "file")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openDaemonLog(filepath.Join(conflict, "sub"), ""); err == nil {
		t.Fatalf("openDaemonLog with unwritable runDir should error")
	}
}

// TestOpenDaemonLogCreateTempFailure covers the CreateTemp error for pending logs.
func TestOpenDaemonLogCreateTempFailure(t *testing.T) {
	// Use a read-only directory to make CreateTemp fail.
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "run", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o400); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()
	if _, err := openDaemonLog(filepath.Join(tmp, "run"), ""); err == nil {
		t.Fatalf("openDaemonLog with read-only logs dir should error")
	}
}

// TestCopyDaemonLogSnapshotNegative covers the negative-size guard.
func TestCopyDaemonLogSnapshotNegative(t *testing.T) {
	var sb tailBuffer
	if err := copyDaemonLogSnapshot(&sb, nil, -1); err == nil {
		t.Fatalf("copyDaemonLogSnapshot with negative size should error")
	}
}

// TestDaemonLogAdoptEmptySessionID covers the empty-sessionID guard.
func TestDaemonLogAdoptEmptySessionID(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-pending-*.log")
	if err != nil {
		t.Fatal(err)
	}
	l := &daemonLog{file: f, path: f.Name(), pending: true}
	l.adopt("")
	if !l.pending {
		t.Fatalf("adopt with empty sessionID should leave pending=true")
	}
}

// TestDaemonLogPromoteNoReplacement covers the no-replacement path.
func TestDaemonLogPromoteNoReplacement(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	l := &daemonLog{file: f, path: f.Name()}
	if err := l.promote(); err != nil {
		t.Fatalf("promote with no replacement target should return nil, got %v", err)
	}
}

// TestDaemonLogTailMissingFile covers the open-error path in tail.
func TestDaemonLogTailMissingFile(t *testing.T) {
	l := &daemonLog{path: filepath.Join(t.TempDir(), "nonexistent.log")}
	if got := l.tail(1024); got != "" {
		t.Fatalf("tail on missing file should return empty string, got %q", got)
	}
}

// TestDaemonLogTailSeekError covers the seek-error path in tail.
func TestDaemonLogTailSeekError(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-*.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	l := &daemonLog{path: f.Name(), launchOffset: -1}
	// Seek to -1 should fail, returning empty string.
	if got := l.tail(1024); got != "" {
		t.Fatalf("tail with bad seek should return empty string, got %q", got)
	}
}

// TestWriteDaemonLogSnapshotMissingSource covers the IsNotExist path.
func TestWriteDaemonLogSnapshotMissingSource(t *testing.T) {
	dst, err := os.CreateTemp(t.TempDir(), "dst-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	if err := writeDaemonLogSnapshot(dst, filepath.Join(t.TempDir(), "nonexistent.log")); err != nil {
		t.Fatalf("writeDaemonLogSnapshot on missing source should return nil, got %v", err)
	}
}

// TestWriteDaemonLogSnapshotSmallSource covers the small-source path
// (no trimming needed).
func TestWriteDaemonLogSnapshotSmallSource(t *testing.T) {
	dir := t.TempDir()
	// Write a small source file.
	srcPath := filepath.Join(dir, "source.log")
	if err := os.WriteFile(srcPath, []byte("small log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err := os.CreateTemp(dir, "dst-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	if err := writeDaemonLogSnapshot(dst, srcPath); err != nil {
		t.Fatalf("writeDaemonLogSnapshot on small source: %v", err)
	}
}

// TestDaemonLogNameSanitizes covers the daemonLogName function.
func TestDaemonLogNameSanitizes(t *testing.T) {
	// Special characters are replaced with _.
	got := daemonLogName("session/../evil")
	want := "daemon-session____evil.log"
	if got != want {
		t.Fatalf("daemonLogName = %q, want %q", got, want)
	}
}

// TestCleanUpDaemonLogTemp covers the cleanup function.
func TestCleanUpDaemonLogTemp(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "daemon-*.log")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	cleanUpDaemonLogTemp(f)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanUpDaemonLogTemp should remove the file: %v", err)
	}
}

// TestOpenDaemonLogPendingSuccess covers the pending-log happy path.
func TestOpenDaemonLogPendingSuccess(t *testing.T) {
	dir := t.TempDir()
	l, err := openDaemonLog(dir, "")
	if err != nil {
		t.Fatalf("openDaemonLog pending: %v", err)
	}
	defer func() { _ = l.file.Close() }()
	if !l.pending {
		t.Fatalf("pending log should have pending=true")
	}
	if l.path == "" {
		t.Fatalf("pending log should have a path")
	}
}

// TestOpenDaemonLogWithSessionID covers the session-ID happy path.
func TestOpenDaemonLogWithSessionID(t *testing.T) {
	dir := t.TempDir()
	l, err := openDaemonLog(dir, "033z7k96Nj0LLiLImAqa9s")
	if err != nil {
		t.Fatalf("openDaemonLog with session ID: %v", err)
	}
	defer func() { _ = l.file.Close() }()
	if l.pending {
		t.Fatalf("session log should have pending=false")
	}
	if l.replacementTarget == "" {
		t.Fatalf("session log should have a replacement target")
	}
	if l.launchOffset != 0 {
		t.Fatalf("launchOffset for new log should be 0, got %d", l.launchOffset)
	}
}
