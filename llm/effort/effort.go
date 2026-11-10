// Package effort is the one definition of evener's reasoning-effort
// vocabulary: the six ranked tiers, their least-to-most order, and the
// explicit off level (#5553).
//
// It sits below llm so both the request side (llm) and the config schema
// (llm/registry) can import it: llm imports registry, so registry cannot
// import llm back, and each side used to restate the ladder.
package effort

import "strings"

// None is the configured value meaning "the user turned thinking off". It is
// distinct from "" (nothing configured), which lets a session apply a default
// effort without overriding an explicit off.
const None = "none"

// tiers are the six ranked levels, least to most. "xhigh" and "max" are
// distinct ascending tiers: GPT-5.6 and Anthropic's output_config.effort both
// place max above xhigh.
var tiers = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

var rank = map[string]int{
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// Tiers returns the six recognized reasoning-effort levels in rank order
// (least to most). The returned slice is a copy.
func Tiers() []string {
	return append([]string(nil), tiers...)
}

// Rank returns the ordinal rank of a reasoning-effort level (minimal=1 …
// max=6), or 0 for empty/unknown values. Comparison callers use this instead
// of a local hierarchy so effort comparisons honor the full vocabulary and
// cannot drift as levels are added.
func Rank(level string) int {
	return rank[strings.ToLower(strings.TrimSpace(level))]
}

// Known reports whether level (trimmed, case-insensitive) is one of Tiers.
func Known(level string) bool {
	_, ok := rank[strings.ToLower(strings.TrimSpace(level))]
	return ok
}

// Valid reports whether level is a value a session or a config may state:
// empty (unset), None, or one of Tiers.
func Valid(level string) bool {
	v := strings.ToLower(strings.TrimSpace(level))
	return v == "" || v == None || Known(v)
}
