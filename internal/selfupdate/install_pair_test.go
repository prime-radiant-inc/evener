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

// TestInstallPairRollsBackOnSecondFailure proves a failure installing the
// second binary restores the first: stage-then-commit with rollback, so a
// cancelled or failed upgrade never leaves evener and evener-dev from
// different releases. Fails today: sequential commit leaves binary one
// replaced when binary two errors.
func TestInstallPairRollsBackOnSecondFailure(t *testing.T) {
	extractDir := t.TempDir()
	shareBin := filepath.Join(t.TempDir(), "share")
	binDir := filepath.Join(t.TempDir(), "bin")
	for _, d := range []string{extractDir, shareBin, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldBody := []byte("old release binary")
	for _, bin := range installBinaries {
		if err := os.WriteFile(filepath.Join(extractDir, bin), []byte("new "+bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shareBin, bin), oldBody, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(shareBin, bin), filepath.Join(binDir, bin)); err != nil {
			t.Fatal(err)
		}
	}

	previous := renameFile
	renameFile = func(oldpath, newpath string) error {
		// Fail the commit of the second binary only.
		if filepath.Base(newpath) == installBinaries[1] {
			return errors.New("injected second-binary failure")
		}
		return os.Rename(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = previous })

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected the injected second-binary failure")
	}
	for _, bin := range installBinaries {
		got, err := os.ReadFile(filepath.Join(shareBin, bin))
		if err != nil {
			t.Fatalf("read %s: %v", bin, err)
		}
		if !bytes.Equal(got, oldBody) {
			t.Fatalf("%s = %q after failed install, want the old release restored (no mixed pair)", bin, got)
		}
		link, err := os.Readlink(filepath.Join(binDir, bin))
		if err != nil {
			t.Fatalf("readlink %s: %v", bin, err)
		}
		if link != filepath.Join(shareBin, bin) {
			t.Fatalf("link %s -> %q, want the managed binary", bin, link)
		}
	}
}

