package hostops

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
)

// The crash-window and fault arms of the custody-first quarantine: the boot's
// writes go through the storeFaults seams, so a directory-sync failure at each
// ordered step of the recovery is observable as the refusal (or the durable
// landing) that step produces. The happy recovery paths live in
// quarantine_test.go; these rows pin what each step's failure leaves on disk.

// failingSyncAt returns a syncDir seam that lets the first n-1 directory syncs
// succeed and fails the nth, so a row can name exactly which write in the
// recovery's fixed order loses its durability.
func failingSyncAt(n int) func(afero.Fs, string) error {
	var calls atomic.Int64
	return func(_ afero.Fs, _ string) error {
		if int(calls.Add(1)) == n {
			return errors.New("directory sync refused")
		}
		return nil
	}
}

// TestQuarantineFaultLadderRefusesAtEachWrite pins the fresh-quarantine order
// (§4): the intent write, the custody write, the epoch write, the corrupt-file
// rename, the replacement write and the intent clear each refuse when their
// own directory sync fails, and the artifacts already landed stay on disk. The
// replacement's post-rename sync failure is the one write whose rename already
// committed the state, so that rung boots and serves.
func TestQuarantineFaultLadderRefusesAtEachWrite(t *testing.T) {
	t.Parallel()
	// The recovery's sync order beside a store whose directory and parent both
	// exist: each artifact write syncs the parent chain (2) plus its own
	// directory entry (1), the aside rename syncs once, the replacement store's
	// save syncs like an artifact write (3), and the intent clear syncs once.
	const (
		intentWriteSync  = 3 // ensureStoreDir(2) + the intent's own entry
		custodyWriteSync = 6
		epochWriteSync   = 9
		asideRenameSync  = 10
		replacementSync  = 13
		intentClearSync  = 14
	)

	cases := []struct {
		name    string
		failAt  int
		wantErr bool
		check   func(t *testing.T, dir string)
	}{
		{
			name:    "the intent write's sync fails",
			failAt:  intentWriteSync,
			wantErr: true,
			check: func(t *testing.T, dir string) {
				path := StorePath(dir)
				if _, err := os.Stat(quarantineIntentPath(path)); err != nil {
					t.Fatalf("the intent file did not land: %v", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("the corrupt store must still stand at its path: %v", err)
				}
			},
		},
		{
			name:    "the custody write's sync fails",
			failAt:  custodyWriteSync,
			wantErr: true,
			check: func(t *testing.T, dir string) {
				path := StorePath(dir)
				if _, err := os.Stat(quarantineIntentPath(path)); err != nil {
					t.Fatalf("the intent file did not land: %v", err)
				}
				custodyPath, _ := quarantineArtifact(t, filepath.Dir(path), ".custody-")
				if _, err := os.Stat(custodyPath); err != nil {
					t.Fatalf("the custody file did not land: %v", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("the corrupt store must still stand at its path: %v", err)
				}
			},
		},
		{
			name:    "the epoch write's sync fails",
			failAt:  epochWriteSync,
			wantErr: true,
			check: func(t *testing.T, dir string) {
				path := StorePath(dir)
				if _, err := os.Stat(quarantineEpochPath(path)); err != nil {
					t.Fatalf("the epoch sidecar did not land: %v", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("the corrupt store must still stand at its path: %v", err)
				}
			},
		},
		{
			name:    "the aside rename's sync fails",
			failAt:  asideRenameSync,
			wantErr: true,
			check: func(t *testing.T, dir string) {
				path := StorePath(dir)
				aside, _ := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
				if info, err := os.Stat(aside); err != nil || !info.Mode().IsRegular() {
					t.Fatalf("the aside file did not land beside the store (stat err = %v)", err)
				}
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the corrupt store must have been renamed aside (stat err = %v)", err)
				}
			},
		},
		{
			name:    "the replacement's own post-rename sync fails but the replacement serves",
			failAt:  replacementSync,
			wantErr: false,
			check: func(t *testing.T, dir string) {
				path := StorePath(dir)
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("the replacement store did not land: %v", err)
				}
			},
		},
		{
			name:    "the intent clear's sync fails",
			failAt:  intentClearSync,
			wantErr: true,
			check: func(t *testing.T, dir string) {
				path := StorePath(dir)
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("the replacement store did not land: %v", err)
				}
				if _, err := os.Stat(quarantineIntentPath(path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the intent was removed before its sync was proven (stat err = %v)", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := StorePath(dir)
			writeRawStore(t, path, 0o600, quarantinedStoreJSON)
			_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(tc.failAt)})
			if tc.wantErr && err == nil {
				t.Fatal("the boot served past a failed durability step")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("the boot refused a replacement whose rename landed: %v", err)
			}
			tc.check(t, dir)
		})
	}
}

