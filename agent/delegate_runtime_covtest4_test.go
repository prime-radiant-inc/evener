package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/sandbox"
)

type covStableDelegateResultError struct{}

func (*covStableDelegateResultError) Error() string { return "fail" }

// TestCovAbortOwedDelegateBootstrap covers abortOwedDelegateBootstrap
// (delegate_runtime.go lines 617-626): the error-cleanup path that iterates
// restored candidates in reverse, calling prepareDeferredOwedStart for each
// not-done entry.
func TestCovAbortOwedDelegateBootstrap(t *testing.T) {
	controller, _ := newDelegateControllerTestHarness(t, 3, 1)
	root := &Session{
		id:                 "root-session",
		state:              SessionProcessing,
		delegateController: controller,
		subagents:          newSubagentManager(nil, 3),
		events:             make(chan events.SessionEvent, 4),
	}
	controller.rootRuntime = root

	newRestored := func(delegateID string) owedBootstrapRuntime {
		t.Helper()
		seedDelegateControllerRunning(t, controller, delegateID, "")
		child := &Session{
			id:        "child-" + delegateID,
			subagents: newSubagentManager(nil, 1),
			events:    make(chan events.SessionEvent, 1),
		}
		sub := &subagent{id: child.id, sess: child}
		root.subagents.track(sub)
		lease := delegateLease{delegateID: delegateID, generation: 1}
		controller.mu.Lock()
		controller.live[delegateID].binding.runtime = child
		controller.live[delegateID].runtime = child
		descriptor := controller.durable[delegateID].Descriptor
		controller.mu.Unlock()
		return owedBootstrapRuntime{
			owner: root,
			sub:   sub,
			started: delegateStartCommit{
				lease:      lease,
				descriptor: descriptor,
			},
		}
	}

	first := newRestored("dlg_first")
	second := newRestored("dlg_second")
	doneChild := &Session{id: "child-done", subagents: newSubagentManager(nil, 1), events: make(chan events.SessionEvent, 1)}
	doneSub := &subagent{id: doneChild.id, sess: doneChild}
	root.subagents.track(doneSub)
	gate := &owedBootstrapRestore{
		restored: []owedBootstrapRuntime{first, {sub: doneSub}, second},
		done:     map[*subagent]bool{doneSub: true},
	}
	if err := root.abortOwedDelegateBootstrap(gate, errors.New("test abort")); err != nil {
		t.Fatalf("abort owed bootstrap: %v", err)
	}

	root.delegateDeliveryMu.Lock()
	gotOrder := make([]string, 0, len(root.pendingDelegateDeliveries))
	for _, delivery := range root.pendingDelegateDeliveries {
		gotOrder = append(gotOrder, delivery.delegateID)
	}
	root.delegateDeliveryMu.Unlock()
	if want := []string{"dlg_second", "dlg_first"}; !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("cleanup delivery order = %v, want reverse restored order %v", gotOrder, want)
	}
	for _, restored := range []owedBootstrapRuntime{first, second} {
		if got := root.subagents.get(restored.sub.id); got != nil {
			t.Fatalf("unfinished restored subagent %q remained tracked", restored.sub.id)
		}
		restored.sub.sess.mu.Lock()
		state := restored.sub.sess.state
		restored.sub.sess.mu.Unlock()
		if state != SessionClosed {
			t.Fatalf("unfinished restored session %q state = %v, want closed", restored.sub.id, state)
		}
	}
	if got := root.subagents.get(doneSub.id); got != doneSub {
		t.Fatalf("done restored subagent = %p, want retained %p", got, doneSub)
	}
	doneChild.mu.Lock()
	doneState := doneChild.state
	doneChild.mu.Unlock()
	if doneState == SessionClosed {
		t.Fatal("done restored session was cleaned up")
	}
}

