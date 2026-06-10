# Job Control Open-Decision Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the resolved job-control open decisions from `docs/superpowers/specs/2026-06-10-job-control-open-decision-fixes.md`.

**Architecture:** Keep durable facts in `agent/internal/jobstore`, keep runtime orchestration in `agent/job_delegate.go` and `agent/job_watch.go`, and keep public tool projections in `agent/session_tools_jobs.go`. Prefer typed internal state over prose error matching, and make every restore decision derive from persisted descriptors plus retained child session state.

**Tech Stack:** Go, Serf agent/jobstore internals, existing `go test` package tests, `make build`, `make test`, `make lint`.

---

## File Map

- `agent/internal/jobstore/event.go`, `record.go`, `fold.go`, `watch.go`, tests: durable event/record fields for delegate restore descriptors, structured-result reasons, and pending watch-send states.
- `agent/job_delegate.go`, `subagents.go`, `subagent_manager.go`, `session_init.go`: delegate descriptor capture, launch ordering, strict restore preflight, child runtime reconstruction, and restored resume.
- `agent/job_watch.go`, `job_watch_test.go`, `job_watch_observer_test.go`: alias validation, contextual `watched`, durable watch-send coalescing, delivery retry, cleanup, diagnostics, and loop suppression.
- `agent/session_tools_jobs.go`, `session_tools_jobs_test.go`: public result shapes for `delegate`, `job_send_message`, `job_read_output`, `job_watch`, and `job_list`.
- `agent/transcript_read.go`, `transcript_test.go`: strict transcript reader used only for restore preflight.
- `agent/internal/tool/definitions.go`, `agent/prompts/`, `docs/job-control.md`, `docs/superpowers/plans/2026-06-09-job-control-contract-cleanup.md`: public contract and model-facing wording cleanup.

## Execution Rules

- Use subagent-driven-development task-by-task. Dispatch a fresh implementer for each task.
- After each implementer commit, run a spec-compliance reviewer first, then a code-quality reviewer. Do not start the code-quality review until spec compliance is approved.
- If a reviewer finds a non-low issue, route it back to an implementer and re-review.
- The controller should not implement code directly after this plan lands.
- Do not add backward compatibility for `main`, public sidecar/private markers, or synonym APIs.

---

### Task 1: Alias Vocabulary And Target Errors

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/job_watch.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/job_watch_test.go`
- Modify: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing tests for `main` removal**

Add focused tests proving `main` is unknown everywhere and does not create side effects:

```go
func TestJobSendMessageMainAliasFailsTargetNotFound(t *testing.T) {
	s := newTestSession(t)
	called := false
	s.cfg.spawn.parentSteer = func(string) { called = true }

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "main",
		Message: "hello",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", res.Err)
	}
	if called {
		t.Fatal("main alias called parentSteer")
	}
}
```

Add equivalent tool-level/watch tests for `job_watch(target:"main")` and `job_watch(send.to:"main")` asserting no watch is registered and no pending send exists.

- [ ] **Step 2: Write failing tests for contextual `watched`**

Add direct-context failure and watch-context success tests:

```go
func TestJobSendMessageWatchedWithoutWatchContextFails(t *testing.T) {
	s := newTestSession(t)
	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "watched",
		Message: "hello",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", res.Err)
	}
}
```

Add a watch delivery test where `send.to:"watched"` on a concrete job resolves to the concrete watched target, and session-event watches that lack a concrete messageable watched identity reject `send.to:"watched"` for `assistant.message`, `assistant.tool`, and `communicate`.

- [ ] **Step 3: Run alias tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*MainAlias|Test.*Watched' -count=1
```

Expected: new tests fail because `main` and `watched` are still broad runtime aliases.

- [ ] **Step 4: Implement alias resolution**

Change normal runtime alias resolution so only `caller` is accepted. Add an internal watch-context path for `watched` instead of treating it as a normal model-facing alias.

Validation rules to implement:

```go
func isRuntimeMessageAlias(target string) bool {
	return target == runtimeMessageAliasCaller
}

func isUnsupportedRuntimeMessageAlias(target string) bool {
	return target == "main" || target == runtimeMessageAliasWatched
}
```