// TestQuarantineFaultLadderBeforeRenameRefusesTheReplacement pins the arm
// where the replacement's own write is refused before its rename: the corrupt
// file is already aside with its custody complete, and the boot reports the
// refusal instead of serving a replacement it never wrote.
func TestQuarantineFaultLadderBeforeRenameRefusesTheReplacement(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	_, err := openFS(afero.NewOsFs(), path, storeFaults{beforeRename: func() error {
		return errors.New("disk full before the replacement's rename")
	}})
	if err == nil {
		t.Fatal("the boot served a replacement whose rename never landed")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no replacement may stand at the store path (stat err = %v)", err)
	}
	if _, err := os.Stat(quarantineIntentPath(path)); err != nil {
		t.Fatalf("the intent must stay: the recovery boot owes the replacement (stat err = %v)", err)
	}
}

// seedStaleIntent writes the pending-work set of a quarantine whose corrupt
// file is still at the store path: the intent, its custody file, and — when
// withAside is set — the aside file the rename would have produced.
func seedStaleIntent(t *testing.T, path, stamp string, withAside bool) {
	t.Helper()
	if err := os.WriteFile(quarantineCustodyPath(path, stamp), validCustodyBody(t, path, nil), 0o600); err != nil {
		t.Fatalf("write custody: %v", err)
	}
	if withAside {
		if err := os.WriteFile(quarantineAsidePath(path, stamp), []byte("corrupt bytes"), 0o600); err != nil {
			t.Fatalf("write aside: %v", err)
		}
	}
	if err := os.WriteFile(quarantineIntentPath(path), validIntentBody(t, path, stamp, nil), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}
}

// TestQuarantineStaleIntentCleanupFaults pins the stale-intent cleanup's own
// steps: a clean store standing at the path makes the intent's custody an
// orphan, and the boot clears the intent, removes the orphaned custody and
// persists the epoch — each step refusing when its own directory sync fails.
func TestQuarantineStaleIntentCleanupFaults(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"
	// The epoch-converge write at the top of the resolve owns the first three
	// syncs; the intent clear and the custody removal follow.
	const (
		intentClearSync = 4
		custodyRmSync   = 5
		orphanRmSync    = 6
	)

	t.Run("the intent clear's sync fails", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, validStoreJSON)
		seedStaleIntent(t, path, stamp, false)
		_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(intentClearSync)})
		if err == nil {
			t.Fatal("the boot served past a failed intent clear")
		}
		// The removal lands before its sync, so the refusal leaves the intent
		// gone while naming the sync that was never proven.
		if _, err := os.Stat(quarantineIntentPath(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the intent was not removed (stat err = %v)", err)
		}
	})

	t.Run("the orphaned custody removal's sync fails", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, validStoreJSON)
		seedStaleIntent(t, path, stamp, false)
		_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(custodyRmSync)})
		if err == nil {
			t.Fatal("the boot served past a failed custody removal")
		}
		if _, err := os.Stat(quarantineCustodyPath(path, stamp)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the orphaned custody was not removed (stat err = %v)", err)
		}
	})

	t.Run("a further orphan's removal fault still refuses", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, validStoreJSON)
		seedStaleIntent(t, path, stamp, false)
		// A second custody file with neither aside nor intent: an orphan the
		// clean-store path must also remove.
		orphanStamp := "20260926T010000.000000000Z"
		if err := os.WriteFile(quarantineCustodyPath(path, orphanStamp), validCustodyBody(t, path, nil), 0o600); err != nil {
			t.Fatalf("write orphan custody: %v", err)
		}
		_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(orphanRmSync)})
		if err == nil {
			t.Fatal("the boot served past a failed orphan removal")
		}
		if _, err := os.Stat(quarantineCustodyPath(path, orphanStamp)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the orphan was not removed (stat err = %v)", err)
		}
	})
}

// TestQuarantineCoveredWindowRenameFault pins the covered-window arm: the
// intent's custody is complete, the aside is missing and the corrupt file is
// still at the path, so the boot re-runs the rename — and a sync failure of
// that rename refuses the boot with the corrupt file preserved.
func TestQuarantineCoveredWindowRenameFault(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	seedStaleIntent(t, path, stamp, false)
	// The custody file proves an epoch above the absent sidecar, so the
	// top-of-resolve converge write owns syncs 1-3 and the aside rename's
	// sync is the fourth.
	_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(4)})
	if err == nil {
		t.Fatal("the boot served past a failed aside rename")
	}
	aside, _ := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
	if _, err := os.Stat(aside); err != nil {
		t.Fatalf("the corrupt file was not renamed aside: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the corrupt store must be gone from its path (stat err = %v)", err)
	}
}

