package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/tool"
)

// registerGoalTools registers the update_goal tool into reg, mirroring registerTaskTools.
func registerGoalTools(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefUpdateGoal(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env

			statusStr := fmt.Sprint(args["status"])
			var st goal.Status
			switch statusStr {
			case "complete":
				st = goal.StatusComplete
			case "blocked":
				st = goal.StatusBlocked
			default:
				return nil, fmt.Errorf("update_goal: invalid status %q (must be \"complete\" or \"blocked\")", statusStr)
			}

			// Conditional verification (spec §6): update_goal("complete")
			// verifies iff the goal carries registered conditions. The
			// verifier evaluates the named conditions check-on-claim and
			// rejects with the failing condition named; the goal stays
			// active. Goals without conditions keep the v1 self-declare
			// path. "blocked" never verifies (a stuck claim needs no
			// proof).
			if st == goal.StatusComplete {
				if conds, ok := deps.goalGuard.Conditions(); ok && len(conds) > 0 {
					checks := deps.goalGuard.EvaluateExpectations(conds)
					if _, failing := goal.VerifyConditions(conds, checks); failing != "" {
						return nil, fmt.Errorf("update_goal: condition %q is not satisfied; the goal stays active — satisfy it or keep working", failing)
					}
				}
			}

			snap, changed := deps.goalGuard.SetTerminal(st, "", deps.now())
			if !changed {
				return tool.StateResult{Output: "No goal is active for this session (none was set at launch); nothing recorded — this tool only updates a goal the harness registered."}, nil
			}

			return tool.StateResult{
				Output: "Goal marked " + statusStr + ".",
				State:  goalStateView(snap),
			}, nil
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Definition:  tool.DefGoalWait(),
		PreValidate: validateGoalWaitArgs,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return goalWaitTool(deps, args)
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefGoalCancelWait(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return goalCancelWaitTool(deps, args)
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Definition:  tool.DefGoalExpect(),
		PreValidate: validateGoalExpectArgs,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return goalExpectTool(deps, args)
		},
	})
}

// goalWaitStringArg reads an optional string goal_wait argument. Absent and
// null both read as empty. A present value of another type is rejected rather
// than coerced (mirroring watchStringArg): coercion would silently register a
// wait on something other than what was asked for.
func goalWaitStringArg(args map[string]any, key string) (string, error) {
	raw, present := args[key]
	if !present || raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("invalid_request: %s must be a string", key)
	}
	return text, nil
}

// goalWaitTimeoutArg reads the goal_wait timeout_seconds argument strictly:
// absent or null means "default" (the store applies DefaultWaitTimeout); an
// int or an integral, finite float64 is its value in seconds; anything else
// (a string, 1.5, NaN) is invalid_request naming the field. Providers hand
// numbers over as float64, so silently truncating would hide a model error.
func goalWaitTimeoutArg(args map[string]any) (time.Duration, error) {
	// float64 cannot represent math.MaxInt exactly - it rounds up to 2^63 - so
	// the upper bound is exclusive, which keeps every float64 that reaches the
	// int conversion below in range.
	const aboveMaxInt = float64(math.MaxInt) + 1
	raw, present := args["timeout_seconds"]
	if !present || raw == nil {
		return 0, nil
	}
	var seconds int
	switch v := raw.(type) {
	case int:
		seconds = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v >= aboveMaxInt || v < math.MinInt {
			return 0, errors.New("invalid_request: timeout_seconds must be an integer")
		}
		seconds = int(v)
	default:
		return 0, errors.New("invalid_request: timeout_seconds must be an integer")
	}
	if seconds < 1 || seconds > 86400 {
		return 0, errors.New("invalid_request: timeout_seconds must be between 1 and 86400")
	}
	return time.Duration(seconds) * time.Second, nil
}