`job_watch.target` rejects `main` and `watched` synchronously. `job_watch.send.to` rejects `main` synchronously; it accepts `watched` only for trigger kinds that carry a concrete messageable watched identity.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*MainAlias|Test.*Watched|TestJobWatch' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add agent/job_delegate.go agent/job_watch.go agent/job_delegate_test.go agent/job_watch_test.go agent/session_tools_jobs_test.go
git commit -m "fix(job-control): remove main runtime alias"
```

---

### Task 2: Jobstore Schema, Descriptor, And Structured Reason Foundations

**Files:**
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/fold.go`
- Modify: `agent/internal/jobstore/event_test.go`
- Modify: `agent/internal/jobstore/fold_test.go`
- Modify: `agent/internal/jobstore/record_test.go`

- [ ] **Step 1: Write failing jobstore tests**

Add tests proving delegate restore descriptors, `result_schema`, and `structured_result_reason` round-trip and fold:

```go
func TestFoldDelegateDescriptorSchemaAndStructuredReason(t *testing.T) {
	valid := false
	events := []Event{
		{
			Kind:  EventJobStarted,
			Seq:   1,
			JobID: "job_1",
			Type:  JobDelegate,
			DelegateRestore: &DelegateRestoreDescriptor{
				Version:        1,
				ChildSessionID: "child_1",
				TranscriptRef:  "transcript_1",
				ResultSchema:   map[string]any{"type": "object"},
			},
		},
		{
			Kind:                   EventJobFinished,
			Seq:                    2,
			JobID:                  "job_1",
			Status:                 StatusCompleted,
			StructuredResultValid:  &valid,
			StructuredResultReason: "schema_result_missing",
		},
	}
	rec := Fold(events)["job_1"]
	if rec.DelegateRestore == nil || rec.DelegateRestore.ResultSchema == nil {
		t.Fatalf("delegate restore/schema not folded: %+v", rec.DelegateRestore)
	}
	if rec.StructuredResultReason != "schema_result_missing" {
		t.Fatalf("reason = %q", rec.StructuredResultReason)
	}
}
```

- [ ] **Step 2: Add durable types and fields**

Add these fields to `jobstore.Event` and `jobstore.JobRecord`:

```go
DelegateRestore          *DelegateRestoreDescriptor `json:"delegate_restore,omitempty"`
StructuredResultReason   string                     `json:"structured_result_reason,omitempty"`
```

Add the descriptor type:

```go
type DelegateRestoreDescriptor struct {
	Version              int    `json:"version"`
	ChildSessionID       string `json:"child_session_id"`
	TranscriptRef         string `json:"transcript_ref"`
	ParentSessionID       string `json:"parent_session_id,omitempty"`
	ParentJobID           string `json:"parent_job_id,omitempty"`
	OwnerSessionID        string `json:"owner_session_id,omitempty"`
	VisibleSessionID      string `json:"visible_session_id,omitempty"`
	OriginTurnID          string `json:"origin_turn_id,omitempty"`
	OriginToolCallID      string `json:"origin_tool_call_id,omitempty"`
	Task                  string `json:"task,omitempty"`
	AgentType             string `json:"agent_type,omitempty"`
	RequestedModel        string `json:"requested_model,omitempty"`
	ResolvedProfileID     string `json:"resolved_profile_id,omitempty"`
	ResolvedModel         string `json:"resolved_model,omitempty"`
	ReasoningEffort       string `json:"reasoning_effort,omitempty"`
	AgentName             string `json:"agent_name,omitempty"`
	FrozenRolePrompt      string `json:"frozen_role_prompt,omitempty"`
	FrozenTaskPrompt      string `json:"frozen_task_prompt,omitempty"`
	FrozenToolNames       []string `json:"frozen_tool_names,omitempty"`
	FrozenSkillNames      []string `json:"frozen_skill_names,omitempty"`
	WorkingDir            string `json:"working_dir,omitempty"`
	LocalEnvPolicy        string `json:"local_env_policy,omitempty"`
	ResultSchema          any    `json:"result_schema,omitempty"`
	ExplicitToolGrants    []string `json:"explicit_tool_grants,omitempty"`
}
```

