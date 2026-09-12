package plugins

import (
	"context"
	"fmt"

	agentplugin "primeradiant.com/evener/agent/plugin"
)

var enabledLoad = agentplugin.Load

// listOrWarn returns m.List results or nil on error (already warned by caller).
// It remains for compatibility with package-local diagnostic tests.
//
//nolint:unused // used by evenerfuzz coverage tests.
func listOrWarn(ctx context.Context, m *Manager) []ListItem {
	items, err := m.List(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: listing plugins: %v\n", err)
		return nil
	}
	return items
}