// TestQuarantineCoveredWindowReplacementFaults pins the replacement-from-
// custody steps' fault arms: the rename landed, and the recovery owes the
// replacement store, the intent clear and the epoch persist. A refusal at the
// replacement's before-rename seam, at the intent clear's sync, or at the
// epoch write's sync leaves the boot refused with the evidence intact.
func TestQuarantineCoveredWindowReplacementFaults(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"
	seed := func(t *testing.T) (string, string) {
		t.Helper()
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, quarantinedStoreJSON)
		seedStaleIntent(t, path, stamp, false)
		return dir, path
	}

	t.Run("the replacement's rename is refused", func(t *testing.T) {
		t.Parallel()
		_, path := seed(t)
		_, err := openFS(afero.NewOsFs(), path, storeFaults{beforeRename: func() error {
			return errors.New("disk full before the replacement's rename")
		}})
		if err == nil {
			t.Fatal("the boot served without writing the replacement")
		}
		if _, err := os.Stat(quarantineIntentPath(path)); err != nil {
			t.Fatalf("the intent must stay for the next boot: %v", err)
		}
	})

	t.Run("the intent clear's sync fails after the replacement landed", func(t *testing.T) {
		t.Parallel()
		_, path := seed(t)
		// Sync order from the covered window: the top-of-resolve converge
		// (1-3), the aside rename (4), the replacement's parent chain and
		// entry (5-7), the intent clear (8).
		_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(8)})
		if err == nil {
			t.Fatal("the boot served past a failed intent clear")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the replacement must have landed: %v", err)
		}
	})

	// The epoch persist that follows the intent clear is a no-op here: the
	// top-of-resolve converge already persisted the epoch the custody proves,
	// and the counter never regresses.
}

// TestQuarantineStaleIntentAsideCorruptionClearFault pins the stale-intent arm:
// the intent's own rename already landed (the aside exists) and a different
// corruption stands at the path, so the boot clears the stale intent before
// quarantining the new corrupt file in its own custody-first order — and a
// failed clear refuses the boot.
func TestQuarantineStaleIntentAsideCorruptionClearFault(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	seedStaleIntent(t, path, stamp, true)
	// The custody file proves an epoch above the absent sidecar, so the
	// top-of-resolve converge write owns syncs 1-3 and the stale intent's
	// clear is the fourth.
	_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(4)})
	if err == nil {
		t.Fatal("the boot quarantined the new corrupt file without clearing the stale intent first")
	}
	if _, err := os.Stat(quarantineIntentPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stale intent was not removed (stat err = %v)", err)
	}
}

// TestQuarantineVanishedReplacementEpochFault pins the top-of-resolve epoch
// converge arm: custody files prove an epoch above the sidecar's own value, so
// the boot persists the counter before any recovery path runs — and a failed
// persist refuses the boot with the aside and custody left as evidence.
func TestQuarantineVanishedReplacementEpochFault(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"
	dir := t.TempDir()
	path := StorePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	// A completed quarantine whose replacement vanished: the aside and its
	// custody remain, with no store file and no sidecar.
	if err := os.WriteFile(quarantineCustodyPath(path, stamp), validCustodyBody(t, path, nil), 0o600); err != nil {
		t.Fatalf("write custody: %v", err)
	}
	if err := os.WriteFile(quarantineAsidePath(path, stamp), []byte("corrupt bytes"), 0o600); err != nil {
		t.Fatalf("write aside: %v", err)
	}
	// Fail the sidecar's own entry sync (the third), so the file itself lands
	// and the refusal names the durability that was never proven.
	_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(3)})
	if err == nil {
		t.Fatal("the boot rebuilt a replacement without persisting the proven epoch")
	}
	if _, err := os.Stat(quarantineEpochPath(path)); err != nil {
		t.Fatalf("the epoch sidecar did not land: %v", err)
	}
}

// TestQuarantineOrphanCustodyCleanupFault pins the orphan-removal fault in the
// no-intent window: a clean store serves, a custody file with neither aside
// nor intent is removed as the orphan it is, and a failed removal sync refuses
// the boot rather than serving beside unproven evidence.
func TestQuarantineOrphanCustodyCleanupFault(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o600, validStoreJSON)
	if err := os.WriteFile(quarantineCustodyPath(path, stamp), validCustodyBody(t, path, nil), 0o600); err != nil {
		t.Fatalf("write orphan custody: %v", err)
	}
	// The orphan's custody proves epoch 2 above the absent sidecar, so the
	// top-of-resolve converge owns syncs 1-3 and the orphan removal is the
	// fourth.
	_, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: failingSyncAt(4)})
	if err == nil {
		t.Fatal("the boot served past a failed orphan custody removal")
	}
	if _, err := os.Stat(quarantineCustodyPath(path, stamp)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the orphan was not removed (stat err = %v)", err)
	}
}
