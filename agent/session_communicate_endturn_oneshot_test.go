package agent

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// endTurnWarningWithRunningJob starts a background shell on s, ends the turn
// through the REAL tool registry, and returns the warning the model would
// actually see plus the job id.
//
// It deliberately drives s.reg.ExecuteCall rather than calling
// runningJobsEndTurnWarning: a session flag that never reaches the communicate
// handler produces a correct-looking helper and a wrong warning, which is the
// class of defect this test exists to catch.
func endTurnWarningWithRunningJob(t *testing.T, s *Session) (warning, jobID string) {
	t.Helper()

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","mode":"background"}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" || shellOut.Status != "running" {
		t.Fatalf("shell output = %+v, want a running job", shellOut)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	res := s.reg.ExecuteCall(context.Background(), s.env, communicateCallArgs("end-turn-warning", map[string]any{
		"message":  "done for now",
		"end_turn": true,
	}))
	if res.IsError {
		t.Fatalf("communicate error: %s", res.Output)
	}
	var resp map[string]any
	if err := json.Unmarshal(toolResultJSON(res), &resp); err != nil {
		t.Fatalf("unmarshal communicate output: %v", err)
	}
	got, ok := resp["warning"].(string)
	if !ok || got == "" {
		t.Fatalf("expected a non-empty warning naming the running job, got: %v", resp)
	}
	if !strings.Contains(got, shellOut.JobID) {
		t.Fatalf("warning = %q, want it to name job id %q", got, shellOut.JobID)
	}
	return got, shellOut.JobID
}

// TestCommunicate_EndTurnWarningIsHonestInOneShot pins the correction to the
// warn-first text for a session whose process exits with the turn. The old
// wording promised "each job remains notification-armed and will report
// separately on completion" unconditionally; under `evener run` there is no
// separately, and a job still running when the drain gives up is killed rather
// than reported on. Telling the model otherwise is what led it to end the turn
// on a live server in Terminal-Bench trial hf-model-inference (#297).
func TestCommunicate_EndTurnWarningIsHonestInOneShot(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))

	warning, _ := endTurnWarningWithRunningJob(t, s)

	if strings.Contains(warning, "report separately on completion") {
		t.Fatalf("one-shot warning = %q, want it NOT to promise separate reporting: this process exits with the turn", warning)
	}
	if !strings.Contains(warning, "killed at exit") {
		t.Fatalf("one-shot warning = %q, want it to say a job still running is killed at exit", warning)
	}
}

// TestCommunicate_EndTurnWarningKeepsTheServeContract pins the other side: a
// session that outlives its turn genuinely does report background jobs later,
// so docs/job-control.md's notification contract is correct there and the
// warning must keep saying so.
func TestCommunicate_EndTurnWarningKeepsTheServeContract(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))

	warning, _ := endTurnWarningWithRunningJob(t, s)

	if !strings.Contains(warning, "report separately on completion") {
		t.Fatalf("serve warning = %q, want today's notification contract preserved", warning)
	}
	if strings.Contains(warning, "killed at exit") {
		t.Fatalf("serve warning = %q, want no claim that the job dies: the session outlives the turn", warning)
	}
}

