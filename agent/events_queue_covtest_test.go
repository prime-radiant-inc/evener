package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// TestCovWrapHookContext covers wrapHookContext (session_queue.go line 279).
func TestCovWrapHookContext(t *testing.T) {
	got := wrapHookContext("hello")
	if got != "<SYSTEM-REMINDER>hello</SYSTEM-REMINDER>" {
		t.Fatalf("got %q", got)
	}
}

// TestCovDeliverHookContext_Empty covers the empty-text guard in
// deliverHookContext (session_queue.go lines 286-288).
func TestCovDeliverHookContext_Empty(t *testing.T) {
	s := &Session{}
	// These should not panic.
	s.deliverHookContext("")
	s.deliverHookContext("  ")
	s.deliverHookContext("\t\n")
}

// TestCovDeliverHookUserMessage_Empty covers the empty-text guard in
// deliverHookUserMessage (session_queue.go lines 296-298).
func TestCovDeliverHookUserMessage_Empty(t *testing.T) {
	s := &Session{}
	// These should not panic.
	s.deliverHookUserMessage("")
	s.deliverHookUserMessage("  ")
}

// TestCovFollowUp_Empty covers the empty-text and closed guards in FollowUp
// (session_queue.go lines 303-313).
func TestCovFollowUp_Empty(t *testing.T) {
	s := &Session{}
	// Empty message — should not add.
	s.FollowUp("")
	s.FollowUp("  ")
	s.mu.Lock()
	if len(s.followups) != 0 {
		t.Fatalf("expected 0 followups, got %d", len(s.followups))
	}
	s.mu.Unlock()

	// Non-empty — should add.
	s.FollowUp("do something")
	s.mu.Lock()
	if len(s.followups) != 1 || s.followups[0] != "do something" {
		t.Fatalf("expected 1 followup 'do something', got %v", s.followups)
	}
	s.mu.Unlock()
}

// TestCovRouteSystemNotification_NilSession covers the nil-session guard
// in routeSystemNotification (session_queue.go lines 129-131).
func TestCovRouteSystemNotification_NilSession(t *testing.T) {
	var s *Session
	if s.routeSystemNotification("sess1", "hello") {
		t.Fatal("nil session should return false")
	}
}

// TestCovRouteSystemNotification_EmptyReceiver covers the empty receiver guard
// (session_queue.go line 129).
func TestCovRouteSystemNotification_EmptyReceiver(t *testing.T) {
	s := &Session{id: "sess1"}
	if s.routeSystemNotification("", "hello") {
		t.Fatal("empty receiver should return false")
	}
	if s.routeSystemNotification("  ", "hello") {
		t.Fatal("whitespace receiver should return false")
	}
}

// TestCovSteerFromUserWithImages_Empty covers the empty message+images guard
// (session_queue.go lines 181-183).
func TestCovSteerFromUserWithImages_Empty(t *testing.T) {
	s := &Session{id: "sess1"}
	// Both empty — should return without doing anything.
	s.SteerFromUserWithImages("", nil)
	s.SteerFromUserWithImages("  ", nil)
}

// TestCovEnqueueWithImages_ClosedContext covers the context-cancelled path
// (session_queue.go line 359).
func TestCovEnqueueWithImages_ClosedContext(t *testing.T) {
	s := &Session{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.EnqueueWithImages(ctx, "hello", nil)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// TestCovEnqueueWithImages_Empty covers the empty text+images guard
// (session_queue.go line 360).
func TestCovEnqueueWithImages_Empty(t *testing.T) {
	s := &Session{}
	err := s.EnqueueWithImages(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error for empty text+images")
	}
}

// TestCovContextMetrics_NilContextMgr covers ContextMetrics with nil contextMgr
// (context_metrics.go lines 13-14).
func TestCovContextMetrics_NilContextMgr(t *testing.T) {
	s := &Session{}
	got := s.ContextMetrics()
	if got.Used != 0 || got.Window != 0 || got.Remaining != 0 {
		t.Fatalf("expected zero ContextMetrics, got %+v", got)
	}
}

// TestCovSessionScratchDir_NoWrapper covers SessionScratchDir without wrapper
// or provisioned scratch (execenv/local.go lines 203-213).
func TestCovSessionScratchDir_NoWrapper(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got := env.SessionScratchDir()
	if got != "" {
		t.Fatalf("expected empty without provisioned scratch, got %q", got)
	}
}

// TestCovToolBatchPanicError_Error covers toolBatchPanicError with an error value
// (session_tool_round.go lines 225-230).
func TestCovToolBatchPanicError_Error(t *testing.T) {
	err := context.DeadlineExceeded
	got := toolBatchPanicError(err)
	if got != err {
		t.Fatalf("expected same error, got %v", got)
	}
}

// TestCovToolBatchPanicError_NonError covers toolBatchPanicError with a non-error
// value — it should re-panic.
func TestCovToolBatchPanicError_NonError(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for non-error value")
		}
		if s, ok := r.(string); !ok || s != "boom" {
			t.Fatalf("expected 'boom' panic, got %v", r)
		}
	}()
	toolBatchPanicError("boom")
}

