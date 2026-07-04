package plugins

import (
	"errors"
	"io/fs"
	"os"
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
func (m *Manager) SeedDefaultMarketplaces() (bool, error) {
	if _, err := os.Stat(m.marketplacesFile()); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return false, err
	}
	defer release()
	// re-check under lock
	if _, err := os.Stat(m.marketplacesFile()); err == nil {
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