// TestCommunicate_EndTurnWarnsForLiveDetachedProcess drives mode:"detached"
// through the real shell and communicate tool handlers. A detached process is
// still owned by this session until it exits, so ending a one-shot turn must
// surface the same warning as a managed background job.
func TestCommunicate_EndTurnWarnsForLiveDetachedProcess(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	if reporter, ok := s.env.(interface{ DetachSupported() bool }); !ok || !reporter.DetachSupported() {
		t.Skip("detached execution is unsupported in this environment")
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "detached-shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"exec sleep 30","mode":"detached"}`),
	})
	if res.IsError {
		t.Fatalf("detached shell returned error: %s", res.Output)
	}
	var shellOut struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &shellOut); err != nil {
		t.Fatalf("unmarshal detached shell output: %v (output: %s)", err, res.Output)
	}
	if shellOut.PID <= 0 {
		t.Fatalf("detached shell output = %s, want a positive pid", res.Output)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(shellOut.PID); err == nil {
			_ = p.Kill()
		}
	})

	communicate := s.reg.ExecuteCall(context.Background(), s.env, communicateCallArgs("detached-warning", map[string]any{
		"message":  "done for now",
		"end_turn": true,
	}))
	if communicate.IsError {
		t.Fatalf("communicate error: %s", communicate.Output)
	}
	var response map[string]any
	if err := json.Unmarshal(toolResultJSON(communicate), &response); err != nil {
		t.Fatalf("unmarshal communicate output: %v", err)
	}
	warning, ok := response["warning"].(string)
	if !ok || warning == "" {
		t.Fatalf("expected a warning for live detached pid %d, got: %v", shellOut.PID, response)
	}
	if !strings.Contains(warning, strconv.Itoa(shellOut.PID)) {
		t.Fatalf("warning = %q, want detached process pid %d", warning, shellOut.PID)
	}
}

// TestCommunicate_EndTurnWarnsForLiveDelegate drives a real stable delegate
// through the delegate tool, holds its single child turn open on a gate, and
// ends the turn through the real communicate handler while a background shell
// job is ALSO still running. runningJobIDs only ever named shell jobs (issue
// #585): a live delegate is session-owned work exactly like a background job,
// so the warning must name both by id, not just the shell job.
func TestCommunicate_EndTurnWarnsForLiveDelegate(t *testing.T) {
	gate := make(chan struct{})
	childAdapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				<-gate
				return toolCallResponse(communicateCall("child-done", "child done"))
			},
		},
	}
	childClient := llm.NewClient()
	childClient.Register(childAdapter)
	registerTestSessionNamer(childClient)

	s := newSession(t, withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			childClientFactory:  func() *llm.Client { return childClient },
		},
	}))
	// Registered after newSession (whose t.Cleanup(sess.Close) ran first): LIFO
	// releases the child's blocked turn before Close waits on it.
	t.Cleanup(func() { close(gate) })

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","mode":"background"}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" || shellOut.Status != "running" {
		t.Fatalf("shell output = %+v, want a running job", shellOut)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	delegateRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "delegate",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"do something slowly"}`),
	})
	if delegateRes.IsError {
		t.Fatalf("delegate returned error: %s", delegateRes.Output)
	}
	var delegateOut struct {
		DelegateID string `json:"delegate_id"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(delegateRes), &delegateOut); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, delegateRes.Output)
	}
	if delegateOut.DelegateID == "" || delegateOut.Status != "running" {
		t.Fatalf("delegate output = %+v, want a running delegate", delegateOut)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, communicateCallArgs("end-turn-warning", map[string]any{
		"message":  "done for now",
		"end_turn": true,
	}))
	if res.IsError {
		t.Fatalf("communicate error: %s", res.Output)
	}
	var resp map[string]any
	if err := json.Unmarshal(toolResultJSON(res), &resp); err != nil {
		t.Fatalf("unmarshal communicate output: %v", err)
	}
	warning, ok := resp["warning"].(string)
	if !ok || warning == "" {
		t.Fatalf("expected a non-empty warning naming the running shell job and delegate, got: %v", resp)
	}
	if !strings.Contains(warning, shellOut.JobID) {
		t.Fatalf("warning = %q, want it to name shell job id %q", warning, shellOut.JobID)
	}
	if !strings.Contains(warning, delegateOut.DelegateID) {
		t.Fatalf("warning = %q, want it to name live delegate id %q", warning, delegateOut.DelegateID)
	}
}

