package plugins

import (
	"context"
	"errors"
	"io/fs"
	"time"
)

// DefaultMarketplaceSeeds is the set of marketplaces seeded on first run
// (pointers only — cloned lazily on first use).
func DefaultMarketplaceSeeds() map[string]Source {
	return map[string]Source{
		"claude-plugins-official": {Kind: SourceGitHub, Repo: "anthropics/claude-plugins-official"},
		"superpowers-marketplace": {Kind: SourceGitHub, Repo: "obra/superpowers-marketplace"},
	}
}

// SeedDefaultMarketplaces writes the default marketplaces IFF known_marketplaces.json
// does not yet exist. It is a no-op once the file exists, so a user who removes a
// seeded marketplace never gets it back. Seeded entries are unfetched pointers
// (empty InstallLocation), cloned lazily on first Browse/Install.
func (m *Manager) SeedDefaultMarketplaces(ctx context.Context) (bool, error) {
	// Every path under an unresolved root is relative, so seeding would write
	// the marketplaces file and take its lock in whatever directory the
	// process happens to be in. Launches seed on the way past and carry a
	// seeding failure as a warning, so refusing here is what keeps a store
	// out of somebody's project. acquireStoreLock refuses the same roots, but
	// only once this gets that far: the stat below is a read of an ambient
	// known_marketplaces.json that would answer "already seeded" and skip the
	// lock entirely.
	if err := m.storeRootError(); err != nil {
		return false, err
	}
	if _, err := marketplaceStat(m.marketplacesFile()); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	release, err := m.acquireStoreLock(ctx, marketplaceAcquireLock, m.lockPath(), 30*time.Second)
	if err != nil {
		return false, err
	}
	defer release()
	// re-check under lock
	if _, err := marketplaceStat(m.marketplacesFile()); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	mk := Marketplaces{}
	for name, src := range DefaultMarketplaceSeeds() {
		mk[name] = MarketplaceRef{Source: src, LastUpdated: m.now().UTC()}
	}
	if err := m.saveMarketplaces(mk); err != nil {
		return false, err
	}
	return true, nil
}