Adjust field names to match existing terminology if the codebase already has exact names for origin turn/tool-call IDs or local env policy.

- [ ] **Step 3: Fold descriptor and reason**

Fold `DelegateRestore` from `EventJobStarted`. Fold `StructuredResultReason` from `EventJobFinished` only when `StructuredResultValid` is present and false.

Do not add a separate `structured_result_error` field.

- [ ] **Step 4: Run jobstore tests**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test.*Delegate.*Descriptor|Test.*RestoreDescriptor|Test.*Structured|Test.*Reason' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/jobstore
git commit -m "fix(job-control): add delegate restore jobstore fields"
```

---

### Task 3: Structured Result Runtime Projection

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing public projection tests**

Add tests for:

- initial foreground `delegate(result_schema=...)` with missing structured output emits no `structured_result`, `structured_result_valid:false`, and `structured_result_reason:"schema_result_missing"`;
- initial background delegate persists the same invalid shape and `job_read_output` returns it;
- no schema plus no structured output omits all structured validity and reason fields;
- persisted oversized structured result emits `schema_result_too_large`;
- bounded tool response omission emits `projection_too_large` without mutating the durable record.

Use this assertion pattern for absent fields:

```go
var parsed map[string]any
if err := json.Unmarshal([]byte(out), &parsed); err != nil {
	t.Fatal(err)
}
if _, ok := parsed["structured_result"]; ok {
	t.Fatal("structured_result present for invalid/missing schema result")
}
if parsed["structured_result_valid"] != false {
	t.Fatalf("structured_result_valid = %v, want false", parsed["structured_result_valid"])
}
if parsed["structured_result_reason"] != "schema_result_missing" {
	t.Fatalf("reason = %v", parsed["structured_result_reason"])
}
```

- [ ] **Step 2: Run projection tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*Structured|Test.*ResultSchema|Test.*Projection|Test.*Stale' -count=1
```

Expected: fail because structured-result reasons are not fully threaded.

- [ ] **Step 3: Thread reason fields through runtime outputs**

Add `StructuredResultReason string` to `delegateResult`, `sendMessageResult`, `delegateToolResult`, `jobSendMessageDelegateResult`, and `jobReadOutputResult`.

Set reason values exactly:

- `schema_validation_failed`
- `schema_result_missing`
- `schema_result_too_large`
- `schema_capture_failed`
- `projection_too_large`

When no schema was requested and no structured result was captured, omit `structured_result`, `structured_result_valid`, and `structured_result_reason`.

- [ ] **Step 4: Persist schema in each delegate descriptor**

When launching an initial or resumed delegate job, copy the inherited schema into that job's own `DelegateRestoreDescriptor.ResultSchema`. Do not make resumed jobs depend on a pointer to an earlier job record as the only schema authority.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*Structured|Test.*ResultSchema|Test.*Projection|Test.*Stale|TestJobSendMessageForegroundResumeReturnsTerminalResult' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add agent/job_delegate.go agent/session_tools_jobs.go agent/job_delegate_test.go agent/session_tools_jobs_test.go
git commit -m "fix(job-control): expose structured result reasons"
```

---

### Task 4: Durable Watch-Send Pending State

**Files:**
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/fold.go`
- Modify: `agent/internal/jobstore/watch.go`
- Modify: `agent/internal/jobstore/watch_test.go`
- Modify: `agent/job_watch.go`
- Modify: `agent/job_watch_test.go`

- [ ] **Step 1: Write failing jobstore fold tests**

Add tests for pending/delivered/dropped/evicted fold behavior:

