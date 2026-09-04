//go:build unix

package plugins

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	agentplugin "primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/internal/bundled"
)

// Preview and launch must agree about a store that cannot be published into:
// a read-only <Root>/bundled fails both rather than previewing as selectable
// and then failing the launch it promised.
func TestPreviewForLaunch_ReadOnlyStoreFailsPreviewAndLaunchAlike(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory mode")
	}
	m := NewManager(t.TempDir())
	store := filepath.Join(m.Root, "bundled")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store, 0o755) })

	preview, err := m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := preview.ValidateSelection(); err == nil {
		t.Errorf("preview selected a bundled plugin a launch cannot publish: %+v", preview.Candidates)
	}
	if len(preview.Diagnostics) != 1 || preview.Diagnostics[0].Source != LaunchPluginSourceBundled {
		t.Errorf("preview Diagnostics = %+v, want one bundled diagnostic", preview.Diagnostics)
	}

	launch, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := launch.ValidateSelection(); err == nil {
		t.Fatalf("launch selected a bundled plugin from a read-only store: %+v", launch.Candidates)
	}
}

// Publication cannot destroy foreign data that lands on the destination after
// it was classified. The source of the rename is always a directory (the
// staging payload), and rename(2) refuses to replace anything a foreign writer
// could leave there: a regular file fails with ENOTDIR, a non-empty directory
// with ENOTEMPTY. Only an absent path or an empty directory can be replaced,
// so the window between classifying a destination and renaming into it costs a
// failed rename, never a lost file. What was found there is not taken for the
// published copy either: a directory is set aside whole, and a file is an
// error, so nothing a foreign writer leaves is ever loaded or destroyed.
func TestBundledStore_PublishNeverReplacesForeignData(t *testing.T) {
	tests := []struct {
		name    string
		plant   func(t *testing.T, dest string)
		want    []error
		survive func(t *testing.T, dest string)
	}{
		{
			name: "regular file",
			plant: func(t *testing.T, dest string) {
				if err := os.WriteFile(dest, []byte("someone else's data"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []error{syscall.ENOTDIR},
			survive: func(t *testing.T, dest string) {
				content, err := os.ReadFile(dest)
				if err != nil {
					t.Fatal(err)
				}
				if string(content) != "someone else's data" {
					t.Errorf("destination content = %q, want it untouched", content)
				}
			},
		},
		{
			name: "non-empty directory",
			plant: func(t *testing.T, dest string) {
				if err := os.MkdirAll(filepath.Join(dest, "theirs"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: []error{syscall.ENOTEMPTY, syscall.EEXIST},
			survive: func(t *testing.T, dest string) {
				if _, err := os.Stat(filepath.Join(dest, "theirs")); err != nil {
					t.Errorf("destination lost its contents: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			dest, staging, _, err := m.prepareBundledStore(context.Background(), "coordinator-workflow", publishBundledStore)
			if err != nil {
				t.Fatal(err)
			}
			if staging == nil {
				t.Fatal("prepareBundledStore adopted a published copy in an empty store")
			}
			t.Cleanup(func() {
				_ = os.RemoveAll(staging.dir)
				staging.release()
			})
			if err := os.CopyFS(staging.payload, mustSubFS(bundled.Plugins(), "coordinator-workflow")); err != nil {
				t.Fatal(err)
			}
			// The race the absent-destination check cannot exclude: a foreign
			// writer takes the destination between that check and the rename.
			test.plant(t, dest)

			err = os.Rename(staging.payload, dest)
			if err == nil {
				t.Fatalf("publishing over a %s succeeded", test.name)
			}
			matched := false
			for _, want := range test.want {
				matched = matched || errors.Is(err, want)
			}
			if !matched {
				t.Errorf("rename error = %v, want one of %v", err, test.want)
			}
			test.survive(t, dest)

			// What a publisher that lost the rename does next: it adopts the
			// winner's copy. Foreign data is not that copy, so it is never
			// loaded as the bundled plugin — a directory is set aside, and
			// anything that is not a directory is an error.
			state, classifyErr := classifyBundledDestination(dest, staging.digest)
			if classifyErr == nil && state == bundledDestinationPublished {
				t.Errorf("the %s at the destination was classified as the published copy", test.name)
			}
		})
	}
}

// Removing the copy it staged is part of what a preview promises. When the
// removal fails the caller has to hear about it: the marked staging directory
// is still in the store, and only a later launch's sweep reclaims it.
func TestPreviewForLaunch_ReportsAFailureToRemoveItsStaging(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory mode")
	}
	m := NewManager(t.TempDir())
	store := filepath.Join(m.Root, "bundled")
	t.Cleanup(func() { _ = os.Chmod(store, 0o755) })
	// Sealing the store from the load the preview runs between staging the
	// copy and removing it: the staging directory itself can no longer be
	// unlinked from its parent.
	load := enabledLoad
	enabledLoad = func(dir string) (agentplugin.Instance, error) {
		instance, err := load(dir)
		if chmodErr := os.Chmod(store, 0o500); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		return instance, err
	}
	t.Cleanup(func() { enabledLoad = load })

	res, err := m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		// The inventory is what the caller asked for and it is complete; a
		// failure to clean up after it is a warning about the store, not a
		// reason to throw the answer away and empty the picker.
		t.Fatalf("PreviewForLaunch error = %v, want the inventory it built", err)
	}
	if err := res.ValidateSelection(); err != nil {
		t.Errorf("a cleanup failure made the preview unselectable: %v", err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Source != LaunchPluginSourceBundled {
		t.Errorf("Candidates = %+v, want the preview it built before cleanup failed", res.Candidates)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Source != LaunchPluginSourceBundled ||
		!strings.Contains(res.Diagnostics[0].Message, "staged bundled preview") {
		t.Fatalf("Diagnostics = %+v, want one naming the staging it could not remove", res.Diagnostics)
	}
	entries := bundledStoreEntries(t, store)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), stagingPrefix) {
		t.Fatalf("bundled store holds %v, want the staging the preview could not remove", entries)
	}
	if _, err := os.Lstat(filepath.Join(store, entries[0].Name(), stagingMarker)); err != nil {
		t.Errorf("leftover staging is unmarked, so no sweep will reclaim it: %v", err)
	}
}

// Two launches meeting the same mismatched destination must not undo each
// other's work. Classifying, setting aside and publishing run as one sequence
// under the bundled cache's lock, so a classification made before the lock is
// never acted on after it: the slot beside the destination keeps one copy of
// the foreign content, the destination holds the copy this build publishes,
// and neither is left vacant for a session that is already pointing at it.
func TestBundledStore_ConcurrentPublishKeepsOneConflictAndOneCopy(t *testing.T) {
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 20 {
		m := NewManager(t.TempDir())
		dest := m.bundledPluginPath("coordinator-workflow", digest)
		writePlugin(t, dest, "coordinator-workflow", map[string]string{"theirs.md": "someone else's data"})

		const publishers = 2
		paths := make([]string, publishers)
		errs := make([]error, publishers)
		var wg sync.WaitGroup
		for i := range publishers {
			wg.Go(func() {
				paths[i], _, errs[i] = m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
			})
		}
		wg.Wait()

		for i := range publishers {
			if errs[i] != nil {
				t.Fatalf("attempt %d publisher %d: %v", attempt, i, errs[i])
			}
			if paths[i] != dest {
				t.Fatalf("attempt %d publisher %d published %s, want %s", attempt, i, paths[i], dest)
			}
		}
		found, err := digestFS(os.DirFS(dest))
		if err != nil || found != digest {
			t.Fatalf("attempt %d: destination digest = %q (err %v), want %q", attempt, found, err, digest)
		}
		content, err := os.ReadFile(filepath.Join(dest+conflictSuffix, "theirs.md"))
		if err != nil || string(content) != "someone else's data" {
			t.Fatalf("attempt %d: set-aside content = %q (err %v), want the foreign data preserved", attempt, content, err)
		}
		entries := bundledStoreEntries(t, filepath.Dir(dest))
		if len(entries) != 2 {
			t.Fatalf("attempt %d: bundled store holds %v, want the published copy and one set-aside slot", attempt, entries)
		}
	}
}

// Readying the store can move somebody's directory aside and then fail at the
// next step. What was moved is theirs, so they are told where it went — the
// warning survives the failure that follows it rather than being dropped with
// the candidate that never resolved — and because nothing is going to publish
// into the name it was moved out of, it goes back there and they are told that
// too.
func TestBundledStore_ReportsASetAsideThatIsFollowedByAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through directory modes")
	}
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir())
	dest := m.bundledPluginPath("coordinator-workflow", digest)
	writePlugin(t, dest, "coordinator-workflow", map[string]string{"theirs.md": "someone else's data"})

	// A umask that denies the owner write leaves the store writable (it is
	// already there) but every directory staging creates unwritable, so the
	// set-aside succeeds and the staging that follows it cannot be marked.
	previous := syscall.Umask(0o200)
	t.Cleanup(func() { syscall.Umask(previous) })

	res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.ValidateSelection(); err == nil {
		t.Errorf("selected a bundled plugin the store could not stage: %+v", res.Candidates)
	}
	aside := dest + conflictSuffix
	var setAside, failure, restored bool
	for _, diagnostic := range res.Diagnostics {
		if diagnostic.Source != LaunchPluginSourceBundled {
			continue
		}
		setAside = setAside || strings.Contains(diagnostic.Message, aside)
		failure = failure || strings.Contains(diagnostic.Message, "stage bundled plugin")
		restored = restored || strings.Contains(diagnostic.Message, "put back")
	}
	if !setAside || !failure || !restored {
		t.Fatalf("Diagnostics = %+v, want the set-aside naming %s, the staging failure, and the restore", res.Diagnostics, aside)
	}
	if content, err := os.ReadFile(filepath.Join(dest, "theirs.md")); err != nil || string(content) != "someone else's data" {
		t.Errorf("destination content = %q (err %v), want what was moved put back", content, err)
	}
	if _, err := os.Stat(aside); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy is still set aside at %s: stat err = %v", aside, err)
	}
}

// Reading a FIFO blocks until somebody writes to it, so a destination holding
// one would hang the launch that classified it — forever, on a pipe nobody
// ever writes. A FIFO is not a regular file, which is decided from the
// directory entry and never by opening it, so the destination is set aside
// without anything in it being read.
func TestBundledStore_SetsAsideADestinationHoldingAFIFO(t *testing.T) {
	m := NewManager(t.TempDir())
	published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(published, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.ValidateSelection(); err != nil {
		t.Fatalf("a destination holding a FIFO left the bundled plugin unselectable: %v", err)
	}
	if len(res.SelectedDirs) != 1 || res.SelectedDirs[0] != published {
		t.Fatalf("SelectedDirs = %v, want a republished copy at %s", res.SelectedDirs, published)
	}
	if _, err := os.Lstat(filepath.Join(published, "pipe")); !os.IsNotExist(err) {
		t.Errorf("the republished copy carries the FIFO (lstat err = %v)", err)
	}
	info, err := os.Lstat(filepath.Join(published+conflictSuffix, "pipe"))
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("set-aside entry mode = %v (err %v), want the FIFO preserved", info, err)
	}
}

// Readying the store waits for the bundled cache's lock, so a caller that has
// given up has to be able to stop that wait. The hub hands its request context
// down for exactly this: a client that disconnected, or a TUI whose own
// deadline passed, must not leave a handler parked on a lock another publisher
// is holding for as long as the lock timeout allows.
func TestResolveForLaunch_StopsWaitingForTheBundledLockWhenCancelled(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(context.Context, *Manager) (LaunchPluginResolution, error)
	}{
		{
			name: "preview",
			resolve: func(ctx context.Context, m *Manager) (LaunchPluginResolution, error) {
				return m.PreviewForLaunch(ctx, nil, &[]string{"coordinator-workflow"})
			},
		},
		{
			name: "launch",
			resolve: func(ctx context.Context, m *Manager) (LaunchPluginResolution, error) {
				return m.ResolveForLaunch(ctx, nil, &[]string{"coordinator-workflow"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			// Another bundled publisher is holding the cache's lock.
			release, err := acquireLock(context.Background(), m.bundledLockPath(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			// Cancelled while it waits, not before: a caller that had already
			// given up never reaches the lock at all.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timer := time.AfterFunc(50*time.Millisecond, cancel)
			defer timer.Stop()
			start := time.Now()
			res, err := test.resolve(ctx, m)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want the cancellation", err)
			}
			if waited := time.Since(start); waited > 5*time.Second {
				t.Errorf("returned after %v, want it to stop waiting when the context did", waited)
			}
			// The caller left; the plugin did nothing wrong. Reported the same
			// way as a cancellation seen before any of this started: the error
			// carries it, and no diagnostic blames the plugin for it.
			if len(res.Diagnostics) != 0 || len(res.SelectionErrors) != 0 {
				t.Fatalf("resolved %+v for a caller that had given up", res)
			}
		})
	}
}

// Staging that looks abandoned may belong to a publisher that is merely slow —
// paused, swapped out, waiting on a filesystem — and the only thing that tells
// the two apart is the bundled cache's lock that publisher holds from its
// first look at the destination until its copy is in place. So the sweep runs
// under that lock or not at all: a launch that cannot take it reclaims
// nothing.
func TestMaterializeBundledPlugin_SweepsOnlyUnderTheBundledLock(t *testing.T) {
	// Both ways a launch meets the store: publishing a copy, and adopting one
	// that is already there. The second reaches the sweep only because it
	// found something to sweep, and it is still not allowed to sweep unlocked.
	tests := map[string]struct {
		publish func(t *testing.T, m *Manager)
		// wantErr is what the launch reports when it cannot have the lock. A
		// publish waits the whole publish budget for it, far longer than this
		// test is willing to wait, so what ends that launch is the caller's
		// own deadline and that is what it hears back. Adopting a copy already
		// published wants the lock only for the sweep, which gives up quietly
		// and leaves the launch to finish.
		wantErr error
	}{
		"publishing": {publish: func(*testing.T, *Manager) {}, wantErr: context.DeadlineExceeded},
		"adopting a published copy": {publish: func(t *testing.T, m *Manager) {
			t.Helper()
			if _, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			test.publish(t, m)
			staging := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-abandoned")
			if err := os.MkdirAll(staging, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(staging, stagingMarker), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }

			// Another publisher holds the lock, so this launch never gets to look.
			release, err := acquireLock(context.Background(), m.bundledLockPath(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			// Long enough to outlast the wait the sweep is allowed, so the
			// launch gives up on the lock rather than on its caller.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			if _, err := m.ResolveForLaunch(ctx, nil, &[]string{"coordinator-workflow"}); !errors.Is(err, test.wantErr) {
				t.Fatalf("ResolveForLaunch error = %v, want %v", err, test.wantErr)
			}
			if _, err := os.Stat(staging); err != nil {
				t.Fatalf("staging was swept without holding the bundled cache lock: %v", err)
			}
		})
	}
}

// A launch that only has to adopt a copy already published must not queue
// behind whatever else is touching the bundled cache. A publisher of a
// different plugin holds that cache's lock for its whole copy; a launch that
// took the lock for housekeeping it has no housekeeping for would wait that
// out, or fail, for nothing. With no abandoned staging to reclaim there is
// nothing to take the lock for, and the launch reads its copy and goes.
func TestMaterializeBundledPlugin_AdoptsAPublishedCopyWithoutTheBundledLock(t *testing.T) {
	m := NewManager(t.TempDir())
	published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	// Another bundled publisher is holding the cache's lock.
	release, err := acquireLock(context.Background(), m.bundledLockPath(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	res, err := m.ResolveForLaunch(ctx, nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.ValidateSelection(); err != nil {
		t.Fatalf("a launch could not adopt a published copy while the bundled cache lock was held: %v", err)
	}
	if len(res.SelectedDirs) != 1 || res.SelectedDirs[0] != published {
		t.Fatalf("SelectedDirs = %v, want the published copy at %s", res.SelectedDirs, published)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("adopting took %v, want it not to wait on the lock at all", waited)
	}
}

// The sweep is housekeeping a launch does on its way past, so it waits for the
// bundled cache's lock the way housekeeping should: briefly. One stale orphan
// must not put every adopting launch behind a publisher that is still copying,
// waiting out the budget a publish is entitled to and then leaving the orphan
// for the next launch to wait out again.
func TestMaterializeBundledPlugin_GivesUpQuicklyOnTheSweepLock(t *testing.T) {
	m := NewManager(t.TempDir())
	published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-abandoned")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, stagingMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }

	// Another bundled publisher is holding the cache's lock.
	release, err := acquireLock(context.Background(), m.bundledLockPath(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// No deadline of its own: the bound has to come from the code.
	start := time.Now()
	res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("adopting waited %v on the sweep lock, want it to give up quickly", waited)
	}
	if err := res.ValidateSelection(); err != nil {
		t.Fatalf("a launch that could not sweep failed to select the published copy: %v", err)
	}
	if len(res.SelectedDirs) != 1 || res.SelectedDirs[0] != published {
		t.Fatalf("SelectedDirs = %v, want the published copy at %s", res.SelectedDirs, published)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("the orphan it could not take the lock for is gone: %v", err)
	}
}

// The marker is the whole proof that a staging directory is this code's to
// delete, so it has to be the file this code writes. A symlink wearing the
// marker's name is somebody else's arrangement, and what it points at is not
// even in the store; a directory with that name is not a marker either. The
// sweep leaves both alone, however old they get.
func TestMaterializeBundledPlugin_LeavesStagingWhoseMarkerIsNotAFile(t *testing.T) {
	plant := map[string]func(t *testing.T, marker string){
		"a symlink": func(t *testing.T, marker string) {
			target := filepath.Join(t.TempDir(), "not-the-marker")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, marker); err != nil {
				t.Fatal(err)
			}
		},
		"a directory": func(t *testing.T, marker string) {
			if err := os.Mkdir(marker, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plantMarker := range plant {
		t.Run(name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			staging := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-foreign")
			if err := os.MkdirAll(staging, 0o755); err != nil {
				t.Fatal(err)
			}
			plantMarker(t, filepath.Join(staging, stagingMarker))
			keep := filepath.Join(staging, "someone-elses-data")
			if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }

			if _, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(keep); err != nil {
				t.Fatalf("the sweep took a directory whose marker it never wrote: %v", err)
			}
		})
	}
}

// A launch can be given up on while its sweep waits for the bundled cache's
// lock. The sweep swallows its own lock failure by design — housekeeping
// nobody is waiting on — and the caller's cancellation arrives as exactly that
// failure, so the context has to be read again before the copy is handed back.
// What the launch has by then is a published copy it is no longer entitled to
// return.
func TestMaterializeBundledPlugin_StopsForACallerThatLeftDuringTheSweep(t *testing.T) {
	m := NewManager(t.TempDir())
	published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-abandoned")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, stagingMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }

	// Another publisher holds the lock, so the sweep has to wait for it.
	release, err := acquireLock(context.Background(), m.bundledLockPath(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// Cancelled while it waits, not before: a caller that had already given up
	// never reaches the sweep.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()

	res, err := m.ResolveForLaunch(ctx, nil, &[]string{"coordinator-workflow"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation the sweep waited into", err)
	}
	if len(res.SelectedDirs) != 0 || len(res.Diagnostics) != 0 {
		t.Errorf("resolved %+v for a caller that had given up", res)
	}
	// Publication is immutable: the copy stays for the launches that follow.
	if _, err := os.Stat(published); err != nil {
		t.Errorf("the published copy did not survive the cancelled launch: %v", err)
	}
}

// A destination this code cannot read is not a copy it published: an
// unreadable tree hashes to nothing, and there is no version of it that is
// this build's own content. Holding the store hostage to it — never
// launching, never moving it aside — serves nobody, so it is preserved the way
// every other conflict is (the rename needs only the parent's write bit) and
// the launch publishes into the name that frees.
func TestBundledStore_AnUnreadableDestinationIsSetAsideLikeAnyOtherConflict(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0o000 directory, so there is no unreadable tree to plant")
	}
	m := NewManager(t.TempDir())
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	dest := m.bundledPluginPath("coordinator-workflow", digest)
	theirs := filepath.Join(dest, "theirs")
	if err := os.MkdirAll(theirs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(theirs, "data"), []byte("someone else's data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(theirs, 0o000); err != nil {
		t.Fatal(err)
	}
	// Whatever the launch does with it, the tree has to be readable again
	// before the temp directory can be removed.
	t.Cleanup(func() {
		_ = os.Chmod(theirs, 0o700)
		_ = os.Chmod(filepath.Join(dest+conflictSuffix, "theirs"), 0o700)
	})

	res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatalf("ResolveForLaunch: %v", err)
	}
	if err := res.ValidateSelection(); err != nil {
		t.Fatalf("the store stayed unusable for the plugin: %v", err)
	}
	if len(res.SelectedDirs) != 1 || res.SelectedDirs[0] != dest {
		t.Fatalf("SelectedDirs = %v, want the published copy at %s", res.SelectedDirs, dest)
	}
	aside := filepath.Join(dest+conflictSuffix, "theirs")
	if err := os.Chmod(aside, 0o700); err != nil {
		t.Fatalf("the unreadable directory was not set aside: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(aside, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "someone else's data" {
		t.Errorf("preserved content = %q, want it untouched", content)
	}
}

// Publishing a bundled copy needs exclusion against other bundled publishers
// and nothing else: it classifies a destination, sets a conflict aside, stages
// and renames, all of it under <Root>/bundled. So it must not queue behind the
// plugin store lock, which install, upgrade, gc, catalog and marketplace
// refresh hold across git fetches for up to 30 seconds. Hub start runs the
// auto-upgrade sweep, so a first launch after a version bump — the one launch
// that has a copy to publish — is exactly the launch that meets it.
func TestMaterializeBundledPlugin_PublishesWhileTheStoreLockIsHeld(t *testing.T) {
	m := NewManager(t.TempDir())
	// Somebody else holds the store lock, the way an auto-upgrade does across
	// its git fetches, and goes on holding it past everything below: the
	// publish has to finish under that lock rather than after it, so the
	// holder lets go in cleanup and nowhere else.
	release, err := acquireLock(context.Background(), m.lockPath(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)

	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	// A hang tripwire, nothing more. A publish that queued behind the store
	// lock gives up on its own after the 30s budget and says which lock it was
	// waiting for, which is the diagnosis worth having, so this sits well past
	// that and decides nothing about whether the separation holds.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	published, _, err := m.materializeBundledPlugin(ctx, "coordinator-workflow")
	if err != nil {
		t.Fatalf("publishing while the store lock was held: %v", err)
	}
	// The premise, checked rather than assumed: a publish that succeeded is
	// only evidence if the store lock was still taken when it did. Nothing
	// here turns on how long anything took — the lock is contended or the
	// success above proves nothing.
	if free, freeErr := acquireLock(context.Background(), m.lockPath(), 0); freeErr == nil {
		free()
		t.Error("the store lock was not held when the publish finished, so this proves nothing about the two locks")
	}
	if want := m.bundledPluginPath("coordinator-workflow", digest); published != want {
		t.Fatalf("published %s, want %s", published, want)
	}
	if _, err := os.Stat(filepath.Join(published, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("published copy incomplete: %v", err)
	}
}

// The other direction of the same separation: a publish in flight holds the
// bundled cache's lock for the whole classify-stage-rename sequence, and an
// install, an upgrade or an auto-upgrade sweep that wants the store lock must
// not wait out the copy it has no stake in.
func TestMaterializeBundledPlugin_LeavesTheStoreLockFreeWhilePublishing(t *testing.T) {
	m := NewManager(t.TempDir())
	publishing := make(chan struct{})
	finish := make(chan struct{})
	original := copyBundledPayload
	t.Cleanup(func() { copyBundledPayload = original })
	copyBundledPayload = func(dir string, fsys fs.FS) error {
		close(publishing)
		<-finish
		return original(dir, fsys)
	}

	var published string
	var publishErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		published, _, publishErr = m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	}()
	unblock := sync.OnceFunc(func() {
		close(finish)
		<-done
	})
	t.Cleanup(unblock)
	<-publishing

	// Mid-copy: whatever the publish is holding, the store lock is not it. No
	// wait budget at all, so nothing here turns on how loaded the machine is —
	// the lock is free on the first try or the publish is holding it.
	storeLock, err := acquireLock(context.Background(), m.lockPath(), 0)
	if err != nil {
		t.Fatalf("a bundled publish in flight blocked the store lock: %v", err)
	}
	storeLock()

	unblock()
	if publishErr != nil {
		t.Fatalf("the publish held mid-copy failed: %v", publishErr)
	}
	if _, err := os.Stat(filepath.Join(published, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("published copy incomplete: %v", err)
	}
}
