package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
)

func TestStableDelegateOutcomeJobStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		outcome delegatestore.OutcomeStatus
		want   jobstore.Status
	}{
		{delegatestore.OutcomeCompleted, jobstore.StatusCompleted},
		{delegatestore.OutcomeCancelled, jobstore.StatusCancelled},
		{delegatestore.OutcomeStopped, jobstore.StatusStopped},
		{delegatestore.OutcomeExhausted, jobstore.StatusExhausted},
		{delegatestore.OutcomeFailed, jobstore.StatusFailed},
		{"unknown", jobstore.StatusFailed},
	} {
		if got := stableDelegateOutcomeJobStatus(tc.outcome); got != tc.want {
			t.Errorf("stableDelegateOutcomeJobStatus(%q) = %q, want %q", tc.outcome, got, tc.want)
		}
	}
}

func TestDelegatePermanentStartFailure(t *testing.T) {
	t.Parallel()
	// With an error, the packet message is the error text.
	finish := delegatePermanentStartFailure(errors.New("boom"), "construction_failed")
	if finish.outcome != delegatestore.OutcomeFailed || finish.disposition != delegatestore.DispositionTerminalError || finish.reason != "construction_failed" {
		t.Fatalf("finish = %+v", finish)
	}
	if finish.packet == nil || finish.packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("packet = %+v", finish.packet)
	}
	var msg string
	if err := json.Unmarshal(finish.packet.Message, &msg); err != nil {
		t.Fatal(err)
	}
	if msg != "boom" {
		t.Fatalf("packet message = %q, want boom", msg)
	}
	// Nil error uses default message.
	finish = delegatePermanentStartFailure(nil, "construction_failed")
	if err := json.Unmarshal(finish.packet.Message, &msg); err != nil {
		t.Fatal(err)
	}
	if msg != "delegate start failed" {
		t.Fatalf("packet message = %q, want default", msg)
	}
	// Long error is truncated to 512 bytes.
	longErr := errors.New(strings.Repeat("x", 600))
	finish = delegatePermanentStartFailure(longErr, "construction_failed")
	if err := json.Unmarshal(finish.packet.Message, &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg) > 512 {
		t.Fatalf("message len = %d, want <= 512", len(msg))
	}
}

func TestStableDelegateResult(t *testing.T) {
	t.Parallel()
	descriptor := delegatestore.Descriptor{
		ChildSessionID:  "child",
		TranscriptRef:   "local:child",
		ResolvedModel:   "gpt-5.2",
		ResolvedProfileID: "openai",
	}
	committed := delegateUpdatePlan{rows: []delegateSnapshot{
		{id: "dlg_1", lifecycle: delegateLifecycleIdle, resumable: true},
	}}
	result := stableDelegateResult(descriptor, "dlg_1", committed, delegateMutationPlans{}, nil)
	if result.DelegateID != "dlg_1" || result.ChildSessionID != "child" || result.Type != delegateResourceType {
		t.Fatalf("result = %+v", result)
	}
	if result.Status != "idle" {
		t.Errorf("status = %q, want idle for idle lifecycle", result.Status)
	}
	if result.Resumable == nil || !*result.Resumable {
		t.Errorf("resumable = %v, want true", result.Resumable)
	}
	if result.Model != "openai/gpt-5.2" {
		t.Errorf("model = %q", result.Model)
	}
	if result.TranscriptRef != "local:child" {
		t.Errorf("transcriptRef = %q", result.TranscriptRef)
	}
	// With a last outcome, Reason is populated.
	outcome := &delegatestore.Outcome{Status: delegatestore.OutcomeFailed, Reason: "timeout"}
	committedWithOutcome := delegateUpdatePlan{rows: []delegateSnapshot{
		{id: "dlg_1", lifecycle: delegateLifecycleIdle, lastOutcome: outcome},
	}}
	result2 := stableDelegateResult(descriptor, "dlg_1", committedWithOutcome, delegateMutationPlans{}, errors.New("err"))
	if result2.Reason != "timeout" {
		t.Errorf("reason = %q, want timeout", result2.Reason)
	}
	if result2.Err == nil {
		t.Error("err should be propagated")
	}
	// Sandbox descriptor produces a sandbox report.
	network := false
	descriptorWithSandbox := descriptor
	descriptorWithSandbox.Sandbox = &delegatestore.SandboxSnapshot{Mode: "off", Network: &network}
	result3 := stableDelegateResult(descriptorWithSandbox, "dlg_1", committed, delegateMutationPlans{}, nil)
	if result3.Sandbox == nil || result3.Sandbox.Mode != "off" || result3.Sandbox.Network {
		t.Fatalf("sandbox = %+v", result3.Sandbox)
	}
	// Sandbox with nil network defaults to true.
	descriptorWithSandbox2 := descriptor
	descriptorWithSandbox2.Sandbox = &delegatestore.SandboxSnapshot{Mode: "workspace-write"}
	result4 := stableDelegateResult(descriptorWithSandbox2, "dlg_1", committed, delegateMutationPlans{}, nil)
	if result4.Sandbox == nil || !result4.Sandbox.Network {
		t.Fatalf("sandbox = %+v, want network=true default", result4.Sandbox)
	}
	// Empty lifecycle maps to StatusRunning.
	emptyCommitted := delegateUpdatePlan{}
	result5 := stableDelegateResult(descriptor, "dlg_1", emptyCommitted, delegateMutationPlans{}, nil)
	if result5.Status != jobstore.StatusRunning {
		t.Errorf("status = %q, want running for empty snapshot", result5.Status)
	}
}

