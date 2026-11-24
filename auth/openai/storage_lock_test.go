package openai

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// The store lock is an flock on the auth directory, which the kernel grants per
// open file description: a second holder inside this process contends exactly
// as a second process does. Pin that the second holder waits for the first and
// then proceeds, using the contention seam to know it is parked rather than
// guessing with a window.
//
// Not parallel: it sets the package-wide contention seam.
func TestAuthStoreLockMakesTwoHoldersTakeTurns(t *testing.T) {
	stateDir := t.TempDir()
	if err := SaveAuth(stateDir, "openai", sampleAuthRecord()); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	first, err := LockAuthStore(stateDir)
	if err != nil {
		t.Fatalf("LockAuthStore() error = %v", err)
	}
	defer first.Release()

	waiting := make(chan struct{})
	var once sync.Once
	restore := setAuthStoreLockWaiting(func() { once.Do(func() { close(waiting) }) })
	defer restore()

	type acquisition struct {
		lock AuthStoreLock
		err  error
	}
	second := make(chan acquisition, 1)
	go func() {
		lock, err := LockAuthStore(stateDir)
		second <- acquisition{lock, err}
	}()

	select {
	case <-waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("the second holder never reached the lock wait")
	}
	first.Release()

	var got acquisition
	select {
	case got = <-second:
	case <-time.After(10 * time.Second):
		t.Fatal("the second holder never acquired the lock after its release")
	}
	if got.err != nil {
		t.Fatalf("second LockAuthStore() error = %v", got.err)
	}
	defer got.lock.Release()

	// The second holder is inside its critical section: a save through its
	// handle lands.
	record := sampleAuthRecord()
	record.Provider = "second"
	if err := got.lock.Save("second", record); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	stored, err := LoadAuth(stateDir, "second")
	if err != nil {
		t.Fatalf("LoadAuth(second) error = %v", err)
	}
	if stored.Provider != "second" {
		t.Fatalf("Provider = %q, want the saved record", stored.Provider)
	}
}

const (
	authStoreLockChildEnv    = "EVENER_AUTH_STORE_LOCK_CHILD"
	authStoreLockChildDirEnv = "EVENER_AUTH_STORE_LOCK_CHILD_DIR"
)

// TestSaveAuthWaitsForAHolderInAnotherProcess is the cross-process half: a
// second process's SaveAuth parks while this process holds the store lock and
// lands once it is released. The child reports from the contention seam, so the
// test knows it is waiting instead of theorising about it.
func TestSaveAuthWaitsForAHolderInAnotherProcess(t *testing.T) {
	if os.Getenv(authStoreLockChildEnv) != "" {
		t.Skip("this process is the test's child")
	}
	stateDir := t.TempDir()
	if err := SaveAuth(stateDir, "openai", sampleAuthRecord()); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	holder, err := LockAuthStore(stateDir)
	if err != nil {
		t.Fatalf("LockAuthStore() error = %v", err)
	}
	defer holder.Release()

	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthStoreLockChildSaves$")
	cmd.Env = append(os.Environ(),
		authStoreLockChildEnv+"=1",
		authStoreLockChildDirEnv+"="+stateDir,
	)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	awaitChildLine(t, lines, "waiting")
	holder.Release()
	awaitChildLine(t, lines, "saved")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the child's SaveAuth failed: %v", err)
	}
	stored, err := LoadAuth(stateDir, "child")
	if err != nil {
		t.Fatalf("the child's save did not land: %v", err)
	}
	if stored.Provider != "child" {
		t.Fatalf("Provider = %q, want the record the child saved", stored.Provider)
	}
}

// TestAuthStoreLockChildSaves is the helper process for
// TestSaveAuthWaitsForAHolderInAnotherProcess: it reports from the contention
// seam when its save parks, then reports when the save lands.
func TestAuthStoreLockChildSaves(t *testing.T) {
	stateDir := os.Getenv(authStoreLockChildDirEnv)
	if os.Getenv(authStoreLockChildEnv) == "" || stateDir == "" {
		t.Skip("helper process for TestSaveAuthWaitsForAHolderInAnotherProcess")
	}
	restore := setAuthStoreLockWaiting(func() { fmt.Println("waiting") })
	defer restore()

	record := sampleAuthRecord()
	record.Provider = "child"
	record.AccessToken = "child-access"
	if err := SaveAuth(stateDir, "child", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	fmt.Println("saved")
}

// awaitChildLine waits for one exact line from the child. The guard is a stall
// detector, not a scheduling window: the line the caller waits for is printed
// by the child itself. The auth module cannot reach the root module's
// internal/testwait helper (a leaf module takes no new module edge), so the
// select is written here.
func awaitChildLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the child's output ended before it reported %q", want)
			}
			if line == want {
				return
			}
		case <-timeout:
			t.Fatalf("the child never reported %q", want)
		}
	}
}
