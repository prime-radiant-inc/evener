package plugins

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