// validateGoalWaitArgs rejects a goal_wait shape before the generic JSON
// schema validator renders its diagnostic, so a direct handler caller (tests,
// internal callers) that bypasses registry PreValidate gets the same
// contract. The schema enforces the kind/event_subtype enums and the timeout
// range for registry callers; this re-validates the timeout range (1..86400s)
// plus what the schema cannot - kind-specific required targets - so direct
// handler callers that bypass PreValidate get the same named errors.
func validateGoalWaitArgs(args map[string]any) error {
	kind, err := goalWaitStringArg(args, "kind")
	if err != nil {
		return err
	}
	target, err := goalWaitStringArg(args, "target")
	if err != nil {
		return err
	}
	subtype, err := goalWaitStringArg(args, "event_subtype")
	if err != nil {
		return err
	}
	if kind == "" {
		return errors.New("invalid_request: kind is required")
	}
	switch goal.Kind(kind) {
	case goal.WaitUntilTime, goal.WaitUntilJob, goal.WaitUntilDelegate,
		goal.WaitUntilApproval, goal.WaitUntilEvent, goal.WaitUntilChild:
	default:
		return fmt.Errorf("invalid_request: unknown wait kind %q (must be until_time | until_job | until_delegate | until_approval | until_event | until_child)", kind)
	}
	if kind == string(goal.WaitUntilEvent) && subtype == "" {
		return errors.New("invalid_request: event_subtype is required for kind \"until_event\"")
	}
	// M2: file_modified/http_match need their target at tool level (a file
	// path / URL); only external_label fires via notification/expiry with no
	// durable target to name, and only until_time is target-free by kind.
	if kind != string(goal.WaitUntilTime) && (kind != string(goal.WaitUntilEvent) || subtype != string(goal.EventExternalLabel)) && strings.TrimSpace(target) == "" {
		return fmt.Errorf("invalid_request: target is required for kind %q", kind)
	}
	if _, err := goalWaitTimeoutArg(args); err != nil {
		return err
	}
	matcher, err := goalWaitStringArg(args, "matcher")
	if err != nil {
		return err
	}
	label, err := goalWaitStringArg(args, "label")
	if err != nil {
		return err
	}
	// Model-controlled field caps (spec section 2: matcher <=1KB, URL <=2KB,
	// label <=256 chars printable). The store enforces the same caps
	// fail-closed; naming them here keeps the tool error specific even for
	// callers that bypass schema validation.
	if len(matcher) > goal.MaxMatcherBytes {
		return fmt.Errorf("invalid_request: matcher exceeds %d bytes (cap 1KB)", goal.MaxMatcherBytes)
	}
	if len(target) > goal.MaxURLBytes {
		return fmt.Errorf("invalid_request: target exceeds %d bytes (cap 2KB)", goal.MaxURLBytes)
	}
	if utf8.RuneCountInString(label) > goal.MaxLabelRunes {
		return fmt.Errorf("invalid_request: label exceeds %d chars", goal.MaxLabelRunes)
	}
	for _, r := range label {
		if !unicode.IsPrint(r) {
			return errors.New("invalid_request: label must be printable text")
		}
	}
	return nil
}

// decodeGoalWaitArgs converts a goal_wait call into a store WaitKind. String
// fields reject non-strings (never fmt.Sprint coercion: a missing target must
// stay missing so the required-target check fires, not register the literal
// "<nil>"). Timeout converts seconds to a Duration; absent means the store
// default.
func decodeGoalWaitArgs(args map[string]any) (goal.WaitKind, error) {
	kind, err := goalWaitStringArg(args, "kind")
	if err != nil {
		return goal.WaitKind{}, err
	}
	target, err := goalWaitStringArg(args, "target")
	if err != nil {
		return goal.WaitKind{}, err
	}
	subtype, err := goalWaitStringArg(args, "event_subtype")
	if err != nil {
		return goal.WaitKind{}, err
	}
	matcher, err := goalWaitStringArg(args, "matcher")
	if err != nil {
		return goal.WaitKind{}, err
	}
	generation, err := goalWaitStringArg(args, "ask_generation")
	if err != nil {
		return goal.WaitKind{}, err
	}
	label, err := goalWaitStringArg(args, "label")
	if err != nil {
		return goal.WaitKind{}, err
	}
	timeout, err := goalWaitTimeoutArg(args)
	if err != nil {
		return goal.WaitKind{}, err
	}
	if err := validateGoalWaitArgs(args); err != nil {
		return goal.WaitKind{}, err
	}
	return goal.WaitKind{
		Kind:          goal.Kind(kind),
		Target:        target,
		Timeout:       timeout,
		Label:         label,
		Matcher:       matcher,
		EventSubtype:  goal.EventSubtype(subtype),
		AskGeneration: generation,
	}, nil
}