```go
func TestFoldWatchSendPendingLatestWinsAndTerminalRemoves(t *testing.T) {
	key := WatchSendKey{
		VisibleSessionID: "root",
		WatchTarget: "job_A",
		ResolvedWatchedIdentity: "job_A",
		ResolvedSendTo: "job_sidecar",
		WatchGeneration: "wg_1",
	}
	events := []Event{
		{Kind: EventWatchSendPending, Seq: 1, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", Message: "first"}},
		{Kind: EventWatchSendPending, Seq: 2, WatchSend: &WatchSendState{Key: key, DeliveryID: "d2", Message: "latest", CoalescedCount: 1}},
	}
	rec := FoldWatchSends(events)
	if got := rec.Pending[key].Message; got != "latest" {
		t.Fatalf("message = %q, want latest", got)
	}
	events = append(events, Event{Kind: EventWatchSendDelivered, Seq: 3, WatchSend: &WatchSendState{Key: key, DeliveryID: "d2"}})
	if got := FoldWatchSends(events).Pending; len(got) != 0 {
		t.Fatalf("pending after delivered = %+v", got)
	}
}
```

- [ ] **Step 2: Add durable event kinds and state**

Add event kinds:

```go
EventWatchSendPending   EventKind = "watch_send_pending"
EventWatchSendDelivered EventKind = "watch_send_delivered"
EventWatchSendDropped   EventKind = "watch_send_dropped"
EventWatchSendEvicted   EventKind = "watch_send_evicted"
```

Add durable key/state:

```go
type WatchSendKey struct {
	VisibleSessionID string `json:"visible_session_id"`
	WatchTarget string `json:"watch_target"`
	ResolvedWatchedIdentity string `json:"resolved_watched_identity"`
	ResolvedSendTo string `json:"resolved_send_to"`
	WatchGeneration string `json:"watch_generation"`
}

type WatchSendState struct {
	Key WatchSendKey `json:"key"`
	DeliveryID string `json:"delivery_id"`
	UpdateSeq uint64 `json:"update_seq,omitempty"`
	Message string `json:"message,omitempty"`
	Frame string `json:"frame,omitempty"`
	TriggerIdentity string `json:"trigger_identity,omitempty"`
	TriggerReason string `json:"trigger_reason,omitempty"`
	CoalescedCount int `json:"coalesced_count,omitempty"`
	DiagnosticReason string `json:"diagnostic_reason,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}
```

Use existing timestamp style if jobstore events already represent time differently.

- [ ] **Step 3: Allocate durable watch generations**

At watch configuration time, allocate `watch_generation` as a durable unique value that cannot be reused after restart. A UUID-like opaque id is acceptable. Do not derive it from an in-memory counter that resets.

Add tests that configure a watch, persist a pending frame, restore, configure the same target/send pair again, and assert the new watch generation is different and does not overwrite the old pending frame.

- [ ] **Step 4: Snapshot pending frames at trigger time**

When a watch fires and delivery is not immediately possible, append `watch_send_pending` with the exact bounded message/frame to send. Retry must use this stored payload and must not reread later job output.

Maintain at most one pending frame per coalescing key. A later frame with the same key replaces the pending payload and increments `CoalescedCount`.

- [ ] **Step 5: Enforce cleanup and cap rules**

Implement cleanup for explicit watch clear/replacement, watched-target prune, job-manager/session close, delivered, dropped, and evicted states. Terminal expiry of a concrete watch must not delete already-fired pending frames.

Enforce a default cap of 32 pending keys per watch generation. When exceeded, append `watch_send_evicted` for the oldest pending key and emit one caller-visible diagnostic naming the evicted trigger.

- [ ] **Step 6: Run focused tests**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test.*WatchSend|Test.*Watch' -count=1
go test ./agent -run 'TestWatchSend.*Pending|TestWatchSend.*Generation|TestWatchSend.*Clear|TestWatchSend.*Cap|TestWatchSend.*Snapshot' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add agent/internal/jobstore agent/job_watch.go agent/job_watch_test.go
git commit -m "fix(job-control): persist pending watch sends"
```

---

### Task 5: Watch-Send Delivery And Retry Semantics

**Files:**
- Modify: `agent/job_watch.go`
- Modify: `agent/jobs.go`
- Modify: `agent/job_watch_test.go`
- Modify: `agent/job_watch_observer_test.go`

- [ ] **Step 1: Write failing delivery tests**

