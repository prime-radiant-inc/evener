package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestInstallLockHonorsContext proves lock acquisition observes the
// caller's deadline: with another holder on the lock, an already-expired
// context fails fast instead of blocking past every timeout. Fails today:
// LOCK_EX blocks indefinitely with no ctx parameter at all.
func TestInstallLockHonorsContext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "share")
	holder, err := acquireInstallLock(dir)
	if err != nil {
		t.Fatalf("acquire holder lock: %v", err)
	}
	defer holder()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = acquireInstallLockCtx(ctx, dir)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error acquiring a held lock with an expiring context")
	}
	if elapsed > 30*time.Second {
		t.Fatalf("lock wait took %v with a 100ms context; acquisition ignores ctx", elapsed)
	}
}

// TestInstallLockAcquiresWhenFree proves the ctx-aware path still grants
// an uncontended lock.
func TestInstallLockAcquiresWhenFree(t *testing.T) {
	release, err := acquireInstallLockCtx(t.Context(), filepath.Join(t.TempDir(), "share"))
	if err != nil {
		t.Fatalf("acquire free lock: %v", err)
	}
	release()
}

// installFixture lays out an upgrade in progress: the new evener extracted,
// and, when oldBody is non-nil, the previous release's evener in the managed
// dir. The caller sets up the binDir entrypoint.
func installFixture(t *testing.T, oldBody []byte) (extractDir, shareBin, binDir string) {
	t.Helper()
	extractDir = t.TempDir()
	shareBin = filepath.Join(t.TempDir(), "share")
	binDir = filepath.Join(t.TempDir(), "bin")
	for _, d := range []string{extractDir, shareBin, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(extractDir, "evener"), []byte("new evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	if oldBody != nil {
		if err := os.WriteFile(filepath.Join(shareBin, "evener"), oldBody, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return extractDir, shareBin, binDir
}

// failEntrypointSwap fails the commit at the binDir entrypoint swap, after the
// managed binary has already been renamed into place.
func failEntrypointSwap(t *testing.T, binDir string) {
	t.Helper()
	previous := renameFile
	renameFile = func(oldpath, newpath string) error {
		if newpath == filepath.Join(binDir, "evener") {
			return errors.New("injected entrypoint swap failure")
		}
		return os.Rename(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = previous })
}

// failDigestAfterCommit lets the whole commit through (binary renamed, its
// entrypoint swapped) and then fails the digest, by removing the committed
// binary right after its rename: the last point where an install can fail.
func failDigestAfterCommit(t *testing.T, shareBin string) {
	t.Helper()
	previous := renameFile
	renameFile = func(oldpath, newpath string) error {
		if err := os.Rename(oldpath, newpath); err != nil {
			return err
		}
		if newpath == filepath.Join(shareBin, "evener") {
			_ = os.Remove(newpath)
		}
		return nil
	}
	t.Cleanup(func() { renameFile = previous })
}

func assertManagedEvener(t *testing.T, shareBin string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(shareBin, "evener"))
	if err != nil {
		t.Fatalf("read managed evener: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("managed evener = %q after the failed install, want the previous release %q restored", got, want)
	}
}

// A failure after the managed binary is renamed into place, at the entrypoint
// swap, restores the previous release: a failed upgrade never leaves the new
// binary live.
func TestInstallRollsBackWhenTheEntrypointSwapFails(t *testing.T) {
	oldBody := []byte("old release binary")
	extractDir, shareBin, binDir := installFixture(t, oldBody)
	if err := os.Symlink(filepath.Join(shareBin, "evener"), filepath.Join(binDir, "evener")); err != nil {
		t.Fatal(err)
	}
	failEntrypointSwap(t, binDir)

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected the injected entrypoint swap failure")
	}
	assertManagedEvener(t, shareBin, oldBody)
	if link, err := os.Readlink(filepath.Join(binDir, "evener")); err != nil || link != filepath.Join(shareBin, "evener") {
		t.Fatalf("entrypoint -> %q (err=%v), want the managed binary", link, err)
	}
}

// A digest failure, after the binary and its entrypoint are both swapped,
// rolls the install back: new bytes must not stay live when the install
// reports an error and no restart follows.
func TestInstallRestoresOnDigestFailure(t *testing.T) {
	oldBody := []byte("old release binary")
	extractDir, shareBin, binDir := installFixture(t, oldBody)
	if err := os.Symlink(filepath.Join(shareBin, "evener"), filepath.Join(binDir, "evener")); err != nil {
		t.Fatal(err)
	}
	failDigestAfterCommit(t, shareBin)

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected a digest error from the removed committed binary")
	}
	assertManagedEvener(t, shareBin, oldBody)
}

// Rollback covers the binDir entrypoint, not just the managed binary: a
// custom symlink the user pointed elsewhere is restored after the swap
// replaced it.
func TestInstallRestoresACustomEntrypoint(t *testing.T) {
	oldBody := []byte("old release binary")
	extractDir, shareBin, binDir := installFixture(t, oldBody)
	customTarget := filepath.Join(t.TempDir(), "custom-evener")
	if err := os.WriteFile(customTarget, []byte("custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(customTarget, filepath.Join(binDir, "evener")); err != nil {
		t.Fatal(err)
	}
	failDigestAfterCommit(t, shareBin)

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected a digest error from the removed committed binary")
	}
	if got, err := os.Readlink(filepath.Join(binDir, "evener")); err != nil || got != customTarget {
		t.Fatalf("entrypoint -> %q (err=%v), want the custom %q restored", got, err, customTarget)
	}
	assertManagedEvener(t, shareBin, oldBody)
}

// On a first-time install (no previous binary, no entrypoint) rollback
// removes the swapped-in link rather than leaving it dangling at a binary it
// also removed.
func TestInstallRollbackRemovesAFreshEntrypoint(t *testing.T) {
	extractDir, shareBin, binDir := installFixture(t, nil)
	failDigestAfterCommit(t, shareBin)

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected a digest error from the removed committed binary")
	}
	if _, err := os.Lstat(filepath.Join(shareBin, "evener")); !os.IsNotExist(err) {
		t.Fatalf("managed evener present after a failed first-time install (err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(binDir, "evener")); !os.IsNotExist(err) {
		t.Fatalf("entrypoint present after a failed first-time install (err=%v), want the swapped-in link removed", err)
	}
}

// A copied (not symlinked) entrypoint is a supported layout, and the swap's
// rename-over destroys it: rollback restores the copied bytes as a regular
// file.
func TestInstallRestoresACopiedEntrypoint(t *testing.T) {
	oldBody := []byte("old release binary")
	extractDir, shareBin, binDir := installFixture(t, oldBody)
	copiedBody := []byte("copied entrypoint binary")
	if err := os.WriteFile(filepath.Join(binDir, "evener"), copiedBody, 0o755); err != nil {
		t.Fatal(err)
	}
	failDigestAfterCommit(t, shareBin)

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected a digest error from the removed committed binary")
	}
	fi, err := os.Lstat(filepath.Join(binDir, "evener"))
	if err != nil {
		t.Fatalf("lstat entrypoint: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("entrypoint is a symlink after rollback, want the original regular file")
	}
	if got, err := os.ReadFile(filepath.Join(binDir, "evener")); err != nil || !bytes.Equal(got, copiedBody) {
		t.Fatalf("entrypoint = %q (err=%v), want the copied entrypoint restored", got, err)
	}
}

// TestUpgradeDigestsUnderLock proves InstalledSHA256 reflects the bytes
// committed while the install lock was held: a test double swaps one
// installed binary between commit and digest time would previously poison
// the pin. Here Upgrade itself computes digests post-commit; this test
// pins the contract that digests match committed bytes by upgrading
// normally and re-hashing each Installed path.
func TestUpgradeDigestsMatchCommittedBytes(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	server := checksumTestServer(t, archive, "")
	t.Cleanup(server.Close)

	prefix := t.TempDir()
	result, err := Upgrade(t.Context(), Options{
		Requested: "snapshot", CurrentChannel: "snapshot",
		Prefix: prefix, GOOS: "linux", GOARCH: "amd64",
		RepoURL: server.URL,
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if len(result.InstalledSHA256) != len(result.Installed) {
		t.Fatalf("digests cover %d of %d installed paths", len(result.InstalledSHA256), len(result.Installed))
	}
	for _, path := range result.Installed {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sum := sha256.Sum256(data)
		if got, want := result.InstalledSHA256[path], hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("digest mismatch for %s", path)
		}
	}
}

// TestRelockVerifyExecSerializesAgainstInstaller proves the exported
// relock helper grants the install lock (and reports contention while
// held): the restart goroutine uses it to close the verify->exec window
// against a concurrent installer.
func TestRelockVerifyExecSerializesAgainstInstaller(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "share")
	holder, err := acquireInstallLock(dir)
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	defer holder()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := AcquireInstallLockForVerify(ctx, dir); err == nil {
		t.Fatal("expected a context error while the install lock is held")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("contended acquire took %v with a 200ms context", elapsed)
	}
	holder()
	release, err := AcquireInstallLockForVerify(context.Background(), dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release()
}