// checkGoalWaitRegistrable names the goal-lifecycle states that reject a
// registration before the store call: no goal set, or a terminal goal. The
// store itself returns false without a reason on these paths, so the tool
// names them here (spec section 7: registration validation errors name the
// failed check).
func checkGoalWaitRegistrable(deps *toolDeps) error {
	snap, ok := deps.goalGuard.Snapshot()
	if !ok {
		return errors.New("goal_wait: no active goal is set for this session; set one with /goal before registering waits")
	}
	if snap.Status == goal.StatusComplete || snap.Status == goal.StatusBlocked {
		return fmt.Errorf("goal_wait: goal is %q; waits register on active goals only", string(snap.Status))
	}
	return nil
}

// describeGoalWaitState names the current goal state for a reason-less store
// rejection (a concurrent terminal transition won between the pre-check and
// the register).
func describeGoalWaitState(deps *toolDeps) string {
	snap, ok := deps.goalGuard.Snapshot()
	if !ok {
		return "no active goal is set for this session"
	}
	return fmt.Sprintf("goal is %q", string(snap.Status))
}

// goalWaitTool executes the goal_wait tool: validate, register, park. All
// waits are model-declared through this tool (no harness-auto registration
// path exists in this slice); store validation rejects hallucinated targets
// with the reason named — never parked. On success the coalesced wait timer
// re-arms to the new lease.
func goalWaitTool(deps *toolDeps, args map[string]any) (any, error) {
	req, err := decodeGoalWaitArgs(args)
	if err != nil {
		return nil, err
	}
	// Spec section 8: the parent→child forward lands in slice 2, so the
	// slice-1 scoped-out rejection is lifted — until_child registers on any
	// session with a known-descendant target (fail-closed otherwise). A child
	// waiting on its own sibling resolves through the shared controller's
	// tracked set; cross-tree targets still reject at the store.
	if err := checkGoalWaitRegistrable(deps); err != nil {
		return nil, err
	}
	wait, ok := deps.goalGuard.RegisterWait(req, deps.now())
	if !ok {
		reason := deps.goalGuard.RejectReason()
		if reason == "" {
			// The store names substrate/validation failures; a concurrent
			// terminal transition between the pre-check and the register
			// leaves it silent - name the state instead of erroring blank.
			reason = describeGoalWaitState(deps)
		}
		return nil, fmt.Errorf("goal_wait: %s", reason)
	}
	snap, _ := deps.goalGuard.Snapshot()
	// Retained-terminal catch-up (spec section 2): the target was already
	// terminal inside the retention window, so no live lease remains - the
	// terminal outcome fires immediately as a pending wake instead of
	// parking. Detect it via the fresh snapshot rather than the returned
	// lease: the wait_id names a pending wake, not a live wait.
	if full, ok := deps.goalGuard.Store().GoalSnapshot(); ok {
		for _, p := range full.PendingWake {
			if p.WaitID == wait.Lease.WaitID {
				return tool.StateResult{
					Output: "Wait " + wait.Lease.WaitID + " fired immediately: the target was already terminal (" + p.Trigger + ").",
					State:  goalStateView(snap),
				}, nil
			}
		}
	}
	return tool.StateResult{
		Output: "Waiting on " + wait.Lease.WaitID + " (" + wait.Lease.Label + ").",
		State:  goalStateView(snap),
	}, nil
}

