package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/sandbox"
)

// TestCovAbortOwedDelegateBootstrap covers abortOwedDelegateBootstrap
// (delegate_runtime.go lines 617-626): the error-cleanup path that iterates
// restored candidates in reverse, calling prepareDeferredOwedStart for each
// not-done entry.
func TestCovAbortOwedDelegateBootstrap(t *testing.T) {
	s := &Session{}
	// Empty gate — no iterations, returns nil.
	gate := &owedBootstrapRestore{held: true, pending: make(map[*Session]func()), done: make(map[*subagent]bool)}
	if err := s.abortOwedDelegateBootstrap(gate, errors.New("test abort")); err != nil {
		t.Fatalf("empty gate: %v", err)
	}

	// Gate with restored entries but all done — no iterations.
	sub := &subagent{}
	gate2 := &owedBootstrapRestore{
		restored: []owedBootstrapRuntime{{sub: sub}},
		done:     make(map[*subagent]bool),
	}
	gate2.done[sub] = true
	if err := s.abortOwedDelegateBootstrap(gate2, nil); err != nil {
		t.Fatalf("all done: %v", err)
	}
}

// TestCovFailAdoptedStart covers failAdoptedStart
// (delegate_runtime.go lines 1775-1786): nil/empty guards.
// This requires a full delegateController setup, so we test the pure helper
// delegatePermanentStartFailure that it calls.
func TestCovFailAdoptedStart_DelegatePermanentStartFailure(t *testing.T) {
	// delegatePermanentStartFailure is already at 100%, but verify its
	// branches are used by failAdoptedStart/failCommittedStart.
	finish := delegatePermanentStartFailure(nil, "test_reason")
	if finish.outcome != delegatestore.OutcomeFailed {
		t.Fatalf("outcome = %v, want %v", finish.outcome, delegatestore.OutcomeFailed)
	}
	if finish.reason != "test_reason" {
		t.Fatalf("reason = %q, want test_reason", finish.reason)
	}
	if finish.packet == nil || finish.packet.Kind != delegatestore.PacketTerminalError {
		t.Fatal("packet should be TerminalError")
	}

	// With error.
	finish = delegatePermanentStartFailure(errors.New("boom"), "construction_failed")
	if finish.reason != "construction_failed" {
		t.Fatalf("reason = %q", finish.reason)
	}
	// Verify message is the error text.
	var msg string
	if err := json.Unmarshal(finish.packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if msg != "boom" {
		t.Fatalf("message = %q, want boom", msg)
	}

	// Long error gets truncated.
	longErr := strings.Repeat("x", 600)
	finish = delegatePermanentStartFailure(errors.New(longErr), "too_long")
	if err := json.Unmarshal(finish.packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg != strings.Repeat("x", 512) {
		t.Fatalf("truncated message = %q, want exactly 512 x characters", msg)
	}
}

// TestCovSandboxPolicyFromStableSnapshot covers sandboxPolicyFromStableSnapshot
// (delegate_runtime.go lines 1646-1666): nil snapshot, invalid mode, valid
// with/without network.
func TestCovSandboxPolicyFromStableSnapshot(t *testing.T) {
	// nil snapshot — nil.
	if sandboxPolicyFromStableSnapshot(nil) != nil {
		t.Fatal("nil snapshot should return nil")
	}

	// Invalid mode — nil.
	policy := sandboxPolicyFromStableSnapshot(&delegatestore.SandboxSnapshot{Mode: "invalid_mode"})
	if policy != nil {
		t.Fatal("invalid mode should return nil")
	}

	for _, tc := range []struct {
		name string
		want sandbox.Mode
	}{
		{name: "off", want: sandbox.ModeOff},
		{name: "read-only", want: sandbox.ModeReadOnly},
		{name: "workspace-write", want: sandbox.ModeWorkspaceWrite},
		{name: "restricted", want: sandbox.ModeRestricted},
	} {
		policy = sandboxPolicyFromStableSnapshot(&delegatestore.SandboxSnapshot{Mode: tc.name})
		if policy == nil {
			t.Fatalf("mode %q returned nil", tc.name)
		}
		if policy.Mode != tc.want {
			t.Fatalf("mode %q restored as %v, want %v", tc.name, policy.Mode, tc.want)
		}
		if policy.Network != nil {
			t.Fatalf("mode %q supplied an unset network value: %v", tc.name, *policy.Network)
		}
	}

	// Valid mode with network=true.
	network := true
	policy = sandboxPolicyFromStableSnapshot(&delegatestore.SandboxSnapshot{Mode: "read-only", Network: &network})
	if policy == nil {
		t.Fatal("valid with network should return non-nil")
	}
	if policy.Mode != sandbox.ModeReadOnly {
		t.Fatalf("mode = %v, want %v", policy.Mode, sandbox.ModeReadOnly)
	}
	if policy.Network == nil || !*policy.Network {
		t.Fatal("network should be true")
	}

	// Valid mode with network=false.
	network = false
	policy = sandboxPolicyFromStableSnapshot(&delegatestore.SandboxSnapshot{Mode: "off", Network: &network})
	if policy == nil {
		t.Fatal("valid with network=false should return non-nil")
	}
	if policy.Network == nil || *policy.Network {
		t.Fatal("network should be false")
	}

	// All collection fields and the network pointer are copied rather than
	// sharing mutable snapshot storage.
	network = true
	snapshot := &delegatestore.SandboxSnapshot{
		Mode:               "workspace-write",
		Network:            &network,
		DenylistAdd:        []string{"/bin"},
		DenylistRemove:     []string{"/usr/bin/git"},
		ExtraWritableRoots: []string{"/tmp/write"},
		ExtraReadRoots:     []string{"/tmp/read"},
	}
	policy = sandboxPolicyFromStableSnapshot(snapshot)
	if policy == nil {
		t.Fatal("should return non-nil")
	}
	if policy.Mode != sandbox.ModeWorkspaceWrite {
		t.Fatalf("mode = %v, want %v", policy.Mode, sandbox.ModeWorkspaceWrite)
	}
	if len(policy.DenylistAdd) != 1 || policy.DenylistAdd[0] != "/bin" {
		t.Fatalf("denylist add: %+v", policy.DenylistAdd)
	}
	if len(policy.DenylistRemove) != 1 || policy.DenylistRemove[0] != "/usr/bin/git" {
		t.Fatalf("denylist remove: %+v", policy.DenylistRemove)
	}
	if len(policy.ExtraWritableRoots) != 1 || policy.ExtraWritableRoots[0] != "/tmp/write" {
		t.Fatalf("writable roots: %+v", policy.ExtraWritableRoots)
	}
	if len(policy.ExtraReadRoots) != 1 || policy.ExtraReadRoots[0] != "/tmp/read" {
		t.Fatalf("read roots: %+v", policy.ExtraReadRoots)
	}
	policy.DenylistAdd[0] = "/mutated"
	policy.DenylistRemove[0] = "/mutated"
	policy.ExtraWritableRoots[0] = "/mutated"
	policy.ExtraReadRoots[0] = "/mutated"
	*policy.Network = false
	if snapshot.DenylistAdd[0] != "/bin" || snapshot.DenylistRemove[0] != "/usr/bin/git" || snapshot.ExtraWritableRoots[0] != "/tmp/write" || snapshot.ExtraReadRoots[0] != "/tmp/read" || !*snapshot.Network {
		t.Fatalf("restored policy shares mutable storage with snapshot: snapshot=%+v", snapshot)
	}
}

// TestCovRestoreColdDelegateOwnerRuntime covers restoreColdDelegateOwnerRuntime
// (delegate_runtime.go lines 520-535): empty parentID path.
func TestCovRestoreColdDelegateOwnerRuntime_EmptyParentID(t *testing.T) {
	s := &Session{}
	// Empty parentID — returns s itself.
	got, err := s.restoreColdDelegateOwnerRuntime("")
	if err != nil {
		t.Fatalf("empty parentID: %v", err)
	}
	if got != s {
		t.Fatal("empty parentID should return the same session")
	}
}

// TestCovCleanup covers delegateIsolation.cleanup
// (delegate_runtime.go lines 1412-1421): no fresh env, no worktree.
func TestCovCleanup_NoFreshEnv(t *testing.T) {
	s := &Session{}
	isolation := delegateIsolation{ownsFreshEnv: false}
	// Should be a no-op — no panic.
	isolation.cleanup(s, "dlg_1")
}

// TestCovCleanup_FreshEnvNotLocal covers cleanup with ownsFreshEnv=true but
// env is not a LocalExecutionEnvironment — DisposeSandboxScratch not called.
// We use a nil env (which is not *LocalExecutionEnvironment) to exercise
// the type assertion failure path.
func TestCovCleanup_FreshEnvNotLocal(t *testing.T) {
	s := &Session{}
	isolation := delegateIsolation{
		ownsFreshEnv: true,
		env:          nil, // nil env — type assertion to *LocalExecutionEnvironment fails
	}
	// Should not panic even though env is nil (type assertion fails silently).
	isolation.cleanup(s, "dlg_1")
}

// TestCovDelegateInputWasPreseeded covers delegateInputWasPreseeded
// (delegate_runtime.go lines 767-769).
func TestCovDelegateInputWasPreseeded(t *testing.T) {
	// No context value — false.
	if delegateInputWasPreseeded(context.Background(), "sess_1", "hello") {
		t.Fatal("no context value should return false")
	}

	// With matching preseeded input.
	ctx := context.WithValue(context.Background(), delegatePreseededInputContextKey{}, delegatePreseededInput{
		sessionID: "sess_1",
		input:     "hello",
	})
	if !delegateInputWasPreseeded(ctx, "sess_1", "hello") {
		t.Fatal("matching preseeded should return true")
	}

	// Non-matching sessionID.
	if delegateInputWasPreseeded(ctx, "sess_2", "hello") {
		t.Fatal("non-matching session should return false")
	}

	// Non-matching input.
	if delegateInputWasPreseeded(ctx, "sess_1", "world") {
		t.Fatal("non-matching input should return false")
	}
}

// TestCovStableDelegateOutcomeJobStatus covers stableDelegateOutcomeJobStatus
// (delegate_runtime.go lines 996-1009): all outcome branches.
func TestCovStableDelegateOutcomeJobStatus(t *testing.T) {
	tests := []struct {
		outcome delegatestore.OutcomeStatus
		want    jobstore.Status
	}{
		{delegatestore.OutcomeCompleted, jobstore.StatusCompleted},
		{delegatestore.OutcomeCancelled, jobstore.StatusCancelled},
		{delegatestore.OutcomeStopped, jobstore.StatusStopped},
		{delegatestore.OutcomeExhausted, jobstore.StatusExhausted},
		{delegatestore.OutcomeFailed, jobstore.StatusFailed},
		{"unknown_outcome", jobstore.StatusFailed},
	}
	for _, tt := range tests {
		got := stableDelegateOutcomeJobStatus(tt.outcome)
		if got != tt.want {
			t.Fatalf("stableDelegateOutcomeJobStatus(%v) = %q, want %q", tt.outcome, got, tt.want)
		}
	}
}

// TestCovDescriptorProvenance covers descriptorProvenance
// (delegate_runtime.go lines 1011-1013).
func TestCovDescriptorProvenance(t *testing.T) {
	// nil provenance — returns nil (Clone nil-safety).
	desc := delegatestore.Descriptor{}
	if descriptorProvenance(desc) != nil {
		t.Fatal("nil provenance should return nil")
	}

	// Non-nil provenance — returns clone.
	// Clone returns NilIfEmpty, so ChainTruncated=true keeps it non-empty.
	desc.Provenance = &provenance.Causal{ChainTruncated: true}
	got := descriptorProvenance(desc)
	if got == nil || !got.ChainTruncated {
		t.Fatal("should return cloned provenance")
	}
	got.ChainTruncated = false
	if !desc.Provenance.ChainTruncated {
		t.Fatal("mutating cloned provenance changed the descriptor")
	}
}

// TestCovStableDelegateResult covers stableDelegateResult
// (delegate_runtime.go lines 1818-1847): the result construction.
func TestCovStableDelegateResult(t *testing.T) {
	desc := delegatestore.Descriptor{
		ChildSessionID:    "child_1",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5",
		TranscriptRef:     "local:child_1",
	}
	result := stableDelegateResult(desc, "dlg_1", delegateUpdatePlan{}, delegateMutationPlans{}, nil)
	if result.DelegateID != "dlg_1" {
		t.Fatalf("delegateID = %q", result.DelegateID)
	}
	if result.ChildSessionID != "child_1" {
		t.Fatalf("childSessionID = %q", result.ChildSessionID)
	}
	if result.Type != delegateResourceType {
		t.Fatalf("type = %q", result.Type)
	}
	if result.Status != jobstore.StatusRunning {
		t.Fatalf("status = %q, want %q", result.Status, jobstore.StatusRunning)
	}
	if result.Model != "openai/gpt-5" {
		t.Fatalf("model = %q", result.Model)
	}
	if result.TranscriptRef != "local:child_1" {
		t.Fatalf("transcript ref = %q, want local:child_1", result.TranscriptRef)
	}
	if result.Resumable == nil || *result.Resumable {
		t.Fatalf("resumable = %v, want explicit false", result.Resumable)
	}
	if !result.RunningInBackground {
		t.Fatal("should be running in background")
	}
	if result.Sandbox != nil {
		t.Fatalf("sandbox = %+v, want nil", result.Sandbox)
	}

	// With sandbox snapshot.
	network := true
	desc.Sandbox = &delegatestore.SandboxSnapshot{Mode: "off", Network: &network}
	result = stableDelegateResult(desc, "dlg_1", delegateUpdatePlan{}, delegateMutationPlans{}, nil)
	if result.Sandbox == nil || result.Sandbox.Mode != "off" || !result.Sandbox.Network {
		t.Fatalf("sandbox = %+v, want mode=off network=true", result.Sandbox)
	}

	// With error.
	wantErr := errors.New("fail")
	result = stableDelegateResult(desc, "dlg_1", delegateUpdatePlan{}, delegateMutationPlans{}, wantErr)
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("err = %v", result.Err)
	}
}

// TestCovLatestDelegateMutationSnapshot covers latestDelegateMutationSnapshot
// (delegate_runtime.go lines 1849-1867): finding the latest snapshot for a
// delegate ID from committed rows and plan updates.
func TestCovLatestDelegateMutationSnapshot(t *testing.T) {
	// No rows, no plans — empty snapshot.
	snap := latestDelegateMutationSnapshot("dlg_1", delegateUpdatePlan{}, delegateMutationPlans{})
	if snap.id != "" {
		t.Fatalf("empty should return zero snapshot, got id=%q", snap.id)
	}

	// Found in committed rows.
	committed := delegateUpdatePlan{
		rows: []delegateSnapshot{
			{id: "dlg_other"},
			{id: "dlg_1", resumable: true},
		},
	}
	snap = latestDelegateMutationSnapshot("dlg_1", committed, delegateMutationPlans{})
	if snap.id != "dlg_1" || !snap.resumable {
		t.Fatalf("should find dlg_1 in committed: %+v", snap)
	}

	// Fallback to last row when target not found.
	committed = delegateUpdatePlan{
		rows: []delegateSnapshot{{id: "dlg_other"}},
	}
	snap = latestDelegateMutationSnapshot("dlg_missing", committed, delegateMutationPlans{})
	if snap.id != "dlg_other" {
		t.Fatalf("should fall back to last row: got %q", snap.id)
	}

	// Found in plan updates (overrides committed).
	committed = delegateUpdatePlan{
		rows: []delegateSnapshot{{id: "dlg_1", resumable: false}},
	}
	plans := delegateMutationPlans{
		updates: []delegateUpdatePlan{{
			rows: []delegateSnapshot{{id: "dlg_1", resumable: true}},
		}},
	}
	snap = latestDelegateMutationSnapshot("dlg_1", committed, plans)
	if !snap.resumable {
		t.Fatal("plan update should override committed")
	}
}

// TestCovDelegateQuietAttentionID covers delegateQuietAttentionID
// (delegate_runtime.go lines 160-162).
func TestCovDelegateQuietAttentionID2(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 5}
	got := delegateQuietAttentionID(lease)
	expected := delegateQuietAttentionIDForStretch(lease, 1)
	if got != expected {
		t.Fatalf("delegateQuietAttentionID = %q, want %q", got, expected)
	}
}