Add tests for:

- fake busy send keeps a pending frame and emits no diagnostic;
- deterministic retry after idle delivers only the latest coalesced frame;
- `watch_send_delivered` is appended only after `job_send_message` succeeds;
- crash after send success but before delivered marker causes at-least-once retry after restore with the same delivery id in the sent frame;
- hard failure drops the pending frame and emits exactly one diagnostic across two restores;
- concrete terminal ordering keeps final watch send ahead of terminal notification when immediately deliverable;
- if final pending/diagnostic persistence fails during job finalization, finalization returns/retries without losing terminal state;
- global `FromWatch` suppression prevents watch-originated delegate lifecycle events from triggering watches.

Use this shape for the delivery classifier seam:

```go
type watchSendDeliveryClass int

const (
	watchSendDelivered watchSendDeliveryClass = iota
	watchSendBusy
	watchSendHardFailure
)
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./agent -run 'TestWatchSend.*Busy|TestWatchSend.*Retry|TestWatchSend.*Delivered|TestWatchSend.*Crash|TestWatchSend.*Hard|TestWatchSend.*Terminal|TestWatchOrigin' -count=1
```

Expected: fail because current watch sends diagnose on busy and lack durable retry semantics.

- [ ] **Step 3: Implement typed delivery classification**

Classify watch-originated sends (`FromWatch`) to running delegate sidecars as busy and queue/coalesce. Non-watch model calls to running delegates keep the existing steering behavior.

Unknown, pruned, non-messageable, and non-resumable targets are hard failures. Do not classify by matching incidental error text.

- [ ] **Step 4: Implement event-driven retry hook**

Add a deterministic retry hook that selects pending delivery work under lock, releases locks, then calls `jm.send`, enqueues diagnostics, and appends delivered/dropped events.

Invoke it after a target delegate's resumable terminal state and child idle state are visible. For a delegate finishing its own watch-originated turn, retry pending frames before enqueueing that target's terminal notification.

- [ ] **Step 5: Preserve at-least-once crash semantics**

Append `watch_send_delivered` only after `job_send_message` returns delivered. Include the stable pending generation/delivery id in the watch frame metadata and diagnostics. Accept duplicate delivery after a crash before the delivered marker; do not implement hidden duplicate suppression.

- [ ] **Step 6: Run focused tests**

Run:

```bash
go test ./agent -run 'TestWatchSend|TestJobWatch|TestWatchOrigin' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add agent/job_watch.go agent/jobs.go agent/job_watch_test.go agent/job_watch_observer_test.go
git commit -m "fix(job-control): retry coalesced watch sends"
```

---

### Task 6: Delegate Launch Ordering And Restore Descriptor Population

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/subagents.go`
- Modify: `agent/session_config.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/internal/jobstore/fold_test.go`

- [ ] **Step 1: Write failing descriptor ordering tests**

Add a blocked model adapter test that launches a delegate and blocks the first child model request. Before unblocking the adapter, reopen or inspect the jobstore and assert the delegate job record exists with `DelegateRestore` populated.

Required fields to assert:

- child session id and transcript ref;
- parent session id, parent job id, owner session id, visible session id, origin turn/tool-call ids when available;
- original task;
- requested agent type/model override, resolved profile id/model, reasoning effort, and agent name;
- frozen role/task/tool/skill shaping fields needed to rebuild the child session if plugin definitions drift;
- working directory and local env policy;
- inherited result schema;
- explicit tool grants if present.

- [ ] **Step 2: Run descriptor ordering tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*Delegate.*Descriptor|Test.*Descriptor.*Before.*Model|Test.*Launch.*Ordering' -count=1
```

Expected: fail because the child run can start before durable descriptor state exists or descriptor fields are missing.

- [ ] **Step 3: Split child construction from run launch**

Refactor delegate launch so the sequence is:

1. construct child/session identity and subagent runtime shell;
2. build complete `DelegateRestoreDescriptor`;
3. append `job_started` with descriptor and transcript ref;
4. only then call `launchSubagentRun` or equivalent model-start path.