// goalCancelWaitTool executes the goal_cancel_wait tool: remove one live
// lease by wait_id and disarm its timer (spec section 7). Cancel removes the
// live lease only; an already-claimed pendingWake entry still drives once
// with the cancellation noted - cancel never silently swallows a consumed
// fire. A cancel naming no live lease reports the miss without failing the
// tool call.
func goalCancelWaitTool(deps *toolDeps, args map[string]any) (any, error) {
	raw, present := args["wait_id"]
	if !present || raw == nil {
		return nil, errors.New("invalid_request: wait_id is required")
	}
	waitID, ok := raw.(string)
	if !ok {
		return nil, errors.New("invalid_request: wait_id must be a string")
	}
	if strings.TrimSpace(waitID) == "" {
		return nil, errors.New("invalid_request: wait_id is required")
	}
	if !deps.goalGuard.CancelWait(waitID, deps.now()) {
		return tool.StateResult{
			Output: "No live wait " + waitID + ": nothing cancelled (an already-fired wait still drives its wake turn once).",
			State:  goalStateViewOf(deps),
		}, nil
	}
	return tool.StateResult{
		Output: "Cancelled wait " + waitID + ".",
		State:  goalStateViewOf(deps),
	}, nil
}

// goalStateViewOf renders the current goal snapshot, or the no-active-goal
// shape when no goal is set (cancel-after-clear reaches here).
func goalStateViewOf(deps *toolDeps) map[string]any {
	if snap, ok := deps.goalGuard.Snapshot(); ok {
		return goalStateView(snap)
	}
	return map[string]any{"status": "none"}
}

// goalStateView converts a goal snapshot into a small serializable map for the
// tool_state side-channel on TOOL_CALL_END events.
func goalStateView(snap goal.Snapshot) map[string]any {
	return map[string]any{
		"objective":  snap.Objective,
		"status":     string(snap.Status),
		"iterations": snap.Iterations,
		"stopReason": snap.StopReason,
	}
}

// validateGoalExpectArgs rejects a goal_expect shape before the generic JSON
// schema validator renders its diagnostic, so direct handler callers get the
// same contract. desc is required; kind defaults to a file check when empty
// (the minimal v1 condition-query shape); the timeout range mirrors
// goal_wait.
func validateGoalExpectArgs(args map[string]any) error {
	desc, err := goalWaitStringArg(args, "desc")
	if err != nil {
		return err
	}
	if strings.TrimSpace(desc) == "" {
		return errors.New("invalid_request: desc is required")
	}
	kind, err := goalWaitStringArg(args, "kind")
	if err != nil {
		return err
	}
	if kind != "" {
		switch goal.Kind(kind) {
		case goal.WaitUntilJob, goal.WaitUntilDelegate, goal.WaitUntilApproval, goal.WaitUntilEvent, goal.WaitUntilChild:
		default:
			return fmt.Errorf("invalid_request: unknown condition kind %q (must be until_job | until_delegate | until_event)", kind)
		}
		// v1 restriction (fix-1/4 I1): registrable goal_expect kinds are
		// file/job/delegate only. Approval/child reject here with a named
		// reason; http/external-label reject on their subtype below.
		switch goal.Kind(kind) {
		case goal.WaitUntilApproval:
			return fmt.Errorf("invalid_request: condition kind %q is not verifiable in v1: approval-answer records are out of scope (register a file, job, or delegate condition)", kind)
		case goal.WaitUntilChild:
			return fmt.Errorf("invalid_request: condition kind %q is not verifiable in v1: child-terminal queries are out of scope (register a file, job, or delegate condition)", kind)
		}
	}
	target, err := goalWaitStringArg(args, "target")
	if err != nil {
		return err
	}
	subtype, err := goalWaitStringArg(args, "event_subtype")
	if err != nil {
		return err
	}
	effKind := kind
	if effKind == "" {
		effKind = string(goal.WaitUntilEvent)
	}
	effSubtype := subtype
	if goal.Kind(effKind) == goal.WaitUntilEvent && effSubtype == "" {
		effSubtype = string(goal.EventFileModified)
	}
	// v1 restriction (fix-1/4 I1): only file_modified is a registrable
	// until_event subtype; http_match and external_label reject named.
	if goal.Kind(effKind) == goal.WaitUntilEvent {
		switch goal.EventSubtype(effSubtype) {
		case goal.EventHTTPMatch:
			return fmt.Errorf("invalid_request: condition subtype %q is not verifiable in v1: http fetch is out of scope (register a file, job, or delegate condition)", effSubtype)
		case goal.EventExternalLabel:
			return fmt.Errorf("invalid_request: condition subtype %q is not verifiable in v1: external labels carry no queryable state (register a file, job, or delegate condition)", effSubtype)
		}
	}
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("invalid_request: target is required for condition kind %q", effKind)
	}
	if _, err := goalWaitTimeoutArg(args); err != nil {
		return err
	}
	matcher, err := goalWaitStringArg(args, "matcher")
	if err != nil {
		return err
	}
	generation, err := goalWaitStringArg(args, "ask_generation")
	if err != nil {
		return err
	}
	_ = generation
	if len(matcher) > goal.MaxMatcherBytes {
		return fmt.Errorf("invalid_request: matcher exceeds %d bytes (cap 1KB)", goal.MaxMatcherBytes)
	}
	if len(target) > goal.MaxURLBytes {
		return fmt.Errorf("invalid_request: target exceeds %d bytes (cap 2KB)", goal.MaxURLBytes)
	}
	if utf8.RuneCountInString(desc) > goal.MaxLabelRunes {
		return fmt.Errorf("invalid_request: desc exceeds %d chars", goal.MaxLabelRunes)
	}
	for _, r := range desc {
		if !unicode.IsPrint(r) {
			return errors.New("invalid_request: desc must be printable text")
		}
	}
	return nil
}

