//go:build darwin || linux

package llm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCovOpenPrivateAPILogFileNotRegular(t *testing.T) {
	file, err := openPrivateAPILogFile("/dev/null")
	if file != nil {
		_ = file.Close()
		t.Fatal("openPrivateAPILogFile(/dev/null) returned a file")
	}
	want := `API-log target "/dev/null" is not a regular file`
	if err == nil || err.Error() != want {
		t.Fatalf("openPrivateAPILogFile(/dev/null) error = %v, want %q", err, want)
	}
}

func TestCovOpenPrivateAPILogFileTargetLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	owner, err := openPrivateAPILogFile(path)
	if err != nil {
		t.Fatalf("first openPrivateAPILogFile: %v", err)
	}
	defer owner.Close()

	contender, err := openPrivateAPILogFile(path)
	if contender != nil {
		_ = contender.Close()
		t.Fatal("second openPrivateAPILogFile returned a file")
	}
	if !errors.Is(err, ErrAPILogTargetLocked) {
		t.Fatalf("second open error = %v, want ErrAPILogTargetLocked identity", err)
	}
	want := fmt.Sprintf("%s: %s", ErrAPILogTargetLocked, path)
	if err.Error() != want {
		t.Fatalf("second open error = %q, want %q", err, want)
	}
}

// TestSessionFileWithErrorRecoversAfterTransientLockReleases covers issue #744:
// a transient flock collision (ErrAPILogTargetLocked) must not latch a session
// out of canonical logging the way a permanent failure does (contrast with
// TestSessionFileWithErrorNilCache in apilog_coverage_test.go). Once the
// contending holder releases the lock, the next call must retry the open
// instead of replaying a cached failure.
//
// This lives here, not next to TestSessionFileWithErrorNilCache, because it
// depends on the real flock semantics openPrivateAPILogFile only implements
// on darwin/linux (see apilog_open_other.go): on other platforms
// openPrivateAPILogFile never contends, so the ErrAPILogTargetLocked
// assertion below would fail.
func TestSessionFileWithErrorRecoversAfterTransientLockReleases(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	defer logger.Close()

	// Hold the flock on "unattributed"'s target file the way another evener
	// process would (e.g. a hub-spawned serve daemon that reached this
	// project's session log first) — same mechanism as
	// TestCovOpenPrivateAPILogFileTargetLocked above, aimed at the session
	// route.
	target := filepath.Join(stateDir, "sessions", "unattributed.api.jsonl")
	contender, err := openPrivateAPILogFile(target)
	if err != nil {
		t.Fatalf("open contender lock: %v", err)
	}

	// First call collides with the contender's flock and fails.
	if _, err := logger.sessionFileWithError(""); !errors.Is(err, ErrAPILogTargetLocked) {
		t.Fatalf("first sessionFileWithError = %v, want ErrAPILogTargetLocked", err)
	}

	// The contender releases the lock — e.g. the other process exits.
	if err := contender.Close(); err != nil {
		t.Fatalf("close contender: %v", err)
	}

	// A later call must retry the open, not replay the cached failure.
	f, err := logger.sessionFileWithError("")
	if err != nil {
		t.Fatalf("sessionFileWithError after lock release = %v, want success", err)
	}
	if f == nil {
		t.Fatal("sessionFileWithError after lock release returned a nil file")
	}
}

func TestCovOpenPrivateAPILogFileOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent-dir", "api.jsonl")
	file, err := openPrivateAPILogFile(path)
	if file != nil {
		_ = file.Close()
		t.Fatal("openPrivateAPILogFile under a missing directory returned a file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("openPrivateAPILogFile under a missing directory error = %v, want fs.ErrNotExist", err)
	}
}

func TestCovOpenPrivateAPILogFileSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	file, err := openPrivateAPILogFile(path)
	if err != nil {
		t.Fatalf("openPrivateAPILogFile: %v", err)
	}
	defer file.Close()

	if file.Name() != path {
		t.Fatalf("opened file name = %q, want %q", file.Name(), path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat opened API log: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("opened API log mode = %v, want regular 0600", info.Mode())
	}
}