Do not persist `SessionConfig.spawn` directly. Persist only the descriptor fields from the spec.

- [ ] **Step 4: Apply profile/model lookup contract**

Store both `ResolvedProfileID` and `ResolvedModel` when available. On later restore, use `ResolvedProfileID + ResolvedModel` first; if only `ResolvedModel` exists, resolve it through the current parent profile resolver; if resolution fails, return `profile_unavailable`. Never reinterpret `RequestedModel` as a different provider/profile on restore.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*Delegate.*Descriptor|Test.*Descriptor.*Before.*Model|Test.*Launch.*Ordering' -count=1
go test ./agent/internal/jobstore -run 'Test.*Delegate.*Descriptor|Test.*RestoreDescriptor' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add agent/job_delegate.go agent/subagents.go agent/session_config.go agent/job_delegate_test.go agent/internal/jobstore/fold_test.go
git commit -m "fix(job-control): persist delegate descriptors before launch"
```

---

### Task 7: Strict Delegate Restore Preflight And Job List Resumability

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_init.go`
- Modify: `agent/transcript_read.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/session_tools_jobs_test.go`
- Modify: `agent/transcript_test.go`

- [ ] **Step 1: Write failing preflight tests**

Add table-driven tests for exact synchronous errors and no side effects:

```go
cases := []struct {
	name string
	breakState func(*testing.T, *Session, *jobstore.JobRecord)
	want string
}{
	{"missing descriptor", removeDescriptor, "target_not_resumable:missing_delegate_resume_metadata"},
	{"bad linkage", corruptDescriptorParent, "target_not_resumable:parent_linkage_unavailable"},
	{"missing meta", removeChildMeta, "target_not_resumable:missing_child_session_meta"},
	{"corrupt meta", corruptChildMeta, "target_not_resumable:corrupt_child_session_meta"},
	{"missing transcript", removeChildTranscript, "target_not_resumable:missing_child_transcript"},
	{"corrupt transcript", corruptChildTranscript, "target_not_resumable:corrupt_child_transcript"},
	{"session mismatch", corruptTranscriptSessionID, "target_not_resumable:transcript_session_mismatch"},
	{"busy child", retainConflictingLiveChild, "target_not_resumable:child_session_busy"},
	{"profile unavailable", removeProfile, "target_not_resumable:profile_unavailable"},
}
```

Each case must assert jobstore counts are unchanged and no child run/model request is created.

- [ ] **Step 2: Write failing `job_list` resumability tests**

Add tests proving `job_list` uses the same reason codes dynamically and does not append `job_session_assigned`, terminal, or descriptor events just to report resumability.

- [ ] **Step 3: Run tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*RestorePreflight|Test.*NotResumable|Test.*JobList.*Resumable' -count=1
go test ./agent -run 'Test.*Strict.*Transcript|Test.*Transcript.*Mismatch' -count=1
```

Expected: fail because strict preflight and dynamic job-list assessment do not exist.

- [ ] **Step 4: Implement strict transcript reader**

Add a strict child transcript reader used only for restore preflight. It must validate the transcript header/session id, reject corrupt non-final entries, and may tolerate an incomplete final line only if existing transcript recovery policy already proves all preceding entries valid.

Keep lenient display transcript reads unchanged.

- [ ] **Step 5: Implement shared pure assessor**

Add a pure helper such as:

```go
type DelegateResumability struct {
	Resumable bool
	Reason string
	Preflight *delegateRestorePreflight
}

func (s *Session) AssessDelegateResumability(rec *jobstore.JobRecord) DelegateResumability
```

Use it from both preflight and `job_list`. Apply reason precedence exactly as specified: descriptor, linkage, meta missing/corrupt, transcript missing/corrupt, transcript mismatch, busy child, profile unavailable.

- [ ] **Step 6: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*RestorePreflight|Test.*NotResumable|Test.*JobList.*Resumable|Test.*Strict.*Transcript|Test.*Transcript.*Mismatch' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add agent/job_delegate.go agent/session_init.go agent/transcript_read.go agent/session_tools_jobs.go agent/job_delegate_test.go agent/session_tools_jobs_test.go agent/transcript_test.go
git commit -m "fix(job-control): validate restored delegate state"
```

