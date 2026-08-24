package agent

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provenance"
)

// ---- subagents.go pure functions ----

// TestCovHasString covers hasString (subagents.go lines 185-187).
func TestCovHasString2(t *testing.T) {
	if !hasString([]string{"a", "b", "c"}, "b") {
		t.Fatal("should find b")
	}
	if hasString([]string{"a", "b"}, "x") {
		t.Fatal("should not find x")
	}
	if hasString(nil, "x") {
		t.Fatal("nil should not find")
	}
}

// TestCovAppendUniqueStrings covers appendUniqueStrings
// (subagents.go lines 189-197).
func TestCovAppendUniqueStrings2(t *testing.T) {
	// Add new strings.
	got := appendUniqueStrings([]string{"a"}, "b", "c")
	if len(got) != 3 || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %+v", got)
	}
	// Skip empty and duplicates.
	got = appendUniqueStrings([]string{"a"}, "", "a", "b")
	if len(got) != 2 || got[1] != "b" {
		t.Fatalf("should skip empty and dup: %+v", got)
	}
	// Nil input.
	got = appendUniqueStrings(nil, "x")
	if len(got) != 1 || got[0] != "x" {
		t.Fatalf("nil input: %+v", got)
	}
}

// TestCovRemoveStrings covers removeStrings (subagents.go lines 206-218).
func TestCovRemoveStrings2(t *testing.T) {
	// No removals — copy of input.
	original := []string{"a", "b"}
	got := removeStrings(original, nil)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("no removals: %+v", got)
	}
	got[0] = "mutated"
	if original[0] != "a" {
		t.Fatalf("no-removal result shares storage with input: input=%v", original)
	}
	// Remove some.
	got = removeStrings([]string{"a", "b", "c"}, []string{"b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("remove b: %+v", got)
	}
	// Remove nothing matching.
	got = removeStrings([]string{"a"}, []string{"x"})
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("no match: %+v", got)
	}
}

// TestCovRootOnlySubagentTools covers rootOnlySubagentTools
// (subagents.go lines 220-223).
func TestCovRootOnlySubagentTools(t *testing.T) {
	tools := rootOnlySubagentTools()
	if want := []string{"delegate", "manage_worktree"}; !slices.Equal(tools, want) {
		t.Fatalf("root-only tools = %v, want exact ordered list %v", tools, want)
	}
}

// TestCovIsRootOnlyJobPresenceTool covers isRootOnlyJobPresenceTool
// (subagents.go lines 225-227).
func TestCovIsRootOnlyJobPresenceTool(t *testing.T) {
	if !isRootOnlyJobPresenceTool("delegate") {
		t.Fatal("delegate should be root-only")
	}
	if isRootOnlyJobPresenceTool("exec_command") {
		t.Fatal("exec_command should not be root-only")
	}
}

// TestCovIsRootOnlySubagentTool covers isRootOnlySubagentTool
// (subagents.go lines 229-231).
func TestCovIsRootOnlySubagentTool(t *testing.T) {
	if !isRootOnlySubagentTool("delegate") {
		t.Fatal("delegate should be root-only")
	}
	if !isRootOnlySubagentTool("manage_worktree") {
		t.Fatal("manage_worktree should be root-only")
	}
	if isRootOnlySubagentTool("read_file") {
		t.Fatal("read_file should not be root-only")
	}
}

// TestCovProtectedGrantTools covers protectedGrantTools and isProtectedGrantTool
// (subagents.go lines 240-246).
func TestCovProtectedGrantTools(t *testing.T) {
	tools := protectedGrantTools()
	if want := []string{"ask_user"}; !slices.Equal(tools, want) {
		t.Fatalf("protected grant tools = %v, want exact ordered list %v", tools, want)
	}
	if !isProtectedGrantTool("ask_user") {
		t.Fatal("ask_user should be protected")
	}
	if isProtectedGrantTool("exec_command") {
		t.Fatal("exec_command should not be protected")
	}
}