// TestCovDelegateQuietAttentionIDForStretch covers
// delegateQuietAttentionIDForStretch (delegate_runtime.go lines 164-166).
func TestCovDelegateQuietAttentionIDForStretch(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 3}
	got := delegateQuietAttentionIDForStretch(lease, 7)
	if got != "quiet:dlg_1:3:7" {
		t.Fatalf("ID = %q, want quiet:dlg_1:3:7", got)
	}
}

// TestCovDelegateQuietAttentionContent covers delegateQuietAttentionContent
// (delegate_runtime.go lines 168-174).
func TestCovDelegateQuietAttentionContent2(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	activityAt := frozenTestTime
	got := delegateQuietAttentionContent(lease, activityAt)
	if !strings.Contains(got, "dlg_1") {
		t.Fatalf("should contain delegate ID: %q", got)
	}
	if !strings.Contains(got, "quiet for") {
		t.Fatalf("should contain quiet for: %q", got)
	}
	if !strings.Contains(got, "<delegate-notification") {
		t.Fatalf("should be wrapped in delegate-notification tag: %q", got)
	}
}

// TestCovBindStableDelegateActivity covers bindStableDelegateActivity
// (delegate_runtime.go lines 281-287): nil guards.
func TestCovBindStableDelegateActivity_NilChild(t *testing.T) {
	// nil child — no-op.
	bindStableDelegateActivity(nil, &delegateTreeController{}, delegateLease{})
	// nil controller — no-op.
	bindStableDelegateActivity(&Session{}, nil, delegateLease{})
}