---

### Task 8: Reconstruct Idle Child Runtime After Restore

**Files:**
- Modify: `agent/session_init.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/subagents.go`
- Modify: `agent/subagent_manager.go`
- Modify: `agent/job_delegate_test.go`

- [ ] **Step 1: Write failing reconstruction tests**

Add tests proving:

- `RestoreSessionFromMetaWithConfig` does not call the model adapter;
- restore does not create a new delegate job;
- restore does not create a retained child runtime entry;
- `job_send_message` after successful preflight lazily reconstructs an idle child runtime with the original profile id, model, reasoning effort, working directory, local env policy, parent session id, parent job id, transcript ref, and result schema.

- [ ] **Step 2: Run reconstruction tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*Reconstruct.*Delegate|Test.*Restore.*NoAutoResume|Test.*Restore.*NoModel' -count=1
```

Expected: fail because restored parent has no child runtime and restore may not project all settings.

- [ ] **Step 3: Add delegate-specific child restore path**

Implement a helper that reconstructs the minimum idle child session runtime from the descriptor and retained child meta/transcript. It must wire parent/caller linkage, `parentJobID`, forwarded nested job visibility, profile/model/reasoning settings, working directory, local env policy, retained transcript history, result schema, and explicit grants.

The helper must not submit the old delegate task as a new turn.

- [ ] **Step 4: Preserve no-auto-resume**

Keep `RestoreSessionFromMetaWithConfig` limited to restore/reconcile/projection. It must not launch delegates, spend tokens, enqueue child turns, or register idle child runtimes before a user/model calls `job_send_message`.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*Reconstruct.*Delegate|Test.*Restore.*NoAutoResume|Test.*Restore.*NoModel' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add agent/session_init.go agent/job_delegate.go agent/subagents.go agent/subagent_manager.go agent/job_delegate_test.go
git commit -m "fix(job-control): reconstruct restored delegate sessions"
```

---

### Task 9: Resume Runtime-Lost Delegates From Retained State

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/jobs.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing end-to-end resume tests**

Add tests that create a delegate, ensure child transcript/session metadata is retained, simulate restart via `RestoreSessionFromMetaWithConfig`, reconcile old job as `stopped/runtime_lost`, then call `job_send_message(target:<old_job_id>, message:"new input")`.

Assert:

- before `job_send_message`, the restored subagent manager has no retained child runtime entry;
- response has a new `job_id`, `action:"resumed"`, and `resumed_from_job_id` equal to the old job;
- old job remains `stopped/runtime_lost`;
- old job output remains old output;
- new job output is the resumed turn output;
- first model request after restore includes retained transcript history and uses only the new message as the fresh input;
- old tool calls are not replayed;
- result schema is inherited and validation behavior from Task 3 applies.

- [ ] **Step 2: Run resume tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*RuntimeLost.*Resume|Test.*Restore.*Resume|Test.*NoReplay|Test.*Structured' -count=1
```

Expected: fail because restored runtime-lost delegates are not yet resumable.

- [ ] **Step 3: Implement restored resume path**

When `sendDelegateMessage` targets a terminal `stopped/runtime_lost` delegate and no retained live child exists, run strict preflight, reconstruct the idle child runtime, then reuse the normal terminal delegate resume path.

Always create a new job id and new durable delegate job record. Preserve the same transcript ref, copy inherited schema into the new descriptor, and return `resumed_from_job_id`.

- [ ] **Step 4: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*RuntimeLost.*Resume|Test.*Restore.*Resume|Test.*NoReplay|Test.*Structured' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add agent/job_delegate.go agent/jobs.go agent/job_delegate_test.go agent/session_tools_jobs_test.go
git commit -m "fix(job-control): resume runtime-lost delegates"
```

---

### Task 10: Contract, Prompts, And Final Gates

