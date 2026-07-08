package sandbox

import "fmt"

// EnforcementLine returns the single startup line that states, loudly, exactly
// what a sandboxed session enforces on this host: the backend, the mode, the
// network decision, and how caches are served (warm read-real/write-private
// overlay vs cold session-private redirect). An off / unenforced policy returns
// "" — there is no containment to announce. The user reads this to know the
// per-host enforcement set before any model turn runs.
func EnforcementLine(rp ResolvedPolicy) string {
	if !rp.Enforced() {
		return ""
	}
	net := "off"
	if rp.Network {
		net = "on"
	}
	return fmt.Sprintf("sandbox: %s enforcing --sandbox %s (network %s, cache %s)",
		rp.Backend, rp.Mode, net, cacheDescription(rp.CacheStrategy))
}

// cacheDescription renders the cache strategy for the enforcement line.
func cacheDescription(c CacheStrategy) string {
	switch c {
	case CacheOverlay:
		return "warm-overlay"
	case CacheSessionPrivate:
		return "cold session-private"
	default:
		return "n/a"
	}
}
