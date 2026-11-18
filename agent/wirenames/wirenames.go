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

// Same reports whether two recorded tool names denote the same tool once each
// is read in its own vocabulary.
//
// A call and its result can record DIFFERENT spellings of one tool: the
// assistant turn persists the provider's wire name (appendAssistantTurn keeps
// the response message, and the dispatch-time canonicalization rewrites only
// the copies ToolCalls() returned), while the result turn records the canonical
// name the registry dispatched. exec_command/shell is the everyday OpenAI
// case. Two names that canonicalize to the same tool are the same tool; two
// that do not are genuinely different tools sharing a call id, which is the
// case the settlement guard exists to keep apart (#1166 review).
//
// Surrounding whitespace is not part of a name. Dispatch trims before it
// executes (canonicalToolName), so a provider that pads a name leaves the
// announcement padded and the result trimmed; comparing raw would split a call
// from its own result on whitespace alone.
func Same(a, b string) bool {
	return Canonical(strings.TrimSpace(a)) == Canonical(strings.TrimSpace(b))
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
