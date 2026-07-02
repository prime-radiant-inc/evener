package llm

import (
	"net/http"
	"sort"
)

// sortedHeaderKeys returns m's keys sorted, so header resolution reports a
// missing-variable error deterministically regardless of map iteration order.
func sortedHeaderKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// MergeHeaders overlays override onto base, returning a new map. Keys in
// override win over the same key in base; keys only in base survive. Keys
// are canonicalized with http.CanonicalHeaderKey (both base and override)
// before merging, so headers that differ only in case (e.g. "User-Agent" vs
// "user-agent") collide into one deterministic entry instead of coexisting
// as two map keys whose winner at request time would depend on map
// iteration order. It returns nil when both are empty so an adapter's
// DefaultHeaders stays nil (its zero-value behavior) unless headers were
// actually configured.
//
// This is the seam that lets a provider-set default header (e.g. a coding-plan
// User-Agent) coexist with user-configured [instances.X.headers]: pass the
// provider default as base and the user headers as override, so the user wins
// on collision but the default survives when the user sets no header of that
// name.
func MergeHeaders(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range override {
		out[http.CanonicalHeaderKey(k)] = v
	}
	return out
}
