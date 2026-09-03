package plugins

import (
	"context"
	"fmt"

	agentplugin "primeradiant.com/evener/agent/plugin"
)

var enabledLoad = agentplugin.Load

// EnabledPluginDirs is the compatibility wrapper for callers that only need
// the effective directory set. The resolver owns loading, ordering, and
// deduplication; this wrapper preserves the existing fail-soft warnings.
func (m *Manager) EnabledPluginDirs(ctx context.Context, explicit []string) []string {
	resolution, err := m.ResolveForLaunch(ctx, explicit, nil)
	for _, diagnostic := range resolution.Diagnostics {
		if diagnostic.Source == LaunchPluginSourceInstalled && diagnostic.Message != "" &&
			isRegistryDuplicateOfExplicit(resolution, diagnostic) {
			// An installed duplicate losing to an explicit directory was
			// historically silent.
			continue
		}
		switch {
		case len(diagnostic.Name) > 0 && isDuplicateDiagnostic(diagnostic):
			_, _ = fmt.Fprintf(m.stderr(), "warning: duplicate plugin name %q at %s; keeping the first\n", diagnostic.Name, diagnostic.Path)
		case diagnostic.Source == LaunchPluginSourceBundled:
			// Not about any directory the caller named: the store itself is
			// unusable, so the message stands on its own.
			_, _ = fmt.Fprintf(m.stderr(), "warning: %s\n", diagnostic.Message)
		case diagnostic.Source == LaunchPluginSourceInstalled:
			_, _ = fmt.Fprintf(m.stderr(), "warning: skipping broken plugin %s: %s\n", diagnostic.Path, diagnostic.Message)
		default:
			_, _ = fmt.Fprintf(m.stderr(), "warning: skipping invalid --plugin-dir %s: %s\n", diagnostic.Path, diagnostic.Message)
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: listing plugins: %v\n", err)
	}
	return resolution.SelectedDirs
}

func isDuplicateDiagnostic(diagnostic LaunchPluginDiagnostic) bool {
	return len(diagnostic.Name) > 0 && len(diagnostic.Path) > 0 &&
		len(diagnostic.Message) >= len("duplicate plugin name") &&
		diagnostic.Message[:len("duplicate plugin name")] == "duplicate plugin name"
}

func isRegistryDuplicateOfExplicit(resolution LaunchPluginResolution, diagnostic LaunchPluginDiagnostic) bool {
	if !isDuplicateDiagnostic(diagnostic) {
		return false
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Name == diagnostic.Name && candidate.Source == LaunchPluginSourceDirectory {
			return true
		}
	}
	return false
}

// listOrWarn returns m.List() results or nil on error (already warned by caller).
// It remains for compatibility with package-local diagnostic tests.
//
//nolint:unused // used by evenerfuzz coverage tests.
func listOrWarn(m *Manager) []ListItem {
	items, err := m.List()
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: listing plugins: %v\n", err)
		return nil
	}
	return items
}