func TestCommunicate_EndTurnDoesNotWarnForExitedDetachedProcess(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	if reporter, ok := s.env.(interface{ DetachSupported() bool }); !ok || !reporter.DetachSupported() {
		t.Skip("detached execution is unsupported in this environment")
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "detached-exited-shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"exec true","mode":"detached"}`),
	})
	if res.IsError {
		t.Fatalf("detached shell returned error: %s", res.Output)
	}
	s.mu.Lock()
	if len(s.detachedProcesses) != 1 {
		s.mu.Unlock()
		t.Fatalf("detached process records = %d, want one", len(s.detachedProcesses))
	}
	done := s.detachedProcesses[0].done
	s.mu.Unlock()
	<-done

	communicate := s.reg.ExecuteCall(context.Background(), s.env, communicateCallArgs("detached-exited", map[string]any{
		"message":  "done for now",
		"end_turn": true,
	}))
	if communicate.IsError {
		t.Fatalf("communicate error: %s", communicate.Output)
	}
	var response map[string]any
	if err := json.Unmarshal(toolResultJSON(communicate), &response); err != nil {
		t.Fatalf("unmarshal communicate output: %v", err)
	}
	if _, warned := response["warning"]; warned {
		t.Fatalf("communicate response = %v, want no warning for an exited detached process", response)
	}
}