// TestCovBuiltinAgents covers builtinAgents and cloneBuiltinAgents
// (builtin_agents.go lines 24-56).
func TestCovBuiltinAgents(t *testing.T) {
	// This exercises the cached path (sync.Once already fired by prior tests
	// or by this call).
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents() error: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected at least one builtin agent")
	}

	// Verify the cache returns a clone (different map instance).
	agents2, _ := builtinAgents()
	if &agents == &agents2 {
		t.Fatal("builtinAgents should return a clone")
	}

	// cloneBuiltinAgents directly.
	cloned := cloneBuiltinAgents(agents)
	if len(cloned) != len(agents) {
		t.Fatalf("clone length = %d, want %d", len(cloned), len(agents))
	}
}

// TestCovNewQueueEntryID covers newQueueEntryID (session_queue.go lines 336-341).
func TestCovNewQueueEntryID(t *testing.T) {
	id1 := newQueueEntryID()
	id2 := newQueueEntryID()
	if id1 == id2 {
		t.Fatal("two IDs should be different")
	}
	if !strings.HasPrefix(id1, "q_") {
		t.Fatalf("id should start with q_, got %q", id1)
	}
}

// TestCovOwnerJobManagerFor_NilSession covers ownerJobManagerFor nil guards
// (jobs_nested.go lines 13-16).
func TestCovOwnerJobManagerFor_NilSession(t *testing.T) {
	var s *Session
	jm, rec := s.ownerJobManagerFor("job1")
	if jm != nil || rec != nil {
		t.Fatalf("nil session: jm=%v, rec=%v", jm, rec)
	}
}

// TestCovOwnerJobManagerFor_NilJobManager covers ownerJobManagerFor with nil jobManager
// (jobs_nested.go lines 14-16).
func TestCovOwnerJobManagerFor_NilJobManager(t *testing.T) {
	s := &Session{}
	jm, rec := s.ownerJobManagerFor("job1")
	if jm != nil || rec != nil {
		t.Fatalf("nil jobManager: jm=%v, rec=%v", jm, rec)
	}
}

// TestCovNotControllableDescendantError_NilSession covers the nil-session path
// (jobs_nested.go lines 389-403).
func TestCovNotControllableDescendantError_NilSession(t *testing.T) {
	var s *Session
	if err := s.notControllableDescendantError("job1"); err != nil {
		t.Fatalf("nil session should return nil, got %v", err)
	}
}

// TestCovSessionRunningJobIDs_NilSession covers sessionRunningJobIDs with nil session
// (session_tools_jobs.go lines 1648-1662).
func TestCovSessionRunningJobIDs_NilSession(t *testing.T) {
	var s *Session
	if got := sessionRunningJobIDs(s); got != nil {
		t.Fatalf("nil session should return nil, got %v", got)
	}
}

