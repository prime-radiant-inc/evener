package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/task"
)

// TestCovDeliverHookContext_Empty covers the empty-text guard in
// deliverHookContext (session_queue.go lines 286-288).
func TestCovDeliverHookContext_Empty(t *testing.T) {
	s := &Session{}
	s.deliverHookContext("")
	s.deliverHookContext("  ")
	s.deliverHookContext("\t\n")
	if len(s.steeringQueue) != 0 {
		t.Fatalf("empty hook context queued steering: %+v", s.steeringQueue)
	}
}

// TestCovDeliverHookUserMessage_Empty covers the empty-text guard in
// deliverHookUserMessage (session_queue.go lines 296-298).
func TestCovDeliverHookUserMessage_Empty(t *testing.T) {
	s := &Session{id: "session_1", events: make(chan events.SessionEvent, 4)}
	s.deliverHookUserMessage("")
	s.deliverHookUserMessage("  ")
	if got := len(s.events); got != 0 {
		t.Fatalf("empty hook user messages emitted %d warnings, want 0", got)
	}

	// Prove the sink is live so the zero count above observes the guard rather
	// than an inert fixture.
	s.deliverHookUserMessage("visible hook warning")
	if got := len(s.events); got != 1 {
		t.Fatalf("non-empty hook user message emitted %d warnings, want 1", got)
	}
	event := <-s.events
	warning, ok := event.Data.(events.WarningData)
	if !ok || event.Kind != events.EventWarning || event.SessionID != "session_1" || warning.Source != "hook" || warning.Message != "visible hook warning" {
		t.Fatalf("hook warning event = %#v with data %#v", event, event.Data)
	}
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

	s.mu.Lock()
	s.state = SessionClosed
	s.mu.Unlock()
	s.FollowUp("ignored after close")
	s.mu.Lock()
	if len(s.followups) != 1 || s.followups[0] != "do something" {
		t.Fatalf("closed session changed followups: %v", s.followups)
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
	if len(s.steeringQueue) != 0 || len(s.inputQueue) != 0 {
		t.Fatalf("empty user steering changed queues: steering=%+v input=%+v", s.steeringQueue, s.inputQueue)
	}
}

// TestCovEnqueueWithImages_ClosedContext covers the context-cancelled path
// (session_queue.go line 359).
func TestCovEnqueueWithImages_ClosedContext(t *testing.T) {
	s := &Session{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.EnqueueWithImages(ctx, "hello", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v, want %v", err, context.Canceled)
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
	if !errors.Is(got, err) {
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
	doctor, ok := agents["doctor"]
	if !ok || len(doctor.Tools) == 0 || len(doctor.Skills) == 0 {
		t.Fatalf("builtin doctor lacks mutable tools/skills fixture: %+v", doctor)
	}
	explorer, ok := agents["explorer"]
	if !ok || len(explorer.Tasks) == 0 {
		t.Fatalf("builtin explorer lacks mutable tasks fixture: %+v", explorer)
	}
	wantDoctorTool := doctor.Tools[0]
	wantDoctorSkill := doctor.Skills[0]
	wantExplorerTask := explorer.Tasks[0]
	doctor.Tools[0] = "mutated-api-tool"
	doctor.Skills[0] = "mutated-api-skill"
	explorer.Tasks[0].Title = "mutated-api-task"
	agents["doctor"] = doctor
	agents["explorer"] = explorer

	// Mutating the returned map and nested slices must not change the cached
	// values returned by the API on its next call.
	var cachedName string
	for name := range agents {
		cachedName = name
		break
	}
	delete(agents, cachedName)
	agents2, err := builtinAgents()
	if err != nil {
		t.Fatalf("second builtinAgents() error: %v", err)
	}
	if _, ok := agents2[cachedName]; !ok {
		t.Fatalf("deleting %q from one result mutated the builtin cache", cachedName)
	}
	doctor2 := agents2["doctor"]
	explorer2 := agents2["explorer"]
	if doctor2.Tools[0] != wantDoctorTool || doctor2.Skills[0] != wantDoctorSkill || explorer2.Tasks[0] != wantExplorerTask {
		t.Fatalf("builtinAgents returned cache-aliased nested values: doctor=%+v explorer task=%+v", doctor2, explorer2.Tasks[0])
	}

	// cloneBuiltinAgents must also copy every mutable slice in an Agent value.
	source := map[string]plugin.Agent{
		"worker": {
			Name:   "worker",
			Tools:  []string{"read_file", "exec_command"},
			Skills: []string{"review", "testing"},
			Tasks:  []task.TaskTemplate{{Title: "inspect", Prompt: "inspect code"}, {Title: "verify", Prompt: "run tests"}},
		},
	}
	cloned := cloneBuiltinAgents(source)
	worker := cloned["worker"]
	worker.Tools[0] = "mutated-tool"
	worker.Skills[0] = "mutated-skill"
	worker.Tasks[0].Title = "mutated-task"
	cloned["worker"] = worker
	delete(cloned, "worker")
	original := source["worker"]
	if original.Tools[0] != "read_file" || original.Skills[0] != "review" || original.Tasks[0].Title != "inspect" {
		t.Fatalf("clone shares mutable agent storage with source: source=%+v", original)
	}
	if _, ok := source["worker"]; !ok {
		t.Fatal("deleting from clone changed source map")
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
	if finish.outcome != delegatestore.OutcomeFailed || finish.disposition != delegatestore.DispositionTerminalError || finish.reason != "failed" {
		t.Fatalf("unreported finish = outcome:%q disposition:%q reason:%q", finish.outcome, finish.disposition, finish.reason)
	}
	if finish.packet == nil || finish.packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("unreported packet = %+v, want terminal error", finish.packet)
	}
	var message string
	if err := json.Unmarshal(finish.packet.Message, &message); err != nil || message != "result" {
		t.Fatalf("unreported packet message = %q, err=%v, want result", message, err)
	}

	reported := stableDelegateFinish(&Session{comm: communicateResult{called: true}}, "reported result", nil)
	if reported.outcome != delegatestore.OutcomeCompleted || reported.disposition != delegatestore.DispositionReported || reported.reason != "" {
		t.Fatalf("reported finish = outcome:%q disposition:%q reason:%q", reported.outcome, reported.disposition, reported.reason)
	}
	if reported.packet == nil || reported.packet.Kind != delegatestore.PacketReported {
		t.Fatalf("reported packet = %+v, want reported", reported.packet)
	}
	if err := json.Unmarshal(reported.packet.Message, &message); err != nil || message != "reported result" {
		t.Fatalf("reported packet message = %q, err=%v, want reported result", message, err)
	}
}

// TestCovStableDelegateFinish_WithError covers stableDelegateFinish with error
func TestCovStableDelegateFinish_WithError(t *testing.T) {
	finish := stableDelegateFinish(&Session{comm: communicateResult{called: true}}, "", context.DeadlineExceeded)
	if finish.outcome != delegatestore.OutcomeFailed || finish.disposition != delegatestore.DispositionTerminalError || finish.reason != "failed" {
		t.Fatalf("error finish = outcome:%q disposition:%q reason:%q", finish.outcome, finish.disposition, finish.reason)
	}
	if finish.packet == nil || finish.packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("error packet = %+v, want terminal error", finish.packet)
	}
	var message string
	if err := json.Unmarshal(finish.packet.Message, &message); err != nil || message != context.DeadlineExceeded.Error() {
		t.Fatalf("error packet message = %q, err=%v, want %q", message, err, context.DeadlineExceeded)
	}
}
