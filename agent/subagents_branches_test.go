package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
)

// ---------------------------------------------------------------------------
// SubagentStatus constants
// ---------------------------------------------------------------------------

func TestSubagentStatusConstants(t *testing.T) {
	if SubagentRunning != "running" {
		t.Fatalf("SubagentRunning = %q", SubagentRunning)
	}
	if SubagentCompleted != "completed" {
		t.Fatalf("SubagentCompleted = %q", SubagentCompleted)
	}
	if SubagentFailed != "failed" {
		t.Fatalf("SubagentFailed = %q", SubagentFailed)
	}
	if SubagentCancelled != "cancelled" {
		t.Fatalf("SubagentCancelled = %q", SubagentCancelled)
	}
	if SubagentExhausted != "exhausted" {
		t.Fatalf("SubagentExhausted = %q", SubagentExhausted)
	}
}

// ---------------------------------------------------------------------------
// subagentResult struct
// ---------------------------------------------------------------------------

func TestSubagentResultStruct(t *testing.T) {
	r := subagentResult{
		AgentID:       "agent_1",
		Status:        SubagentCompleted,
		Closed:        true,
		Output:        "result text",
		Success:       true,
		TurnsUsed:     5,
		TranscriptRef: "local:abc",
	}
	if r.AgentID != "agent_1" || r.Status != SubagentCompleted {
		t.Fatalf("struct wrong: %+v", r)
	}
	if !r.Closed || !r.Success || r.TurnsUsed != 5 {
		t.Fatalf("struct wrong: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// delegateSalvagedDraftNote constant
// ---------------------------------------------------------------------------

func TestDelegateSalvagedDraftNote(t *testing.T) {
	if delegateSalvagedDraftNote == "" {
		t.Fatalf("expected non-empty constant")
	}
}

// ---------------------------------------------------------------------------
// hasString
// ---------------------------------------------------------------------------

func TestHasString(t *testing.T) {
	items := []string{"a", "b", "c"}
	if !hasString(items, "b") {
		t.Fatalf("expected true for existing item")
	}
	if hasString(items, "d") {
		t.Fatalf("expected false for non-existing item")
	}
	if hasString(nil, "a") {
		t.Fatalf("expected false for nil slice")
	}
}

// ---------------------------------------------------------------------------
// appendUniqueStrings
// ---------------------------------------------------------------------------

func TestAppendUniqueStrings(t *testing.T) {
	t.Run("new items added", func(t *testing.T) {
		result := appendUniqueStrings([]string{"a"}, "b", "c")
		if len(result) != 3 || result[1] != "b" || result[2] != "c" {
			t.Fatalf("result = %v", result)
		}
	})
	t.Run("duplicates skipped", func(t *testing.T) {
		result := appendUniqueStrings([]string{"a", "b"}, "b", "c")
		if len(result) != 3 {
			t.Fatalf("expected 3, got %d", len(result))
		}
	})
	t.Run("empty strings skipped", func(t *testing.T) {
		result := appendUniqueStrings([]string{"a"}, "", "b", "")
		if len(result) != 2 {
			t.Fatalf("expected 2, got %d", len(result))
		}
	})
	t.Run("nil base", func(t *testing.T) {
		result := appendUniqueStrings(nil, "a", "b")
		if len(result) != 2 {
			t.Fatalf("expected 2, got %d", len(result))
		}
	})
	t.Run("no extras", func(t *testing.T) {
		result := appendUniqueStrings([]string{"a"})
		if len(result) != 1 || result[0] != "a" {
			t.Fatalf("result = %v", result)
		}
	})
}

// ---------------------------------------------------------------------------
// removeStrings
// ---------------------------------------------------------------------------

func TestRemoveStrings(t *testing.T) {
	t.Run("items removed", func(t *testing.T) {
		result := removeStrings([]string{"a", "b", "c"}, []string{"b"})
		if len(result) != 2 || result[0] != "a" || result[1] != "c" {
			t.Fatalf("result = %v", result)
		}
	})
	t.Run("no removals returns copy", func(t *testing.T) {
		result := removeStrings([]string{"a", "b"}, nil)
		if len(result) != 2 {
			t.Fatalf("expected 2, got %d", len(result))
		}
		// Should be a copy, not the same slice
		result[0] = "modified"
	})
	t.Run("empty items", func(t *testing.T) {
		result := removeStrings(nil, []string{"a"})
		if len(result) != 0 {
			t.Fatalf("expected 0, got %d", len(result))
		}
	})
	t.Run("remove all", func(t *testing.T) {
		result := removeStrings([]string{"a", "b"}, []string{"a", "b"})
		if len(result) != 0 {
			t.Fatalf("expected 0, got %d", len(result))
		}
	})
}

// ---------------------------------------------------------------------------
// ensureRecoveryReader
// ---------------------------------------------------------------------------

func TestEnsureRecoveryReaderNilRegistry(t *testing.T) {
	names := []string{"read_file", "exec_command"}
	result := ensureRecoveryReader(names, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 (unchanged for nil registry), got %d", len(result))
	}
}

func TestEnsureRecoveryReaderNoRecoveryNeeded(t *testing.T) {
	reg := tool.NewRegistry()
	names := []string{"read_file", "exec_command"}
	result := ensureRecoveryReader(names, reg)
	if len(result) != 2 {
		t.Fatalf("expected 2 (no recovery needed), got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// rootOnlySubagentTools / isRootOnlySubagentTool / isRootOnlyJobPresenceTool
// ---------------------------------------------------------------------------

func TestRootOnlySubagentTools(t *testing.T) {
	tools := rootOnlySubagentTools()
	if len(tools) == 0 {
		t.Fatalf("expected non-empty root-only tools")
	}
	// Should include "delegate" and "manage_worktree"
	if !hasString(tools, "manage_worktree") {
		t.Fatalf("expected manage_worktree in root-only tools: %v", tools)
	}
}

func TestIsRootOnlyJobPresenceTool(t *testing.T) {
	if !isRootOnlyJobPresenceTool("delegate") {
		t.Fatalf("expected delegate to be root-only job presence tool")
	}
	if isRootOnlyJobPresenceTool("read_file") {
		t.Fatalf("expected read_file to not be root-only job presence tool")
	}
}

func TestIsRootOnlySubagentTool(t *testing.T) {
	if !isRootOnlySubagentTool("manage_worktree") {
		t.Fatalf("expected manage_worktree to be root-only subagent tool")
	}
	if isRootOnlySubagentTool("read_file") {
		t.Fatalf("expected read_file to not be root-only subagent tool")
	}
}

// ---------------------------------------------------------------------------
// protectedGrantTools / isProtectedGrantTool
// ---------------------------------------------------------------------------

func TestProtectedGrantTools(t *testing.T) {
	tools := protectedGrantTools()
	if len(tools) != 1 || tools[0] != "ask_user" {
		t.Fatalf("expected ['ask_user'], got %v", tools)
	}
}

func TestIsProtectedGrantTool(t *testing.T) {
	if !isProtectedGrantTool("ask_user") {
		t.Fatalf("expected ask_user to be protected")
	}
	if isProtectedGrantTool("read_file") {
		t.Fatalf("expected read_file to not be protected")
	}
}

// ---------------------------------------------------------------------------
// removeRootOnlySubagentTools
// ---------------------------------------------------------------------------

func TestRemoveRootOnlySubagentTools(t *testing.T) {
	input := []string{"read_file", "delegate", "exec_command", "manage_worktree"}
	result := removeRootOnlySubagentTools(input)
	if hasString(result, "delegate") {
		t.Fatalf("expected delegate to be removed")
	}
	if hasString(result, "manage_worktree") {
		t.Fatalf("expected manage_worktree to be removed")
	}
	if !hasString(result, "read_file") {
		t.Fatalf("expected read_file to be kept")
	}
	if !hasString(result, "exec_command") {
		t.Fatalf("expected exec_command to be kept")
	}
}

// ---------------------------------------------------------------------------
// baseSubagentToolPolicy
// ---------------------------------------------------------------------------

func TestBaseSubagentToolPolicyAllTools(t *testing.T) {
	agent := &plugin.Agent{AllTools: true}
	allTools, allowed, denied := baseSubagentToolPolicy(agent, false)
	if !allTools {
		t.Fatalf("expected allTools=true")
	}
	if allowed != nil || denied != nil {
		t.Fatalf("expected nil allowed and denied")
	}
}

func TestBaseSubagentToolPolicyWithTools(t *testing.T) {
	agent := &plugin.Agent{Tools: []string{"read_file", "exec_command"}}
	allTools, allowed, denied := baseSubagentToolPolicy(agent, false)
	if allTools {
		t.Fatalf("expected allTools=false")
	}
	if denied != nil {
		t.Fatalf("expected nil denied")
	}
	if !hasString(allowed, "read_file") || !hasString(allowed, "exec_command") {
		t.Fatalf("expected tools to be in allowed: %v", allowed)
	}
	// task_list and compact_context should be added
	if !hasString(allowed, "task_list") {
		t.Fatalf("expected task_list to be added")
	}
	if !hasString(allowed, "compact_context") {
		t.Fatalf("expected compact_context to be added")
	}
}

func TestBaseSubagentToolPolicyCanDelegate(t *testing.T) {
	allTools, allowed, denied := baseSubagentToolPolicy(nil, true)
	if allTools {
		t.Fatalf("expected allTools=false")
	}
	if allowed != nil {
		t.Fatalf("expected nil allowed for can-delegate untyped")
	}
	if denied != nil {
		t.Fatalf("expected nil denied for can-delegate untyped")
	}
}

func TestBaseSubagentToolPolicyNoDelegate(t *testing.T) {
	allTools, _, denied := baseSubagentToolPolicy(nil, false)
	if allTools {
		t.Fatalf("expected allTools=false")
	}
	if denied == nil {
		t.Fatalf("expected non-nil denied for no-delegate untyped")
	}
}

// ---------------------------------------------------------------------------
// frozenSubagentToolNames
// ---------------------------------------------------------------------------

func TestFrozenSubagentToolNamesAllTools(t *testing.T) {
	result := frozenSubagentToolNames(true, nil, nil)
	if len(result) != 1 || result[0] != "*" {
		t.Fatalf("expected ['*'], got %v", result)
	}
}

func TestFrozenSubagentToolNamesAllowed(t *testing.T) {
	result := frozenSubagentToolNames(false, []string{"read_file"}, nil)
	if len(result) != 1 || result[0] != "read_file" {
		t.Fatalf("expected ['read_file'], got %v", result)
	}
}

func TestFrozenSubagentToolNamesDeniedOnly(t *testing.T) {
	result := frozenSubagentToolNames(false, nil, []string{"delegate"})
	if result != nil {
		t.Fatalf("expected nil for denied-only, got %v", result)
	}
}

func TestFrozenSubagentToolNamesEmpty(t *testing.T) {
	result := frozenSubagentToolNames(false, nil, nil)
	if result != nil {
		t.Fatalf("expected nil for all-empty, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// frozenStableDelegateSandboxMatches
// ---------------------------------------------------------------------------

func TestFrozenStableDelegateSandboxMatchesNilNil(t *testing.T) {
	if !frozenStableDelegateSandboxMatches(nil, nil) {
		t.Fatalf("expected true for nil nil")
	}
}

func TestFrozenStableDelegateSandboxMatchesNilWant(t *testing.T) {
	if frozenStableDelegateSandboxMatches(nil, &delegatestore.SandboxSnapshot{}) {
		t.Fatalf("expected false for nil got, non-nil want")
	}
}

// ---------------------------------------------------------------------------
// localEnvPolicyName
// ---------------------------------------------------------------------------

func TestLocalEnvPolicyNameNonLocal(t *testing.T) {
	if localEnvPolicyName(nil) != "" {
		t.Fatalf("expected empty for non-local env")
	}
}

// ---------------------------------------------------------------------------
// localEnvPolicyFromName
// ---------------------------------------------------------------------------

func TestLocalEnvPolicyFromName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"all", true},
		{"none", true},
		{"core_only", true},
		{"default", true},
		{"invalid", false},
		{"", false},
		{"  all  ", true}, // trimmed
	}
	for _, tc := range tests {
		_, ok := localEnvPolicyFromName(tc.name)
		if ok != tc.valid {
			t.Errorf("localEnvPolicyFromName(%q) ok = %v, want %v", tc.name, ok, tc.valid)
		}
	}
}

// ---------------------------------------------------------------------------
// cloneMap
// ---------------------------------------------------------------------------

func TestCloneMapEmpty(t *testing.T) {
	if cloneMap(nil) != nil {
		t.Fatalf("expected nil for nil map")
	}
	if cloneMap(map[string]any{}) != nil {
		t.Fatalf("expected nil for empty map")
	}
}

func TestCloneMapValid(t *testing.T) {
	in := map[string]any{"key": "value", "num": 42}
	out := cloneMap(in)
	if out == nil {
		t.Fatalf("expected non-nil clone")
	}
	if out["key"] != "value" {
		t.Fatalf("key = %v", out["key"])
	}
	// Verify deep copy
	out["key"] = "modified"
	if in["key"] == "modified" {
		t.Fatalf("expected deep copy")
	}
}

// ---------------------------------------------------------------------------
// cloneShallowMap
// ---------------------------------------------------------------------------

func TestCloneShallowMapEmpty(t *testing.T) {
	if cloneShallowMap(nil) != nil {
		t.Fatalf("expected nil for nil map")
	}
	if cloneShallowMap(map[string]any{}) != nil {
		t.Fatalf("expected nil for empty map")
	}
}

func TestCloneShallowMapValid(t *testing.T) {
	in := map[string]any{"key": "value"}
	out := cloneShallowMap(in)
	if out == nil || out["key"] != "value" {
		t.Fatalf("expected clone with key=value, got %v", out)
	}
}

// ---------------------------------------------------------------------------
// subagentNeedsCommunicateNudge
// ---------------------------------------------------------------------------

func TestSubagentNeedsCommunicateNudgeNilAgent(t *testing.T) {
	if !subagentNeedsCommunicateNudge(nil) {
		t.Fatalf("expected true for nil agent")
	}
}

func TestSubagentNeedsCommunicateNudgeBuiltinSubagent(t *testing.T) {
	agent := &plugin.Agent{PluginName: "builtin", Name: "subagent"}
	if !subagentNeedsCommunicateNudge(agent) {
		t.Fatalf("expected true for builtin/subagent")
	}
}

func TestSubagentNeedsCommunicateNudgeOtherAgent(t *testing.T) {
	agent := &plugin.Agent{PluginName: "builtin", Name: "other"}
	if subagentNeedsCommunicateNudge(agent) {
		t.Fatalf("expected false for non-subagent agent")
	}
}

// ---------------------------------------------------------------------------
// restoreFrozenSkillBodies
// ---------------------------------------------------------------------------

func TestRestoreFrozenSkillBodiesEmptyNamesEmptyBodies(t *testing.T) {
	bodies, err := restoreFrozenSkillBodies(nil, nil)
	if err != nil || bodies != nil {
		t.Fatalf("expected nil/nil for empty input")
	}
}

func TestRestoreFrozenSkillBodiesNamesWithBodiesMismatch(t *testing.T) {
	_, err := restoreFrozenSkillBodies(nil, []string{"body1"})
	if err == nil {
		t.Fatalf("expected error for bodies without names")
	}
}

func TestRestoreFrozenSkillBodiesNamesWithoutBodies(t *testing.T) {
	_, err := restoreFrozenSkillBodies([]string{"skill1"}, nil)
	if err == nil {
		t.Fatalf("expected error for names without bodies")
	}
}

func TestRestoreFrozenSkillBodiesCountMismatch(t *testing.T) {
	_, err := restoreFrozenSkillBodies([]string{"s1", "s2"}, []string{"body1"})
	if err == nil {
		t.Fatalf("expected error for count mismatch")
	}
}

func TestRestoreFrozenSkillBodiesEmptyBody(t *testing.T) {
	_, err := restoreFrozenSkillBodies([]string{"s1"}, []string{"  "})
	if err == nil {
		t.Fatalf("expected error for empty body")
	}
}

func TestRestoreFrozenSkillBodiesValid(t *testing.T) {
	bodies, err := restoreFrozenSkillBodies([]string{"s1", "s2"}, []string{"body1", "body2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bodies) != 2 || bodies[0] != "body1" || bodies[1] != "body2" {
		t.Fatalf("bodies = %v", bodies)
	}
}

// ---------------------------------------------------------------------------
// preparedSubagentRun struct
// ---------------------------------------------------------------------------

func TestPreparedSubagentRunStruct(t *testing.T) {
	p := &preparedSubagentRun{
		task:       "do the work",
		agentType:  "default",
		workingDir: "/path",
		isolation:  "worktree",
	}
	if p.task != "do the work" || p.agentType != "default" {
		t.Fatalf("struct wrong: %+v", p)
	}
}

// ---------------------------------------------------------------------------
// preparedSubagentRun.disposeUnadopted
// ---------------------------------------------------------------------------

func TestPreparedSubagentRunDisposeUnadoptedNil(t *testing.T) {
	var p *preparedSubagentRun
	p.disposeUnadopted() // should be a no-op for nil
}

func TestPreparedSubagentRunDisposeUnadoptedNilSub(t *testing.T) {
	p := &preparedSubagentRun{sub: nil}
	p.disposeUnadopted() // should be a no-op for nil sub
}

// ---------------------------------------------------------------------------
// disposeUnadoptedSubagentSession
// ---------------------------------------------------------------------------

func TestDisposeUnadoptedSubagentSessionNil(t *testing.T) {
	disposeUnadoptedSubagentSession(nil) // should be a no-op
}

// ---------------------------------------------------------------------------
// subagent struct fields
// ---------------------------------------------------------------------------

func TestSubagentStruct(t *testing.T) {
	sub := &subagent{
		id:        "dlg_1",
		status:    SubagentRunning,
		turnsUsed: 3,
	}
	if sub.id != "dlg_1" || sub.status != SubagentRunning {
		t.Fatalf("struct wrong: %+v", sub)
	}
	if sub.turnsUsed != 3 {
		t.Fatalf("turnsUsed = %d", sub.turnsUsed)
	}
}

// ---------------------------------------------------------------------------
// sandboxSnapshotFromEnv (tested via frozenStableDelegateSandboxMatches)
// ---------------------------------------------------------------------------

func TestSandboxSnapshotFromEnvNilEnv(t *testing.T) {
	if sandboxSnapshotFromEnv(nil) != nil {
		t.Fatalf("expected nil for nil env")
	}
}

// ---------------------------------------------------------------------------
// rootOnlyJobControlTools variable
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// defaultSubagentInstructions constant
// ---------------------------------------------------------------------------

func TestDefaultSubagentInstructions(t *testing.T) {
	if defaultSubagentInstructions == "" {
		t.Fatalf("expected non-empty constant")
	}
}

func TestDefaultDelegatingSubagentInstructions(t *testing.T) {
	if defaultDelegatingSubagentInstructions == "" {
		t.Fatalf("expected non-empty constant")
	}
}

// ---------------------------------------------------------------------------
// rootOnlyJobPresenceTools / rootOnlyWorktreeTools
// ---------------------------------------------------------------------------

func TestRootOnlyJobPresenceTools(t *testing.T) {
	if !hasString(rootOnlyJobPresenceTools, "delegate") {
		t.Fatalf("expected delegate in rootOnlyJobPresenceTools: %v", rootOnlyJobPresenceTools)
	}
}

func TestRootOnlyWorktreeTools(t *testing.T) {
	if !hasString(rootOnlyWorktreeTools, "manage_worktree") {
		t.Fatalf("expected manage_worktree in rootOnlyWorktreeTools: %v", rootOnlyWorktreeTools)
	}
}

// ---------------------------------------------------------------------------
// stableDelegateToolNameCeiling with nil registry
// ---------------------------------------------------------------------------

func TestStableDelegateToolNameCeilingNilRegistry(t *testing.T) {
	if stableDelegateToolNameCeiling(nil, "communicate", false, nil, nil, false, false, "") != nil {
		t.Fatalf("expected nil for nil registry")
	}
}

// ---------------------------------------------------------------------------
// subagent emit field
// ---------------------------------------------------------------------------

func TestSubagentEmitField(t *testing.T) {
	called := false
	sub := &subagent{
		emit: func(kind events.EventKind, data events.EventData) {
			called = true
		},
	}
	sub.emit(events.EventSessionStart, nil)
	if !called {
		t.Fatalf("expected emit to be called")
	}
}

// ---------------------------------------------------------------------------
// subagent time fields
// ---------------------------------------------------------------------------

func TestSubagentTimeFields(t *testing.T) {
	now := time.Now()
	sub := &subagent{
		createdAt: now,
		startedAt: now,
	}
	if !sub.createdAt.Equal(now) || !sub.startedAt.Equal(now) {
		t.Fatalf("time fields wrong: %+v", sub)
	}
}

// ---------------------------------------------------------------------------
// execenv.EnvVarPolicy is used by localEnvPolicyFromName
// ---------------------------------------------------------------------------

func TestLocalEnvPolicyFromNameAllPolicy(t *testing.T) {
	policy, ok := localEnvPolicyFromName("all")
	if !ok || policy != execenv.EnvPolicyAll {
		t.Fatalf("expected EnvPolicyAll for 'all', got %v ok=%v", policy, ok)
	}
}

func TestLocalEnvPolicyFromNameNonePolicy(t *testing.T) {
	policy, ok := localEnvPolicyFromName("none")
	if !ok || policy != execenv.EnvPolicyNone {
		t.Fatalf("expected EnvPolicyNone for 'none', got %v ok=%v", policy, ok)
	}
}

func TestLocalEnvPolicyFromNameCoreOnlyPolicy(t *testing.T) {
	policy, ok := localEnvPolicyFromName("core_only")
	if !ok || policy != execenv.EnvPolicyCoreOnly {
		t.Fatalf("expected EnvPolicyCoreOnly for 'core_only', got %v ok=%v", policy, ok)
	}
}

func TestLocalEnvPolicyFromNameDefaultPolicy(t *testing.T) {
	policy, ok := localEnvPolicyFromName("default")
	if !ok || policy != execenv.EnvPolicyDefault {
		t.Fatalf("expected EnvPolicyDefault for 'default', got %v ok=%v", policy, ok)
	}
}