// TestCommunicate_EndTurnWarnsForLiveGrandchildDelegate drives a real depth-2
// delegate tree: root creates A with a delegation_allowance, then A itself
// (not root) creates B and ends ITS OWN turn while B is still live. Adversarial
// review of #585 found the shipped fix used
// row.descriptor.OwnerSessionID == s.ID(), but OwnerSessionID is the TREE'S
// ROOT session id on every row (delegate_tree_start.go), never the immediate
// parent — so that filter only ever matches when s IS the root, and every
// non-root delegate (the default at MaxSubagentDepth 2: "a delegate itself
// delegate one level") silently got nil for its own live children. This drives
// A's REAL communicate handler (via A.reg.ExecuteCall, not a hand-built
// message) to prove A's own end_turn warning names A's own child, not just
// root's.
//
// It also pins the direct-vs-transitive design choice the review flagged as
// needing a decision: while B is live, ROOT's own end_turn warning (checked
// first, before A ends its turn and goes idle) must name A — root's own
// direct child — but must NOT name B, A's child and root's grandchild. Each
// session warns about what it itself is holding open, matching the scope
// sessionRunningJobIDs already gives shell jobs (a session's own job manager
// only ever lists jobs it launched itself, never a child's).
func TestCommunicate_EndTurnWarnsForLiveGrandchildDelegate(t *testing.T) {
	gate := make(chan struct{})
	childAdapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				<-gate
				return toolCallResponse(communicateCall("grandchild-done", "grandchild done"))
			},
		},
	}
	childClient := llm.NewClient()
	childClient.Register(childAdapter)
	registerTestSessionNamer(childClient)

	root := newSession(t, withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			childClientFactory:  func() *llm.Client { return childClient },
		},
	}))
	// Registered after newSession (whose t.Cleanup(sess.Close) ran first): LIFO
	// releases the grandchild's blocked turn before Close waits on it.
	t.Cleanup(func() { close(gate) })

	parentRes := root.reg.ExecuteCall(context.Background(), root.env, llm.ToolCallData{
		ID:        "delegate-parent",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"supervise a child delegate","delegation_allowance":1}`),
	})
	if parentRes.IsError {
		t.Fatalf("root delegate returned error: %s", parentRes.Output)
	}
	var parentOut struct {
		DelegateID     string `json:"delegate_id"`
		ChildSessionID string `json:"child_session_id"`
	}
	if err := json.Unmarshal(toolResultJSON(parentRes), &parentOut); err != nil {
		t.Fatalf("unmarshal root delegate output: %v (output: %s)", err, parentRes.Output)
	}
	a := root.subagents.get(parentOut.ChildSessionID)
	if a == nil || a.sess == nil {
		t.Fatalf("A's child session %q is not tracked", parentOut.ChildSessionID)
	}

	// A's own tool calls need A's current lease in context, exactly as its real
	// run loop would supply it (delegate_runtime.go sets this on runCtx before
	// sub.run): driving A.reg.ExecuteCall directly (as this file's other tests
	// already do for root) skips that plumbing, so it is reconstructed here the
	// same way TestDelegateResourceCreate_RegisteredNestedCreateUsesCurrentLease
	// (delegate_resource_create_test.go) does for the identical situation.
	root.delegateController.mu.Lock()
	aLive := root.delegateController.live[parentOut.DelegateID]
	if aLive == nil || aLive.binding == nil {
		root.delegateController.mu.Unlock()
		t.Fatalf("A (%q) has no live generation binding", parentOut.DelegateID)
	}
	aLease := aLive.binding.lease
	root.delegateController.mu.Unlock()
	aCtx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, aLease)

	grandchildRes := a.sess.reg.ExecuteCall(aCtx, a.sess.env, llm.ToolCallData{
		ID:        "delegate-grandchild",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"do something slowly"}`),
	})
	if grandchildRes.IsError {
		t.Fatalf("A's own delegate call returned error: %s", grandchildRes.Output)
	}
	var grandchildOut struct {
		DelegateID string `json:"delegate_id"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(grandchildRes), &grandchildOut); err != nil {
		t.Fatalf("unmarshal A's delegate output: %v (output: %s)", err, grandchildRes.Output)
	}
	if grandchildOut.DelegateID == "" || grandchildOut.Status != "running" {
		t.Fatalf("A's delegate output = %+v, want a running delegate", grandchildOut)
	}

	// Root's own end_turn, checked first (A is still running, hasn't gone idle
	// yet): root's warning must name its own direct child A, but not reach past
	// A into A's own child B.
	rootRes := root.reg.ExecuteCall(context.Background(), root.env, communicateCallArgs("root-end-turn", map[string]any{
		"message":  "pausing, my own child is still working",
		"end_turn": true,
	}))
	if rootRes.IsError {
		t.Fatalf("root's communicate error: %s", rootRes.Output)
	}
	var rootResp map[string]any
	if err := json.Unmarshal(toolResultJSON(rootRes), &rootResp); err != nil {
		t.Fatalf("unmarshal root's communicate output: %v", err)
	}
	rootWarning, ok := rootResp["warning"].(string)
	if !ok || rootWarning == "" {
		t.Fatalf("expected root's end_turn to warn about root's own live child delegate, got: %v", rootResp)
	}
	if !strings.Contains(rootWarning, parentOut.DelegateID) {
		t.Fatalf("root warning = %q, want it to name root's own live delegate id %q", rootWarning, parentOut.DelegateID)
	}
	if strings.Contains(rootWarning, grandchildOut.DelegateID) {
		t.Fatalf("root warning = %q, want it to NOT name grandchild delegate id %q (not root's direct child)", rootWarning, grandchildOut.DelegateID)
	}

	res := a.sess.reg.ExecuteCall(aCtx, a.sess.env, communicateCallArgs("a-end-turn", map[string]any{
		"message":  "pausing, my child is still working",
		"end_turn": true,
	}))
	if res.IsError {
		t.Fatalf("A's communicate error: %s", res.Output)
	}
	var resp map[string]any
	if err := json.Unmarshal(toolResultJSON(res), &resp); err != nil {
		t.Fatalf("unmarshal A's communicate output: %v", err)
	}
	warning, ok := resp["warning"].(string)
	if !ok || warning == "" {
		t.Fatalf("expected A's end_turn to warn about A's own live child delegate, got: %v", resp)
	}
	if !strings.Contains(warning, grandchildOut.DelegateID) {
		t.Fatalf("warning = %q, want it to name A's own live delegate id %q", warning, grandchildOut.DelegateID)
	}
}