// TestCovRemoveRootOnlySubagentTools covers removeRootOnlySubagentTools
// (subagents.go lines 248-250).
func TestCovRemoveRootOnlySubagentTools(t *testing.T) {
	got := removeRootOnlySubagentTools([]string{"read_file", "delegate", "exec_command"})
	if want := []string{"read_file", "exec_command"}; !slices.Equal(got, want) {
		t.Fatalf("tools after root-only removal = %v, want %v", got, want)
	}
}

// TestCovFrozenSubagentToolNames covers frozenSubagentToolNames
// (subagents.go lines 273-284).
func TestCovFrozenSubagentToolNames(t *testing.T) {
	// allTools=true.
	got := frozenSubagentToolNames(true, nil, nil)
	if len(got) != 1 || got[0] != "*" {
		t.Fatalf("allTools: %+v", got)
	}
	// Has allowed list.
	allowed := []string{"read_file", "exec_command"}
	got = frozenSubagentToolNames(false, allowed, nil)
	if !slices.Equal(got, []string{"read_file", "exec_command"}) {
		t.Fatalf("allowed frozen names = %v, want [read_file exec_command]", got)
	}
	got[0] = "mutated"
	if allowed[0] != "read_file" {
		t.Fatalf("frozen tool names share storage with allowed input: %v", allowed)
	}
	// Only denied (non-empty).
	got = frozenSubagentToolNames(false, nil, []string{"delegate"})
	if got != nil {
		t.Fatalf("denied only: should be nil, got %+v", got)
	}
	// All empty.
	got = frozenSubagentToolNames(false, nil, nil)
	if got != nil {
		t.Fatalf("empty: should be nil, got %+v", got)
	}
}

// TestCovFrozenStableDelegateSandboxMatches covers
// frozenStableDelegateSandboxMatches (subagents.go lines 336-348).
func TestCovFrozenStableDelegateSandboxMatches(t *testing.T) {
	// Both nil — match.
	if !frozenStableDelegateSandboxMatches(nil, nil) {
		t.Fatal("both nil should match")
	}
	// env nil, want non-nil — no match.
	if frozenStableDelegateSandboxMatches(nil, &delegatestore.SandboxSnapshot{Mode: "off"}) {
		t.Fatal("nil env with non-nil want should not match")
	}
}

// TestCovSubagentNeedsCommunicateNudge covers subagentNeedsCommunicateNudge
// (subagents.go lines 406-411).
func TestCovSubagentNeedsCommunicateNudge(t *testing.T) {
	// nil agent — true (default subagent).
	if !subagentNeedsCommunicateNudge(nil) {
		t.Fatal("nil agent should need nudge")
	}
}

// TestCovRestoreFrozenSkillBodies covers restoreFrozenSkillBodies
// (subagents.go lines 413-434).
func TestCovRestoreFrozenSkillBodies(t *testing.T) {
	// Both empty — nil, no error.
	bodies, err := restoreFrozenSkillBodies(nil, nil)
	if err != nil || bodies != nil {
		t.Fatalf("both empty: bodies=%v err=%v", bodies, err)
	}

	// Names empty but bodies present — error.
	_, err = restoreFrozenSkillBodies(nil, []string{"body1"})
	if err == nil || !strings.Contains(err.Error(), "skill bodies without skill names") {
		t.Fatalf("bodies without names: %v", err)
	}

	// Names present but bodies empty — error.
	_, err = restoreFrozenSkillBodies([]string{"skill1"}, nil)
	if err == nil || !strings.Contains(err.Error(), "missing frozen skill bodies") {
		t.Fatalf("names without bodies: %v", err)
	}

	// Mismatched lengths — error.
	_, err = restoreFrozenSkillBodies([]string{"s1"}, []string{"b1", "b2"})
	if err == nil || !strings.Contains(err.Error(), "2 skill bodies for 1 skill names") {
		t.Fatalf("mismatched lengths: %v", err)
	}

	// Empty body — error.
	_, err = restoreFrozenSkillBodies([]string{"s1"}, []string{"  "})
	if err == nil || !strings.Contains(err.Error(), "skill body unavailable") {
		t.Fatalf("empty body: %v", err)
	}

	// Valid.
	bodies, err = restoreFrozenSkillBodies([]string{"s1", "s2"}, []string{"body1", "body2"})
	if err != nil || len(bodies) != 2 || bodies[0] != "body1" || bodies[1] != "body2" {
		t.Fatalf("valid: bodies=%v err=%v", bodies, err)
	}
}

