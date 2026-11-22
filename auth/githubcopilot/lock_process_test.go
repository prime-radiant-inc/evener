//go:build unix

package githubcopilot

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	lockHelperModeEnv    = "EVENER_TEST_COPILOT_LOCK_MODE"
	lockHelperStateEnv   = "EVENER_TEST_COPILOT_LOCK_STATE"
	lockHelperMarkerEnv  = "EVENER_TEST_COPILOT_LOCK_MARKER"
	lockHelperReadyEnv   = "EVENER_TEST_COPILOT_LOCK_READY"
	lockHelperMarkersEnv = "EVENER_TEST_COPILOT_LOCK_MARKERS"
)

// lockIsolationWindow is how long the test waits to see whether a second
// process enters the critical section while the first is still inside it. A
// working lock blocks that process for the whole window; an unlocked
// implementation lets it in almost immediately.
const lockIsolationWindow = 200 * time.Millisecond

// TestWithRecordLockSerializesReadModifyWriteAcrossProcesses starts two real
// test processes that each take the lock, read the record, pause inside the
// critical section, append a marker, and save. The test first proves mutual
// exclusion: while one process is inside the lock, the other must not enter.
// It then releases both and asserts both markers survived, which requires the
// second reader to see the first writer's save.
func TestWithRecordLockSerializesReadModifyWriteAcrossProcesses(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	record.AccessToken = "start"
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	markersDir := t.TempDir()

	first := startRecordLockHelper(t, stateDir, "serialize", "A", "", markersDir)
	second := startRecordLockHelper(t, stateDir, "serialize", "B", "", markersDir)

	waitForFile(t, markerPath(markersDir, "attempting-A"))
	waitForFile(t, markerPath(markersDir, "attempting-B"))
	entered := waitForAnyFile(t, markerPath(markersDir, "loaded-A"), markerPath(markersDir, "loaded-B"))
	other := markerPath(markersDir, "loaded-A")
	if entered == other {
		other = markerPath(markersDir, "loaded-B")
	}
	if fileAppearsWithin(other, lockIsolationWindow) {
		t.Fatalf("both helper processes entered WithRecordLock at once (%s and %s)", entered, other)
	}

	writeGo(t, first)
	writeGo(t, second)
	if err := first.wait(); err != nil {
		t.Fatalf("first helper process: %v\n%s", err, first.output())
	}
	if err := second.wait(); err != nil {
		t.Fatalf("second helper process: %v\n%s", err, second.output())
	}

	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	for _, marker := range []string{"+A", "+B"} {
		if !strings.Contains(loaded.AccessToken, marker) {
			t.Fatalf("access token %q lost marker %q; read-modify-write was not serialized", loaded.AccessToken, marker)
		}
	}
}

// TestWithRecordLockStaleWriterCannotRecreateDeletedRecord proves the
// documented caller protocol: the helper reads a snapshot, the parent deletes
// the record under the lock, and the helper re-reads under the lock before
// saving. Seeing ErrAuthNotFound, it must leave the deletion alone.
func TestWithRecordLockStaleWriterCannotRecreateDeletedRecord(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	helper := startRecordLockHelper(t, stateDir, "stale", "", readyPath, "")
	waitForFile(t, readyPath)

	if err := WithRecordLock(stateDir, record.Instance, func() error {
		return DeleteAuth(stateDir, record.Instance)
	}); err != nil {
		t.Fatalf("DeleteAuth() under lock error = %v", err)
	}
	writeGo(t, helper)
	if err := helper.wait(); err != nil {
		t.Fatalf("stale writer process: %v\n%s", err, helper.output())
	}
	if !strings.Contains(helper.output(), "aborted:not-found") {
		t.Fatalf("stale writer output = %q, want it to abort on the deleted record", helper.output())
	}
	if _, err := LoadAuth(stateDir, record.Instance); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() after stale write attempt error = %v, want ErrAuthNotFound", err)
	}
}

