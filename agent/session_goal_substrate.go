package agent

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/goal"
)

// Slice-2 goal substrate (spec §2 validation + §8 forward support): the
// session-owned predicate substrate consulted by RegisterWait, fail-closed.
// Until_time needs no substrate; until_child resolves against the tracked
// direct-child set plus stable-delegate durable records (LookupChild: known
// descendant).

// goalSessionSubstrate is the session's live predicate substrate. It holds no
// locks of its own: every lookup acquires, reads, and releases the underlying
// manager lock (never held across a claim — the §3-top pre-read discipline).
type goalSessionSubstrate struct {
	sess *Session
}

// LookupJob resolves a supervised-job target. Live wake-capable targets (a
// running non-detached job) park; retained-terminal jobs inside the
// record-retention window route to terminal catch-up with the terminal
// outcome excerpt; anything else rejects fail-closed.
func (g *goalSessionSubstrate) LookupJob(id string) (live, retainedTerminal bool, excerpt string, ok bool) {
	s := g.sess
	if s == nil || s.jobManager == nil || id == "" {
		return false, false, "", false
	}
	jm := s.jobManager
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if r, ok := jm.running[id]; ok && r != nil && r.rec != nil {
		return true, false, "", true
	}
	// Retained-terminal catch-up: consult the durable store for a terminal
	// record inside the retention window.
	if recs, err := jm.store.Load(); err == nil {
		if job, ok := recs[id]; ok && job != nil && job.Status.IsTerminal() {
			return false, true, "job " + id + " " + strings.ToLower(string(job.Status)), true
		}
	}
	return false, false, "", false
}

// LookupDelegate resolves a delegate target: live (running/settling/stopping)
// parks; retained-terminal routes to catch-up; anything else rejects.
func (g *goalSessionSubstrate) LookupDelegate(id string) (live, retainedTerminal bool, excerpt string, ok bool) {
	s := g.sess
	if s == nil || s.delegateController == nil || id == "" {
		return false, false, "", false
	}
	c := s.delegateController
	c.mu.Lock()
	defer c.mu.Unlock()
	agg, ok := c.durable[id]
	if !ok || agg == nil {
		return false, false, "", false
	}
	switch agg.Phase {
	case delegatestore.PhaseRunning, delegatestore.PhaseSettling, delegatestore.PhaseStopping:
		return true, false, "", true
	case delegatestore.PhaseClosed:
		return false, true, "delegate " + id + " " + string(agg.Phase), true
	default:
		return false, false, "", false
	}
}

// StatFile resolves a file_modified target inside the session sandbox. The
// baseline is name+size+mtime (never contents).
func (g *goalSessionSubstrate) StatFile(path string) (baseline string, ok bool) {
	s := g.sess
	if s == nil || path == "" {
		return "", false
	}
	fi, err := s.delegateRestoreStat(path)
	if err != nil {
		return "", false
	}
	return fi.Name() + "/" + itoa(fi.Size()) + "/" + fi.ModTime().UTC().Format(time.RFC3339Nano), true
}

// LookupApproval reports whether the (content key, ask generation) pair
// matches a live ask. Consumed answers never match (no catch-up by design).
// The content key is the (header, question) pair: targets encoding
// "header\x00question" match only an ask with both halves equal (no
// cross-generation aliasing); bare single-half text matches only an ask
// whose other half is empty, and the wake excerpt notes it as
// ambiguous-by-construction. askQuestion carries no stable ask-call ID in
// this slice, so a non-empty generation never matches (fail closed — a
// dangling wait from an earlier same-text ask cannot validate against a
// later generation). Tightened per Task-7 Minor-1: the pre-Task-8 loose OR
// (header==key || question==key) is replaced by this pair contract.
func (g *goalSessionSubstrate) LookupApproval(contentKey, generation string) bool {
	s := g.sess
	if s == nil || contentKey == "" {
		return false
	}
	if generation != "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ask := range s.askPending {
		if approvalKeyMatches(ask.Header, ask.Question, contentKey) {
			return true
		}
	}
	return false
}

// approvalKeyMatches implements the (header, question) pair contract: a
// two-half key ("header\x00question") matches only on both halves; a bare
// key matches a lone half (the other half empty) as ambiguous-by-construction.
func approvalKeyMatches(header, question, key string) bool {
	if h, q, ok := splitApprovalKey(key); ok {
		return header == h && question == q
	}
	if header != "" && question != "" {
		return false
	}
	return header == key || question == key
}

// splitApprovalKey splits a two-half approval key. Reports false for bare
// single-half keys.
func splitApprovalKey(key string) (header, question string, ok bool) {
	h, q, found := splitNul(key)
	if !found {
		return "", "", false
	}
	return h, q, true
}

