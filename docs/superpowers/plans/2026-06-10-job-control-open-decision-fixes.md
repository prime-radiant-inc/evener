# Job Control Open-Decision Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the resolved job-control open decisions from `docs/superpowers/specs/2026-06-10-job-control-open-decision-fixes.md`.

**Architecture:** Keep job-control identity and persistence in `agent/internal/jobstore`, keep runtime orchestration in `agent/job_delegate.go` and `agent/job_watch.go`, and keep public tool projections in `agent/session_tools_jobs.go`. Use explicit durable fields and typed internal classifications instead of parsing error prose.

**Tech Stack:** Go, Serf agent/jobstore internals, existing `go test` package tests, `make build`, `make test`, `make lint`.

---

## File Map

- `agent/internal/jobstore/event.go`, `record.go`, `fold.go`, tests: durable event/record fields for delegate restore metadata, `result_schema`, `structured_result_reason`, and pending watch-send records if stored in the job event log.
- `agent/job_delegate.go`, `subagents.go`, `subagent_manager.go`, `session_init.go`: delegate launch metadata capture, live and restored resume, child-session reconstruction, and exact resumability errors.
- `agent/job_watch.go`, `job_watch_test.go`, `job_watch_observer_test.go`: alias validation, contextual `watched`, pending watch-send coalescing, retry, cleanup, durability, and loop suppression.
- `agent/session_tools_jobs.go`, `session_tools_jobs_test.go`: public result shapes for `delegate`, `job_send_message`, `job_read_output`, `job_watch`, and `job_list`.
- `agent/internal/tool/definitions.go`, `agent/prompts/`, `docs/job-control.md`, `docs/superpowers/plans/2026-06-09-job-control-contract-cleanup.md`: public contract and model-facing wording cleanup.

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

Add tests for direct `watched` and watch-context delivery:

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

Add a watch delivery test where `send.to:"watched"` on a concrete job resolves to the concrete watched target, and a wildcard ambiguity test emits one diagnostic reason `watched_unresolved`.

- [ ] **Step 3: Run alias tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*MainAlias|Test.*Watched' -count=1
```

Expected: new tests fail because `main` and `watched` are still broad runtime aliases.

- [ ] **Step 4: Implement alias resolution**

Change `isRuntimeMessageAlias` to accept only `caller`; add an internal watch context path for `watched` instead of treating it as a normal runtime alias.

Change `isWatchSessionTarget` so `main` is no longer valid. `watched` is valid only where the watch code can resolve it from trigger context.

Validate `job_watch.send.to` at registration for unsupported aliases and known non-messageable concrete job IDs. Revalidate at delivery.

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

### Task 2: Structured Result Schema And Reason Fields

**Files:**
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/fold.go`
- Modify: `agent/internal/jobstore/*_test.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing jobstore tests**

Add tests proving `result_schema` and `structured_result_reason` round-trip and fold:

```go
func TestFoldDelegateResultSchemaAndStructuredReason(t *testing.T) {
	valid := false
	events := []Event{
		{Kind: EventJobStarted, Seq: 1, JobID: "job_1", Type: JobDelegate, ResultSchema: map[string]any{"type": "object"}},
		{Kind: EventJobFinished, Seq: 2, JobID: "job_1", Status: StatusCompleted, StructuredResultValid: &valid, StructuredResultReason: "schema_result_missing"},
	}
	rec := Fold(events)["job_1"]
	if rec.ResultSchema == nil {
		t.Fatal("result_schema not folded")
	}
	if rec.StructuredResultReason != "schema_result_missing" {
		t.Fatalf("reason = %q", rec.StructuredResultReason)
	}
}
```

- [ ] **Step 2: Write failing public projection tests**

Add tests for:

- no schema + no structured output omits `structured_result_valid` and `structured_result_reason`;
- missing schema result emits `schema_result_missing`;
- oversized persisted result emits `schema_result_too_large`;
- bounded projection emits `projection_too_large` without changing durable record;
- stale result does not leak from one resumed turn to the next.

- [ ] **Step 3: Run structured tests to verify failure**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test.*Structured|Test.*ResultSchema|Test.*Reason' -count=1
go test ./agent -run 'Test.*Structured|Test.*ResultSchema|Test.*Projection|Test.*Stale' -count=1
```