// TestCovBindStableDelegateActivityToOwner covers
// bindStableDelegateActivityToOwner (delegate_runtime.go lines 289-308):
// nil guards.
func TestCovBindStableDelegateActivityToOwner_NilChild(t *testing.T) {
	// nil child — no-op.
	bindStableDelegateActivityToOwner(nil, &delegateTreeController{}, delegateLease{}, &Session{})
	// nil controller — no-op.
	bindStableDelegateActivityToOwner(&Session{}, nil, delegateLease{}, &Session{})
}

// TestCovResolveStableSharedTaskStore covers resolveStableSharedTaskStore
// (delegate_runtime.go lines 1668-1691): nil/empty guards.
func TestCovResolveStableSharedTaskStore(t *testing.T) {
	// nil session — error.
	_, err := (*Session)(nil).resolveStableSharedTaskStore("owner_1")
	if err == nil {
		t.Fatal("nil session should return error")
	}

	// nil controller — error.
	s := &Session{}
	_, err = s.resolveStableSharedTaskStore("owner_1")
	if err == nil {
		t.Fatal("nil controller should return error")
	}

	// Empty ownerSessionID — error.
	s.delegateController = &delegateTreeController{}
	_, err = s.resolveStableSharedTaskStore("")
	if err == nil {
		t.Fatal("empty owner should return error")
	}
	_, err = s.resolveStableSharedTaskStore("  ")
	if err == nil {
		t.Fatal("whitespace owner should return error")
	}

	// Controller with no matching owner — error.
	_, err = s.resolveStableSharedTaskStore("owner_nonexistent")
	if err == nil || !strings.Contains(err.Error(), "not resident") {
		t.Fatalf("non-existent owner: %v", err)
	}
}

