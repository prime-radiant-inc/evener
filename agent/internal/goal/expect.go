// Condition registration + check-on-claim verification (spec §6).
//
// goal_expect registers a (desc, predicate) stop-claim condition with the
// identical §2 registration validation + attach-scan snapshot at registration
// (hallucinated conditions rejected immediately with the reason named).
// Expect-conditions evaluate check-on-claim only: update_goal("complete")
// verifies iff the goal carries conditions (goals without conditions keep the
// v1 self-declare path); with conditions the verifier evaluates the named
// conditions and rejects with the failing condition named. Expect-conditions
// never feed the ledger mid-episode (waits' flips are the only subgoal
// evidence), so no expect polling exists and the re-park counter cannot be
// laundered through trivial conditions.
package goal

import (
	"fmt"
	"strings"
	"time"
)

// ExpectRequest is a goal_expect registration: the condition description plus
// the predicate it checks (same shape as a wait predicate: kind + target +
// matcher + subtype + generation).
type ExpectRequest struct {
	// Desc names the condition; the verifier names it on rejection.
	Desc string
	// Predicate is the condition query. Kind selects the source (until_job |
	// until_delegate | until_approval | until_event | until_child; until_time
	// is rejected — a bare timer is a wait, not a claim condition).
	Predicate WaitKind
}

// validExpectKind reports whether k may serve as an expect predicate. Every
// registry kind except the bare timer qualifies: a condition must query
// durable or re-derivable state, not the passage of time.
func validExpectKind(k Kind) bool {
	switch k {
	case WaitUntilJob, WaitUntilDelegate, WaitUntilApproval, WaitUntilEvent, WaitUntilChild:
		return true
	}
	return false
}

// RegisterExpect validates and installs one stop-claim condition (spec §6),
// running the identical §2 registration validation + attach-scan snapshot at
// registration. It reports the registered condition and whether registration
// succeeded; on failure LastRejectReason names the failed check. Terminal
// goals and missing goals reject (conditions register on live goals only).
// Until-time predicates reject (a bare timer is a wait, not a claim
// condition). The attach-scan snapshot (file baseline + current truth) is
// informational: the verifier re-evaluates at claim time.
func (s *Store) RegisterExpect(req ExpectRequest, now time.Time) (Condition, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.goal
	if g == nil {
		return Condition{}, false
	}
	if g.Status == StatusComplete || g.Status == StatusBlocked {
		return Condition{}, false
	}
	desc := strings.TrimSpace(req.Desc)
	if desc == "" {
		s.lastRejectReason = "condition description is required"
		return Condition{}, false
	}
	pred := req.Predicate
	if !validExpectKind(pred.Kind) {
		s.lastRejectReason = fmt.Sprintf("condition kind %q is not verifiable (must query durable state, not time)", pred.Kind)
		return Condition{}, false
	}
	if !checkSizeCaps(WaitKind{Matcher: pred.Matcher, Target: pred.Target, Label: desc}) {
		s.lastRejectReason = "condition exceeds the model-controlled field caps (matcher ≤1KB, target ≤2KB, desc ≤256 printable chars)"
		return Condition{}, false
	}
	// Identical §2 substrate validation (fail-closed), shared with RegisterWait
	// via validatePredicateLocked minus the timer/catch-up branches: expect
	// never parks, so terminal catch-up does not apply (a retained-terminal
	// target snapshots satisfied=true instead).
	satisfied, baseline, ok := s.expectAttachScanLocked(pred)
	if !ok {
		return Condition{}, false
	}
	for _, c := range g.Conditions {
		if c.Desc == desc && c.Predicate.Kind == pred.Kind && c.Predicate.Target == pred.Target &&
			c.Predicate.EventSubtype == pred.EventSubtype && c.Predicate.Matcher == pred.Matcher &&
			c.Predicate.AskGeneration == pred.AskGeneration {
			s.lastRejectReason = ""
			return c, true
		}
	}
	cond := Condition{
		Desc:         desc,
		Predicate:    pred,
		Baseline:     baseline,
		Satisfied:    satisfied,
		RegisteredAt: now,
	}
	g.Conditions = append(g.Conditions, cond)
	g.UpdatedAt = now
	s.lastRejectReason = ""
	return cond, true
}