// decodeGoalExpectArgs converts a goal_expect call into an ExpectRequest.
// Empty kind defaults to a file_modified check on target (the minimal v1
// condition-query shape: desc + file path).
func decodeGoalExpectArgs(args map[string]any) (goal.ExpectRequest, error) {
	if err := validateGoalExpectArgs(args); err != nil {
		return goal.ExpectRequest{}, err
	}
	desc, _ := goalWaitStringArg(args, "desc")
	kind, _ := goalWaitStringArg(args, "kind")
	target, _ := goalWaitStringArg(args, "target")
	subtype, _ := goalWaitStringArg(args, "event_subtype")
	matcher, _ := goalWaitStringArg(args, "matcher")
	generation, _ := goalWaitStringArg(args, "ask_generation")
	label, _ := goalWaitStringArg(args, "label")
	timeout, _ := goalWaitTimeoutArg(args)
	_ = label
	if kind == "" {
		kind = string(goal.WaitUntilEvent)
	}
	if goal.Kind(kind) == goal.WaitUntilEvent && subtype == "" {
		subtype = string(goal.EventFileModified)
	}
	return goal.ExpectRequest{
		Desc: desc,
		Predicate: goal.WaitKind{
			Kind:          goal.Kind(kind),
			Target:        target,
			Timeout:       timeout,
			Matcher:       matcher,
			EventSubtype:  goal.EventSubtype(subtype),
			AskGeneration: generation,
		},
	}, nil
}

// goalExpectTool executes the goal_expect tool: validate (identical §2
// registration validation + attach-scan snapshot at registration —
// hallucinated conditions rejected immediately with the reason named) and
// register the stop-claim condition. Registration never feeds the ledger
// (check-on-claim only).
func goalExpectTool(deps *toolDeps, args map[string]any) (any, error) {
	req, err := decodeGoalExpectArgs(args)
	if err != nil {
		return nil, err
	}
	if snap, ok := deps.goalGuard.Snapshot(); !ok {
		return nil, errors.New("goal_expect: no active goal is set for this session; set one with /goal before registering conditions")
	} else if snap.Status == goal.StatusComplete || snap.Status == goal.StatusBlocked {
		return nil, fmt.Errorf("goal_expect: goal is %q; conditions register on active goals only", string(snap.Status))
	}
	cond, ok := deps.goalGuard.RegisterExpect(req, deps.now())
	if !ok {
		reason := deps.goalGuard.RejectReason()
		if reason == "" {
			reason = describeGoalWaitState(deps)
		}
		return nil, fmt.Errorf("goal_expect: %s", reason)
	}
	snap, _ := deps.goalGuard.Snapshot()
	return tool.StateResult{
		Output: "Condition " + cond.Desc + " registered; update_goal(\"complete\") will verify it.",
		State:  goalStateView(snap),
	}, nil
}
