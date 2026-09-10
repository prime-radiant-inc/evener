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
	case WaitUntilJob, WaitUntilDelegate:
		// Registrable condition kinds (cf. RegisterExpect's registrable
		// gate): verifiable in v1, never rejected. Explicit so the two
		// registrable kinds cannot fall through to "unknown condition kind".
		return "", false
	case WaitUntilApproval:
		return fmt.Sprintf("condition kind %q is not verifiable in v1: approval-answer records are out of scope (register a file, job, or delegate condition)", pred.Kind), true
	case WaitUntilChild:
		return fmt.Sprintf("condition kind %q is not verifiable in v1: child-terminal queries are out of scope (register a file, job, or delegate condition)", pred.Kind), true
	case WaitUntilEvent:
		switch pred.EventSubtype {
		case EventHTTPMatch:
			return fmt.Sprintf("condition subtype %q is not supported (removed; follow issue #1061 for the fetch-based watch type)", pred.EventSubtype), true
		case EventExternalLabel:
			return fmt.Sprintf("condition subtype %q is not supported (removed; follow issue #1063 for the notification-based watch type)", pred.EventSubtype), true
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
	// Substrate pre-pass (lock-order discipline, cf. RegisterWait): the
	// attach-scan validation reads (LookupJob/LookupDelegate/LookupApproval/
	// LookupChild/StatFile) take session/manager locks and must never run
	// under the store lock. Evaluate the snapshot outside it, then replay
	// the decision under it. TOCTOU: point-in-time, like RegisterWait --
	// the verifier re-evaluates at claim time.
	s.mu.Lock()
	preSub := s.substrate
	s.mu.Unlock()
	pre := prescanExpect(preSub, req.Predicate)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inRegisterCritical.Store(true)
	defer s.inRegisterCritical.Store(false)
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
	// minus the timer/catch-up branches: expect never parks, so terminal
	// catch-up does not apply (a retained-terminal target snapshots
	// satisfied=true instead). The decision replays the pre-pass snapshot
	// (no substrate calls under the lock). Error strings are byte-identical
	// to the in-lock reads they replace.
	satisfied, baseline, ok := replayExpectPrescan(s, pre, pred)
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

// expectPrescan is the §2 validation + attach-scan snapshot for one expect
// predicate, evaluated OUTSIDE the store lock (the lock-order discipline).
// Pure over (sub, pred): no store locks. The jobOK/delegateOK bits preserve
// the unknown-vs-neither distinction so the replay names byte-identical
// rejection reasons.
type expectPrescan struct {
	subNil      bool
	jobOK       bool
	delegateOK  bool
	live        bool
	retained    bool
	approval    bool
	approvalSet bool
	childKnown  bool
	childSet    bool
	baseline    string
	baselineOK  bool
}

// prescanExpect evaluates the expect snapshot for pred against sub with no
// store lock held. Caller passes the substrate pointer copied under a short
// hold (nil = unwired).
func prescanExpect(sub Substrate, pred WaitKind) expectPrescan {
	var pre expectPrescan
	if sub == nil {
		pre.subNil = true
		return pre
	}
	switch pred.Kind {
	case WaitUntilJob:
		live, retained, _, ok := sub.LookupJob(pred.Target)
		pre.jobOK = ok
		pre.live, pre.retained = live, retained
	case WaitUntilDelegate:
		live, retained, _, ok := sub.LookupDelegate(pred.Target)
		pre.delegateOK = ok
		pre.live, pre.retained = live, retained
	case WaitUntilApproval:
		if strings.TrimSpace(pred.Target) == "" {
			return pre
		}
		pre.approvalSet = true
		pre.approval = sub.LookupApproval(pred.Target, pred.AskGeneration)
	case WaitUntilChild:
		if strings.TrimSpace(pred.Target) == "" {
			return pre
		}
		pre.childSet = true
		pre.childKnown = sub.LookupChild(pred.Target)
	case WaitUntilEvent:
		if pred.EventSubtype == EventFileModified {
			baseline, ok := sub.StatFile(pred.Target)
			pre.baseline, pre.baselineOK = baseline, ok
		}
	}
	return pre
}

// replayExpectPrescan replays the pre-pass snapshot without substrate calls.
// It mirrors RegisterWait's substrate branches (same fail-closed reasons)
// without parking: job/delegate retained-terminal targets snapshot
// satisfied=true; live targets snapshot their current truth; file_modified
// snapshots the baseline and compares; approval snapshots the live-ask
// match; until_child snapshots known-descendant truth. Caller must hold
// s.mu (it writes lastRejectReason); it performs no substrate calls — the
// s.substrate read below is a pointer comparison only, never a method call.
func replayExpectPrescan(s *Store, pre expectPrescan, pred WaitKind) (satisfied bool, baseline string, ok bool) {
	if s.substrate == nil {
		s.lastRejectReason = fmt.Sprintf("%s %q: no wait substrate wired", pred.Kind, pred.Target)
		return false, "", false
	}
	switch pred.Kind {
	case WaitUntilJob:
		if !pre.jobOK {
			s.lastRejectReason = fmt.Sprintf("unknown job %q: no record in this session tree", pred.Target)
			return false, "", false
		}
		if !pre.live && !pre.retained {
			s.lastRejectReason = fmt.Sprintf("job %q is neither running nor retained-terminal", pred.Target)
			return false, "", false
		}
		return pre.retained, "", true
	case WaitUntilDelegate:
		if !pre.delegateOK {
			s.lastRejectReason = fmt.Sprintf("unknown delegate %q: no record in this session tree", pred.Target)
			return false, "", false
		}
		if !pre.live && !pre.retained {
			s.lastRejectReason = fmt.Sprintf("delegate %q is neither running/settling/stopping nor retained-terminal", pred.Target)
			return false, "", false
		}
		return pre.retained, "", true
	case WaitUntilApproval:
		if strings.TrimSpace(pred.Target) == "" {
			s.lastRejectReason = "approval condition requires a content key target"
			return false, "", false
		}
		return pre.approval, "", true
	case WaitUntilChild:
		if strings.TrimSpace(pred.Target) == "" {
			s.lastRejectReason = "child condition requires a child session id"
			return false, "", false
		}
		return pre.childKnown, "", true
	case WaitUntilEvent:
		switch pred.EventSubtype {
		case EventFileModified:
			if !pre.baselineOK {
				s.lastRejectReason = fmt.Sprintf("unstatable file %q: not inside the session sandbox or missing", pred.Target)
				return false, "", false
			}
			return false, pre.baseline, true
		case EventHTTPMatch:
			s.lastRejectReason = fmt.Sprintf("event subtype %q is not supported: http_match was removed (issue #1061 — follow it for the fetch-based watch type)", pred.EventSubtype)
			return false, "", false
		case EventExternalLabel:
			s.lastRejectReason = fmt.Sprintf("event subtype %q is not supported: external_label was removed (issue #1063 — follow it for the notification-based watch type)", pred.EventSubtype)
			return false, "", false
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
			// Removed (issue #1061): registration rejects, so a persisted
			// pre-removal condition can only read unsatisfied here.
			return false
		case EventExternalLabel:
			// Removed (issue #1063): registration rejects, so a persisted
			// pre-removal condition can only read unsatisfied here.
			return false
		}
	}
	return false
}