// TestCovCommunicateNudge covers communicateNudge
// (subagents.go lines 1475-1480).
func TestCovCommunicateNudge2(t *testing.T) {
	got := communicateNudge("communicate")
	if !strings.Contains(got, "communicate") {
		t.Fatalf("should contain tool name: %q", got)
	}
	if !strings.Contains(got, "end_turn=true") {
		t.Fatalf("should mention end_turn=true: %q", got)
	}
	if !strings.Contains(got, "summarizing your complete findings") {
		t.Fatalf("should mention findings: %q", got)
	}
}

// TestCovFollowUpProvenance covers followUpProvenance
// (subagents.go lines 2007-2012): nil subagent and nil session.
func TestCovFollowUpProvenance(t *testing.T) {
	// nil subagent — returns clone of input.
	var a *subagent
	input := &provenance.Causal{
		WatchKeys:      []provenance.WatchKey{{WatchID: "watch_original", WatchGeneration: "generation_original"}},
		Chain:          []provenance.Entry{{Kind: "watch", DeliveryID: "delivery_original"}},
		ChainTruncated: true,
	}
	wantInput := &provenance.Causal{
		WatchKeys:      []provenance.WatchKey{{WatchID: "watch_original", WatchGeneration: "generation_original"}},
		Chain:          []provenance.Entry{{Kind: "watch", DeliveryID: "delivery_original"}},
		ChainTruncated: true,
	}
	got := a.followUpProvenance(input)
	if got == nil || !reflect.DeepEqual(got, wantInput) {
		t.Fatalf("nil-subagent provenance = %#v, want %#v", got, wantInput)
	}
	got.WatchKeys[0].WatchID = "watch_mutated"
	got.Chain[0].DeliveryID = "delivery_mutated"
	got.ChainTruncated = false
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("nil-subagent result shares storage with input: got %#v, want %#v", input, wantInput)
	}

	// Non-nil subagent with nil sess — returns clone of input.
	a = &subagent{}
	got = a.followUpProvenance(input)
	if got == nil || !reflect.DeepEqual(got, wantInput) {
		t.Fatalf("nil-session provenance = %#v, want %#v", got, wantInput)
	}
	got.WatchKeys[0].WatchGeneration = "generation_mutated"
	got.Chain[0].Kind = "mutated"
	got.ChainTruncated = false
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("nil-session result shares storage with input: got %#v, want %#v", input, wantInput)
	}

	// nil input — nil result (Clone(nil) is nil).
	got = a.followUpProvenance(nil)
	if got != nil {
		t.Fatal("nil input should return nil")
	}
}

// TestCovFatalRunGatedSnapshot covers fatalRunGatedSnapshot
// (subagents.go lines 1456-1463).
func TestCovFatalRunGatedSnapshot2(t *testing.T) {
	// nil subagent — false.
	var a *subagent
	if a.fatalRunGatedSnapshot() {
		t.Fatal("nil subagent should return false")
	}

	// Non-nil with fatalRunGated=false.
	a = &subagent{}
	if a.fatalRunGatedSnapshot() {
		t.Fatal("fatalRunGated=false should return false")
	}

	// Non-nil with fatalRunGated=true.
	a.fatalRunGated = true
	if !a.fatalRunGatedSnapshot() {
		t.Fatal("fatalRunGated=true should return true")
	}
}