Expected: new tests fail because fields do not exist.

- [ ] **Step 4: Add durable fields**

Add fields:

```go
ResultSchema any `json:"result_schema,omitempty"`
StructuredResultReason string `json:"structured_result_reason,omitempty"`
```

to `jobstore.Event` and `jobstore.JobRecord`. Fold `ResultSchema` on `EventJobStarted`; fold `StructuredResultReason` on first `EventJobFinished`.

- [ ] **Step 5: Thread result reason through runtime projections**

Add `StructuredResultReason string` to `delegateResult`, `sendMessageResult`, `delegateToolResult`, `jobSendMessageDelegateResult`, and `jobReadOutputResult`.

Set reason values exactly:

- `schema_result_missing`
- `schema_result_too_large`
- `schema_capture_failed`
- `schema_validation_failed`
- `projection_too_large`

- [ ] **Step 6: Run focused tests**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test.*Structured|Test.*ResultSchema|Test.*Reason' -count=1
go test ./agent -run 'Test.*Structured|Test.*ResultSchema|Test.*Projection|Test.*Stale' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add agent/internal/jobstore agent/job_delegate.go agent/session_tools_jobs.go agent/job_delegate_test.go agent/session_tools_jobs_test.go
git commit -m "fix(job-control): persist delegate result schema"
```

---

### Task 3: Watch-Send Coalescing

**Files:**
- Modify: `agent/job_watch.go`
- Modify: `agent/jobs.go`
- Modify: `agent/job_watch_test.go`
- Modify: `agent/job_watch_observer_test.go`
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/fold.go`

- [ ] **Step 1: Write failing pending-send tests**

Add tests for:

- two busy frames for one key produce one pending frame and deliver latest on retry;
- wildcard frames from different watched jobs use different keys;
- clear/replacement removes old pending frames;
- job-manager close removes pending frames;
- restored pending frame survives store reopen and later delivers or diagnoses once.

Use deterministic retry helper in tests:

```go
func TestWatchSendBusyCoalescesLatestFrame(t *testing.T) {
	jm := newTestJobManager(t)
	var sent []sendMessageArgs
	busy := true
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		if busy {
			return sendMessageFailed(a.Target, errWatchSendBusy)
		}
		sent = append(sent, a)
		return sendMessageResult{Target: a.Target, Delivered: true, Action: "sent", MessageType: "runtime"}
	}
	// configure watch, fire two frames, assert pending count 1, make idle, retry, assert latest body.
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./agent -run 'TestWatchSend.*Coalesce|TestWatchSend.*Pending|TestWatchSend.*Restore|TestWatchSend.*Clear|TestWatchSend.*Wildcard' -count=1
```

Expected: fail because current watch sends immediately and diagnoses on busy.

- [ ] **Step 3: Add pending-frame data model**

Add a stable key and pending frame state in `job_watch.go` or jobstore-backed watch files:

```go
type watchSendPendingKey struct {
	VisibleSessionID string
	WatchTarget string
	ResolvedWatchedIdentity string
	ResolvedSendTo string
	WatchGeneration string
}

type pendingWatchSend struct {
	Key watchSendPendingKey
	Message string
	Trigger string
	Frame string
	CoalescedCount int
	FromWatch bool
}
```

Persist pending frames durably. Keep one pending frame per key.

- [ ] **Step 4: Implement delivery classification**

Add a typed classification helper. Do not parse incidental error prose.

```go
type watchSendDeliveryClass int

const (
	watchSendDelivered watchSendDeliveryClass = iota
	watchSendBusy
	watchSendHardFailure
)
```

Classify watch-originated sends to running delegate sidecars as `watchSendBusy`; ordinary `job_send_message` keeps live-steer behavior.

- [ ] **Step 5: Implement retry and cleanup**

Add deterministic retry hook called when delegate jobs transition to idle/resumable. Ensure it runs outside `jm.mu`.

Clean pending frames on success, hard diagnostic, watch clear/replacement, concrete expiry, prune, and job-manager close.

- [ ] **Step 6: Run focused watch tests**

Run:

