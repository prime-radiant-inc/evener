package wirenames

import (
	"maps"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

var (
	openAI      = map[string]string{"shell": "exec_command", "grep": "grep_files", "glob": "find_files"}
	google      = map[string]string{"shell": "run_shell_command", "grep": "grep_search", "list_dir": "list_directory"}
	toCanonical = buildCanonicalMap(openAI, google)

	// Per-surface inverse tables. A rename belongs to the surface that applies
	// it: exec_command names shell on OpenAI and is an ordinary tool name
	// everywhere else, so identity read through the union of every surface is
	// not identity at all (#1166 review).
	surfaceCanonical = map[string]map[string]string{
		registry.SurfaceOpenAI: buildCanonicalMap(openAI),
		registry.SurfaceGoogle: buildCanonicalMap(google),
		// Anthropic renames nothing. It must be listed explicitly: an absent
		// key falls back to the union, which would read Anthropic's own
		// exec_command as shell.
		registry.SurfaceAnthropic: {},
		// Generic applies no aliases in dispatch either (surfaceConventions
		// returns nil for it), so the same reasoning holds: listing it makes
		// "renames nothing" a fact instead of an omission (#1166 review).
		registry.SurfaceGeneric: {},
	}
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

// CanonicalFor returns the canonical name a SURFACE gives name, reading the
// rename table of that surface alone.
//
// An empty or unknown surface — a transcript written before the surface was
// recorded — falls back to Canonical, the union, because there is no narrower
// vocabulary to read the name in.
func CanonicalFor(surface, name string) string {
	renames, ok := surfaceCanonical[surface]
	if !ok {
		return Canonical(strings.TrimSpace(name))
	}
	name = strings.TrimSpace(name)
	if canonical, ok := renames[name]; ok {
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

// SameFor reports whether two recorded names denote the same tool when the
// ANNOUNCING SURFACE decides what its own names mean.
//
// Same reads both names through the union of every surface's aliases, which is
// right only when no surface's vocabulary can make a name mean something else.
// It can: exec_command is shell on OpenAI and an ordinary name on Anthropic, so
// a symmetric union check turns two different tools into one and lets a result
// settle a call it never answered (#1166 review). Identity belongs to the
// surface that announced the call.
func SameFor(surface, a, b string) bool {
	return CanonicalFor(surface, a) == CanonicalFor(surface, b)
}

// Package wirenames owns the tool names each provider surface trains models
// on, where they differ from Evener's canonical names. Provider profiles
// apply them to the registry; unknown-tool diagnosis uses them to suggest the
// name a surface actually exposes.
