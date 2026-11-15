// Package wirenames owns the tool names each provider surface trains models
// on, where they differ from Evener's canonical names. Provider profiles
// apply them to the registry; unknown-tool diagnosis uses them to suggest the
// name a surface actually exposes.
package wirenames

import (
	"maps"

	"primeradiant.com/evener/llm/registry"
)

var (
	openAI      = map[string]string{"shell": "exec_command", "grep": "grep_files", "glob": "find_files"}
	google      = map[string]string{"shell": "run_shell_command", "grep": "grep_search", "list_dir": "list_directory"}
	toCanonical = buildCanonicalMap(openAI, google)
)

func buildCanonicalMap(surfaces ...map[string]string) map[string]string {
	inv := make(map[string]string)
	for _, renames := range surfaces {
		for canonical, wire := range renames {
			if prev, exists := inv[wire]; exists && prev != canonical {
				panic("wirenames: conflicting canonical mappings for " + wire)
			}
			inv[wire] = canonical
		}
	}
	return inv
}

// Canonical returns the canonical Evener tool name for a provider wire alias,
// or name unchanged when it is already canonical or belongs to an unmapped tool.
func Canonical(name string) string {
	if canonical, ok := toCanonical[name]; ok {
		return canonical
	}
	return name
}

// ForSurface returns the canonical→wire renames for surface, or nil when the
// surface uses canonical names. The map is the caller's to keep.
func ForSurface(surface string) map[string]string {
	switch surface {
	case registry.SurfaceOpenAI:
		return cloneMap(openAI)
	case registry.SurfaceGoogle:
		return cloneMap(google)
	default:
		return nil
	}
}

// NamesOf returns canonical followed by every name a surface exposes it under.
func NamesOf(canonical string) []string {
	names := []string{canonical}
	for _, renames := range []map[string]string{openAI, google} {
		if wire, ok := renames[canonical]; ok {
			names = append(names, wire)
		}
	}
	return names
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}
