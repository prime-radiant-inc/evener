// Condition registration + check-on-claim verification (spec §6).
//
// goal_expect registers a (desc, predicate) stop-claim condition with the
// identical §2 registration validation + attach-scan snapshot at registration
// (hallucinated conditions rejected immediately with the reason named).
// Expect-conditions verify at claim time: update_goal("complete") verifies
// iff the goal carries conditions (goals without conditions keep the v1
// self-declare path); with conditions the verifier evaluates the named
// conditions and rejects with the failing condition named. Reads are cheap
// substrate re-reads (EvaluateExpectations) and never feed the ledger
// mid-episode (waits' flips are the only subgoal evidence): the delta frame
// on plain drives re-reads condition truth for display, and the re-park
// counter cannot be laundered through trivial conditions.
package goal

import (
	"fmt"
	"strings"
	"time"
)

// ExpectRequest is a goal_expect registration: the condition description plus
// the predicate it checks (same shape as a wait predicate: kind + target +
// matcher + subtype + generation). Registrable kinds are file/job/delegate
// only (fix-1/4 I1): approval/child/http/external-label reject with a named
// reason — their substrates (child-terminal query, approval-answer record,
// http fetch) are out of scope for v1.
type ExpectRequest struct {
	// Desc names the condition; the verifier names it on rejection.
	Desc string
	// Predicate is the condition query. Kind selects the source
	// (until_job | until_delegate | until_event/file_modified only;
	// until_time, until_approval, until_child, http_match, and
	// external_label reject — a bare timer is a wait, not a claim
	// condition, and the other substrates are out of scope for v1).
	Predicate WaitKind
}

// expectKindRejected reports the named rejection for a non-registrable
// condition kind/subtype (fix-1/4 I1). ok=false means registrable.
func expectKindRejected(pred WaitKind) (reason string, rejected bool) {
	switch pred.Kind {
	case WaitUntilApproval:
		return fmt.Sprintf("condition kind %q is not verifiable in v1: approval-answer records are out of scope (register a file, job, or delegate condition)", pred.Kind), true
	case WaitUntilChild:
		return fmt.Sprintf("condition kind %q is not verifiable in v1: child-terminal queries are out of scope (register a file, job, or delegate condition)", pred.Kind), true
	case WaitUntilEvent:
		switch pred.EventSubtype {
		case EventHTTPMatch:
			return fmt.Sprintf("condition subtype %q is not verifiable in v1: http fetch is out of scope (register a file, job, or delegate condition)", pred.EventSubtype), true
		case EventExternalLabel:
			return fmt.Sprintf("condition subtype %q is not verifiable in v1: external labels carry no queryable state (register a file, job, or delegate condition)", pred.EventSubtype), true
		case EventFileModified, "":
			return "", false
		default:
			return fmt.Sprintf("unknown event subtype %q", pred.EventSubtype), true
		}
	case WaitUntilTime:
		return fmt.Sprintf("condition kind %q is not verifiable (must query durable state, not time)", pred.Kind), true
	}
	return fmt.Sprintf("unknown condition kind %q", pred.Kind), true
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
	registrable := pred.Kind == WaitUntilJob || pred.Kind == WaitUntilDelegate ||
		(pred.Kind == WaitUntilEvent && (pred.EventSubtype == EventFileModified || pred.EventSubtype == ""))
	if !registrable {
		reason, _ := expectKindRejected(pred)
		s.lastRejectReason = reason
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
	kindNeedsSubstrate := pred.Kind != WaitUntilEvent || pred.EventSubtype != EventExternalLabel
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
// named. Positional: checks[i] answers conds[i] (EvaluateExpectations builds
// them in order) — never joined by Desc, so duplicate descriptions cannot
// collapse two conditions into one truth. Cost scales with the claim (no
// continuous ticks).
func VerifyConditions(conds []Condition, checks []ConditionCheck) (satisfied bool, failing string) {
	if len(conds) == 0 {
		return true, ""
	}
	for i, cond := range conds {
		if i >= len(checks) {
			return false, cond.Desc
		}
		if !checks[i].Satisfied {
			return false, cond.Desc
		}
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