// expectAttachScanLocked runs the §2 validation + attach-scan snapshot for an
// expect predicate under the held store lock. It mirrors RegisterWait's
// substrate branches (same fail-closed reasons) without parking: job/delegate
// retained-terminal targets snapshot satisfied=true; live targets snapshot
// their current truth; file_modified snapshots the baseline and compares;
// http_match snapshots matcher state via CheckURL reachability (truth
// re-evaluates at claim); approval snapshots the live-ask match;
// until_child snapshots known-descendant truth. Caller must hold s.mu.
func (s *Store) expectAttachScanLocked(pred WaitKind) (satisfied bool, baseline string, ok bool) {
	sub := s.substrate
	kindNeedsSubstrate := true
	if pred.Kind == WaitUntilEvent && pred.EventSubtype == EventExternalLabel {
		kindNeedsSubstrate = false
	}
	if kindNeedsSubstrate && sub == nil {
		s.lastRejectReason = fmt.Sprintf("%s %q: no wait substrate wired", pred.Kind, pred.Target)
		return false, "", false
	}
	switch pred.Kind {
	case WaitUntilJob:
		live, retained, _, ok := sub.LookupJob(pred.Target)
		if !ok {
			s.lastRejectReason = fmt.Sprintf("unknown job %q: no record in this session tree", pred.Target)
			return false, "", false
		}
		if !live && !retained {
			s.lastRejectReason = fmt.Sprintf("job %q is neither running nor retained-terminal", pred.Target)
			return false, "", false
		}
		return retained, "", true
	case WaitUntilDelegate:
		live, retained, _, ok := sub.LookupDelegate(pred.Target)
		if !ok {
			s.lastRejectReason = fmt.Sprintf("unknown delegate %q: no record in this session tree", pred.Target)
			return false, "", false
		}
		if !live && !retained {
			s.lastRejectReason = fmt.Sprintf("delegate %q is neither running/settling/stopping nor retained-terminal", pred.Target)
			return false, "", false
		}
		return retained, "", true
	case WaitUntilApproval:
		if strings.TrimSpace(pred.Target) == "" {
			s.lastRejectReason = "approval condition requires a content key target"
			return false, "", false
		}
		return sub.LookupApproval(pred.Target, pred.AskGeneration), "", true
	case WaitUntilChild:
		if strings.TrimSpace(pred.Target) == "" {
			s.lastRejectReason = "child condition requires a child session id"
			return false, "", false
		}
		return sub.LookupChild(pred.Target), "", true
	case WaitUntilEvent:
		switch pred.EventSubtype {
		case EventFileModified:
			baseline, ok := sub.StatFile(pred.Target)
			if !ok {
				s.lastRejectReason = fmt.Sprintf("unstatable file %q: not inside the session sandbox or missing", pred.Target)
				return false, "", false
			}
			return false, baseline, true
		case EventHTTPMatch:
			if !ValidHTTPURL(pred.Target) || !sub.CheckURL(pred.Target, pred.Timeout) {
				s.lastRejectReason = fmt.Sprintf("rejected URL %q: must be well-formed with an explicit timeout under the session egress policy", pred.Target)
				return false, "", false
			}
			return false, "", true
		case EventExternalLabel:
			return false, "", true
		default:
			s.lastRejectReason = fmt.Sprintf("unknown event subtype %q", pred.EventSubtype)
			return false, "", false
		}
	default:
		s.lastRejectReason = fmt.Sprintf("unknown condition kind %q", pred.Kind)
		return false, "", false
	}
}

// ConditionCheck is one evaluated condition: its registered description plus
// its claim-time truth.
type ConditionCheck struct {
	Desc      string
	Satisfied bool
}

// VerifyConditions evaluates the goal's registered conditions check-on-claim
// (spec §6): pure over the given claim-time checks. Empty conditions mean the
// v1 self-declare path (satisfied=true, no failure). With conditions, every
// check must be satisfied; the first unsatisfied check fails with its desc
// named. Cost scales with the claim (no continuous ticks).
func VerifyConditions(conds []Condition, checks []ConditionCheck) (satisfied bool, failing string) {
	if len(conds) == 0 {
		return true, ""
	}
	byDesc := make(map[string]bool, len(checks))
	for _, c := range checks {
		byDesc[c.Desc] = c.Satisfied
	}
	for _, cond := range conds {
		if sat, ok := byDesc[cond.Desc]; ok {
			if !sat {
				return false, cond.Desc
			}
			continue
		}
		return false, cond.Desc
	}
	return true, ""
}

// EvaluateExpectations re-evaluates every registered condition against the
// live substrate at claim time (spec §6 check-on-claim). It returns the
// per-condition truth in registration order. File_modified compares the live
// baseline against the registration baseline (changed = satisfied);
// approval/child/external-label re-read liveness; job/delegate satisfied
// means retained-terminal (the awaited completion landed). Caller holds no
// store lock (Substrate lookups are session reads); the caller snapshots the
// condition list first.
func (s *Store) EvaluateExpectations(conds []Condition) []ConditionCheck {
	s.mu.Lock()
	sub := s.substrate
	s.mu.Unlock()
	out := make([]ConditionCheck, 0, len(conds))
	for _, c := range conds {
		out = append(out, ConditionCheck{Desc: c.Desc, Satisfied: evaluateExpectation(sub, c)})
	}
	return out
}

// evaluateExpectation evaluates one condition against the live substrate.
// Pure over (sub, cond): no store locks.
func evaluateExpectation(sub Substrate, c Condition) bool {
	if sub == nil {
		return false
	}
	pred := c.Predicate
	switch pred.Kind {
	case WaitUntilJob:
		_, retained, _, ok := sub.LookupJob(pred.Target)
		return ok && retained
	case WaitUntilDelegate:
		_, retained, _, ok := sub.LookupDelegate(pred.Target)
		return ok && retained
	case WaitUntilApproval:
		return sub.LookupApproval(pred.Target, pred.AskGeneration)
	case WaitUntilChild:
		return sub.LookupChild(pred.Target)
	case WaitUntilEvent:
		switch pred.EventSubtype {
		case EventFileModified:
			live, ok := sub.StatFile(pred.Target)
			return ok && live != c.Baseline
		case EventHTTPMatch:
			return ValidHTTPURL(pred.Target) && sub.CheckURL(pred.Target, pred.Timeout)
		case EventExternalLabel:
			return false
		}
	}
	return false
}
