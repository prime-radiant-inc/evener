// Env-slice helpers for applying an environment overlay the way a spawned
// child receives one: the parent environment first, the overlay applied last
// so its entries win. One writer shared by every applier — launchconfig.ToEnv
// for spawned daemons, MCP config discovery for the settings probe — keeps
// the two from drifting apart.
package envvars

import (
	"maps"
	"slices"
	"strings"
)

// Set sets key=value in env, replacing the entry already there in place, and
// returns the updated slice, appending when no entry holds key.
func Set(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// ApplyOverlay applies overlay onto a copy of parent: overlay entries are set
// last, so they win, in sorted key order for determinism. The parent is
// copied, never mutated; a nil overlay returns an equal copy.
func ApplyOverlay(parent []string, overlay map[string]string) []string {
	out := append([]string(nil), parent...)
	for _, key := range slices.Sorted(maps.Keys(overlay)) {
		out = Set(out, key, overlay[key])
	}
	return out
}
