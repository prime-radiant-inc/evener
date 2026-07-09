package sandbox

import "strings"

// providerWebEgressCapable records, per LLM provider, whether the provider runs
// SERVER-SIDE web egress for the model (its own web-search / fetch tooling that
// reaches the internet on the model's behalf). This is orthogonal to serf's own
// web_fetch/web_search tools and to LLM inference traffic: net=off governs the
// tool plane (spawned procs, serf tool egress, provider-native web), never the
// model connection itself.
//
// The map is intentionally small and explicit. Everything NOT listed is unknown
// and treated as egress-capable (fail closed): under net=off an unknown provider's
// web capability is refused rather than silently allowed through a path the user
// cannot inspect.
var providerWebEgressCapable = map[string]bool{
	"openai":    true,
	"anthropic": true,
	"gemini":    true,
	"google":    true,
}

// WebEgress reports whether a provider is known to run server-side web egress
// (egressCapable) and whether the provider is in the registry at all (known). An
// unknown provider returns (true, false): fail closed — treated as egress-capable
// so net=off refuses it.
func WebEgress(provider string) (egressCapable, known bool) {
	c, ok := providerWebEgressCapable[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return true, false
	}
	return c, true
}

// ProviderWebAllowedUnderNetOff reports whether a provider's native web egress may
// run while network egress is denied. It is true only for a KNOWN, non-egress
// provider; every egress-capable or unknown provider is refused (fail closed), so
// net=off can never be silently false through provider-native web.
func ProviderWebAllowedUnderNetOff(provider string) bool {
	egress, _ := WebEgress(provider)
	return !egress
}