// TestCovChildFatalRunGated covers childFatalRunGated
// (subagents.go lines 1465-1471).
func TestCovChildFatalRunGated2(t *testing.T) {
	// nil session — false.
	var s *Session
	if s.childFatalRunGated("child_1") {
		t.Fatal("nil session should return false")
	}

	// nil subagents — false.
	s = &Session{}
	if s.childFatalRunGated("child_1") {
		t.Fatal("nil subagents should return false")
	}

	// Empty child session ID — false.
	s.subagents = newSubagentManager(nil, 10)
	if s.childFatalRunGated("") {
		t.Fatal("empty child ID should return false")
	}
}

// TestCovDisposeUnadoptedSubagentSession covers disposeUnadoptedSubagentSession
// (subagents.go lines 165-176): nil session, non-ownsEnv.
func TestCovDisposeUnadoptedSubagentSession2(t *testing.T) {
	// nil session — no-op.
	disposeUnadoptedSubagentSession(nil, false)
	disposeUnadoptedSubagentSession(nil, true)
}

// TestCovPreparedSubagentRunDisposeUnadopted covers
// preparedSubagentRun.disposeUnadopted (subagents.go lines 178-183).
func TestCovPreparedSubagentRunDisposeUnadopted(t *testing.T) {
	// nil prepared — no-op.
	var p *preparedSubagentRun
	p.disposeUnadopted()

	// nil sub — no-op.
	p = &preparedSubagentRun{}
	p.disposeUnadopted()
}

// TestCovDelegateSettlementModeForRun covers delegateSettlementModeForRun
// (subagents.go lines 1716-1727).
func TestCovDelegateSettlementModeForRun(t *testing.T) {
	// cancelRequested — terminal.
	if delegateSettlementModeForRun(nil, true) != delegateSettlementTerminal {
		t.Fatal("cancelRequested should be terminal")
	}
	// Budget exhaustion is terminal even when its descriptor says resumable.
	exhausted := &budgetExhaustionError{Budget: exhaustedBudgetToolRounds, Limit: 4, Resumable: true}
	if delegateSettlementModeForRun(exhausted, false) != delegateSettlementTerminal {
		t.Fatal("budget exhaustion should be terminal")
	}
	// nil error, not cancelled — ordinary.
	if delegateSettlementModeForRun(nil, false) != delegateSettlementOrdinary {
		t.Fatal("nil error should be ordinary")
	}
	// errBareTextWithoutResultTool — ordinary.
	if delegateSettlementModeForRun(errBareTextWithoutResultTool, false) != delegateSettlementOrdinary {
		t.Fatal("errBareTextWithoutResultTool should be ordinary")
	}
	// errEmptyResponseExhausted — ordinary.
	if delegateSettlementModeForRun(errEmptyResponseExhausted, false) != delegateSettlementOrdinary {
		t.Fatal("errEmptyResponseExhausted should be ordinary")
	}
	// Generic error — terminal.
	if delegateSettlementModeForRun(errors.New("boom"), false) != delegateSettlementTerminal {
		t.Fatal("generic error should be terminal")
	}
}

// TestCovStableDelegateFatalRun covers stableDelegateFatalRun
// (subagents.go lines 1729-1735).
func TestCovStableDelegateFatalRun2(t *testing.T) {
	// nil error — not fatal.
	if stableDelegateFatalRun(nil) {
		t.Fatal("nil should not be fatal")
	}
	// context.Canceled — not fatal.
	if stableDelegateFatalRun(context.Canceled) {
		t.Fatal("context.Canceled should not be fatal")
	}
	// errBareTextWithoutResultTool — not fatal.
	if stableDelegateFatalRun(errBareTextWithoutResultTool) {
		t.Fatal("errBareTextWithoutResultTool should not be fatal")
	}
	// errEmptyResponseExhausted — not fatal.
	if stableDelegateFatalRun(errEmptyResponseExhausted) {
		t.Fatal("errEmptyResponseExhausted should not be fatal")
	}
	// Generic error — fatal.
	if !stableDelegateFatalRun(errors.New("boom")) {
		t.Fatal("generic error should be fatal")
	}
}