func TestLatestDelegateMutationSnapshot(t *testing.T) {
	t.Parallel()
	// Finds the matching delegate ID in committed rows.
	committed := delegateUpdatePlan{rows: []delegateSnapshot{
		{id: "dlg_other"},
		{id: "dlg_1", lifecycle: delegateLifecycleRunning},
	}}
	got := latestDelegateMutationSnapshot("dlg_1", committed, delegateMutationPlans{})
	if got.id != "dlg_1" || got.lifecycle != delegateLifecycleRunning {
		t.Fatalf("snapshot = %+v", got)
	}
	// Falls back to last committed row when ID not found.
	got = latestDelegateMutationSnapshot("dlg_missing", committed, delegateMutationPlans{})
	if got.id != "dlg_1" {
		t.Fatalf("fallback snapshot = %+v, want last row", got)
	}
	// Plans updates override committed.
	plans := delegateMutationPlans{
		updates: []delegateUpdatePlan{{rows: []delegateSnapshot{{id: "dlg_1", lifecycle: delegateLifecycleIdle}}}},
	}
	got = latestDelegateMutationSnapshot("dlg_1", committed, plans)
	if got.lifecycle != delegateLifecycleIdle {
		t.Fatalf("snapshot from plans = %+v, want idle", got)
	}
	// Empty everything returns zero.
	got = latestDelegateMutationSnapshot("dlg_1", delegateUpdatePlan{}, delegateMutationPlans{})
	if got.id != "" {
		t.Fatalf("snapshot = %+v, want zero", got)
	}
}

