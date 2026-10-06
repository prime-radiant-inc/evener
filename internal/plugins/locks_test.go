package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The invariant lockStore establishes needs the marketplaces file, so every
// acquisition reads it — the mutations that go on to touch only the registry
// included. One that cannot be parsed fails them, and the error names the
// file, so the user is told which one to fix.
func TestLockStore_ACorruptMarketplacesFileFailsARegistryOnlyMutation(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "acme", "widget")
	if err := os.WriteFile(m.marketplacesFile(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := m.SetEnabled(context.Background(), "widget", "acme", false)
	if err == nil {
		t.Fatal("SetEnabled succeeded on a store whose marketplaces file cannot be parsed")
	}
	if !strings.Contains(err.Error(), marketplacesFileName) {
		t.Fatalf("error = %v, want it to name %s", err, marketplacesFileName)
	}
}

// Every marketplace verb takes the store lock first thing, and acquireLock's
// own error text names the absolute lock file. A lock file that cannot be
// opened (a permission problem, or a directory in its place) must not leak that
// path to the RPC caller: lockStore scrubs it and logs the raw error
// server-side, the way saveFailed does for a failed write (#1883).
func TestRemoveMarketplace_LockFailureCarriesNoPath(t *testing.T) {
	m := NewManager(t.TempDir())
	var logged strings.Builder
	m.Stderr = &logged
	// A directory where the lock file would be makes the open fail, naming the
	// path the same way a permission problem would.
	if err := os.Mkdir(m.lockPath(), 0o755); err != nil {
		t.Fatal(err)
	}

	err := m.RemoveMarketplace(context.Background(), "acme")
	if err == nil {
		t.Fatal("expected the unopenable lock file to fail the removal")
	}
	if strings.Contains(err.Error(), m.lockPath()) || strings.Contains(err.Error(), m.Root) {
		t.Fatalf("err = %v, want no absolute store or lock path in the client-facing error", err)
	}
	if !strings.Contains(logged.String(), m.lockPath()) {
		t.Fatalf("stderr = %q, want the raw lock error with its path logged server-side", logged.String())
	}
}

// EditMarketplace wraps every failure it returns with the marketplace name, so
// a caller can correlate it. Scrubbing the lock path in lockStore must not
// short-circuit that wrap and hand back a message with no name (#1883 review).
func TestEditMarketplace_LockFailureNamesTheMarketplaceWithoutThePath(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	if err := os.Mkdir(m.lockPath(), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.EditMarketplace(context.Background(), "acme", "", &Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"})
	if err == nil {
		t.Fatal("expected the unopenable lock file to fail the edit")
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("err = %v, want it to name the marketplace", err)
	}
	if strings.Contains(err.Error(), m.lockPath()) {
		t.Fatalf("err = %v, want no lock path", err)
	}
}

// The bundled-cache lock is reached from plugin/preview (PreviewForLaunch ->
// prepareBundledStore), whose failure message is copied onto the wire
// diagnostic, so its own unopenable lock file must not leak its path either
// (#1883 review).
func TestAcquireBundledLock_FailureCarriesNoPath(t *testing.T) {
	m := NewManager(t.TempDir())
	var logged strings.Builder
	m.Stderr = &logged
	if err := os.MkdirAll(m.bundledLockPath(), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.acquireBundledLock(context.Background(), time.Second)
	if err == nil {
		t.Fatal("expected the unopenable bundled lock file to fail the acquisition")
	}
	if strings.Contains(err.Error(), m.bundledLockPath()) || strings.Contains(err.Error(), m.Root) {
		t.Fatalf("err = %v, want no absolute store or lock path in the client-facing error", err)
	}
	if !strings.Contains(logged.String(), m.bundledLockPath()) {
		t.Fatalf("stderr = %q, want the raw lock error with its path logged server-side", logged.String())
	}
}

// Scrubbing a lock failure must not cost the caller the reason it acts on: a
// cancellation, a deadline, a store root that cannot be used, and lock
// contention all have to survive path-free through lockStore, which every
// writer reaches before it does anything else (#1883).
func TestLockStore_LockFailureKeepsItsReasonWithoutThePath(t *testing.T) {
	injected := errors.New("injected lock failure")
	cases := []struct {
		name  string
		build func(m *Manager) error
		want  error
	}{
		{"an unrecognized error", func(m *Manager) error {
			return injected // the acquirer's own error, carrying no known sentinel
		}, injected},
		{"lock contention", func(m *Manager) error {
			return fmt.Errorf("%w (locked: %s)", errLockContention, m.lockPath())
		}, errLockContention},
		{"a canceled request", func(m *Manager) error {
			return fmt.Errorf("waiting for plugin lock %s: %w", m.lockPath(), context.Canceled)
		}, context.Canceled},
		{"a deadline", func(m *Manager) error {
			return fmt.Errorf("waiting for plugin lock %s: %w", m.lockPath(), context.DeadlineExceeded)
		}, context.DeadlineExceeded},
		{"an unusable store root", func(m *Manager) error {
			return m.storeRootError()
		}, errStoreRootNotAbsolute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{Root: t.TempDir(), Stderr: io.Discard}
			if errors.Is(tc.want, errStoreRootNotAbsolute) {
				m.Root = "relative/store" // storeRootError refuses the root and names it
			}
			acquireErr := tc.build(m)
			acquire := func(context.Context, string, time.Duration) (func(), error) {
				return nil, acquireErr
			}

			_, err := m.lockStore(context.Background(), acquire, time.Second)
			if err == nil {
				t.Fatal("expected the acquisition to fail")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, tc.want)
			}
			if strings.Contains(err.Error(), m.lockPath()) {
				t.Fatalf("err = %v, want no lock path", err)
			}
		})
	}
}

func TestAcquireLock_ExclusiveWithTimeout(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "l.lock")

	release, err := acquireLock(context.Background(), lp, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire must fail within the timeout while the first is held.
	_, err = acquireLock(context.Background(), lp, 100*time.Millisecond)
	if err == nil {
		t.Fatal("second acquire succeeded while lock held; want timeout error")
	}

	release()

	// After release, acquire must succeed again.
	release2, err := acquireLock(context.Background(), lp, time.Second)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

// TestAcquireLock_ObservesContextCancellation pins that a canceled request
// stops waiting for a contended lock promptly instead of spinning out the
// full timeout (a disconnected client's handler used to park here up to 30s).
func TestAcquireLock_ObservesContextCancellation(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "l.lock")

	release, err := acquireLock(context.Background(), lp, time.Second)
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = acquireLock(ctx, lp, 30*time.Second)
	if err == nil {
		t.Fatal("acquire succeeded while lock held; want cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// Prompt means bounded by the backoff cap (200ms), not the 30s timeout.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("acquire took %v after cancellation; want prompt return", elapsed)
	}
}

// Every mutation of the store takes the store lock, and creating that lock
// file is the mutation's first write. A root that is empty (none could be
// resolved) or relative resolves against whatever directory the process
// happens to be in, so a writer that reached the lock without checking planted
// a store in somebody's project. The check belongs at the acquisition rather
// than in each writer, where it was forgotten. Every caller that takes the
// store lock is here, so dropping the check from any one acquisition turns
// this red rather than leaving a single untested site behind.
func TestStoreWriters_RefuseARootThatIsNotResolved(t *testing.T) {
	writers := []struct {
		name  string
		write func(context.Context, *Manager) error
	}{
		{"Install", func(ctx context.Context, m *Manager) error {
			_, err := m.Install(ctx, "plugin", "marketplace")
			return err
		}},
		{"Upgrade", func(ctx context.Context, m *Manager) error {
			_, err := m.Upgrade(ctx, "plugin", "marketplace")
			return err
		}},
		{"Remove", func(ctx context.Context, m *Manager) error {
			return m.Remove(ctx, "plugin", "marketplace")
		}},
		{"SetEnabled", func(ctx context.Context, m *Manager) error {
			return m.SetEnabled(ctx, "plugin", "marketplace", false)
		}},
		{"SetAutoUpgrade", func(ctx context.Context, m *Manager) error {
			return m.SetAutoUpgrade(ctx, "plugin", "marketplace", true)
		}},
		{"Gc", func(ctx context.Context, m *Manager) error {
			_, err := m.Gc(ctx)
			return err
		}},
		{"AddMarketplace", func(ctx context.Context, m *Manager) error {
			_, err := m.AddMarketplace(ctx, "marketplace", Source{Kind: SourceGitHub, Repo: "acme/plugins"})
			return err
		}},
		{"RemoveMarketplace", func(ctx context.Context, m *Manager) error {
			return m.RemoveMarketplace(ctx, "marketplace")
		}},
		{"RefreshMarketplace", func(ctx context.Context, m *Manager) error {
			return m.RefreshMarketplace(ctx, "marketplace")
		}},
		// Browse mutates too: it clones a marketplace that was only seeded as
		// a pointer, so on a broken root it cloned into the working directory.
		{"Browse", func(ctx context.Context, m *Manager) error {
			_, err := m.Browse(ctx, "marketplace")
			return err
		}},
		// The two sweeps take the store lock before they enumerate the
		// registry, so the root check lands on them the way it lands on every
		// other writer here.
		{"UpdateAll", func(ctx context.Context, m *Manager) error {
			_, err := m.UpdateAll(ctx)
			return err
		}},
		{"UpdateAutoUpgrade", func(ctx context.Context, m *Manager) error {
			_, err := m.UpdateAutoUpgrade(ctx)
			return err
		}},
	}
	roots := []struct {
		name    string
		root    string
		wantErr string
	}{
		{"no root could be resolved", "", "no plugin store root is configured"},
		{"the root names a relative directory", "store", "not an absolute path"},
	}
	for _, writer := range writers {
		for _, root := range roots {
			t.Run(writer.name+"/"+root.name, func(t *testing.T) {
				cwd := t.TempDir()
				t.Chdir(cwd)
				m := &Manager{Root: root.root, Stderr: io.Discard}

				err := writer.write(context.Background(), m)
				if err == nil || !strings.Contains(err.Error(), root.wantErr) {
					t.Fatalf("%s error = %v, want %q", writer.name, err, root.wantErr)
				}
				entries, readErr := os.ReadDir(cwd)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(entries) != 0 {
					t.Errorf("%s wrote %v into the working directory", writer.name, entries)
				}
			})
		}
	}
}

// The launch read path reads the registry and the bundled store before
// anything under the root would be created, so it never reaches the lock: the
// guard at the acquisition cannot be what protects it, and its own entry check
// has to stay. A refused launch leaves no lock file — nothing at all — behind.
func TestResolveForLaunch_RefusesAnUnresolvedRootWithoutTakingTheLock(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	m := &Manager{Root: "", Stderr: io.Discard}

	res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("Candidates = %+v, want nothing resolved from the working directory", res.Candidates)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Source != LaunchPluginSourceBundled {
		t.Fatalf("Diagnostics = %+v, want one bundled diagnostic", res.Diagnostics)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".lock")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat .lock = %v, want the launch to have taken no lock", err)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the refused launch wrote %v into the working directory", entries)
	}
}

// A clone lock taken while its holder removes the lock file (as removing a
// marketplace does) is taken on the file at the path, not on the removed one,
// so a later taker still waits for it.
func TestLockClone_AWaiterOutlivesTheLockFilesRemoval(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := m.marketplaceDir("acme")
	release, err := m.lockClone(context.Background(), dir)
	if err != nil {
		t.Fatalf("lockClone: %v", err)
	}
	// The file is removed only once the waiter is contending on it, so the
	// waiter is granted the lock on the removed file, not on a new one.
	contending := make(chan struct{})
	var once sync.Once
	realFlock := lockFlock
	t.Cleanup(func() { lockFlock = realFlock })
	lockFlock = func(fd int, how int) error {
		err := realFlock(fd, how)
		if isLockContended(err) {
			once.Do(func() { close(contending) })
		}
		return err
	}
	acquired := make(chan func())
	go func() {
		next, err := NewManager(m.Root).lockClone(context.Background(), dir)
		if err != nil {
			t.Errorf("waiting lockClone: %v", err)
			close(acquired)
			return
		}
		acquired <- next
	}()
	<-contending
	m.removeCloneLock(dir)
	release()
	next := <-acquired
	if next == nil {
		return
	}
	defer next()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	third := NewManager(m.Root)
	stderr := &bytes.Buffer{}
	third.Stderr = stderr
	if release, err := third.lockClone(ctx, dir); err == nil {
		release()
		t.Fatal("a third taker got the clone lock while the waiter held it on the removed file")
	}
	if !strings.Contains(stderr.String(), "taking the lock on marketplace clone") {
		t.Fatalf("the third taker's wait was not logged: %q", stderr.String())
	}
}

// A clone lock taken for the first time is taken once: the lock file is made
// before the wait, so the first grant is on the file at the path.
func TestLockClone_AFirstLockIsTakenOnce(t *testing.T) {
	m := NewManager(t.TempDir())
	realFlock := lockFlock
	t.Cleanup(func() { lockFlock = realFlock })
	grants := 0
	lockFlock = func(fd int, how int) error {
		err := realFlock(fd, how)
		if err == nil && how == lockOpExclusiveNB {
			grants++
		}
		return err
	}
	release, err := m.lockClone(context.Background(), m.marketplaceDir("acme"))
	if err != nil {
		t.Fatalf("lockClone: %v", err)
	}
	release()
	if grants != 1 {
		t.Fatalf("a first clone lock was granted %d times, want once", grants)
	}
}