```bash
go test ./agent -run 'TestWatchSend|TestJobWatch|TestWatchOrigin' -count=1
go test ./agent/internal/jobstore -run 'Test.*Watch' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add agent/job_watch.go agent/jobs.go agent/job_watch_test.go agent/job_watch_observer_test.go agent/internal/jobstore
git commit -m "fix(job-control): coalesce busy watch sends"
```

---

### Task 4: Delegate Restore Descriptor

**Files:**
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/fold.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/subagents.go`
- Modify: `agent/session_config.go`
- Modify: `agent/job_delegate_test.go`

- [ ] **Step 1: Write failing descriptor tests**

Add tests proving delegate launch persists a versioned descriptor with child id, transcript ref, parent/session linkage, agent/model/reasoning/profile, working directory, env policy, result schema, and tool policy.

- [ ] **Step 2: Run descriptor tests to verify failure**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test.*Delegate.*Descriptor|Test.*RestoreDescriptor' -count=1
go test ./agent -run 'Test.*Delegate.*Descriptor' -count=1
```

Expected: fail because descriptor fields do not exist.

- [ ] **Step 3: Add descriptor type**

Add a durable descriptor type under `agent/internal/jobstore` or an agent-local serializable type referenced by jobstore:

```go
type DelegateRestoreDescriptor struct {
	Version int `json:"version"`
	ChildSessionID string `json:"child_session_id"`
	TranscriptRef string `json:"transcript_ref"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	ParentJobID string `json:"parent_job_id,omitempty"`
	OwnerSessionID string `json:"owner_session_id,omitempty"`
	VisibleToSessionID string `json:"visible_to_session_id,omitempty"`
	Task string `json:"task,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	RequestedModel string `json:"requested_model,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	Model string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	LocalEnvPolicy string `json:"local_env_policy,omitempty"`
	ResultSchema any `json:"result_schema,omitempty"`
}
```

Keep the exact fields aligned with the final implementation; avoid persisting arbitrary environment variables.

- [ ] **Step 4: Populate descriptor during delegate launch**

Capture descriptor before or with durable `job_started`. Fold it into the job record.

- [ ] **Step 5: Run descriptor tests**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test.*Delegate.*Descriptor|Test.*RestoreDescriptor' -count=1
go test ./agent -run 'Test.*Delegate.*Descriptor' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add agent/internal/jobstore agent/job_delegate.go agent/subagents.go agent/session_config.go agent/job_delegate_test.go
git commit -m "fix(job-control): persist delegate restore descriptors"
```

---

### Task 5: Strict Restore Preflight

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_init.go`
- Modify: `agent/transcript_read.go` if strict read helper is needed
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing preflight tests**

Add table-driven tests for exact errors:

```go
cases := []struct{
	name string
	breakState func(*testing.T, *Session, *jobstore.JobRecord)
	want string
}{
	{"missing meta", removeChildMeta, "target_not_resumable:missing_child_session_meta"},
	{"missing transcript", removeChildTranscript, "target_not_resumable:missing_child_transcript"},
	{"corrupt transcript", corruptChildTranscript, "target_not_resumable:corrupt_child_transcript"},
	{"bad linkage", corruptDescriptorParent, "target_not_resumable:parent_linkage_unavailable"},
	{"missing descriptor", removeDescriptor, "target_not_resumable:missing_delegate_resume_metadata"},
}
```

Each case asserts no new job/run is created.

- [ ] **Step 2: Run preflight tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*RestorePreflight|Test.*NotResumable' -count=1
```

Expected: fail because restore preflight does not exist.

- [ ] **Step 3: Implement preflight helper**

Add a helper returning stable reason codes:

```go
type delegateRestorePreflight struct {
	Descriptor jobstore.DelegateRestoreDescriptor
	ChildMeta schema.SessionMeta
	TranscriptPath string
}

func (s *Session) preflightDelegateRestore(rec *jobstore.JobRecord) (delegateRestorePreflight, string, error)
```

Return errors exactly as `target_not_resumable:<reason>`.

- [ ] **Step 4: Project resumability in `job_list`**

