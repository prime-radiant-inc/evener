//go:build evenerfuzz && (linux || darwin)

package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type coverageLockFile struct{}

func (*coverageLockFile) Fd() uintptr  { return 1 }
func (*coverageLockFile) Close() error { return nil }

func FuzzAtomicGitCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		t.Run("git", coverageGit)
		t.Run("locks", coverageLocks)
	})
}

func coverageGit(t *testing.T) {
	origLook, origMkdir, origRun, origStdout := gitLookPath, gitMkdirAll, gitRun, gitStdout
	t.Cleanup(func() { gitLookPath, gitMkdirAll, gitRun, gitStdout = origLook, origMkdir, origRun, origStdout })
	boom := errors.New("boom")
	gitLookPath = func(string) (string, error) { return "", boom }
	if gitAvailable() {
		t.Fatal("git unexpectedly available")
	}

	dir := filepath.Join(t.TempDir(), "parent", "repo")
	gitMkdirAll = func(string, os.FileMode) error { return boom }
	if err := gitClone(context.Background(), "url", dir, "", ""); err == nil {
		t.Fatal("want mkdir error")
	}
	if err := gitSparseClone(context.Background(), "url", dir, "sub", "", ""); err == nil {
		t.Fatal("want mkdir error")
	}
	gitMkdirAll = os.MkdirAll

	var calls []string
	gitRun = func(_ context.Context, dir string, args ...string) (string, error) {
		calls = append(calls, dir+":"+strings.Join(args, " "))
		return " abc \n", nil
	}
	gitStdout = func(_ context.Context, dir string, args ...string) (string, error) {
		calls = append(calls, "stdout:"+dir+":"+strings.Join(args, " "))
		if len(args) > 0 && args[0] == "cat-file" {
			return "commit", nil
		}
		return "abc1234", nil
	}
	if err := gitClone(context.Background(), "url", dir, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := gitClone(context.Background(), "url", dir, "ref", "sha"); err != nil {
		t.Fatal(err)
	}
	if err := gitClone(context.Background(), "url", dir, "ref", ""); err != nil {
		t.Fatal(err)
	}
	if err := gitSparseClone(context.Background(), "url", dir, "sub", "ref", ""); err != nil {
		t.Fatal(err)
	}
	if err := gitSparseClone(context.Background(), "url", dir, "sub", "", "sha"); err != nil {
		t.Fatal(err)
	}
	if err := gitPull(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if sha, err := gitHeadSHA(context.Background(), dir); err != nil || sha != "abc" {
		t.Fatalf("sha=%q err=%v", sha, err)
	}
	if len(calls) == 0 {
		t.Fatal("no git calls")
	}
	for _, tc := range []struct{ url, ref, sha string }{{"-u", "", ""}, {"u", "-r", ""}, {"u", "", "-h"}} {
		if err := gitClone(context.Background(), tc.url, dir, tc.ref, tc.sha); err == nil {
			t.Fatal("want guard error")
		}
	}

	for failAt := 1; failAt <= 3; failAt++ {
		n := 0
		gitRun = func(context.Context, string, ...string) (string, error) {
			n++
			if n == failAt {
				return "", boom
			}
			return "", nil
		}
		_ = gitClone(context.Background(), "url", dir, "ref", "sha")
	}
	for failAt := 1; failAt <= 3; failAt++ {
		n := 0
		gitRun = func(context.Context, string, ...string) (string, error) {
			n++
			if n == failAt {
				return "", boom
			}
			return "", nil
		}
		_ = gitSparseClone(context.Background(), "url", dir, "sub", "ref", "sha")
	}
	gitRun = func(context.Context, string, ...string) (string, error) { return "", boom }
	if err := gitPull(context.Background(), dir); err == nil {
		t.Fatal("want pull error")
	}
	if _, err := gitHeadSHA(context.Background(), dir); err == nil {
		t.Fatal("want head error")
	}
	for _, tc := range []struct{ url, subdir, ref, sha string }{{"-u", "s", "", ""}, {"u", "-s", "", ""}, {"u", "s", "-r", ""}, {"u", "s", "", "-h"}} {
		if err := gitSparseClone(context.Background(), tc.url, dir, tc.subdir, tc.ref, tc.sha); err == nil {
			t.Fatal("want guard error")
		}
	}
}

func coverageLocks(t *testing.T) {
	origMkdir, origOpen := lockMkdirAll, lockOpenFile
	origFlock, origNow, origSleep := lockFlock, lockNow, lockSleep
	t.Cleanup(func() {
		lockMkdirAll, lockOpenFile, lockFlock, lockNow, lockSleep = origMkdir, origOpen, origFlock, origNow, origSleep
	})
	boom := errors.New("boom")
	lockMkdirAll = func(string, os.FileMode) error { return boom }
	if _, err := acquireLock(context.Background(), "x/lock", 0); err == nil {
		t.Fatal("want mkdir error")
	}
	lockMkdirAll = func(string, os.FileMode) error { return nil }
	lockOpenFile = func(string, int, os.FileMode) (lockFile, error) { return nil, boom }
	if _, err := acquireLock(context.Background(), "x/lock", 0); err == nil {
		t.Fatal("want open error")
	}
	// acquireExistingLock differs only in its open: a lock file that is not
	// there is not an error, it is a store no writer has ever locked.
	lockOpenFile = func(string, int, os.FileMode) (lockFile, error) { return nil, os.ErrNotExist }
	missingRelease, mErr := acquireExistingLock(context.Background(), "x/lock", 0)
	if mErr != nil {
		t.Fatalf("absent lock file: %v", mErr)
	}
	missingRelease()
	lockOpenFile = func(string, int, os.FileMode) (lockFile, error) { return nil, boom }
	if _, err := acquireExistingLock(context.Background(), "x/lock", 0); err == nil {
		t.Fatal("want existing-lock open error")
	}

	lockOpenFile = func(string, int, os.FileMode) (lockFile, error) { return &coverageLockFile{}, nil }
	lockFlock = func(int, int) error { return boom }
	if _, err := acquireLock(context.Background(), "x/lock", 0); err == nil {
		t.Fatal("want flock error")
	}

	lockFlock = func(int, int) error { return unix.EWOULDBLOCK }
	n := 0
	lockNow = func() time.Time { n++; return time.Unix(int64(n), 0) }
	lockSleep = func(time.Duration) {}
	if _, err := acquireLock(context.Background(), "x/lock", 0); err == nil {
		t.Fatal("want timeout")
	}

	flocks := 0
	lockFlock = func(_ int, op int) error {
		flocks++
		if op == unix.LOCK_EX|unix.LOCK_NB && flocks < 7 {
			return unix.EAGAIN
		}
		return nil
	}
	lockNow = func() time.Time { return time.Unix(0, 0) }
	var sleeps []time.Duration
	lockSleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	release, err := acquireLock(context.Background(), "x/lock", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if len(sleeps) < 6 || sleeps[len(sleeps)-1] != 200*time.Millisecond {
		t.Fatalf("sleeps=%v", sleeps)
	}
}