// TestInstallPairRestoresBinDirEntrypoints proves rollback covers the
// binDir entrypoints, not just the managed binaries: with a custom symlink
// for evener and no entry for evener-dev, a mid-commit failure must leave
// the custom link untouched and no dangling new link. Fails today:
// restore() only handles shareBinDir files.
func TestInstallPairRestoresBinDirEntrypoints(t *testing.T) {
	extractDir := t.TempDir()
	shareBin := filepath.Join(t.TempDir(), "share")
	binDir := filepath.Join(t.TempDir(), "bin")
	for _, d := range []string{extractDir, shareBin, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldBody := []byte("old release binary")
	for _, bin := range installBinaries {
		if err := os.WriteFile(filepath.Join(extractDir, bin), []byte("new "+bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shareBin, bin), oldBody, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Custom pre-existing entrypoint for evener only; evener-dev absent.
	customTarget := filepath.Join(t.TempDir(), "custom-evener")
	if err := os.WriteFile(customTarget, []byte("custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(customTarget, filepath.Join(binDir, "evener")); err != nil {
		t.Fatal(err)
	}

	previous := renameFile
	renameFile = func(oldpath, newpath string) error {
		if filepath.Base(newpath) == installBinaries[1] {
			return errors.New("injected second-binary failure")
		}
		return os.Rename(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = previous })

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected the injected second-binary failure")
	}
	// Custom evener link must be untouched.
	got, err := os.Readlink(filepath.Join(binDir, "evener"))
	if err != nil {
		t.Fatalf("readlink evener: %v", err)
	}
	if got != customTarget {
		t.Fatalf("evener link -> %q, want custom %q restored", got, customTarget)
	}
	// Absent evener-dev entry must not dangle: either removed or pointing
	// at a live managed binary.
	if got, err := os.Readlink(filepath.Join(binDir, "evener-dev")); err == nil {
		if _, statErr := os.Stat(got); statErr != nil && !filepath.IsAbs(got) {
			t.Fatalf("evener-dev link dangles at %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(binDir, "evener-dev")); statErr != nil {
			t.Fatalf("evener-dev entry unusable: %v", statErr)
		}
	}
	// Managed pair still the old release (existing rollback guarantee).
	for _, bin := range installBinaries {
		data, err := os.ReadFile(filepath.Join(shareBin, bin))
		if err != nil {
			t.Fatalf("read %s: %v", bin, err)
		}
		if !bytes.Equal(data, oldBody) {
			t.Fatalf("%s = %q, want old release restored", bin, data)
		}
	}
}

// TestInstallPairRollbackRemovesFreshLinks proves rollback handles the
// binDir entry independently of hadPrev: on a first-time install (no prior
// managed binaries) a mid-commit failure must remove the swapped-in link,
// not leave it dangling after its target is deleted. Fails today:
// restore() `continue`s past link handling when !hadPrev.
func TestInstallPairRollbackRemovesFreshLinks(t *testing.T) {
	extractDir := t.TempDir()
	shareBin := filepath.Join(t.TempDir(), "share")
	binDir := filepath.Join(t.TempDir(), "bin")
	for _, d := range []string{extractDir, shareBin, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// First-time install: extract dir has the new pair, share/bin empty.
	for _, bin := range installBinaries {
		if err := os.WriteFile(filepath.Join(extractDir, bin), []byte("new "+bin), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	previous := renameFile
	renameFile = func(oldpath, newpath string) error {
		if filepath.Base(newpath) == installBinaries[1] {
			return errors.New("injected second-binary failure")
		}
		return os.Rename(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = previous })

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected the injected second-binary failure")
	}
	// The first binary committed its dst and swapped its link before the
	// failure; rollback must undo both, leaving no dangling entrypoint.
	first := installBinaries[0]
	if _, err := os.Lstat(filepath.Join(shareBin, first)); !os.IsNotExist(err) {
		t.Fatalf("managed %s present after failed first-time install (err=%v)", first, err)
	}
	if _, err := os.Lstat(filepath.Join(binDir, first)); !os.IsNotExist(err) {
		t.Fatalf("binDir %s present after failed first-time install (err=%v), want the swapped-in link removed", first, err)
	}
}

// TestInstallPairRestoresOnDigestFailure proves a digest failure after the
// commit rolls the pair back: committed binaries must not stay live when
// the install reports an error and no restart follows. Fails today:
// committed=true is set before digestsUnderLock runs, so a digest error
// leaves the swapped pair on disk unrestored.
func TestInstallPairRestoresOnDigestFailure(t *testing.T) {
	extractDir := t.TempDir()
	shareBin := filepath.Join(t.TempDir(), "share")
	binDir := filepath.Join(t.TempDir(), "bin")
	for _, d := range []string{extractDir, shareBin, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldBody := []byte("old release binary")
	for _, bin := range installBinaries {
		if err := os.WriteFile(filepath.Join(extractDir, bin), []byte("new "+bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shareBin, bin), oldBody, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(shareBin, bin), filepath.Join(binDir, bin)); err != nil {
			t.Fatal(err)
		}
	}

	// Remove one committed binary before digest time: digestsUnderLock must
	// fail, and the install must roll back instead of leaving the new pair.
	previous := renameFile
	renameFile = func(oldpath, newpath string) error {
		if err := os.Rename(oldpath, newpath); err != nil {
			return err
		}
		if filepath.Base(newpath) == installBinaries[1] {
			_ = os.Remove(newpath)
		}
		return nil
	}
	t.Cleanup(func() { renameFile = previous })

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected a digest error from the removed committed binary")
	}
	for _, bin := range installBinaries {
		got, err := os.ReadFile(filepath.Join(shareBin, bin))
		if err != nil {
			t.Fatalf("read %s: %v", bin, err)
		}
		if !bytes.Equal(got, oldBody) {
			t.Fatalf("%s = %q after digest failure, want the old release restored", bin, got)
		}
	}
}

// TestInstallPairPreservesCopiedEntrypoint proves rollback restores a
// pre-existing non-symlink binDir entry: a copied (not symlinked) binary is
// a supported layout, and swapSymlink's rename-over destroys it. Fails
// today: restore() leaves the swapped-in link in place for non-symlinks.
func TestInstallPairPreservesCopiedEntrypoint(t *testing.T) {
	extractDir := t.TempDir()
	shareBin := filepath.Join(t.TempDir(), "share")
	binDir := filepath.Join(t.TempDir(), "bin")
	for _, d := range []string{extractDir, shareBin, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldBody := []byte("old release binary")
	for _, bin := range installBinaries {
		if err := os.WriteFile(filepath.Join(extractDir, bin), []byte("new "+bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shareBin, bin), oldBody, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Copied entrypoint for evener: a regular file, not a symlink.
	copiedBody := []byte("copied entrypoint binary")
	if err := os.WriteFile(filepath.Join(binDir, "evener"), copiedBody, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shareBin, "evener-dev"), filepath.Join(binDir, "evener-dev")); err != nil {
		t.Fatal(err)
	}

	previous := renameFile
	renameFile = func(oldpath, newpath string) error {
		if filepath.Base(newpath) == installBinaries[1] {
			return errors.New("injected second-binary failure")
		}
		return os.Rename(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = previous })

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil {
		t.Fatal("expected the injected second-binary failure")
	}
	got, err := os.ReadFile(filepath.Join(binDir, "evener"))
	if err != nil {
		t.Fatalf("read binDir evener: %v", err)
	}
	if !bytes.Equal(got, copiedBody) {
		t.Fatalf("binDir evener = %q after rollback, want the copied entrypoint restored", got)
	}
	if fi, err := os.Lstat(filepath.Join(binDir, "evener")); err != nil {
		t.Fatalf("lstat binDir evener: %v", err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("binDir evener is a symlink after rollback, want the original regular file")
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
