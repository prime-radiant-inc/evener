package plugins

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// UpgradedPlugin is one installed plugin whose sha actually changed during an
// UpdateAutoUpgrade pass. No-ops (sha unchanged, or a directory/relative
// source that can never change) are omitted.
type UpgradedPlugin struct {
	Plugin      string
	Marketplace string
	Entry       InstallEntry
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
		entries := reg.Plugins[key]
		if len(entries) == 0 {
			continue
		}
		before := entries[0]
		if !before.AutoUpgrade || before.Source.Rel || before.Source.Kind == SourceDirectory {
			continue
		}
		plugin, marketplace := splitKey(key)
		after, err := m.Upgrade(ctx, plugin, marketplace)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		if after.GitCommitSha != before.GitCommitSha {
			updated = append(updated, UpgradedPlugin{Plugin: plugin, Marketplace: marketplace, Entry: after})
		}
	}
	if len(errs) > 0 {
		return updated, fmt.Errorf("some auto-upgrades failed:\n%s", strings.Join(errs, "\n"))
	}
	return updated, nil
}