// TestCovDelegatePermanentStartFailurePacket pins the direct packet-construction
// contract used by committed delegate start failure paths.
func TestCovDelegatePermanentStartFailurePacket(t *testing.T) {
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

	// Non-nil provenance — returns a deep clone of every mutable field.
	desc.Provenance = &provenance.Causal{
		WatchKeys:      []provenance.WatchKey{{WatchID: "watch_original", WatchGeneration: "generation_original"}},
		Chain:          []provenance.Entry{{Kind: "watch", DeliveryID: "delivery_original"}},
		ChainTruncated: true,
	}
	got := descriptorProvenance(desc)
	if got == nil || !reflect.DeepEqual(got, desc.Provenance) {
		t.Fatalf("cloned provenance = %#v, want %#v", got, desc.Provenance)
	}
	got.WatchKeys[0].WatchID = "watch_mutated"
	got.Chain[0].DeliveryID = "delivery_mutated"
	got.ChainTruncated = false
	if desc.Provenance.WatchKeys[0].WatchID != "watch_original" || desc.Provenance.Chain[0].DeliveryID != "delivery_original" || !desc.Provenance.ChainTruncated {
		t.Fatalf("mutating cloned provenance changed descriptor: %#v", desc.Provenance)
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
	committed := delegateUpdatePlan{rows: []delegateSnapshot{{
		id:        "dlg_1",
		lifecycle: delegateLifecycleRunning,
		resumable: false,
	}}}
	latestOutcome := &delegatestore.Outcome{Status: delegatestore.OutcomeCompleted, Reason: "reported_result"}
	plans := delegateMutationPlans{updates: []delegateUpdatePlan{{rows: []delegateSnapshot{{
		id:          "dlg_1",
		lifecycle:   delegateLifecycleIdle,
		resumable:   true,
		lastOutcome: latestOutcome,
	}}}}}
	result := stableDelegateResult(desc, "dlg_1", committed, plans, nil)
	if result.DelegateID != "dlg_1" {
		t.Fatalf("delegateID = %q", result.DelegateID)
	}
	if result.ChildSessionID != "child_1" {
		t.Fatalf("childSessionID = %q", result.ChildSessionID)
	}
	if result.Type != delegateResourceType {
		t.Fatalf("type = %q", result.Type)
	}
	if result.Status != jobstore.Status(delegateLifecycleIdle) {
		t.Fatalf("status = %q, want %q from latest durable snapshot", result.Status, delegateLifecycleIdle)
	}
	if result.Model != "openai/gpt-5" {
		t.Fatalf("model = %q", result.Model)
	}
	if result.TranscriptRef != "local:child_1" {
		t.Fatalf("transcript ref = %q, want local:child_1", result.TranscriptRef)
	}
	if result.Resumable == nil || !*result.Resumable {
		t.Fatalf("resumable = %v, want explicit true from latest durable snapshot", result.Resumable)
	}
	if result.Reason != "reported_result" {
		t.Fatalf("reason = %q, want reported_result from latest durable outcome", result.Reason)
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
	result = stableDelegateResult(desc, "dlg_1", committed, plans, nil)
	if result.Sandbox == nil || result.Sandbox.Mode != "off" || !result.Sandbox.Network {
		t.Fatalf("sandbox = %+v, want mode=off network=true", result.Sandbox)
	}

	// With error.
	wantErr := &covStableDelegateResultError{}
	result = stableDelegateResult(desc, "dlg_1", committed, plans, wantErr)
	if reflect.TypeOf(result.Err) != reflect.TypeOf(wantErr) || reflect.ValueOf(result.Err).Pointer() != reflect.ValueOf(wantErr).Pointer() {
		t.Fatalf("error object = %T %v, want exact input object %p", result.Err, result.Err, wantErr)
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

// TestCovEmitStableDelegateUpdate_WithRows covers forwarding a stable update
// to the root callback when the delegate owner runtime is not resident.
func TestCovEmitStableDelegateUpdate_WithRows(t *testing.T) {
	now := time.Date(2026, 8, 24, 20, 30, 0, 0, time.UTC)
	var forwarded []events.SessionEvent
	s := &Session{
		clock:              agenttest.NewFakeClockAt(now),
		delegateController: &delegateTreeController{rootSessionID: "root-session"},
		cfg: SessionConfig{spawn: spawnConfig{
			descendantEvent: func(event events.SessionEvent) { forwarded = append(forwarded, event) },
		}},
	}
	originalProvenance := &provenance.Causal{
		WatchKeys: []provenance.WatchKey{{WatchID: "watch_1", WatchGeneration: "wg_1"}},
		Chain:     []provenance.Entry{{Kind: "watch", DeliveryID: "delivery_1"}},
	}
	s.emitStableDelegateUpdate(delegateUpdatePlan{
		rows: []delegateSnapshot{
			{
				id:        "dlg_remote",
				lifecycle: delegateLifecycleIdle,
				phase:     delegatestore.PhaseClosed,
				resumable: true,
				revision:  9,
				lastOutcome: &delegatestore.Outcome{
					Status: delegatestore.OutcomeCompleted,
					Reason: "reported_result",
				},
				descriptor: delegatestore.Descriptor{
					OwnerSessionID:    "owner-remote",
					ChildSessionID:    "child-remote",
					TranscriptRef:     "local:child-remote",
					Task:              "inspect remote state",
					Description:       "remote delegate",
					AgentType:         "reviewer",
					ResolvedProfileID: "openai",
					ResolvedModel:     "gpt-5",
					Provenance:        originalProvenance,
				},
			},
		},
	})
	if len(forwarded) != 1 {
		t.Fatalf("forwarded events = %d, want 1", len(forwarded))
	}
	event := forwarded[0]
	if event.Kind != events.EventDelegateUpdated || event.SessionID != "owner-remote" || !event.Timestamp.Equal(now) {
		t.Fatalf("forwarded envelope = kind:%q session:%q timestamp:%v", event.Kind, event.SessionID, event.Timestamp)
	}
	wantData := events.DelegateUpdatedData{
		DelegateID: "dlg_remote", OwnerSessionID: "owner-remote", RootSessionID: "root-session",
		ChildSessionID: "child-remote", TranscriptRef: "local:child-remote", Type: "delegate",
		Lifecycle: "idle", Phase: "closed", Status: "idle", Outcome: "completed", Reason: "reported_result",
		Terminal: true, Resumable: true, ProjectionRevision: 9, Task: "inspect remote state",
		Description: "remote delegate", AgentType: "reviewer", ResolvedProfileID: "openai", ResolvedModel: "gpt-5", Model: "gpt-5",
	}
	gotData, ok := event.Data.(events.DelegateUpdatedData)
	if !ok || !reflect.DeepEqual(gotData, wantData) {
		t.Fatalf("forwarded data = %#v (%T), want %#v", event.Data, event.Data, wantData)
	}
	if event.Provenance == originalProvenance || !reflect.DeepEqual(event.Provenance, originalProvenance) {
		t.Fatalf("forwarded provenance = %#v, want independent clone of %#v", event.Provenance, originalProvenance)
	}
	event.Provenance.WatchKeys[0].WatchID = "mutated"
	event.Provenance.Chain[0].DeliveryID = "mutated"
	if originalProvenance.WatchKeys[0].WatchID != "watch_1" || originalProvenance.Chain[0].DeliveryID != "delivery_1" {
		t.Fatalf("forwarded provenance shares storage with descriptor: %#v", originalProvenance)
	}
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