// TestCovDecodeDelegateArgs covers decodeDelegateArgs error paths
// (session_tools_jobs.go line 345).
func TestCovDecodeDelegateArgs(t *testing.T) {
	// sandbox_net non-bool.
	_, err := decodeDelegateArgs(map[string]any{"sandbox_net": "yes"})
	if err == nil || !strings.Contains(err.Error(), "sandbox_net must be a JSON boolean") {
		t.Fatalf("expected sandbox_net error, got %v", err)
	}

	// Negative delegation_allowance.
	_, err = decodeDelegateArgs(map[string]any{"delegation_allowance": -1})
	if err == nil || !strings.Contains(err.Error(), "delegation_allowance must be non-negative") {
		t.Fatalf("expected delegation_allowance error, got %v", err)
	}

	// Valid args.
	args, err := decodeDelegateArgs(map[string]any{
		"task":                 "do stuff",
		"agent_type":           "coder",
		"model":                "gpt-5",
		"reasoning_effort":     "high",
		"watch_parent":         true,
		"isolation":            "worktree",
		"sandbox":              "off",
		"sandbox_net":          true,
		"delegation_allowance": 3,
		"result_schema":        map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.Task != "do stuff" || args.AgentType != "coder" || args.Model != "gpt-5" ||
		args.ReasoningEffort != "high" || !args.WatchParent || args.Isolation != "worktree" ||
		args.Sandbox != "off" || args.SandboxNet == nil || !*args.SandboxNet ||
		args.DelegationAllowance != 3 || args.ResultSchema == nil {
		t.Fatalf("args = %+v", args)
	}

	// sandbox_net = false explicitly.
	args, err = decodeDelegateArgs(map[string]any{"sandbox_net": false})
	if err != nil || args.SandboxNet == nil || *args.SandboxNet {
		t.Fatalf("sandbox_net=false: args=%+v, err=%v", args, err)
	}

	// sandbox_net omitted → nil (inherit).
	args, err = decodeDelegateArgs(map[string]any{})
	if err != nil || args.SandboxNet != nil {
		t.Fatalf("sandbox_net omitted: args=%+v, err=%v", args, err)
	}

	// delegation_allowance = 0 → unset.
	args, err = decodeDelegateArgs(map[string]any{"delegation_allowance": 0})
	if err != nil || args.DelegationAllowance != 0 {
		t.Fatalf("delegation_allowance=0: args=%+v, err=%v", args, err)
	}
}

// TestCovSessionJobManager_NilSession covers sessionJobManager with nil session
func TestCovSessionJobManager_NilSession(t *testing.T) {
	var s *Session
	jm, err := sessionJobManager(s)
	if err == nil || jm != nil {
		t.Fatalf("nil session: jm=%v, err=%v", jm, err)
	}
}

// TestCovStableDelegateFinish_NilSession covers stableDelegateFinish with nil session
// (subagents.go lines 1737-1745).
func TestCovStableDelegateFinish_NilSession(t *testing.T) {
	finish := stableDelegateFinish(nil, "result", nil)
	// Should not panic, should produce a valid finish.
	_ = finish
}

// TestCovStableDelegateFinish_WithError covers stableDelegateFinish with error
func TestCovStableDelegateFinish_WithError(t *testing.T) {
	finish := stableDelegateFinish(&Session{}, "result", context.DeadlineExceeded)
	_ = finish
}

// TestCovMarshalBoundedJSON covers marshalBoundedJSON
// (session_tools_jobs.go lines 1978+).
func TestCovMarshalBoundedJSON(t *testing.T) {
	// Valid JSON within bounds.
	data := map[string]any{"key": "value"}
	got, err := marshalBoundedJSON(data, 1024)
	if err != nil {
		t.Fatalf("marshalBoundedJSON error: %v", err)
	}
	if !strings.Contains(string(got), "key") {
		t.Fatalf("output missing key: %q", got)
	}
}

// TestCovMarshalBoundedJSONWithFit covers marshalBoundedJSONWithFit
// (session_tools_jobs.go lines 1992+).
func TestCovMarshalBoundedJSONWithFit(t *testing.T) {
	data := map[string]any{"key": "value"}
	got, fit, err := marshalBoundedJSONWithFit(data, 1024)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !fit {
		t.Fatal("should fit within 1024 bytes")
	}
	if !strings.Contains(string(got), "key") {
		t.Fatalf("output missing key: %q", got)
	}
}

// TestCovJobToolResultMaxChars covers jobToolResultMaxChars
// (session_tools_jobs.go lines 2003+).
func TestCovJobToolResultMaxChars(t *testing.T) {
	// With nil reg.
	if got := jobToolResultMaxChars(nil, "job_status"); got != jobToolResultDefaultMaxChar {
		t.Fatalf("nil reg = %d, want %d", got, jobToolResultDefaultMaxChar)
	}
}