func TestStableDelegateSandboxSnapshot(t *testing.T) {
	t.Parallel()
	// Nil policy returns nil.
	if got := stableDelegateSandboxSnapshot(nil); got != nil {
		t.Fatal("nil policy should return nil")
	}
	// ModeOff returns nil.
	off := &sandbox.SandboxPolicy{Mode: sandbox.ModeOff}
	if got := stableDelegateSandboxSnapshot(off); got != nil {
		t.Fatal("ModeOff should return nil")
	}
	// Active policy produces a snapshot with copied slices.
	net := true
	policy := &sandbox.SandboxPolicy{
		Mode:               sandbox.ModeWorkspaceWrite,
		DenylistAdd:        []string{"a"},
		DenylistRemove:     []string{"b"},
		ExtraWritableRoots: []string{"c"},
		ExtraReadRoots:     []string{"d"},
		Network:            &net,
	}
	got := stableDelegateSandboxSnapshot(policy)
	if got == nil || got.Mode != "workspace-write" {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Network == nil || !*got.Network {
		t.Error("network not copied")
	}
	if len(got.DenylistAdd) != 1 || got.DenylistAdd[0] != "a" {
		t.Errorf("denylistAdd = %v", got.DenylistAdd)
	}
	// Nil network stays nil.
	policy.Network = nil
	got = stableDelegateSandboxSnapshot(policy)
	if got.Network != nil {
		t.Error("nil network should stay nil")
	}
}

func TestSandboxPolicyFromStableSnapshot(t *testing.T) {
	t.Parallel()
	// Nil returns nil.
	if got := sandboxPolicyFromStableSnapshot(nil); got != nil {
		t.Fatal("nil snapshot should return nil")
	}
	// Invalid mode returns nil.
	got := sandboxPolicyFromStableSnapshot(&delegatestore.SandboxSnapshot{Mode: "invalid"})
	if got != nil {
		t.Fatal("invalid mode should return nil")
	}
	// Valid snapshot round-trips.
	net := true
	snap := &delegatestore.SandboxSnapshot{
		Mode:               "workspace-write",
		DenylistAdd:        []string{"a"},
		ExtraWritableRoots: []string{"c"},
		Network:            &net,
	}
	got = sandboxPolicyFromStableSnapshot(snap)
	if got == nil || got.Mode != sandbox.ModeWorkspaceWrite {
		t.Fatalf("policy = %+v", got)
	}
	if got.Network == nil || !*got.Network {
		t.Error("network not copied")
	}
	// Nil network stays nil.
	snap.Network = nil
	got = sandboxPolicyFromStableSnapshot(snap)
	if got.Network != nil {
		t.Error("nil network should stay nil")
	}
}

func TestDescriptorProvenance(t *testing.T) {
	t.Parallel()
	// Nil provenance returns nil.
	if got := descriptorProvenance(delegatestore.Descriptor{}); got != nil {
		t.Fatal("nil provenance should return nil")
	}
	// Non-nil provenance returns a clone. Clone returns NilIfEmpty, so
	// ChainTruncated=true keeps it non-empty.
	orig := &provenance.Causal{ChainTruncated: true}
	got := descriptorProvenance(delegatestore.Descriptor{Provenance: orig})
	if got == nil || !got.ChainTruncated {
		t.Fatalf("provenance = %+v, want clone with ChainTruncated=true", got)
	}
}

func TestDelegateRestoreOperationalIOError(t *testing.T) {
	t.Parallel()
	// Nil and ErrNotExist are not operational errors.
	if delegateRestoreOperationalIOError(nil) {
		t.Error("nil should not be operational")
	}
	if delegateRestoreOperationalIOError(os.ErrNotExist) {
		t.Error("ErrNotExist should not be operational")
	}
	// PathError is operational.
	if !delegateRestoreOperationalIOError(&os.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}) {
		t.Error("PathError should be operational")
	}
	// SyscallError is operational.
	if !delegateRestoreOperationalIOError(&os.SyscallError{Syscall: "open", Err: syscall.EIO}) {
		t.Error("SyscallError should be operational")
	}
	// Raw errno is operational.
	if !delegateRestoreOperationalIOError(syscall.EIO) {
		t.Error("Errno should be operational")
	}
	// Generic error is not operational.
	if delegateRestoreOperationalIOError(errors.New("generic")) {
		t.Error("generic error should not be operational")
	}
}