**Files:**
- Modify: `docs/job-control.md`
- Modify: `docs/superpowers/plans/2026-06-09-job-control-contract-cleanup.md`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/prompts/`
- Modify: `docs/tools/` if job-control wording appears there
- Modify: tests for tool descriptions/profile prompts as needed

- [ ] **Step 1: Update public documentation**

Update `docs/job-control.md` so it no longer marks alias/schema/sidecar/runtime-lost resume behavior open. State the implemented behavior from the spec:

- `main` is not a v1 alias;
- `caller` is the normal runtime alias;
- `watched` is contextual and valid only for concrete watch delivery context;
- watch-send frames coalesce by durable key and retry on busy sidecars;
- invalid structured results omit `structured_result`, set `structured_result_valid:false`, and include `structured_result_reason`;
- v1 sidecars are ordinary delegate jobs with internal watch-origin bookkeeping only;
- `stopped/runtime_lost` delegates are resumable from retained transcript/session state when strict preflight passes.

Update the cleanup doc to check A1, B1, B2, B3, B4, and B7c with a short disposition and the actual commit ids produced by these tasks.

- [ ] **Step 2: Update tool definitions and prompts**

Remove `main` alias examples and stale sidecar/private/capacity escape-hatch language from model-facing tool descriptions and prompts.

- [ ] **Step 3: Run final gates**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test.*Delegate|Test.*Structured|Test.*Restore|Test.*WatchSend' -count=1
go test ./agent -run 'Test.*Watch|Test.*Delegate|Test.*JobSend|Test.*Structured|Test.*Restore|Test.*RuntimeLost' -count=1
go test ./agent ./agent/internal/jobstore ./agent/internal/tool ./appwire ./internal/appprojector ./cmd/serf-hub ./cmd/serf-tui/internal/msgrender ./cmd/serf-tui/internal/toolsummary -count=1
make build
make test
make lint
```

Run token and public-surface gates:

```bash
rg -n 'spawn_agent|resume_agent|close_agent|cancel_agent|list_agents|subagent_output|subagent-notification|DefSpawnAgent|DefSendInput|DefWait|DefCloseAgent|DefCancelAgent|DefListAgents|DefSubagentOutput|rootOnlyAgentManagementTools|SUBAGENT_START|SUBAGENT_END|EventSubagentStart|EventSubagentEnd|SubagentStartData|SubagentEndData|NotifySerfSubagent|SerfSubagentInfo|SubagentStatusInfo' \
  | rg -v 'docs/superpowers/(specs|plans)/|docs/job-control\.md|CHANGELOG|/original-attractor-specs/|/design/2026-'

rg -n 'caller[[:space:]]*\|[[:space:]]*main[[:space:]]*\|[[:space:]]*watched|caller`, `main`, `watched|"?(target|to)"?[[:space:]]*[:=][[:space:]]*"main"|"main".*alias|alias.*"main"' \
  docs/job-control.md agent/internal/tool agent/prompts docs/tools \
  | rg -v 'docs/superpowers/(specs|plans)/|main session|package main|func main'

rg -n 'private-sidecar|private sidecar|sidecar.*capacity|capacity.*sidecar|implementation-specific policy.*sidecar|sidecar.*implementation-specific policy' \
  docs/job-control.md agent/internal/tool agent/prompts docs/tools \
  | rg -v 'docs/superpowers/(specs|plans)/'

rg -n 'Open decision|open decision|deferred/open decision|remains open|not yet normative' \
  docs/job-control.md agent/internal/tool agent/prompts docs/tools \
  | rg -v 'docs/superpowers/(specs|plans)/'
```

Expected: tests pass; all `rg` gates return no output.

- [ ] **Step 4: Commit**

```bash
git add docs/job-control.md docs/superpowers/plans/2026-06-09-job-control-contract-cleanup.md agent/internal/tool/definitions.go agent/prompts docs/tools
git commit -m "docs(job-control): publish resolved open-decision contract"
```

---

## Final Review

- [ ] Run a final spec-compliance reviewer over the full implementation range.
- [ ] Run a final code-quality reviewer over the full implementation range.
- [ ] Fix any non-low findings through the relevant implementer.
- [ ] Run the final validation block again.
- [ ] Ensure `git status --short` is clean.