// TestCovDriveStableDelegateAttention_NilSession covers
// driveStableDelegateAttention (delegate_runtime.go lines 310-368) nil guard.
func TestCovDriveStableDelegateAttention_NilSession(t *testing.T) {
	var s *Session
	if s.driveStableDelegateAttention(nil) {
		t.Fatal("nil session should return false")
	}
}

// TestCovDriveStableDelegateAttention_NilSub covers with nil sub.
func TestCovDriveStableDelegateAttention_NilSub(t *testing.T) {
	s := &Session{}
	if s.driveStableDelegateAttention(nil) {
		t.Fatal("nil sub should return false")
	}
}

// TestCovDriveStableDelegateAttention_NilSubSession covers with sub having nil sess.
func TestCovDriveStableDelegateAttention_NilSubSession(t *testing.T) {
	s := &Session{}
	sub := &subagent{}
	if s.driveStableDelegateAttention(sub) {
		t.Fatal("nil sub.sess should return false")
	}
}

// TestCovRestoreColdDelegateAttentionRuntime_NilSession covers
// restoreColdDelegateAttentionRuntime (delegate_runtime.go lines 394-409)
// nil/empty guards.
func TestCovRestoreColdDelegateAttentionRuntime_NilSession(t *testing.T) {
	var s *Session
	_, _, err := s.restoreColdDelegateAttentionRuntime("dlg_1")
	if err == nil {
		t.Fatal("nil session should return error")
	}
}

