//go:build unix

package plugins

import (
	"context"
	"errors"
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
			dest, staging, _, err := m.prepareBundledStore(context.Background(), "coordinator-workflow", true)
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
	entries, readErr := os.ReadDir(store)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), stagingPrefix) {
		t.Fatalf("bundled store holds %v, want the staging the preview could not remove", entries)
	}
	if _, err := os.Lstat(filepath.Join(store, entries[0].Name(), stagingMarker)); err != nil {
		t.Errorf("leftover staging is unmarked, so no sweep will reclaim it: %v", err)
	}
}

// Two launches meeting the same mismatched destination must not undo each
// other's work. Classifying, setting aside and publishing run as one sequence
// under the store lock, so a classification made before the lock is never
// acted on after it: the slot beside the destination keeps one copy of the
// foreign content, the destination holds the copy this build publishes, and
// neither is left vacant for a session that is already pointing at it.
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
		entries, err := os.ReadDir(filepath.Dir(dest))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("attempt %d: bundled store holds %v, want the published copy and one set-aside slot", attempt, entries)
		}
	}
}

// Readying the store can move somebody's directory aside and then fail at the
// next step. What was moved is theirs and they have to be told where it went,
// so the warning survives the failure that follows it rather than being
// dropped with the candidate that never resolved.
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
	var setAside, failure bool
	for _, diagnostic := range res.Diagnostics {
		if diagnostic.Source != LaunchPluginSourceBundled {
			continue
		}
		setAside = setAside || strings.Contains(diagnostic.Message, aside)
		failure = failure || strings.Contains(diagnostic.Message, "stage bundled plugin")
	}
	if !setAside || !failure {
		t.Fatalf("Diagnostics = %+v, want both the set-aside naming %s and the staging failure", res.Diagnostics, aside)
	}
	if content, err := os.ReadFile(filepath.Join(aside, "theirs.md")); err != nil || string(content) != "someone else's data" {
		t.Errorf("set-aside content = %q (err %v), want what was moved preserved", content, err)
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

// Readying the store waits for the store lock, so a caller that has given up
// has to be able to stop that wait. The hub hands its request context down for
// exactly this: a client that disconnected, or a TUI whose own deadline
// passed, must not leave a handler parked on a lock some install is holding
// for as long as the lock timeout allows.
func TestResolveForLaunch_StopsWaitingForTheStoreLockWhenCancelled(t *testing.T) {
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
			// Somebody else is holding the store lock, the way an install does.
			release, err := acquireLock(context.Background(), m.lockPath(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			start := time.Now()
			res, err := test.resolve(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			if waited := time.Since(start); waited > 5*time.Second {
				t.Errorf("returned after %v, want it to stop waiting when the context did", waited)
			}
			if len(res.Diagnostics) != 1 || res.Diagnostics[0].Source != LaunchPluginSourceBundled ||
				!strings.Contains(res.Diagnostics[0].Message, context.Canceled.Error()) {
				t.Fatalf("Diagnostics = %+v, want one bundled diagnostic reporting the cancellation", res.Diagnostics)
			}
			if err := res.ValidateSelection(); err == nil {
				t.Error("selected a bundled plugin that was never staged")
			}
		})
	}
}

// Staging that looks abandoned may belong to a publisher that is merely slow —
// paused, swapped out, waiting on a filesystem — and the only thing that tells
// the two apart is the store lock that publisher holds from its first look at
// the destination until its copy is in place. So the sweep runs under that
// lock or not at all: a launch that cannot take it reclaims nothing.
func TestMaterializeBundledPlugin_SweepsOnlyUnderTheStoreLock(t *testing.T) {
	// Both ways a launch meets the store: publishing a copy, and adopting one
	// that is already there. The second reaches the sweep only because it
	// found something to sweep, and it is still not allowed to sweep unlocked.
	tests := map[string]func(t *testing.T, m *Manager){
		"publishing": func(*testing.T, *Manager) {},
		"adopting a published copy": func(t *testing.T, m *Manager) {
			t.Helper()
			if _, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow"); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, publish := range tests {
		t.Run(name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			publish(t, m)
			staging := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-abandoned")
			if err := os.MkdirAll(staging, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(staging, stagingMarker), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }

			// Somebody else holds the lock, so this launch never gets to look.
			release, err := acquireLock(context.Background(), m.lockPath(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if _, err := m.ResolveForLaunch(ctx, nil, &[]string{"coordinator-workflow"}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(staging); err != nil {
				t.Fatalf("staging was swept without holding the store lock: %v", err)
			}
		})
	}
}

// A launch that only has to adopt a copy already published must not queue
// behind whatever else is touching the store. Hub start runs the auto-upgrade
// pass, which holds the store lock across git fetches; a launch that took the
// lock for housekeeping it has no housekeeping for would wait that out, or
// fail, for nothing. With no abandoned staging to reclaim there is nothing to
// take the lock for, and the launch reads its copy and goes.
func TestMaterializeBundledPlugin_AdoptsAPublishedCopyWithoutTheStoreLock(t *testing.T) {
	m := NewManager(t.TempDir())
	published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	// Somebody else is holding the store lock, the way an auto-upgrade does.
	release, err := acquireLock(context.Background(), m.lockPath(), time.Second)
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
		t.Fatalf("a launch could not adopt a published copy while the store lock was held: %v", err)
	}
	if len(res.SelectedDirs) != 1 || res.SelectedDirs[0] != published {
		t.Fatalf("SelectedDirs = %v, want the published copy at %s", res.SelectedDirs, published)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("adopting took %v, want it not to wait on the lock at all", waited)
	}
}