// ---- session_attention.go ----

// TestCovScheduleStableDelegateAttentionRetry_NilSession covers
// scheduleStableDelegateAttentionRetry (session_attention.go lines 740-782)
// nil guard.
func TestCovScheduleStableDelegateAttentionRetry_NilSession(t *testing.T) {
	var s *Session
	s.scheduleStableDelegateAttentionRetry() // should not panic
}

// TestCovResetStableDelegateAttentionRetry_NilSession covers
// resetStableDelegateAttentionRetry (session_attention.go lines 784+).
func TestCovResetStableDelegateAttentionRetry_NilSession(t *testing.T) {
	var s *Session
	s.resetStableDelegateAttentionRetry() // should not panic
}

// TestCovHasPendingDelegateAttentionArmRetry covers
// hasPendingDelegateAttentionArmRetry (session_attention.go lines 485-493).
func TestCovHasPendingDelegateAttentionArmRetry_NilSession(t *testing.T) {
	var s *Session
	if s.hasPendingDelegateAttentionArmRetry() {
		t.Fatal("nil session should return false")
	}
}

// TestCovHasPendingDelegateAttentionArmRetry_NoArms covers with no arms.
func TestCovHasPendingDelegateAttentionArmRetry_NoArms(t *testing.T) {
	s := &Session{}
	if s.hasPendingDelegateAttentionArmRetry() {
		t.Fatal("no arms should return false")
	}
}

// TestCovPendingDelegateAttentionIDs_NilSession covers
// pendingDelegateAttentionIDs (session_attention.go lines 495+).
func TestCovPendingDelegateAttentionIDs_NilSession(t *testing.T) {
	var s *Session
	ids, err := s.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("nil session: %v", err)
	}
	if ids != nil {
		t.Fatal("nil session should return nil ids")
	}
}

// TestCovStableDelegateRowsForSession_NilSession covers
// stableDelegateRowsForSession (session_tools_jobs.go lines 614-635).
func TestCovStableDelegateRowsForSession_NilSession(t *testing.T) {
	if got := stableDelegateRowsForSession(nil, false); got != nil {
		t.Fatal("nil session should return nil")
	}
	// No controller.
	s := &Session{}
	if got := stableDelegateRowsForSession(s, false); got != nil {
		t.Fatal("no controller should return nil")
	}
}

// TestCovEnsureRecoveryReader covers ensureRecoveryReader
// (subagents.go lines 199-204).
func TestCovEnsureRecoveryReader(t *testing.T) {
	// nil registry — returns names unchanged.
	got := ensureRecoveryReader([]string{"read_file", "exec_command"}, nil)
	if !slices.Equal(got, []string{"read_file", "exec_command"}) {
		t.Fatalf("nil registry tools = %v, want [read_file exec_command]", got)
	}

	// Non-nil registry without recovery requirement — unchanged.
	reg := tool.NewRegistry()
	got = ensureRecoveryReader([]string{"read_file"}, reg)
	if !slices.Equal(got, []string{"read_file"}) {
		t.Fatalf("no recovery needed: got %+v", got)
	}
}

// TestCovLocalEnvPolicyName covers localEnvPolicyName
// (subagents.go lines 350-365).
func TestCovLocalEnvPolicyName2(t *testing.T) {
	// nil env (not a LocalExecutionEnvironment) — empty string.
	if got := localEnvPolicyName(nil); got != "" {
		t.Fatalf("nil env: got %q", got)
	}
}