// TestCovRestoreColdDelegateAttentionRuntime_EmptyDelegateID covers
// the empty delegateID guard.
func TestCovRestoreColdDelegateAttentionRuntime_EmptyDelegateID(t *testing.T) {
	s := &Session{}
	_, _, err := s.restoreColdDelegateAttentionRuntime("")
	if err == nil {
		t.Fatal("empty delegateID should return error")
	}
}

// TestCovAdmitOwedDelegateAttentionStarts_NilSession covers
// admitOwedDelegateAttentionStarts (delegate_runtime.go lines 543-597)
// nil guard.
func TestCovAdmitOwedDelegateAttentionStarts_NilSession(t *testing.T) {
	var s *Session
	if err := s.admitOwedDelegateAttentionStarts(); err == nil {
		t.Fatal("nil session should return error")
	}
}

// TestCovAdmitOwedDelegateAttentionStarts_NilController covers
// the nil-controller guard.
func TestCovAdmitOwedDelegateAttentionStarts_NilController(t *testing.T) {
	s := &Session{}
	if err := s.admitOwedDelegateAttentionStarts(); err == nil {
		t.Fatal("nil controller should return error")
	}
}

// TestCovAdmitOwedDelegateAttentionStarts_EmptyStateDir covers the
// empty StateDir early return (returns nil, no error).
func TestCovAdmitOwedDelegateAttentionStarts_EmptyStateDir(t *testing.T) {
	s := &Session{delegateController: &delegateTreeController{}}
	if err := s.admitOwedDelegateAttentionStarts(); err != nil {
		t.Fatalf("empty StateDir should return nil: %v", err)
	}
}

