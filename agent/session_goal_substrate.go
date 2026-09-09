package agent

import (
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
		c.mu.Lock()
		_, ok := c.durable[id]
		c.mu.Unlock()
		if ok {
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
// well-formed absolute http(s) (goal.ValidHTTPURL) with deny
// link-local/loopback by default.
func (g *goalSessionSubstrate) CheckURL(rawURL string, timeout time.Duration) bool {
	if !goal.ValidHTTPURL(rawURL) {
		return false
	}
	_ = timeout
	lower := strings.ToLower(rawURL)
	for _, denied := range []string{"localhost", "127.", "0.0.0.0", "::1", "[::1]", "10.", "192.168.", "169.254."} {
		if strings.Contains(lower, denied) {
			return false
		}
	}
	return true
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