func TestMissingDelegateRestoreInputReason(t *testing.T) {
	t.Parallel()
	stat := func(path string) (os.FileInfo, error) { return os.Stat(path) }
	readFile := func(path string) ([]byte, error) { return os.ReadFile(path) }

	// Missing resume metadata (empty child session ID).
	reason, err := missingDelegateRestoreInputReason("/tmp", delegatestore.Descriptor{}, stat, readFile)
	if err != nil || reason != notResumableMissingDelegateResumeMetadata {
		t.Fatalf("empty descriptor: reason=%q err=%v", reason, err)
	}

	// Malformed transcript ref (childID mismatch).
	desc := delegatestore.Descriptor{
		ChildSessionID:      "child",
		Task:                "task",
		AgentType:           "general",
		ResolvedProfileID:   "openai",
		ResolvedModel:        "gpt-5.2",
		TranscriptRef:       "local:other",
	}
	reason, err = missingDelegateRestoreInputReason("/tmp", desc, stat, readFile)
	if err != nil || reason != notResumableParentLinkageUnavailable {
		t.Fatalf("mismatched ref: reason=%q err=%v", reason, err)
	}

	// Empty stateDir -> missing child session meta.
	desc.TranscriptRef = "local:child"
	reason, err = missingDelegateRestoreInputReason("", desc, stat, readFile)
	if err != nil || reason != notResumableMissingChildSessionMeta {
		t.Fatalf("empty stateDir: reason=%q err=%v", reason, err)
	}

	// WorkingDir missing -> working_dir_missing.
	tmpDir := t.TempDir()
	missingDir := filepath.Join(tmpDir, "nonexistent")
	desc.WorkingDir = missingDir
	reason, err = missingDelegateRestoreInputReason(tmpDir, desc, stat, readFile)
	if err != nil || reason != notResumableWorkingDirMissing {
		t.Fatalf("missing working dir: reason=%q err=%v", reason, err)
	}

	// WorkingDir stat error (non-ErrNotExist) -> operational error.
	desc.WorkingDir = missingDir
	errStat := func(string) (os.FileInfo, error) { return nil, syscall.EIO }
	reason, err = missingDelegateRestoreInputReason(tmpDir, desc, errStat, readFile)
	if err == nil {
		t.Fatalf("stat error should propagate: reason=%q", reason)
	}

	// Missing child session meta file.
	desc.WorkingDir = ""
	reason, err = missingDelegateRestoreInputReason(tmpDir, desc, stat, readFile)
	if err != nil || reason != notResumableMissingChildSessionMeta {
		t.Fatalf("missing meta file: reason=%q err=%v", reason, err)
	}

	// Corrupt child session meta (invalid JSON).
	childID := "02" + strings.Repeat("a", 20)
	metaPath := filepath.Join(tmpDir, sessionsSubdir, childID+".meta.json")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	desc.ChildSessionID = childID
	desc.TranscriptRef = encodeRef("", childID)
	reason, err = missingDelegateRestoreInputReason(tmpDir, desc, stat, readFile)
	if err != nil || reason != notResumableCorruptChildSessionMeta {
		t.Fatalf("corrupt meta: reason=%q err=%v", reason, err)
	}

	// Meta with wrong ID.
	validMeta, _ := json.Marshal(schema.SessionMeta{ID: "wrong"})
	if err := os.WriteFile(metaPath, validMeta, 0o644); err != nil {
		t.Fatal(err)
	}
	reason, err = missingDelegateRestoreInputReason(tmpDir, desc, stat, readFile)
	if err != nil || reason != notResumableCorruptChildSessionMeta {
		t.Fatalf("wrong ID meta: reason=%q err=%v", reason, err)
	}

	// Valid meta but missing transcript -> missing child transcript.
	correctMeta, _ := json.Marshal(schema.SessionMeta{ID: childID})
	if err := os.WriteFile(metaPath, correctMeta, 0o644); err != nil {
		t.Fatal(err)
	}
	reason, err = missingDelegateRestoreInputReason(tmpDir, desc, stat, readFile)
	if err != nil || reason != notResumableMissingChildTranscript {
		t.Fatalf("missing transcript: reason=%q err=%v", reason, err)
	}
}

func TestDelegateInputWasPreseeded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// No preseeded input in context.
	if delegateInputWasPreseeded(ctx, "s1", "hello") {
		t.Error("empty context should not have preseeded input")
	}
	// With preseeded input matching.
	ctxWithInput := context.WithValue(ctx, delegatePreseededInputContextKey{}, delegatePreseededInput{sessionID: "s1", input: "hello"})
	if !delegateInputWasPreseeded(ctxWithInput, "s1", "hello") {
		t.Error("matching preseeded input should return true")
	}
	// Non-matching session.
	if delegateInputWasPreseeded(ctxWithInput, "s2", "hello") {
		t.Error("non-matching session should return false")
	}
	// Non-matching input.
	if delegateInputWasPreseeded(ctxWithInput, "s1", "world") {
		t.Error("non-matching input should return false")
	}
}