// TestCovOwedBootstrapRestoreOpen covers owedBootstrapRestore.open
// (delegate_runtime.go lines 452-463).
func TestCovOwedBootstrapRestoreOpen(t *testing.T) {
	gate := &owedBootstrapRestore{
		held:    true,
		pending: make(map[*Session]func()),
		done:    make(map[*subagent]bool),
	}
	// Add a pending callback.
	called := false
	sess := &Session{}
	gate.pending[sess] = func() { called = true }
	gate.open()
	if gate.held {
		t.Fatal("held should be false after open")
	}
	if !called {
		t.Fatal("pending callback should be called")
	}
	if len(gate.pending) != 0 {
		t.Fatal("pending should be cleared")
	}
	if gate.restored != nil {
		t.Fatal("restored should be cleared")
	}
	if gate.done != nil {
		t.Fatal("done should be cleared")
	}

	// Open with no pending — no panic.
	gate2 := &owedBootstrapRestore{held: true, pending: make(map[*Session]func()), done: make(map[*subagent]bool)}
	gate2.open()
	if gate2.held {
		t.Fatal("held should be false")
	}
}

// TestCovOwedBootstrapRestoreAdd covers owedBootstrapRestore.add
// (delegate_runtime.go lines 429-451): nil notify callback.
func TestCovOwedBootstrapRestoreAdd_NilNotify(t *testing.T) {
	gate := &owedBootstrapRestore{held: true, pending: make(map[*Session]func()), done: make(map[*subagent]bool)}
	sub := &subagent{sess: &Session{}}
	// sess has nil notifyFunc — should return error.
	err := gate.add(&Session{}, sub, delegateStartCommit{})
	if err == nil {
		t.Fatal("nil notifyFunc should return error")
	}
	if !strings.Contains(err.Error(), "notify callback is unavailable") {
		t.Fatalf("error: %v", err)
	}
}

// TestCovCloseOwnedDelegateStore covers closeOwnedDelegateStore
// (delegate_runtime.go lines 2141-2143).
func TestCovCloseOwnedDelegateStore_NilSession(t *testing.T) {
	var s *Session
	// Should not panic.
	s.closeOwnedDelegateStore()
}

// TestCovCloseOwnedDelegateStoreWithContext covers
// closeOwnedDelegateStoreWithContext (delegate_runtime.go lines 2145-2150).
func TestCovCloseOwnedDelegateStoreWithContext_NilSession(t *testing.T) {
	var s *Session
	ctx := context.Background()
	// Should not panic.
	s.closeOwnedDelegateStoreWithContext(ctx)
}

// TestCovCloseOwnedDelegateRuntimeTree covers closeOwnedDelegateRuntimeTree
// (delegate_runtime.go lines 2152+).
func TestCovCloseOwnedDelegateRuntimeTree_NilSession(t *testing.T) {
	var s *Session
	ctx := context.Background()
	// Should not panic.
	s.closeOwnedDelegateRuntimeTree(ctx)
}