For `stopped/runtime_lost` delegate records, expose `resumable:true` only when preflight succeeds; otherwise `resumable:false` and `not_resumable_reason`.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*RestorePreflight|Test.*NotResumable|Test.*JobList.*Resumable' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add agent/job_delegate.go agent/session_init.go agent/transcript_read.go agent/session_tools_jobs.go agent/job_delegate_test.go agent/session_tools_jobs_test.go
git commit -m "fix(job-control): validate restored delegate resume state"
```

---

### Task 6: Reconstruct Idle Child Runtime

**Files:**
- Modify: `agent/session_init.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/subagents.go`
- Modify: `agent/subagent_manager.go`
- Modify: `agent/job_delegate_test.go`

- [ ] **Step 1: Write failing reconstruction tests**

Add tests proving restore does not launch tokens/jobs, and `job_send_message` lazily reconstructs an idle child with the original profile/model/reasoning/working dir/result schema.

- [ ] **Step 2: Run reconstruction tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*Reconstruct.*Delegate|Test.*Restore.*NoAutoResume' -count=1
```

Expected: fail because restored parent has no child runtime.

- [ ] **Step 3: Add delegate-specific child restore path**

Implement a helper that restores the child session from retained meta/transcript using descriptor values, wires `parentSteer`, `parentJobID`, `forwardJobEvent`, depth/tool restrictions, and registers an idle retained `subagent`.

- [ ] **Step 4: Preserve no-auto-resume**

Ensure `RestoreSessionFromMetaWithConfig` still only reconciles jobs and does not call the model or launch child turns by itself.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*Reconstruct.*Delegate|Test.*Restore.*NoAutoResume' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add agent/session_init.go agent/job_delegate.go agent/subagents.go agent/subagent_manager.go agent/job_delegate_test.go
git commit -m "fix(job-control): reconstruct restored delegate sessions"
```

---

### Task 7: Resume Runtime-Lost Delegates

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/jobs.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing end-to-end resume tests**

Add tests that create a delegate, restore parent from disk, reconcile old job as `stopped/runtime_lost`, then call `job_send_message(target:<old_job_id>)`.

Assert:

- response has a new `job_id`;
- `action:"resumed"`;
- `resumed_from_job_id` equals old job;
- old job output remains old output;
- new job output is new turn output;
- first model request after restore uses retained history and the new message;
- old tool calls are not replayed.

- [ ] **Step 2: Run resume tests to verify failure**

Run:

```bash
go test ./agent -run 'Test.*RuntimeLost.*Resume|Test.*Restore.*Resume|Test.*NoReplay' -count=1
```

Expected: fail because restored runtime-lost delegates are not resumable.

- [ ] **Step 3: Implement restored resume path**

When `sendDelegateMessage` targets a terminal `stopped/runtime_lost` delegate and no retained live child exists, run preflight, reconstruct idle child runtime, then reuse the normal terminal delegate resume path.

Always create a new job id and preserve `resumed_from_job_id`.

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

### Task 8: Contract, Prompts, And Final Gates

**Files:**
- Modify: `docs/job-control.md`
- Modify: `docs/superpowers/plans/2026-06-09-job-control-contract-cleanup.md`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/prompts/`
- Modify: `docs/tools/` if job-control wording appears there
- Modify tests for tool descriptions/profile prompts as needed

- [ ] **Step 1: Update public documentation**

Update `docs/job-control.md` so it no longer marks alias/schema/sidecar/runtime-lost resume behavior open. State the implemented behavior from the spec.

Update the cleanup doc to check A1, B1, B2, B3, B4, and B7c with a short disposition that cites the actual commit ids produced by these tasks.

- [ ] **Step 2: Update tool definitions and prompts**

Remove `main` alias examples and stale sidecar/private/capacity escape-hatch language from model-facing tool descriptions and prompts.

- [ ] **Step 3: Run final gates**

Run:

```bash
go test ./agent/internal/jobstore -run 'Test.*Delegate|Test.*Structured|Test.*Restore' -count=1
go test ./agent -run 'Test.*Watch|Test.*Delegate|Test.*JobSend|Test.*Structured|Test.*Restore' -count=1
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
- [ ] Fix any medium/high findings through the relevant implementer.
- [ ] Run the full validation block again.
- [ ] Ensure `git status --short` is clean.