func TestDelegateQuietAttentionIDAndContent(t *testing.T) {
	t.Parallel()
	lease := delegateLease{delegateID: "dlg_1", generation: 3}
	id := delegateQuietAttentionID(lease)
	if id != "quiet:dlg_1:3:1" {
		t.Fatalf("quiet attention ID = %q, want quiet:dlg_1:3:1", id)
	}
	stretched := delegateQuietAttentionIDForStretch(lease, 5)
	if stretched != "quiet:dlg_1:3:5" {
		t.Fatalf("stretched ID = %q, want quiet:dlg_1:3:5", stretched)
	}
	content := delegateQuietAttentionContent(lease, time.Unix(1000, 0).UTC())
	if !strings.Contains(content, "dlg_1") {
		t.Fatalf("content = %q, missing delegate id", content)
	}
	if !strings.HasPrefix(content, "<delegate-notification") {
		t.Fatalf("content = %q, missing prefix", content)
	}
}

func TestStableDelegateRole(t *testing.T) {
	t.Parallel()
	s := newSubagentSessionForAvailability(t, "", 1)
	// Custom agent with system prompt takes priority.
	agent := &plugin.Agent{Name: "custom", SystemPrompt: "custom instructions"}
	selection := subagentModelSelection{agent: agent}
	name, prompt := stableDelegateRole(selection, false, s)
	if name != "custom" || prompt != "custom instructions" {
		t.Fatalf("name=%q prompt=%q, want custom agent", name, prompt)
	}
	// No agent but child can delegate -> delegating instructions.
	name, prompt = stableDelegateRole(subagentModelSelection{}, true, s)
	if name != "subagent" || prompt != defaultDelegatingSubagentInstructions {
		t.Fatalf("name=%q prompt=%q, want delegating subagent", name, prompt)
	}
	// No agent, no delegation, with pluginAgents["subagent"] entry -> plugin's
	// system prompt (the test session registers a "subagent" plugin agent).
	name, prompt = stableDelegateRole(subagentModelSelection{}, false, s)
	if name != "subagent" {
		t.Fatalf("name=%q, want subagent", name)
	}
	if prompt == defaultSubagentInstructions {
		t.Error("prompt should be the plugin agent's instructions, not the default")
	}
	if prompt == "" {
		t.Error("prompt should not be empty")
	}
	// Agent with empty system prompt falls through.
	emptyAgent := &plugin.Agent{Name: "empty", SystemPrompt: "  "}
	selection = subagentModelSelection{agent: emptyAgent}
	name, prompt = stableDelegateRole(selection, false, s)
	if name == "empty" {
		t.Fatalf("name=%q, should fall through empty-prompt agent", name)
	}
}

