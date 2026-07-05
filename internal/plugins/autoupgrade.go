package plugins

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// UpgradedPlugin is one installed plugin whose sha actually changed during an
// UpdateAutoUpgrade pass. No-ops (sha unchanged, or a directory/relative
// source that can never change) are omitted.
type UpgradedPlugin struct {
	Plugin      string
	Marketplace string
	Entry       InstallEntry
}

// upgradeAuto acquires the manager lock and, while holding it, upgrades
// plugin only if it is (still) eligible for unattended upgrade: AutoUpgrade
// enabled, git-backed. See upgradeLocked for why the eligibility check and
// the change-detection both happen fresh, under this same lock acquisition,
// rather than against a snapshot taken before the lock was acquired.
func (m *Manager) upgradeAuto(ctx context.Context, plugin, marketplace string) (entry InstallEntry, changed, skipped bool, err error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return InstallEntry{}, false, false, err
	}
	defer release()
	return m.upgradeLocked(ctx, plugin, marketplace, true)
}

// UpdateAutoUpgrade upgrades every installed, git-backed plugin that has
// autoUpgrade enabled (SetAutoUpgrade). Directory and relative sources are
// inherently current and are always skipped, exactly like UpdateAll.
//
// This is the filtered sibling of UpdateAll: UpdateAll powers the explicit
// `serf plugin upgrade --all`, which upgrades every installed plugin
// regardless of the opt-in flag — an explicit user request overrides the
// gate. UpdateAutoUpgrade powers the background auto-upgrade daemon (design
// doc §9.1), which must only touch plugins a user has opted into: enabling
// autoUpgrade on an already-installed, git-backed plugin is the standing
// consent for the daemon to act on it unattended.
//
// The initial registry read below only enumerates WHICH plugins exist; it is
// deliberately not consulted for eligibility or change-detection. Each
// plugin's AutoUpgrade flag is re-checked, and its sha comparison performed,
// inside upgradeAuto's locked section at the moment that plugin is actually
// processed. This matters because two overlapping calls to
// UpdateAutoUpgrade (the periodic tick racing a manual
// serf/plugin/checkNow, or checkNow from two clients) must not both observe
// the same stale pre-sweep sha and both report the plugin as updated — and a
// SetAutoUpgrade(false) landing mid-sweep must be honored for any plugin the
// sweep hasn't reached yet.
//
// Failures are collected but do not stop the others (failure-isolated),
// matching UpdateAll.
func (m *Manager) UpdateAutoUpgrade(ctx context.Context) ([]UpgradedPlugin, error) {
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(reg.Plugins))
	for key := range reg.Plugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var updated []UpgradedPlugin
	var errs []string
	for _, key := range keys {
		plugin, marketplace := splitKey(key)
		entry, changed, skipped, err := m.upgradeAuto(ctx, plugin, marketplace)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		if skipped || !changed {
			continue
		}
		updated = append(updated, UpgradedPlugin{Plugin: plugin, Marketplace: marketplace, Entry: entry})
	}
	if len(errs) > 0 {
		return updated, fmt.Errorf("some auto-upgrades failed:\n%s", strings.Join(errs, "\n"))
	}
	return updated, nil
}
