package hub

// issue #1780: the hub's background plugin maintenance paths must operate on
// the store the server serves — hubcore.WebConfig.PluginRoot, reached through
// the one wired cfg.PluginManager newWebServer installs — rather than minting
// their own Manager over plugins.DefaultRoot(). The two coincide in production
// (main.go derives PluginRoot from the same DefaultRoot), so the mismatch is
// invisible there; it bites a sandbox or test that points PluginRoot inside its
// own temp root, where the background paths would write the real
// ~/.config/evener/plugins the caller deliberately contained.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/plugins"
)

func TestHubBackgroundMaintenanceUsesTheServersPluginManager(t *testing.T) {
	// The stand-in "real" root: a still-broken background path can only escape
	// into this throwaway dir, never the developer's actual plugin store.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	serverRoot := t.TempDir()

	web := NewWebServer(hubcore.WebConfig{PluginRoot: serverRoot})
	mgr := web.cfg.PluginManager
	if mgr == nil || mgr.Root != serverRoot {
		t.Fatalf("web.cfg.PluginManager = %+v, want a Manager rooted at %q", mgr, serverRoot)
	}

	// Capture the Manager each background path hands its work to, while still
	// running the real seed/GC so the store write is observable below.
	oldSeed, oldGC, oldDaemon := hubSeedDefaults, hubPluginGC, hubStartUpgradeDaemon
	t.Cleanup(func() { hubSeedDefaults, hubPluginGC, hubStartUpgradeDaemon = oldSeed, oldGC, oldDaemon })

	var seeded, collected, upgraded *plugins.Manager
	hubSeedDefaults = func(ctx context.Context, m *plugins.Manager) error {
		seeded = m
		return oldSeed(ctx, m)
	}
	hubPluginGC = func(ctx context.Context, m *plugins.Manager) ([]string, error) {
		collected = m
		return oldGC(ctx, m)
	}
	hubStartUpgradeDaemon = func(_ context.Context, m *plugins.Manager, _ time.Duration) {
		upgraded = m
	}

	seedHubMarketplaces(context.Background(), web)
	startHubPluginMaintenance(context.Background(), Config{PluginAutoUpgrade: true}, web, func(fn func()) { fn() })

	for name, got := range map[string]*plugins.Manager{
		"seedHubMarketplaces":            seeded,
		"startHubPluginMaintenance's GC": collected,
		"hubStartUpgrade's daemon":       upgraded,
	} {
		if got != mgr {
			t.Errorf("%s ran on manager %p (root %q), want the server's %p (root %q)",
				name, got, rootOf(got), mgr, serverRoot)
		}
	}

	// The seed's write must land in the store the server serves, and no
	// maintenance path may touch the default root.
	seededFile := filepath.Join(serverRoot, "known_marketplaces.json")
	if _, err := os.Stat(seededFile); err != nil {
		t.Fatalf("background seed did not write the server's store: %s missing (%v)", seededFile, err)
	}
	escapedFile := filepath.Join(plugins.DefaultRoot(), "known_marketplaces.json")
	if _, err := os.Stat(escapedFile); err == nil {
		t.Fatalf("background maintenance wrote the default plugin root %s instead of the server's store", escapedFile)
	}
}

func rootOf(m *plugins.Manager) string {
	if m == nil {
		return ""
	}
	return m.Root
}
