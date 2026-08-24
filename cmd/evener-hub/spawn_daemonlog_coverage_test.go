package hub

import (
	"os"
	"path/filepath"
	"strings"
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

// TestDaemonLogAdoptRenamesPending covers the adopt path where a pending log
// is renamed to the session's canonical name.
func TestDaemonLogAdoptRenamesPending(t *testing.T) {
	dir := t.TempDir()
	l, err := openDaemonLog(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.file.Close() }()
	l.adopt("033z7k96Nj0LLiLImAqa9s")
	if l.pending {
		t.Fatalf("adopt should clear pending")
	}
	if filepath.Base(l.path) != "daemon-033z7k96Nj0LLiLImAqa9s.log" {
		t.Fatalf("adopt should rename to canonical name, got %q", filepath.Base(l.path))
	}
}

// TestDaemonLogAdoptSamePath covers the early-return path when the target
// path is the same as the current path.
func TestDaemonLogAdoptSamePath(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	l := &daemonLog{file: f, path: f.Name(), pending: true}
	// Set path to the canonical name it would adopt to.
	canonical := filepath.Join(filepath.Dir(f.Name()), daemonLogName("mysession"))
	l.path = canonical
	l.adopt("mysession")
	// Should have cleared pending but not renamed (same path).
	if l.pending {
		t.Fatalf("adopt should clear pending")
	}
}

// TestDaemonLogPromoteRenames covers the promote path with a replacement
// target that succeeds.
func TestDaemonLogPromoteRenames(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-replacement-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	target := filepath.Join(dir, "daemon-target.log")
	l := &daemonLog{file: f, path: f.Name(), replacementTarget: target}
	if err := l.promote(); err != nil {
		t.Fatalf("promote should succeed: %v", err)
	}
	if l.path != target {
		t.Fatalf("promote should set path to target, got %q", l.path)
	}
	if l.replacementTarget != "" {
		t.Fatalf("promote should clear replacementTarget")
	}
}

// TestDaemonLogRemoveIfPending covers the removeIfPending path for a pending
// log.
func TestDaemonLogRemoveIfPending(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-pending-*.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	path := f.Name()
	l := &daemonLog{path: path, pending: true}
	l.removeIfPending()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removeIfPending should delete the file")
	}
}

// TestDaemonLogRemoveIfPendingNotPending covers the no-op path when the log
// is not pending.
func TestDaemonLogRemoveIfPendingNotPending(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-*.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	path := f.Name()
	l := &daemonLog{path: path, pending: false}
	l.removeIfPending()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("removeIfPending on non-pending should leave the file")
	}
}

// TestDaemonLogRemoveIfUncommittedReplacement covers the replacement-candidate
// removal path.
func TestDaemonLogRemoveIfUncommittedReplacement(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-replacement-*.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	path := f.Name()
	l := &daemonLog{path: path, replacementTarget: filepath.Join(dir, "target.log")}
	l.removeIfUncommitted()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removeIfUncommitted should delete replacement candidate")
	}
}

// TestDaemonLogRemoveIfUncommittedPending covers the pending-log removal path
// via removeIfUncommitted.
func TestDaemonLogRemoveIfUncommittedPending(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-pending-*.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	path := f.Name()
	l := &daemonLog{path: path, pending: true}
	l.removeIfUncommitted()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removeIfUncommitted should delete pending log")
	}
}

// TestDaemonLogTailWithContent covers the tail function with actual content.
func TestDaemonLogTailWithContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon-test.log")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &daemonLog{path: path, launchOffset: 0}
	got := l.tail(1024)
	if got != content {
		t.Fatalf("tail = %q, want %q", got, content)
	}
}

// TestDaemonLogTailWithLaunchOffset covers tail with a non-zero launchOffset.
func TestDaemonLogTailWithLaunchOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon-test.log")
	content := "old content\nnew content\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &daemonLog{path: path, launchOffset: 12} // skip "old content\n"
	got := l.tail(1024)
	if got != "new content\n" {
		t.Fatalf("tail = %q, want 'new content\\n'", got)
	}
}

// TestDaemonLogClose covers the close function.
func TestDaemonLogClose(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "daemon-*.log")
	if err != nil {
		t.Fatal(err)
	}
	l := &daemonLog{file: f}
	l.close()
	// File should be closed; a second close should error.
	if err := f.Close(); err == nil {
		t.Fatalf("close should have closed the file")
	}
}

// TestWriteDaemonLogSnapshotLargeSource covers the trimming path for a source
// larger than daemonLogRetainedBytes.
func TestWriteDaemonLogSnapshotLargeSource(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "large.log")
	// Write a source larger than daemonLogRetainedBytes.
	big := make([]byte, daemonLogRetainedBytes+100)
	for i := range big {
		big[i] = 'x'
	}
	big[daemonLogRetainedBytes+50] = '\n'
	if err := os.WriteFile(srcPath, big, 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err := os.CreateTemp(dir, "dst-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	if err := writeDaemonLogSnapshot(dst, srcPath); err != nil {
		t.Fatalf("writeDaemonLogSnapshot on large source: %v", err)
	}
	info, _ := dst.Stat()
	if info.Size() > daemonLogRetainedBytes {
		t.Fatalf("snapshot size = %d, should be <= %d", info.Size(), daemonLogRetainedBytes)
	}
}

// TestCopyDaemonLogSnapshotUnexpectedEOF covers the short-copy path.
func TestCopyDaemonLogSnapshotUnexpectedEOF(t *testing.T) {
	// A reader that returns 0 bytes with io.EOF immediately.
	src := strings.NewReader("")
	var sb tailBuffer
	err := copyDaemonLogSnapshot(&sb, src, 100)
	if err == nil {
		t.Fatalf("copyDaemonLogSnapshot with empty reader should error")
	}
}

// TestOpenDaemonLogResumeWithExistingLog covers the resume path where an
// existing log is snapshotted into a replacement.
func TestOpenDaemonLogResumeWithExistingLog(t *testing.T) {
	dir := t.TempDir()
	sessionID := "033z7k96Nj0LLiLImAqa9s"
	// Create an existing canonical log.
	canonicalPath := filepath.Join(dir, daemonLogDirName, daemonLogName(sessionID))
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonicalPath, []byte("old daemon output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := openDaemonLog(dir, sessionID)
	if err != nil {
		t.Fatalf("openDaemonLog resume: %v", err)
	}
	defer func() { _ = l.file.Close() }()
	if l.launchOffset != 18 {
		t.Fatalf("launchOffset = %d, want 18", l.launchOffset)
	}
}