// splitNul splits on the first NUL byte.
func splitNul(s string) (a, b string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// LookupChild reports whether id is a known descendant session (tracked
// direct child or stable-delegate durable record). Sibling waiters share the
// parent's controller: a waiter child resolves the parent's tracked set
// alongside its own (a child waiting on its sibling is the §8 sibling shape).
func (g *goalSessionSubstrate) LookupChild(id string) bool {
	s := g.sess
	if s == nil || id == "" {
		return false
	}
	if s.subagents != nil {
		for _, sub := range s.subagents.directSubagents() {
			if sub != nil && sub.id == id {
				return true
			}
		}
	}
	if c := s.delegateController; c != nil {
		// Durable aggregates are keyed by DELEGATE id, but until_child
		// targets CHILD SESSION ids: match on
		// Aggregate.Descriptor.ChildSessionID (a raw delegate-id hit
		// would wrongly accept a non-child target). Restored children
		// whose runtime is untracked still resolve here.
		c.mu.Lock()
		childKnown := false
		for _, agg := range c.durable {
			if agg != nil && agg.Descriptor.ChildSessionID == id {
				childKnown = true
				break
			}
		}
		c.mu.Unlock()
		if childKnown {
			return true
		}
		// Sibling shape: walk the tracked sets of the controller's live
		// runtimes (the parent first) for a matching direct child.
		c.mu.Lock()
		parents := []*Session{c.rootRuntime}
		for _, live := range c.live {
			if live != nil && live.runtime != nil {
				parents = append(parents, live.runtime)
			}
		}
		c.mu.Unlock()
		for _, parent := range parents {
			if parent == nil || parent.subagents == nil {
				continue
			}
			for _, sub := range parent.subagents.directSubagents() {
				if sub != nil && sub.id == id {
					return true
				}
			}
		}
	}
	return false
}

// CheckURL validates an http_match URL under the session egress policy:
// well-formed absolute http(s) (goal.ValidHTTPURL) with loopback,
// link-local, and private ranges denied (matcher/fetch evaluation itself is
// a deferred slice — this gate is validation-only). The host parses
// strictly: canonical IP literals deny by range; legacy numeric spellings
// (decimal/octal/hex parts, short forms like 127.1, bare integers like
// 2130706433) deny outright since resolvers may interpret them as IPs;
// localhost names deny; anything else must be a syntactically valid DNS
// name. Resolution-time (DNS rebinding) checks belong at the deferred fetch
// leg, which resolves and re-checks the destination IP.
func (g *goalSessionSubstrate) CheckURL(rawURL string, timeout time.Duration) bool {
	if !goal.ValidHTTPURL(rawURL) {
		return false
	}
	_ = timeout
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if host == "" {
		return false
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsPrivate() || !addr.IsValid() || addr.IsUnspecified() {
			return false
		}
		return true
	}
	// Not a canonical IP literal: deny legacy numeric spellings resolvers
	// may still interpret as IPs (isLegacyIPv4Literal mirrors the mobile
	// pairing gate in cmd/evener-hub/app_mobile.go), then require a valid
	// DNS name so encoded/odd spellings fail closed.
	if isLegacyIPv4Literal(host) {
		return false
	}
	if !isValidDNSName(lower) {
		return false
	}
	return true
}

// isLegacyIPv4Literal reports whether host looks like an inet_aton-style
// numeric address (decimal/octal/hex parts, 1-4 parts, bare integers like
// 2130706433 or 0x7f000001). Mirrors the mobile pairing gate: such
// spellings are denied deterministically rather than resolved.
func isLegacyIPv4Literal(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return false
	}
	for i, part := range parts {
		base := 10
		digits := part
		if len(part) > 2 && part[0] == '0' && (part[1] == 'x' || part[1] == 'X') {
			base = 16
			digits = part[2:]
		} else if len(part) > 1 && part[0] == '0' {
			base = 8
		}
		if digits == "" {
			return false
		}
		value, err := strconv.ParseUint(digits, base, 32)
		if err != nil {
			return false
		}
		bits := 8
		if i == len(parts)-1 {
			bits = 8 * (5 - len(parts))
		}
		if value >= uint64(1)<<bits {
			return false
		}
	}
	return true
}

// isDNSLabelChar reports whether c is valid inside a DNS label:
// lowercase letter, digit, or hyphen.
func isDNSLabelChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	default:
		return c == '-'
	}
}

// isValidDNSName reports whether host is a syntactically valid DNS name:
// dot-separated labels of letters/digits/hyphens, no empty labels, no
// leading/trailing hyphens, TLD not all-numeric (numeric TLDs are IP-like).
func isValidDNSName(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			if !isDNSLabelChar(label[i]) {
				return false
			}
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	tld := labels[len(labels)-1]
	allDigits := true
	for i := 0; i < len(tld); i++ {
		if tld[i] < '0' || tld[i] > '9' {
			allDigits = false
			break
		}
	}
	return !allDigits
}

// itoa renders an int64 without importing strconv at this site.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [32]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