func TestPopulateStableDelegateSendResult(t *testing.T) {
	t.Parallel()
	// Nil result is a no-op.
	populateStableDelegateSendResult(nil, delegatestore.TerminalPacket{})
	// Full packet populates all fields.
	resumable := false
	valid := true
	metadata, _ := json.Marshal(delegateTerminalPacketMetadata{
		Task:            "inspect",
		Description:     "desc",
		AgentType:       "general",
		RequestedModel:  "gpt-5.2",
		ResolvedProfileID: "openai",
		ResolvedModel:   "gpt-5.2",
		ReasoningEffort: "high",
		RunStartedAt:    "2026-01-01T00:00:00Z",
		RunEndedAt:      "2026-01-01T00:10:00Z",
		LatestActivityAt: "2026-01-01T00:09:00Z",
		CumulativeUsage: &schema.CumulativeUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
		Worktree:        &delegateTerminalWorktreeReport{Path: "/wt", Branch: "br", HeadSHA: "abc", Ahead: 2, Dirty: true},
		ExhaustionBudget: delegatestore.ExhaustionBudgetTurns,
		ExhaustionLimit:  5,
		ExhaustionResumable: &resumable,
	})
	packet := delegatestore.TerminalPacket{
		Kind:                  delegatestore.PacketReported,
		Message:               json.RawMessage(`"output text"`),
		StructuredResult:       json.RawMessage(`{"ok":true}`),
		StructuredResultValid:  &valid,
		StructuredResultReason: "validated",
		Warnings:              []string{"w1", "w2"},
		Metadata:              metadata,
	}
	var result sendMessageResult
	populateStableDelegateSendResult(&result, packet)
	if result.Task != "inspect" || result.Description != "desc" || result.AgentType != "general" {
		t.Fatalf("task/desc/agent = %q/%q/%q", result.Task, result.Description, result.AgentType)
	}
	if result.RequestedModel != "gpt-5.2" || result.ResolvedProfileID != "openai" || result.ResolvedModel != "gpt-5.2" {
		t.Fatalf("model fields = %q/%q/%q", result.RequestedModel, result.ResolvedProfileID, result.ResolvedModel)
	}
	if result.ReasoningEffort != "high" {
		t.Errorf("reasoningEffort = %q", result.ReasoningEffort)
	}
	if result.RunStartedAt != "2026-01-01T00:00:00Z" || result.RunEndedAt != "2026-01-01T00:10:00Z" {
		t.Errorf("run timestamps = %q/%q", result.RunStartedAt, result.RunEndedAt)
	}
	if result.Output != "output text" {
		t.Errorf("output = %q", result.Output)
	}
	if result.StructuredResultValidSet != true || !result.StructuredResultValid {
		t.Errorf("structuredValid = %v/%v", result.StructuredResultValidSet, result.StructuredResultValid)
	}
	if result.StructuredResultReason != "validated" {
		t.Errorf("structuredResultReason = %q", result.StructuredResultReason)
	}
	if len(result.Warnings) != 2 || result.Warnings[0] != "w1" {
		t.Errorf("warnings = %v", result.Warnings)
	}
	if result.CumulativeUsage == nil || result.CumulativeUsage.InputTokens != 100 {
		t.Errorf("cumulativeUsage = %+v", result.CumulativeUsage)
	}
	if result.Worktree == nil || result.Worktree.Path != "/wt" || result.Worktree.Ahead != 2 {
		t.Errorf("worktree = %+v", result.Worktree)
	}
	// Exhaustion fields are populated by delegatePreparedFinish only for
	// non-reported (exhausted) packets; for PacketReported the finish
	// returns before the exhaustion branch, so they stay zero here.
	// Status should be Completed for a reported packet.
	if result.Status != jobstore.StatusCompleted {
		t.Errorf("status = %q, want completed", result.Status)
	}
}

func TestPopulateStableDelegateSendResult_InvalidMetadata(t *testing.T) {
	t.Parallel()
	packet := delegatestore.TerminalPacket{
		Kind:     delegatestore.PacketTerminalError,
		Metadata: json.RawMessage(`{invalid`),
	}
	var result sendMessageResult
	populateStableDelegateSendResult(&result, packet)
	// With invalid metadata, metadata-derived fields stay zero.
	if result.Task != "" || result.Worktree != nil {
		t.Fatalf("invalid metadata should leave fields empty: task=%q worktree=%v", result.Task, result.Worktree)
	}
	// Status should be Failed for terminal error.
	if result.Status != jobstore.StatusFailed {
		t.Errorf("status = %q, want failed", result.Status)
	}
}

func TestPopulateStableDelegateSendResult_NoMetadata(t *testing.T) {
	t.Parallel()
	// Packet with no metadata: finish comes from delegatePreparedFinish with
	// default outcome.
	packet := delegatestore.TerminalPacket{
		Kind:    delegatestore.PacketReported,
		Message: json.RawMessage(`"done"`),
	}
	var result sendMessageResult
	populateStableDelegateSendResult(&result, packet)
	if result.Status != jobstore.StatusCompleted {
		t.Errorf("status = %q, want completed", result.Status)
	}
	if result.Output != "done" {
		t.Errorf("output = %q", result.Output)
	}
}

func TestPopulateStableDelegateSendResult_StructuredResultOnly(t *testing.T) {
	t.Parallel()
	// StructuredResult present but StructuredResultValid nil.
	packet := delegatestore.TerminalPacket{
		Kind:             delegatestore.PacketReported,
		StructuredResult: json.RawMessage(`{"result":"data"}`),
	}
	var result sendMessageResult
	populateStableDelegateSendResult(&result, packet)
	if !result.StructuredResultValidSet {
		t.Error("StructuredResultValidSet should be true when StructuredResult is present")
	}
	if result.StructuredResultValid {
		t.Error("StructuredResultValid should be false (default) when packet value is nil")
	}
}