func TestWithRecordLockReturnsCallbackErrorAndReleases(t *testing.T) {
	stateDir := t.TempDir()
	wantErr := errors.New("callback failed")
	if err := WithRecordLock(stateDir, "github-copilot", func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("WithRecordLock() error = %v, want %v", err, wantErr)
	}
	ran := false
	if err := WithRecordLock(stateDir, "github-copilot", func() error {
		ran = true
		return nil
	}); err != nil || !ran {
		t.Fatalf("WithRecordLock() after callback error = %v, ran = %v, want nil and true", err, ran)
	}
}

type recordLockHelper struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    bytes.Buffer
	waited bool
}

func startRecordLockHelper(t *testing.T, stateDir, mode, marker, readyPath, markersDir string) *recordLockHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecordLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		lockHelperModeEnv+"="+mode,
		lockHelperStateEnv+"="+stateDir,
		lockHelperMarkerEnv+"="+marker,
		lockHelperReadyEnv+"="+readyPath,
		lockHelperMarkersEnv+"="+markersDir,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe() error = %v", err)
	}
	helper := &recordLockHelper{cmd: cmd, stdin: stdin}
	cmd.Stdout = &helper.out
	cmd.Stderr = &helper.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if !helper.waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return helper
}

func (h *recordLockHelper) wait() error {
	h.waited = true
	return h.cmd.Wait()
}

func (h *recordLockHelper) output() string { return h.out.String() }

func writeGo(t *testing.T, helper *recordLockHelper) {
	t.Helper()
	if _, err := io.WriteString(helper.stdin, "go\n"); err != nil {
		t.Fatalf("signal helper process: %v", err)
	}
}

func markerPath(dir, name string) string { return filepath.Join(dir, name) }

func writeMarker(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(markerPath(dir, name), nil, 0o600); err != nil {
		t.Fatalf("write marker %s: %v", name, err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	if !fileAppearsWithin(path, 10*time.Second) {
		t.Fatalf("timed out waiting for %s", path)
	}
}

func waitForAnyFile(t *testing.T, paths ...string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for any of %v", paths)
		}
		<-time.After(time.Millisecond)
	}
}

func fileAppearsWithin(path string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		<-time.After(time.Millisecond)
	}
}

func TestRecordLockHelperProcess(t *testing.T) {
	mode := os.Getenv(lockHelperModeEnv)
	if mode == "" {
		t.Skip("record lock helper subprocess only")
	}
	stateDir := os.Getenv(lockHelperStateEnv)
	instance := sampleRecord().Instance

	switch mode {
	case "serialize":
		markersDir := os.Getenv(lockHelperMarkersEnv)
		marker := os.Getenv(lockHelperMarkerEnv)
		writeMarker(t, markersDir, "attempting-"+marker)
		if err := WithRecordLock(stateDir, instance, func() error {
			record, err := LoadAuth(stateDir, instance)
			if err != nil {
				return err
			}
			writeMarker(t, markersDir, "loaded-"+marker)
			waitForGo(t)
			record.AccessToken += "+" + marker
			return SaveAuth(stateDir, instance, record)
		}); err != nil {
			t.Fatalf("serialized write under lock: %v", err)
		}
	case "stale":
		snapshot, err := LoadAuth(stateDir, instance)
		if err != nil {
			t.Fatalf("snapshot read: %v", err)
		}
		if err := os.WriteFile(os.Getenv(lockHelperReadyEnv), []byte("snapshot\n"), 0o600); err != nil {
			t.Fatalf("write readiness file: %v", err)
		}
		waitForGo(t)
		err = WithRecordLock(stateDir, instance, func() error {
			current, err := LoadAuth(stateDir, instance)
			if errors.Is(err, ErrAuthNotFound) {
				_, _ = os.Stdout.WriteString("aborted:not-found\n")
				return nil
			}
			if err != nil {
				return err
			}
			if current.AccessToken != snapshot.AccessToken {
				_, _ = os.Stdout.WriteString("aborted:changed\n")
				return nil
			}
			current.AccessToken += "+stale"
			return SaveAuth(stateDir, instance, current)
		})
		if err != nil {
			t.Fatalf("stale write under lock: %v", err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func waitForGo(t *testing.T) {
	t.Helper()
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatalf("wait for go signal: %v", err)
	}
}