// TestCovLocalEnvPolicyFromName covers localEnvPolicyFromName
// (subagents.go lines 367-380).
func TestCovLocalEnvPolicyFromName2(t *testing.T) {
	for _, tc := range []struct {
		name string
		want execenv.EnvVarPolicy
	}{
		{name: "all", want: execenv.EnvPolicyAll},
		{name: "none", want: execenv.EnvPolicyNone},
		{name: "core_only", want: execenv.EnvPolicyCoreOnly},
		{name: "default", want: execenv.EnvPolicyDefault},
	} {
		got, ok := localEnvPolicyFromName(tc.name)
		if !ok || got != tc.want {
			t.Fatalf("localEnvPolicyFromName(%q) = (%v, %v), want (%v, true)", tc.name, got, ok, tc.want)
		}
	}
	// Invalid.
	got, ok := localEnvPolicyFromName("invalid")
	if ok || got != execenv.EnvPolicyDefault {
		t.Fatalf("invalid policy = (%v, %v), want (%v, false)", got, ok, execenv.EnvPolicyDefault)
	}
	// Whitespace trimmed.
	got, ok = localEnvPolicyFromName("  all  ")
	if !ok || got != execenv.EnvPolicyAll {
		t.Fatalf("whitespace policy = (%v, %v), want (%v, true)", got, ok, execenv.EnvPolicyAll)
	}
}

// TestCovCloneMap covers cloneMap (subagents.go lines 382-395).
func TestCovCloneMap2(t *testing.T) {
	// Empty/nil — nil.
	if cloneMap(nil) != nil {
		t.Fatal("nil should return nil")
	}
	if cloneMap(map[string]any{}) != nil {
		t.Fatal("empty should return nil")
	}

	// Valid map.
	original := map[string]any{
		"key": "value",
		"num": 42.0,
		"nested": map[string]any{
			"items": []any{"first", "second"},
		},
	}
	cloned := cloneMap(original)
	if cloned["key"] != "value" || cloned["num"] != 42.0 {
		t.Fatalf("clone mismatch: %+v", cloned)
	}
	// Mutating both the outer and nested clone must not affect the source.
	cloned["key"] = "changed"
	clonedNested := cloned["nested"].(map[string]any)
	clonedNested["items"].([]any)[0] = "changed"
	originalNested := original["nested"].(map[string]any)
	if original["key"] != "value" || originalNested["items"].([]any)[0] != "first" {
		t.Fatalf("mutating deep clone changed source: %+v", original)
	}
}

// TestCovCloneShallowMap covers cloneShallowMap (subagents.go lines 397-404).
func TestCovCloneShallowMap(t *testing.T) {
	// Empty/nil — nil.
	if cloneShallowMap(nil) != nil {
		t.Fatal("nil should return nil")
	}
	if cloneShallowMap(map[string]any{}) != nil {
		t.Fatal("empty should return nil")
	}

	// Valid map.
	original := map[string]any{"key": "value"}
	cloned := cloneShallowMap(original)
	if cloned["key"] != "value" {
		t.Fatalf("clone mismatch: %+v", cloned)
	}
	cloned["key"] = "changed"
	if original["key"] != "value" {
		t.Fatalf("mutating shallow clone changed source map: %+v", original)
	}
}

// TestCovSharedWorkspaceDelegateWarning covers
// sharedWorkspaceDelegateWarning (subagents.go lines 480-513).
func TestCovSharedWorkspaceDelegateWarning_NilSession(t *testing.T) {
	var s *Session
	if s.sharedWorkspaceDelegateWarning("") != "" {
		t.Fatal("nil session should return empty")
	}
}

// TestCovSharedWorkspaceDelegateWarning_NilSubagents covers with nil subagents.
func TestCovSharedWorkspaceDelegateWarning_NilSubagents(t *testing.T) {
	s := &Session{}
	if s.sharedWorkspaceDelegateWarning("") != "" {
		t.Fatal("nil subagents should return empty")
	}
	// With isolation set — should return empty (non-empty isolation skips).
	if s.sharedWorkspaceDelegateWarning("worktree") != "" {
		t.Fatal("non-empty isolation should return empty")
	}
}
