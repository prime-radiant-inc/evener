package sandbox

import "fmt"

// EnforcementLine returns the single startup line that states, loudly, exactly
// what a sandboxed session enforces on this host: the backend, the mode, the
// network decision, that credential/secret paths are masked, and how the language
// caches are served (shared-read/private-write vs a fully private redirect), in
// plain words rather than cache-implementation jargon. It names the MODE, not the
// --sandbox flag, so it reads truthfully for a per-delegate box too. An off /
// unenforced policy returns "" — there is no containment to announce. Printed from
// the RESOLVED policy so it can never overstate what the host actually enforces.
func EnforcementLine(rp ResolvedPolicy) string {
	if !rp.Enforced() {
		return ""
	}
	net := "off"
	if rp.Network {
		net = "on"
	}
	return fmt.Sprintf("sandbox: %s enforcing %s (network %s, secrets masked, cache %s)",
		rp.Backend, rp.Mode, net, cacheDescription(rp.CacheStrategy))
}

// cacheDescription renders the cache strategy for the enforcement line in plain
// words: an overlay reads the real host caches and keeps writes private; a
// session-private strategy redirects the caches into a private per-session dir; a
// mode that never writes caches needs none.
func cacheDescription(c CacheStrategy) string {
	switch c {
	case CacheOverlay:
		return "shared-read/private-write"
	case CacheSessionPrivate:
		return "private"
	default:
		return "none"
	}
}