// TestCovEmitStableDelegateUpdate covers emitStableDelegateUpdate with
// a nil controller session.
func TestCovEmitStableDelegateUpdate2(t *testing.T) {
	s := &Session{}
	// No controller — should not panic.
	s.emitStableDelegateUpdate(delegateUpdatePlan{})
}

// TestCovEmitStableDelegateUpdate_WithRows covers the row iteration path
// when there's no runtime and no descendant event forward.
func TestCovEmitStableDelegateUpdate_WithRows(t *testing.T) {
	s := &Session{}
	s.emitStableDelegateUpdate(delegateUpdatePlan{
		rows: []delegateSnapshot{
			{id: "dlg_1", descriptor: delegatestore.Descriptor{OwnerSessionID: "other"}},
		},
	})
	// Should not panic with no controller.
}

// TestCovDelegateRestoreStat covers delegateRestoreStat
// (delegate_runtime.go lines 2113-2118): a method on Session.
func TestCovDelegateRestoreStat(t *testing.T) {
	s := &Session{}
	// Non-existent file — error.
	_, err := s.delegateRestoreStat(t.TempDir() + "/nonexistent")
	if err == nil {
		t.Fatal("non-existent file should return error")
	}
}

// TestCovDelegateRestoreReadFile covers delegateRestoreReadFile
// (delegate_runtime.go lines 2120-2125): a method on Session.
func TestCovDelegateRestoreReadFile(t *testing.T) {
	s := &Session{}
	// Non-existent file — error.
	_, err := s.delegateRestoreReadFile(t.TempDir() + "/nonexistent")
	if err == nil {
		t.Fatal("non-existent file should return error")
	}
}

// TestCovDelegateRestoreOperationalIOError covers
// delegateRestoreOperationalIOError
// (delegate_runtime.go lines 2127-2139): classifies whether an error
// is operational (retryable) vs a permanent missing-file condition.
func TestCovDelegateRestoreOperationalIOError(t *testing.T) {
	// nil error — not operational.
	if delegateRestoreOperationalIOError(nil) {
		t.Fatal("nil error should not be operational")
	}
	// os.ErrNotExist — not operational (classified as permanent missing).
	if delegateRestoreOperationalIOError(os.ErrNotExist) {
		t.Fatal("ErrNotExist should not be operational")
	}
	// A plain errors.New — not operational (not a PathError/SyscallError).
	if delegateRestoreOperationalIOError(errors.New("plain error")) {
		t.Fatal("plain error should not be operational")
	}
	// A *os.PathError — IS operational.
	pathErr := &os.PathError{Op: "stat", Path: "/tmp/nonexistent", Err: errors.New("permission")}
	if !delegateRestoreOperationalIOError(pathErr) {
		t.Fatal("PathError should be operational")
	}
}

// TestCovMissingDelegateRestoreInputReason covers
// missingDelegateRestoreInputReason (delegate_runtime.go lines 2051+).
func TestCovMissingDelegateRestoreInputReason(t *testing.T) {
	// Empty descriptor — missing metadata.
	reason, err := missingDelegateRestoreInputReason(
		"", delegatestore.Descriptor{}, os.Stat, os.ReadFile,
	)
	if err != nil {
		t.Fatalf("empty descriptor: %v", err)
	}
	if reason != notResumableMissingDelegateResumeMetadata {
		t.Fatalf("reason = %q, want %q", reason, notResumableMissingDelegateResumeMetadata)
	}

	// With valid descriptor but empty stateDir — missing child session meta.
	reason, err = missingDelegateRestoreInputReason(
		"", delegatestore.Descriptor{
			ChildSessionID:    "child_1",
			Task:              "do work",
			AgentType:         "default",
			ResolvedProfileID: "openai",
			ResolvedModel:     "gpt-5",
			TranscriptRef:     encodeRef("", "child_1"),
		}, os.Stat, os.ReadFile,
	)
	if err != nil {
		t.Fatalf("valid desc empty stateDir: %v", err)
	}
	if reason != notResumableMissingChildSessionMeta {
		t.Fatalf("reason = %q, want %q", reason, notResumableMissingChildSessionMeta)
	}
}
