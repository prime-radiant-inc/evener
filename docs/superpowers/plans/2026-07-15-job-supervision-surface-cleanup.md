# Job Supervision Surface Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the job-supervision cutover so models use compact `job_status` for state, bounded cursor-based `read_transcript` reads for evidence, and durably ordered/coalesced terminal notifications instead of `job_read_output`.

**Architecture:** Keep lifecycle truth in the existing jobstore records and terminal generations. Delete the unreachable public `job_read_output` definition/result path, extend only the `job:<job_id>` arm of `read_transcript` with byte-window metadata and the existing nested/watch-grant routing, persist terminal state before explicitly flushing output and arming notification, and preserve each terminal notification's durable pending/delivered ledger while presenting all deliverable terminals in one already-durable steering turn. Session transcript/API-log behavior, watch matching, process execution, retention, and AppWire remain unchanged.

**Tech Stack:** Go, Serf tool registry, append-only jobstore/output files, scripted provider tests, YAML tool-fluency probes, Markdown operational scenarios.

## Global Constraints

This implementation must not:

- retain a model-facing `job_read_output` compatibility path;
- add a new gate-execution service;
- change shell process execution or retention limits;
- expose provider API logs through ordinary job transcripts;
- change watch trigger semantics;
- modify Superpowers.

- Treat every requirement outside `docs/superpowers/specs/2026-07-15-job-supervision-surface-cleanup-design.md` as a defect. Stop and ask Jesse instead of expanding the implementation scope.

---

## Scope Audit Before Implementation

**Execution prerequisite:** Project 1 (delegate-budget truthfulness) and Project 2 (transcript/API-log separation) must be merged first. Treat `jobstore.StatusExhausted`, `ExhaustionBudget`, `ExhaustionLimit`, delegate `Resumable`, transcript-v2 semantic entries, and explicit `source="api_log"` access as existing contracts. This project preserves them; it does not recreate or redesign either prerequisite.

The checkout already has `job_status` and `read_transcript` registered, and already omits `job_read_output` from `registerJobToolsWithRegistrar`. Do not reimplement those landed pieces. This plan covers only the remaining gaps:

1. delete the obsolete definition, public result/handler, and stale model guidance;
2. keep prerequisite-provided exhausted/resumability metadata in compact `job_status` while proving it remains output-free, and explicitly retain Project 3's known delegate `not_resumable_reason` copied from `JobRecord.NotResumableWhy`;
3. make `read_transcript(job:...)` bounded and cursor/range navigable, including existing descendant and observer read-grant routes;
4. persist terminal truth first, then make output flush failure prevent notification arming;
5. lock the existing one-steering-turn notification coalescing behavior with per-job ledger tests;
6. update current prompts, probes, docs, and manual scenarios.

Do not delete, rename, recompute, or omit prerequisite-provided exhausted status/metadata while removing the overlapping output surface. Likewise, change only the `job:` branch of transcript reading: default session reads remain semantic-only, and exact provider transport data remains reachable only through the explicit API-log source from Project 2.

AppWire inspection found no job-control RPC or alternate tool-dispatch surface. `server/appwire_runtime.go` only projects diagnostic tool/job inventory. Keep that protocol and `appwire.SerfJobInfo` unchanged; final verification must prove it still passes.

## File Structure

### Production files

- Modify `agent/internal/tool/definitions.go`
  - Delete `DefJobReadOutput`.
  - Document exact job-ref cursor/range semantics on `DefReadTranscript`.
- Modify `agent/session_tools_jobs.go`
  - Delete the obsolete public read handler/result/windowing code.
  - Preserve prerequisite-provided delegate resumability/exhaustion fields and add the compact delegate not-resumable reason to `jobStatusResult`.
  - Retain output/grep helpers still used by `job_watch`; do not alter watch matching.
- Modify `agent/session_tools_transcript.go`
  - Own job-output evidence projection and bounded cursor/range slicing.
  - Route local, descendant, and watch-granted job refs through one snapshot interface.
  - Preserve semantic-only default session reads and explicit API-log source isolation from Project 2.
- Modify `agent/job_watch.go`
  - Rename the existing observer read-grant view to transcript terminology and remove its obsolete grep closure; do not change grant creation, keys, persistence, or trigger behavior.
- Modify `agent/session_config.go`, `agent/subagents.go`, and `agent/job_delegate.go`
  - Carry the renamed parent transcript-read grant callback without changing authority.
- Modify `agent/session_tool_registry.go`
  - Give transcript tools an owning-session snapshot resolver closure without introducing a `Session` dependency into tool handlers.
- Modify `agent/internal/jobstore/fold.go`
  - Update the grant comment only; `EventWatchReadGrant` and `FoldGrants` stay byte-for-byte compatible.
- Modify `agent/internal/jobstore/output.go`
  - Add an explicit `Flush() error` durable boundary using the existing output/meta persistence path.
- Modify `agent/jobs.go`
  - Persist `EventJobFinished`, flush output, and only then arm notification across live finalization, `NotifyNotArmed` restart re-arm, and runtime-lost reconciliation.
- Modify `agent/job_output_digest.go`
  - Delete only line-window helpers used solely by `job_read_output`; retain the inline shell digest.
- Modify `scripts/run-fuzz.sh`
  - Replace removed job-reader fuzz registrations with transcript/supervision targets and keep retained shell-digest coverage correctly anchored.
- Modify `agent/prompts/sections/background-jobs.md` and `agent/prompts/sections/delegation.md`
  - Teach notification-first supervision, `job_status`, transcript refs, and bounded cursor reads.
- Modify `internal/bundled/plugins/coordinator-workflow/agents/coordinator.md`
  - Replace the stale tool allowance with `job_status` and `read_transcript`.

### Behavioral tests

- Modify `agent/job_supervision_test.go`
- Modify `agent/transcript_tools_test.go`
- Modify `agent/job_notify_test.go`
- Modify `agent/jobs_test.go`
- Modify `agent/internal/tool/definitions_test.go`
- Modify `agent/internal/tool/definitions_program_fuzz_test.go`
- Modify `agent/internal/jobstore/output_test.go`
- Modify `agent/profile_test.go`
- Create `tools/tool-fluency/cmd/serf-fluency/job_supervision_probe_test.go`
- Modify `agent/job_nested_test.go`
- Modify `agent/job_watch_test.go`
- Modify `agent/job_watch_loopguard_test.go`
- Modify `agent/job_watch_timers_observe_fuzz_test.go`
- Modify `agent/watch_grant_lifecycle_fuzz_test.go`
- Modify `agent/root_watch_tree_program_fuzz_test.go`
- Modify `agent/job_runtime_recovery_program_fuzz_test.go`
- Modify `agent/session_tools_shell_test.go`
- Modify `agent/session_tools_jobs_test.go`
- Modify `agent/session_tools_jobs_list_test.go`
- Modify `agent/session_tools_jobs_stop_delegate_test.go`
- Modify `agent/session_tools_jobs_fuzz_test.go`
- Modify `agent/session_tools_jobs_lifecycle_fuzz_test.go`
- Modify `agent/session_tools_jobs_seed100_more_test.go`
- Modify `agent/session_tools_jobs_seed100_range_a_test.go`
- Modify `agent/session_tools_jobs_seed100_range_b_test.go`
- Modify `agent/session_tools_jobs_seed100_range_c_test.go`
- Modify `agent/session_tools_jobs_seed100_range_d_test.go`
- Modify `agent/jobs_seed100_fuzz_test.go`
- Modify `agent/fuzz_fc2_dispatch_test.go`
- Modify `agent/cov_s3_jobsfmt_test.go`
- Modify `agent/cov_w2tail_jobs_helpers_test.go`
- Modify `agent/shell_notify_digest_program_fuzz_test.go`
- Delete `agent/session_tools_jobs_read_output_test.go`
- Delete `agent/cov_s3_jobread_test.go`
- Delete `agent/cov_s1_output_digest_test.go`
- Delete `agent/job_output_digest_seed_coverage_fuzz_test.go`
- Create `agent/job_transcript_projection_seed_coverage_fuzz_test.go`
- Delete `agent/job_read_recovery_grant_fuzz_test.go`
- Create `agent/job_transcript_recovery_grant_fuzz_test.go`

The deletions remove tests of an unreachable public API. Preserve their meaningful contracts by moving only these cases to transcript tests: retained/dropped bytes, same-cursor no-replay, terminal-file reads, live descendant reads, closed-owner fallback, and durable watch-granted reads. Blocking/grep cases are already `job_watch` contracts and must not be recreated in `read_transcript`.

### Current guidance, probes, and scenarios

- Modify `docs/job-control.md`
- Modify `docs/agentic-testing.md`
- Modify `docs/architecture.md`
- Modify `docs/skills/tool-fluency/SKILL.md`
- Modify `docs/subagent-management/08-standalone-llm-calls.md`
- Modify `docs/web-ui/ux-and-implementation-plan.md`
- Modify `tools/tool-fluency/README.md`
- Modify `tools/tool-fluency/probes/jobs_control.yaml`
- Modify `tools/tool-fluency/probes/job_watch.yaml`
- Modify `test/scenarios/INDEX.md`
- Modify `test/scenarios/job-delegate-result-schema.md`
- Modify `test/scenarios/job-delegate-wait-no-poll.md`
- Modify `test/scenarios/job-nested-visibility.md`
- Modify `test/scenarios/job-notification-wake.md`
- Modify `test/scenarios/job-restart-durability.md`
- Modify `test/scenarios/job-send-message-surface.md`
- Modify `test/scenarios/job-shell-lifecycle.md`
- Modify `test/scenarios/job-stop-and-children.md`
- Modify `test/scenarios/job-watch-caller-notification-delivery.md`
- Modify `test/scenarios/job-watch-output-match-catchup.md`
- Modify `test/scenarios/job-watch-passive-observer-noop-filter.md`
- Modify `test/scenarios/job-watch-sidecar-observer.md`
- Modify `test/scenarios/recursion-coordinator-fanout.md`
- Modify `test/scenarios/recursion-deaf-coordinator-drivedown.md`
- Modify `test/scenarios/sidecar-approval-broker-communicate.md`
- Modify `test/scenarios/sidecar-handoff-packager-job-notification.md`
- Modify `test/scenarios/sidecar-memory-reminder-read-file.md`
- Modify `test/scenarios/sidecar-progress-digest-output-match.md`
- Modify `test/scenarios/sidecar-runbook-capture-output-match.md`
- Modify `test/scenarios/sidecar-test-triage-output-match.md`
- Modify `test/scenarios/transcript-subagent-audit-children-of.md`
- Delete `test/scenarios/job-read-output-blocking-grep.md`
- Create `test/scenarios/job-transcript-cursor-and-output-watch.md`
- Delete `test/scenarios/subagent-list-and-output.md`
- Create `test/scenarios/subagent-list-and-transcript.md`

Historical artifacts under `docs/superpowers/{specs,plans,research,reports}/`, dated `docs/specs/`, dated `docs/web-ui/specs/`, and `tools/tool-fluency/reports/` are append-only decision/evidence records. Do not rewrite them. Historical Hub/TUI renderers for already-persisted tool calls are not a callable alias or AppWire dispatch surface and are also out of scope.

## Shared Interfaces

The tasks below use these exact interfaces. Do not invent a second cursor/result shape in a later task.

```go
const (
	defaultJobTranscriptBytes = 8 * 1024
	maxJobTranscriptBytes     = 64 * 1024
)

type jobTranscriptByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Start/End use half-open lifetime byte offsets: [Start, End).
type readJobTranscriptResult struct {
	TranscriptRef     string                 `json:"transcript_ref"`
	ContentType       string                 `json:"content_type"`
	Content           string                 `json:"content"`
	TotalBytes        int64                  `json:"total_bytes"`
	DroppedBytes      int64                  `json:"dropped_bytes"`
	ReturnedByteRange jobTranscriptByteRange `json:"returned_byte_range"`
	NextCursor        int64                  `json:"next_cursor"`
}

type jobTranscriptSnapshot struct {
	Record       *jobstore.JobRecord
	Content      string
	TotalBytes   int64
	DroppedBytes int64
}

type grantedJobTranscriptRead struct {
	record       *jobstore.JobRecord
	readRetained func() (content string, total int64, dropped int64, err error)
}
```

`read_transcript` job-ref argument semantics are exact:

- neither `cursor` nor `range`: return at most the newest 8 KiB of retained output;
- `cursor=N`: return bytes beginning at lifetime offset `N`, capped by `max_bytes` (default 8 KiB, maximum 64 KiB);
- `max_bytes` is valid only when `cursor` is present; a positive `max_bytes` by itself is `invalid_request` rather than a request to resize the default newest-tail read;
- `range="bytes:START-END"`: return that half-open lifetime range, capped to 64 KiB; it cannot be combined with `cursor` or `max_bytes`;
- byte ranges require `0 <= START < END <= total_bytes`; retained data loss still clamps `START` upward to `dropped_bytes` and remains visible in metadata;
- a cursor below `dropped_bytes` begins at `dropped_bytes`, making the loss visible in metadata;
- a cursor above `total_bytes`, a malformed/empty range, a negative cursor, or a non-positive/over-limit `max_bytes` returns `invalid_request`;
- `next_cursor` is always the returned range's exclusive end;
- when `cursor == total_bytes`, return empty `content`, `[total_bytes,total_bytes)`, and the same `next_cursor` rather than replaying the default tail;
- job refs accept only the default/`markdown` format selector and return `content_type:"text/plain"`; `outline`/`jsonl` remain session-ref-only, so ordinary job reads cannot expose provider API bodies.

---

### Task 1: Delete the public read surface and finish compact status

**Files:**

- Modify: `agent/internal/tool/definitions_test.go`
- Modify: `agent/internal/tool/definitions_program_fuzz_test.go`
- Modify: `agent/job_supervision_test.go`
- Modify: `agent/profile_test.go`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `internal/bundled/plugins/coordinator-workflow/agents/coordinator.md`

**Interfaces:**

- Consumes: prerequisite-provided `jobStatusTool`, `projectJobStatus`, `StatusExhausted`, `ExhaustionBudget`, `ExhaustionLimit`, and `Resumable`, plus the existing `JobRecord.NotResumableWhy` fact.
- Produces: `jobStatusResult.NotResumableReason *string` as Project 3's compact delegate-orientation field; no callable or defined `job_read_output` tool.

- [ ] **Step 1: Write the failing surface and status tests**

In `agent/internal/tool/definitions_program_fuzz_test.go`, replace the obsolete entry in `toolProgramDefinitions` only after first adding this list contract against that complete definition corpus:

```go
func TestToolProgramDefinitionsUseCurrentJobSupervisionSurface(t *testing.T) {
	defs := toolProgramDefinitions([]string{"default"}, []string{"assistant.tool"}, []string{"medium"}, "communicate")
	names := make(map[string]bool, len(defs))
	for _, def := range defs {
		names[def.Name] = true
	}
	for _, want := range []string{"job_status", "job_list", "job_stop", "job_watch", "read_transcript"} {
		if !names[want] {
			t.Fatalf("current job supervision definition %q missing", want)
		}
	}
	if names["job_read_output"] {
		t.Fatal("obsolete job_read_output definition remains")
	}
}
```

In `agent/job_supervision_test.go`, characterize the already-landed registry/dispatch removal against a real session:

```go
func TestJobReadOutputAbsentFromCallableSurface(t *testing.T) {
	s := newTestSession(t)
	for _, def := range s.ToolDefinitions() {
		if def.Name == "job_read_output" {
			t.Fatal("job_read_output is still advertised")
		}
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "removed-job-read", Name: "job_read_output", Arguments: json.RawMessage(`{}`),
	})
	if !res.IsError || !strings.Contains(res.Output, "unknown tool") {
		t.Fatalf("removed dispatch result = error:%v output:%q", res.IsError, res.Output)
	}
}
```

In `agent/profile_test.go`, pin the bundled coordinator's actual allowance:

```go
func TestCoordinatorWorkflowUsesCurrentJobSupervisionSurface(t *testing.T) {
	a := coordinatorWorkflowAgentForTest(t, "coordinator")
	tools := make(map[string]bool, len(a.Tools))
	for _, name := range a.Tools {
		tools[name] = true
	}
	for _, want := range []string{"job_status", "read_transcript", "job_list", "job_stop", "job_watch"} {
		if !tools[want] {
			t.Errorf("coordinator missing %q", want)
		}
	}
	if tools["job_read_output"] {
		t.Error("coordinator still allows job_read_output")
	}
}
```

Add a prerequisite-preservation projection test to `agent/job_supervision_test.go`. Assert exhausted metadata/resumability remain present and inspect the exact marshaled result for banned output fields; the existing `job_status` registry tests continue to cover handler wiring:

```go
func TestJobStatusPreservesExhaustionMetadataWithoutOutput(t *testing.T) {
	resumable := true
	now := time.Unix(8000, 0).UTC()
	rec := &jobstore.JobRecord{
		JobID: "job_delegate_status", Type: jobstore.JobDelegate,
		Status: jobstore.StatusExhausted, Reason: "tool_round_budget_exhausted",
		StartedAt: now, EndedAt: &now,
		TranscriptRef: "local:child_status", Resumable: &resumable,
		ExhaustionBudget: "max_tool_rounds_per_input", ExhaustionLimit: 25,
		OutputBytes: 4096,
	}
	out := projectJobStatus(now, rec)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["resumable"] != true {
		t.Fatalf("resumable = %v, want true", got["resumable"])
	}
	if got["status"] != "exhausted" || got["exhaustion_budget"] != "max_tool_rounds_per_input" || got["exhaustion_limit"] != float64(25) {
		t.Fatalf("exhaustion projection = %s", b)
	}
	for _, banned := range []string{"output", "total_bytes", "dropped_bytes", "notification_status", "terminal_generation"} {
		if _, ok := got[banned]; ok {
			t.Errorf("job_status leaked %q: %s", banned, b)
		}
	}
}
```

Add `TestJobStatusDelegateCarriesNotResumableReason` beside it. Project a delegate record with `Resumable` set to false and `NotResumableWhy: "session_missing"`; assert the exact JSON contains `resumable:false` and `not_resumable_reason:"session_missing"`. Do not add this reason to shell jobs or recompute it from status.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/tool -run '^TestToolProgramDefinitionsUseCurrentJobSupervisionSurface$' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestCoordinatorWorkflowUsesCurrentJobSupervisionSurface|TestJobStatusPreservesExhaustionMetadataWithoutOutput|TestJobStatusDelegateCarriesNotResumableReason' -count=1 -v
```

Expected: the definition-corpus and coordinator tests FAIL because they still name `job_read_output`; the not-resumable-reason test FAILS because Project 1 intentionally does not add that result field. The exhaustion preservation test PASSes on the prerequisite baseline; a failure means Project 1 is missing or was regressed, so stop rather than recreating budget semantics here. `TestJobReadOutputAbsentFromCallableSurface` is deliberately not part of this RED command because dispatch removal already landed; it runs in the final focused check.

- [ ] **Step 3: Preserve prerequisite status fields and correct the coordinator allowance**

Preserve the prerequisite fields and add only Project 3's reason field to `jobStatusResult`:

```go
Resumable          *bool   `json:"resumable,omitempty"`
ExhaustionBudget   string  `json:"exhaustion_budget,omitempty"`
ExhaustionLimit    int     `json:"exhaustion_limit,omitempty"`
NotResumableReason *string `json:"not_resumable_reason,omitempty"`
```

In `projectJobStatus`, copy `NotResumableWhy` only for delegate records using the existing empty-string-to-nil helper. Do not change Project 1's persisted resumability or exhaustion projections.

Change the coordinator front matter to:

```yaml
tools: [glob, grep, read_file, shell, delegate, delegate_send, job_status, job_list, read_transcript, job_stop, job_watch, task_list]
```

- [ ] **Step 4: Delete the obsolete public definition**

Delete `DefJobReadOutput` from `agent/internal/tool/definitions.go` and remove it from `toolProgramDefinitions`, `TestSchemaWaitKnobs`, and other definition-only schema cases. Do not delete `jobReadOutputTool` or its private helpers in this task: existing tests still compile against them until Task 2 migrates the meaningful contracts. The handler is already unregistered, and Task 2 removes it in the same commit as those test migrations.

Do not add a replacement alias, hidden registration, deprecated schema, provider-name mapping, or fallback dispatch.

- [ ] **Step 5: Run focused tests and the static surface audit**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/tool -run '^TestToolProgramDefinitionsUseCurrentJobSupervisionSurface$' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestJobReadOutputAbsentFromCallableSurface|TestCoordinatorWorkflowUsesCurrentJobSupervisionSurface|TestJobStatusPreservesExhaustionMetadataWithoutOutput|TestJobStatusDelegateCarriesNotResumableReason|TestJobStatusRunningShellProjectsSupervisionFields' -count=1 -v
rg -n 'DefJobReadOutput' agent/internal/tool
```

Expected: tests PASS; `rg` exits 1 with no matches. The private unregistered handler remains temporarily until Task 2; do not advertise or register it.

- [ ] **Step 6: Commit**

```bash
git status --short
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/internal/tool/definitions_program_fuzz_test.go agent/session_tools_jobs.go agent/job_supervision_test.go agent/profile_test.go internal/bundled/plugins/coordinator-workflow/agents/coordinator.md
git commit -m "refactor(agent): remove obsolete job output definition

Delete the model-facing job_read_output definition now that job_status and
read_transcript are the supported supervision primitives. Keep job status
compact, preserve prerequisite-provided exhausted/resumability facts, and
surface the existing delegate not-resumable reason for orientation."
```

---

### Task 2: Make job transcript reads bounded, cursor-based, and correctly routed

**Files:**

- Modify: `agent/session_tools_transcript.go`
- Modify: `agent/session_tool_registry.go`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_output_digest.go`
- Modify: `scripts/run-fuzz.sh`
- Modify: `agent/job_watch.go`
- Modify: `agent/session_config.go`
- Modify: `agent/subagents.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/internal/jobstore/fold.go`
- Modify/Create/Delete the behavioral test files listed under “Behavioral tests” above.

**Interfaces:**

- Consumes: `jobManager.readJobWindow(jobID, maxJobOutputRetentionBytes, false)`, `Session.resolveDescendantJobOwner`, `Session.jobReadClosedStoreFallback` (renamed to transcript terminology), `EventWatchReadGrant`, and `FoldGrants`.
- Produces: `readJobTranscriptResult`, `jobTranscriptSnapshot`, `grantedJobTranscriptRead`, `toolDeps.readJobTranscriptSnapshot`, and `spawnConfig.parentGrantedJobTranscriptRead` exactly as defined under Shared Interfaces.

- [ ] **Step 1: Replace the shell-ref smoke test with failing byte-contract tests**

In `agent/transcript_tools_test.go`, replace `TestReadTranscriptReadsShellJobRef` and add these exact helpers. They use the real registry and job output store below a deterministic synthetic shell runtime:

```go
func runningShellWithOutput(t *testing.T, output string) (*Session, string) {
	t.Helper()
	s := newTestSession(t)
	jm := s.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "test fixture"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	run := runningJobByID(t, jm, rec.JobID)
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte(output)); err != nil {
		t.Fatalf("appendJobOutput: %v", err)
	}
	return s, rec.JobID
}

func readJobTranscriptForTest(t *testing.T, s *Session, args map[string]any) readJobTranscriptResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "read-job-transcript", Name: "read_transcript", Arguments: raw,
	})
	if res.IsError {
		t.Fatalf("read_transcript: %s", res.Output)
	}
	var out readJobTranscriptResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal read_transcript: %v (output: %s)", err, res.Output)
	}
	return out
}
```

Then add these behavioral cases:

```go
func TestReadTranscriptJobRefDefaultsToNewestBoundedBytes(t *testing.T) {
	s, jobID := runningShellWithOutput(t, strings.Repeat("a", defaultJobTranscriptBytes)+"NEWEST")
	out := readJobTranscriptForTest(t, s, map[string]any{"transcript_ref": "job:" + jobID})
	if len([]byte(out.Content)) > defaultJobTranscriptBytes {
		t.Fatalf("content bytes = %d, want <= %d", len([]byte(out.Content)), defaultJobTranscriptBytes)
	}
	if !strings.HasSuffix(out.Content, "NEWEST") {
		t.Fatalf("default read is not newest-oriented: %q", out.Content)
	}
	if out.ReturnedByteRange.End != out.TotalBytes || out.NextCursor != out.TotalBytes {
		t.Fatalf("range/cursor = %+v/%d, total=%d", out.ReturnedByteRange, out.NextCursor, out.TotalBytes)
	}
}

func TestReadTranscriptJobRefCursorAdvancesWithoutReplay(t *testing.T) {
	s, jobID := runningShellWithOutput(t, "first-second-third")
	first := readJobTranscriptForTest(t, s, map[string]any{
		"transcript_ref": "job:" + jobID,
		"cursor": 0,
		"max_bytes": 6,
	})
	second := readJobTranscriptForTest(t, s, map[string]any{
		"transcript_ref": "job:" + jobID,
		"cursor": first.NextCursor,
		"max_bytes": 7,
	})
	if first.Content != "first-" || second.Content != "second-" {
		t.Fatalf("cursor pages replayed or skipped: first=%q second=%q", first.Content, second.Content)
	}
	if second.ReturnedByteRange.Start != first.ReturnedByteRange.End {
		t.Fatalf("ranges are not contiguous: first=%+v second=%+v", first.ReturnedByteRange, second.ReturnedByteRange)
	}
}

func TestReadTranscriptJobRefCursorAtEndReturnsNoOutput(t *testing.T) {
	s, jobID := runningShellWithOutput(t, "done")
	atEnd := readJobTranscriptForTest(t, s, map[string]any{
		"transcript_ref": "job:" + jobID,
		"cursor": 4,
	})
	if atEnd.Content != "" || atEnd.ReturnedByteRange != (jobTranscriptByteRange{Start: 4, End: 4}) || atEnd.NextCursor != 4 {
		t.Fatalf("end cursor replayed output: %+v", atEnd)
	}
}

func TestReadTranscriptJobRefReportsDroppedBytesAndClampsEvictedCursor(t *testing.T) {
	s, jobID := runningShellWithOutput(t, strings.Repeat("x", maxJobOutputRetentionBytes+32))
	out := readJobTranscriptForTest(t, s, map[string]any{
		"transcript_ref": "job:" + jobID,
		"cursor": 0,
		"max_bytes": 16,
	})
	if out.DroppedBytes != 32 || out.ReturnedByteRange.Start != 32 || len(out.Content) != 16 {
		t.Fatalf("dropped cursor projection = %+v", out)
	}
}
```

Add table tests for `range="bytes:2-7"`, malformed ranges, negative/oversized args, positive `max_bytes` without `cursor`, `cursor > total_bytes`, job-ref `format=jsonl` rejection, and `read_transcript` session-ref rejection of the job-only `cursor`/`max_bytes` properties. Tests must assert structured fields and content, not rendered JSON strings. Do not apply that rejection to Project 2's separate `read_session_transcript` tool, whose explicit API-log body expansion legitimately uses `max_bytes`.

- [ ] **Step 2: Run the transcript tests and verify RED**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestReadTranscriptJobRef' -count=1 -v
```

Expected: FAIL because the current job-ref reader ignores `range`, has no `cursor`/`max_bytes` schema or byte metadata, and replays the retained tail.

- [ ] **Step 3: Define the exact tool schema**

Update `DefReadTranscript` properties in `agent/internal/tool/definitions.go`:

```go
"range": map[string]any{
	"type": "string",
	"description": "Session refs: turn window (12-40, last:40, start:40). Job refs: half-open lifetime byte range bytes:START-END; cannot combine with cursor or max_bytes.",
},
"cursor": map[string]any{
	"type": "integer",
	"description": "Job refs only: lifetime byte offset for the next incremental read. Reusing an end-of-log cursor returns empty content.",
},
"max_bytes": map[string]any{
	"type": "integer",
	"description": "Job refs only: positive byte cap for cursor reads; default 8192, maximum 65536.",
},
```

Keep `additionalProperties:false`. Update the definition description to say `job_status` supplies state and `read_transcript` supplies bounded evidence; explicitly direct future matching to `job_watch` and say session `jsonl`/API-log access is not part of a job ref.

In the non-job branch of `execReadTranscript`, before delegating to `execReadSessionTranscript`, reject a present `cursor` or `max_bytes` with `invalid_request: cursor and max_bytes are job-ref-only`. Do not put this validation inside shared `execReadSessionTranscript`: Project 2's registered `read_session_transcript(source="api_log", ..., max_bytes=...)` contract must remain unchanged.

- [ ] **Step 4: Implement pure request parsing and byte projection**

Add these functions to `agent/session_tools_transcript.go`:

```go
type jobTranscriptReadRequest struct {
	Cursor   *int64
	Range    *jobTranscriptByteRange
	MaxBytes int64
}

func parseJobTranscriptReadRequest(args map[string]any) (jobTranscriptReadRequest, error)

func projectJobTranscriptSnapshot(ref string, snap jobTranscriptSnapshot, req jobTranscriptReadRequest) (readJobTranscriptResult, error)
```

`parseJobTranscriptReadRequest` must enforce the Shared Interfaces rules that do not depend on the snapshot. Use `strconv.ParseInt` for both `bytes:` endpoints, reject negative, empty, or reversed ranges, reject `range` combined with `cursor`/`max_bytes`, reject any present `max_bytes` when `cursor` is absent, and distinguish an absent cursor from explicit zero by reading the raw argument map. Prefix every caller error with `invalid_request:`. `projectJobTranscriptSnapshot` rejects a cursor or range endpoint above `snap.TotalBytes` with the same prefix.

`projectJobTranscriptSnapshot` must treat `snap.Content` as bytes whose lifetime offset zero is `snap.DroppedBytes`:

```go
	retained := []byte(snap.Content)
	retainedStart := snap.DroppedBytes
	retainedEnd := retainedStart + int64(len(retained))
	if retainedEnd != snap.TotalBytes {
		return readJobTranscriptResult{}, errors.New("job transcript metadata is inconsistent")
	}

	start, end := retainedEnd-int64(min(len(retained), defaultJobTranscriptBytes)), retainedEnd
	if req.Cursor != nil {
		start = *req.Cursor
		if start < retainedStart {
			start = retainedStart
		}
		end = min(start+req.MaxBytes, retainedEnd)
	}
	if req.Range != nil {
		start = max(req.Range.Start, retainedStart)
		end = min(req.Range.End, retainedEnd, start+maxJobTranscriptBytes)
		if end < start {
			end = start
		}
	}
	content := string(retained[start-retainedStart : end-retainedStart])
```

Return `ContentType: "text/plain"`, `ReturnedByteRange: [start,end)`, and `NextCursor: end`. Normalize invalid UTF-8 with `strings.ToValidUTF8` only after slicing; byte offsets always refer to the original bytes.

- [ ] **Step 5: Move existing authority/routing into the transcript reader**

Rename the observer grant without changing durable events:

```go
type grantedJobTranscriptRead struct {
	record       *jobstore.JobRecord
	readRetained func() (string, int64, int64, error)
}

func (s *Session) lookupGrantedJobTranscriptRead(observerSessionID, jobID string) (*grantedJobTranscriptRead, bool)
```

The closure must call:

```go
content, total, dropped, _, err := jm.readJobWindow(jobID, maxJobOutputRetentionBytes, false)
return content, total, dropped, err
```

Rename `spawnConfig.parentGrantedJobRead` to `parentGrantedJobTranscriptRead` at all assignments. Keep `EventWatchReadGrant`, the `(observerSessionID, jobID)` key, `mintWatchCreateReadGrant`, watch installation, and `FoldGrants` unchanged.

Keep concrete session traversal out of the tool handler. Add this closure to `toolDeps` in `agent/session_tool_registry.go` and initialize it in `newToolDeps`:

```go
type toolDeps struct {
	// existing fields...
	readJobTranscriptSnapshot func(jobID string) (jobTranscriptSnapshot, error)
}

// In newToolDeps:
readJobTranscriptSnapshot: s.readJobTranscriptSnapshot,
```

Replace `readJobTranscript` with a closure-backed resolver:

```go
func readJobTranscript(deps *toolDeps, ref string, args map[string]any) (any, error)

func (s *Session) readJobTranscriptSnapshot(jobID string) (jobTranscriptSnapshot, error)
```

Resolution order must match the old authorized reader exactly:

1. local/direct-child `nestedOrLocalJobManager`;
2. live depth-2+ owner via `resolveDescendantJobOwner`;
3. existing closed-store forwarded-copy fallback;
4. durable parent watch grant via `parentGrantedJobTranscriptRead`;
5. otherwise return the existing not-found error.

Have `readJobTranscript` call only `deps.readJobTranscriptSnapshot`; keep descendant/grant authority in the `Session` method and do not add a concrete `*Session` field to `toolDeps`. Do not grant `job_list`, `job_stop`, `delegate_send`, session transcript access, or provider-log access.

- [ ] **Step 6: Migrate meaningful tests and delete obsolete public-reader tests**

After the test callers have moved, delete this private unreachable implementation from `agent/session_tools_jobs.go`:

```text
jobReadWindowMode and jobReadMode* constants
classifyJobReadWindow
jobReadOutputTool
formatJobReadOutput
derefString
jobReadOutputResult
jobOutputMatch
strictZeroJobBytesArg
validateJobGrepPattern
maxJobGrepPatternJSONChars
boundedMatchLine
projectJobOutputMatches
digestHeadLines, digestTailLines, jobLineReadBudget, maxJobOutputBytes,
maxJobGrepPatternBytes, and grantedReadBlockUnsupportedErr
```

Delete `readJobOutputDigest` and `midLineBytes` from `agent/job_output_digest.go`. Keep `shellInlineDigest`, `assembleOutputDigest`, `firstLineBytes`, `lastLineBytes`, `maxJobOutputRetentionBytes`, and all matching/output-store helpers still used by `job_watch`.

While editing `session_tools_jobs.go`, preserve Project 1's `StatusExhausted`, `ExhaustionBudget`, `ExhaustionLimit`, and `Resumable` projections in `job_status`, `job_list`, delegate results, and notification inputs, plus Project 3's `NotResumableReason` status field added in Task 1. They overlap the file but not the removed reader.

Apply this exact test routing:

| Old contract | New home |
|---|---|
| shell retained bytes/default bounded read | `agent/transcript_tools_test.go` |
| same-cursor no replay and explicit range | `agent/transcript_tools_test.go` |
| terminal output-file read after live runtime removal | `agent/transcript_tools_test.go` |
| dropped byte accounting | `agent/transcript_tools_test.go` |
| depth-2+ owner and closed-owner fallback | `agent/job_nested_test.go` using `read_transcript` |
| observer durable read grant and restart recovery | new `agent/job_transcript_recovery_grant_fuzz_test.go` |
| shell large-output retrievability | `agent/session_tools_shell_test.go` using `read_transcript` |
| grep/wait behavior | keep existing `job_watch` tests only |
| structured delegate result replay | remove; delegate evidence comes from its session transcript and its terminal result/notification |

Remove handler calls, result fixtures, and fuzz program arms that exist solely to execute/format `jobReadOutputTool`. Extend existing transcript fuzz argument generation with valid/invalid `cursor`, `max_bytes`, and `bytes:START-END` values instead. Do not weaken `job_watch` grep/match tests.

Delete `agent/cov_s1_output_digest_test.go`. Replace `agent/job_output_digest_seed_coverage_fuzz_test.go` with `agent/job_transcript_projection_seed_coverage_fuzz_test.go` and rename its target to `FuzzJobTranscriptProjectionSeedCoverage`; seed default, cursor, explicit range, dropped-prefix clamp, end-cursor empty, invalid UTF-8, invalid range, and positive-`max_bytes`-without-cursor projections through `parseJobTranscriptReadRequest` and `projectJobTranscriptSnapshot`.

In mixed suites, remove only obsolete reader cases. Rewrite `FuzzShellDigestReadProgram` in `agent/shell_notify_digest_program_fuzz_test.go` to retain its name but exercise only `shellInlineDigest`, `assembleOutputDigest`, and their UTF-8/elision contracts. Rename `FuzzJobtoolsExec` to `FuzzJobSupervisionExec`; replace its `jobReadOutputTool` determinism/execution arms with `jobStatusTool` while retaining `jobListTool`, `jobWatchTool`, `jobStopTool`, store-fault, and deterministic replay coverage. Remove reader formatting helpers from `agent/cov_s3_jobsfmt_test.go` and `agent/cov_w2tail_jobs_helpers_test.go`, and delete helper arms from `agent/jobs_seed100_fuzz_test.go` plus `agent/session_tools_jobs_seed100_range_{c,d}_test.go`. Keep unrelated shell notification, stop outcome, delegate projection, watch, and tool-description coverage.

Rename `FuzzJobReadRecoveryGrant` to `FuzzJobTranscriptRecoveryGrant` in the new `agent/job_transcript_recovery_grant_fuzz_test.go`. Replace head/tail/line/grep/wait selectors with default/cursor/range/max-byte programs; retain the deterministic differential across direct-owner, durable observer-grant, live descendant, and closed-owner fallback reads. Future-match behavior remains covered by existing `job_watch` fuzz targets, not transcript grep.

Update these `scripts/run-fuzz.sh` entries exactly and remove the old names:

```bash
"native:agent:.:FuzzJobTranscriptProjectionSeedCoverage::session_tools_transcript.go#projectJobTranscriptSnapshot"
"native:agent:.:FuzzJobSupervisionExec::session_tools_jobs.go#jobStatusTool"
"native:agent:.:FuzzJobTranscriptRecoveryGrant::session_tools_transcript.go;job_watch.go;jobs.go;job_shell.go"
"native:agent:.:FuzzShellDigestReadProgram::job_output_digest.go#shellInlineDigest"
```

- [ ] **Step 7: Run focused and package tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestReadTranscriptJobRef|Test.*Transcript.*Grant|Test.*Nested.*Transcript|TestShell.*Transcript' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent ./agent/internal/tool -run 'ReadSessionTranscript|APILogSource|AttemptExpansion|OversizedExpansion' -count=1
GOCACHE=/tmp/serf-gocache go test ./agent -count=1
make fuzz-registry-check
```

Expected: PASS. The second command proves Project 2's semantic/API-log source split and byte-paged API expansion survived the overlapping schema/handler edits. The package command compiles every migrated seed/fuzz harness and catches stale references to deleted result types. `fuzz-registry-check` reports no missing old targets, unregistered new targets, stale source anchors, or duplicate registrations.

- [ ] **Step 8: Commit**

```bash
git status --short
git add -- \
  agent/session_tools_transcript.go agent/session_tool_registry.go agent/internal/tool/definitions.go \
  agent/session_tools_jobs.go agent/job_output_digest.go agent/job_watch.go agent/session_config.go \
  agent/subagents.go agent/job_delegate.go agent/internal/jobstore/fold.go \
  agent/job_supervision_test.go agent/transcript_tools_test.go \
  agent/internal/tool/definitions_test.go agent/internal/tool/definitions_program_fuzz_test.go \
  agent/job_nested_test.go agent/job_watch_test.go agent/job_watch_loopguard_test.go \
  agent/job_watch_timers_observe_fuzz_test.go agent/watch_grant_lifecycle_fuzz_test.go \
  agent/root_watch_tree_program_fuzz_test.go agent/job_runtime_recovery_program_fuzz_test.go \
  agent/session_tools_shell_test.go agent/session_tools_jobs_test.go agent/session_tools_jobs_list_test.go \
  agent/session_tools_jobs_stop_delegate_test.go agent/session_tools_jobs_fuzz_test.go \
  agent/session_tools_jobs_lifecycle_fuzz_test.go agent/session_tools_jobs_seed100_more_test.go \
  agent/session_tools_jobs_seed100_range_a_test.go agent/session_tools_jobs_seed100_range_b_test.go \
  agent/session_tools_jobs_seed100_range_c_test.go agent/session_tools_jobs_seed100_range_d_test.go \
  agent/jobs_seed100_fuzz_test.go agent/fuzz_fc2_dispatch_test.go agent/cov_s3_jobsfmt_test.go \
  agent/cov_w2tail_jobs_helpers_test.go agent/shell_notify_digest_program_fuzz_test.go \
  agent/session_tools_jobs_read_output_test.go agent/cov_s3_jobread_test.go \
  agent/cov_s1_output_digest_test.go agent/job_output_digest_seed_coverage_fuzz_test.go \
  agent/job_transcript_projection_seed_coverage_fuzz_test.go \
  agent/job_read_recovery_grant_fuzz_test.go agent/job_transcript_recovery_grant_fuzz_test.go \
  scripts/run-fuzz.sh
git commit -m "feat(agent): add bounded cursor job transcript reads

Route shell job evidence through read_transcript with explicit lifetime byte
ranges, dropped-byte metadata, and no-replay cursors. Preserve the existing
nested-job and observer read-grant authority without changing watch semantics."
```

---

### Task 3: Persist terminal truth, then flush evidence before notifying

**Files:**

- Modify: `agent/internal/jobstore/output_test.go`
- Modify: `agent/internal/jobstore/output.go`
- Modify: `agent/jobs_test.go`
- Modify: `agent/jobs.go`

**Interfaces:**

- Consumes: `OutputStore.persistMetaLocked`, `Store.Append`, `jobstore.Reconcile`, `reconcileLostJobsWithLoad`, `EventJobFinished`, and `EventJobNotificationPending`.
- Produces: `func (o *OutputStore) Flush() error`; a terminal event/generation remains durable when output flush fails, but live, re-armed, or runtime-lost notification pending/delivery cannot advance until a retry flushes successfully.

- [ ] **Step 1: Write failing flush and terminal-order tests**

Add a jobstore test that calls the missing method after an append and verifies the persisted total/retained metadata still reopens correctly:

```go
func TestOutputStoreFlushPersistsCurrentBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := OpenOutput(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if _, err := o.Append([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if err := o.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	total, dropped, err := OutputFileStats(path)
	if err != nil || total != 10 || dropped != 2 {
		t.Fatalf("stats=(%d,%d,%v), want (10,2,nil)", total, dropped, err)
	}
}
```

Add two agent behavior tests:

```go
func TestFinalizePersistsTerminalBeforeOutputFlushAndLeavesNotificationUnarmedOnFailure(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatal(err)
	}
	run := jm.running[rec.JobID]
	if err := run.output.Close(); err != nil {
		t.Fatal(err)
	}
	run.output, err = jobstore.OpenOutput(jm.outputPathForJob(rec, rec.JobID), maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatal(err)
	}
	originalAppend := jm.appendEvent
	jm.appendEvent = func(event jobstore.Event) error {
		if err := originalAppend(event); err != nil {
			return err
		}
		if event.Kind == jobstore.EventJobFinished {
			return run.output.Close()
		}
		return nil
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err == nil {
		t.Fatal("finalize succeeded after output durable boundary failed")
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := recs[rec.JobID]
	if !got.Status.IsTerminal() || got.TerminalGen == "" || got.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("terminal/notification order after flush failure: %+v", got)
	}
}

func TestFinalizeEnqueuesOnlyAfterTerminalAndOutputAreDurable(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatal(err)
	}
	run := jm.running[rec.JobID]
	if _, err := run.output.Append([]byte("durable evidence")); err != nil {
		t.Fatal(err)
	}
	jm.enqueue = func(n jobNotification) {
		recs, loadErr := jm.store.Load()
		if loadErr != nil {
			t.Errorf("load at enqueue: %v", loadErr)
			return
		}
		got := recs[n.JobID]
		if got == nil || !got.Status.IsTerminal() || got.NotifyState != jobstore.NotifyPending || got.TerminalGen == "" {
			t.Errorf("record at enqueue = %+v", got)
		}
		total, _, statErr := jobstore.OutputFileStats(jm.outputPathForJob(got, got.JobID))
		if statErr != nil || total != int64(len("durable evidence")) {
			t.Errorf("output at enqueue=(%d,%v)", total, statErr)
		}
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRestartRearmFlushesTerminalOutputBeforePendingNotification(t *testing.T) {
	stateDir := t.TempDir()
	first, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(9000, 0).UTC()
	ended := started.Add(time.Second)
	const jobID = "job_crash_before_pending"
	outputPath := first.outputPathForJob(nil, jobID)
	output, err := jobstore.OpenOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Append([]byte("terminal evidence")); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.appendJobEvents([]jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell, OwnerSessionID: "S1", VisibleToSession: "S1", StartedAt: &started, OutputPath: outputPath},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID, Status: jobstore.StatusCompleted, Reason: "exit_zero", EndedAt: &ended, OutputBytes: int64(len("terminal evidence")), TerminalGen: "GEN_CRASH"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.closeStoreOnly(); err != nil {
		t.Fatal(err)
	}

	var queued []jobNotification
	restarted, err := newJobManager(stateDir, "S1", func(n jobNotification) { queued = append(queued, n) })
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.closeStoreOnly()
	originalOpen := restarted.openOutput
	restarted.openOutput = func(path string, capBytes int64) (*jobstore.OutputStore, error) {
		o, err := originalOpen(path, capBytes)
		if err != nil {
			return nil, err
		}
		if err := o.Close(); err != nil {
			return nil, err
		}
		return o, nil
	}
	if err := restarted.armPendingTerminalNotifications(); err == nil {
		t.Fatal("restart rearm ignored output flush failure")
	}
	recs, err := restarted.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs[jobID].NotifyState != jobstore.NotifyNotArmed || len(queued) != 0 {
		t.Fatalf("failed recovery flush advanced notification: state=%s queued=%d", recs[jobID].NotifyState, len(queued))
	}

	restarted.openOutput = originalOpen
	if err := restarted.armPendingTerminalNotifications(); err != nil {
		t.Fatal(err)
	}
	recs, err = restarted.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs[jobID].NotifyState != jobstore.NotifyPending || len(queued) != 1 {
		t.Fatalf("successful recovery flush did not arm once: state=%s queued=%d", recs[jobID].NotifyState, len(queued))
	}
}
```

Add the independent runtime-lost reconciliation contract:

```go
func TestReconcileLostJobFlushesOutputBeforePendingNotification(t *testing.T) {
	var queued []jobNotification
	jm, err := newJobManager(t.TempDir(), "S1", func(n jobNotification) { queued = append(queued, n) })
	if err != nil {
		t.Fatal(err)
	}
	defer jm.closeStoreOnly()
	rec, err := jm.createShell(createShellOpts{Command: "lost runtime"})
	if err != nil {
		t.Fatal(err)
	}
	run := jm.running[rec.JobID]
	if _, err := run.output.Append([]byte("evidence before crash")); err != nil {
		t.Fatal(err)
	}
	if err := run.output.Close(); err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	delete(jm.running, rec.JobID) // durable started record with no live runtime
	jm.mu.Unlock()
	defer run.treeSlot.release()

	originalOpen := jm.openOutput
	jm.openOutput = func(path string, capBytes int64) (*jobstore.OutputStore, error) {
		o, err := originalOpen(path, capBytes)
		if err != nil {
			return nil, err
		}
		if err := o.Close(); err != nil {
			return nil, err
		}
		return o, nil
	}
	if err := jm.reconcileLostJobs(); err == nil {
		t.Fatal("runtime-lost reconciliation ignored output flush failure")
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := recs[rec.JobID]
	if !got.Status.IsTerminal() || got.Reason != "runtime_lost" || got.NotifyState != jobstore.NotifyNotArmed || len(queued) != 0 {
		t.Fatalf("runtime-lost flush failure order: rec=%+v queued=%d", got, len(queued))
	}

	jm.openOutput = originalOpen
	if err := jm.armPendingTerminalNotifications(); err != nil {
		t.Fatal(err)
	}
	recs, err = jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs[rec.JobID].NotifyState != jobstore.NotifyPending || len(queued) != 1 {
		t.Fatalf("runtime-lost retry did not arm once: state=%s queued=%d", recs[rec.JobID].NotifyState, len(queued))
	}
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/jobstore -run '^TestOutputStoreFlushPersistsCurrentBoundary$' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestFinalizePersistsTerminalBeforeOutputFlushAndLeavesNotificationUnarmedOnFailure|TestFinalizeEnqueuesOnlyAfterTerminalAndOutputAreDurable|TestRestartRearmFlushesTerminalOutputBeforePendingNotification|TestReconcileLostJobFlushesOutputBeforePendingNotification' -count=1 -v
```

Expected: the first test FAILS to compile because `Flush` is absent. The agent group FAILS because current live finalization has no explicit post-terminal flush boundary, restart re-arming appends pending without reopening/flushing terminal output, and runtime-lost reconciliation batches `EventJobFinished` with pending before any flush gate.

- [ ] **Step 3: Add the explicit output durable boundary**

In `agent/internal/jobstore/output.go`:

```go
// Flush persists the current retained output bytes and lifetime-offset metadata.
func (o *OutputStore) Flush() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.persistMetaLocked(); err != nil {
		return fmt.Errorf("jobstore: flush output: %w", err)
	}
	return nil
}
```

`persistMetaLocked` already syncs the output before atomically writing/syncing metadata; do not add a second file format or change retention.

At the beginning of `writeFinishJob` in `agent/jobs.go`, make the final byte-count read fail closed, but do not flush yet:

```go
	if run == nil || run.output == nil {
		return nil, errors.New("job output store is unavailable at terminal boundary")
	}
	_, outputBytes, _, err := run.output.Tail(0)
	if err != nil {
		return nil, err
	}
```

Add `outputFlushed bool` to `terminalJob`. At the start of `forwardFinishedJob`, before checking or setting `finishedForwarded`, call this retry-safe helper:

```go
func (jm *jobManager) flushTerminalOutput(run *runningJob, terminal *terminalJob) error {
	if terminal == nil {
		return errors.New("terminal job is unavailable at output flush boundary")
	}
	jm.mu.Lock()
	flushed := terminal.outputFlushed
	jm.mu.Unlock()
	if flushed {
		return nil
	}
	if run == nil || run.output == nil {
		return errors.New("job output store is unavailable at terminal boundary")
	}
	if err := run.output.Flush(); err != nil {
		return err
	}
	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run && run.terminal == terminal {
		terminal.outputFlushed = true
	}
	jm.mu.Unlock()
	return nil
}
```

`writeFinishJob` already appends `EventJobFinished`, stores `run.terminal`, and then calls `forwardFinishedJob`; retry finalization also calls `forwardFinishedJob` with the stored terminal. This placement enforces the approved order on both first attempt and retry:

```text
EventJobFinished
output Flush
EventJobNotificationPending
durable notification steering turn
EventJobNotificationDelivered
```

Add the equivalent closed-output boundary for crash recovery:

```go
func (jm *jobManager) flushRecoveredTerminalOutput(rec *jobstore.JobRecord) (err error) {
	path := jm.outputPathForJob(rec, rec.JobID)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("terminal output unavailable before notification rearm: %w", err)
	}
	output, err := jm.openOutput(path, maxJobOutputRetentionBytes)
	if err != nil {
		return err
	}
	flushErr := output.Flush()
	closeErr := output.Close()
	return errors.Join(flushErr, closeErr)
}
```

In `armPendingTerminalNotifications`, call `flushRecoveredTerminalOutput(rec)` only for the `NotifyNotArmed` branch, immediately before appending `EventJobNotificationPending`. A record already in `NotifyPending` crossed this boundary before its pending event and is only re-enqueued; do not reopen it. A missing/pruned output artifact must fail re-arm instead of being recreated as empty evidence.

In `reconcileLostJobsWithLoad`, replace the current combined `finished + pending + watch-clear` append. Append `finished` together with the unchanged `recoveredTerminalWatchClearEvents` first, call `flushRecoveredTerminalOutput(rec)`, then append `pending` alone and enqueue. Keeping the watch-clear events beside the recovered terminal preserves their crash behavior without changing watch matching/delivery semantics; a crash or flush failure after that batch leaves durable terminal truth with `NotifyNotArmed`, which the normal re-arm path completes later.

- [ ] **Step 4: Run ordering, retry, shell, and delegate finalization tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/jobstore -run 'TestOutputStoreFlush|TestOutputStore' -count=1
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestFinalize|TestJobManagerFinalize|TestRestartRearmFlushesTerminalOutputBeforePendingNotification|TestReconcileLostJobFlushesOutputBeforePendingNotification|TestRunShell.*Finaliz|Test.*Delegate.*Finaliz' -count=1
```

Expected: PASS, including existing same-generation retry and crash/rearm tests.

- [ ] **Step 5: Commit**

```bash
git status --short
git add agent/internal/jobstore/output.go agent/internal/jobstore/output_test.go agent/jobs.go agent/jobs_test.go
git commit -m "fix(agent): flush terminal evidence before notification

Persist terminal truth and its generation before flushing the final output
boundary, while preventing live, restart-rearmed, or runtime-lost notification
state from advancing until that flush succeeds. Preserve same-generation retry."
```

---

### Task 4: Prove test-only terminal notification coalescing and durable append retry

**Files:**

- Modify: `agent/job_notify_test.go`
- Verify unchanged: `agent/session_lifecycle.go`
- Verify unchanged: `agent/transcript/transcript.go`
- Verify unchanged: `fuzz/fault/fault.go`
- Verify unchanged: `fuzz/fault/fault_fs.go`
- Verify unchanged: `agent/internal/jobstore/event.go`
- Verify unchanged: `agent/internal/jobstore/fold.go`

**Interfaces:**

- Consumes: `filterDeliverableJobNotifications`, `formatJobNotificationReminder`, `appendTurnDurably`, `markJobNotificationsDelivered`, and the existing test-facing `transcript.NewWriterWithFS(fault.FS(...))` durable-writer/filesystem seam.
- Produces: behavioral proof that one steering turn can contain several terminal blocks while every `(job_id, terminal_generation)` advances independently.

The production path already drains all deliverable notifications, formats one reminder, appends one durable steering turn, and only then loops over delivery events. Do not add a batch notification event, shared generation, or watch semantic. This is a characterization/contract task, and the expected implementation diff is test-only.

For append-failure coverage, inject the failure at `transcript.Writer.AppendDurable`'s actual entry-write operation through `transcript.NewWriterWithFS` and the existing `fault.FS` wrapper. Do not use the `sessionLifecycleFault(ctx, "append_notification")` hook: it is joined after `appendTurnDurably` returns and therefore cannot prove that the durable write itself failed. Do not add another production or session-level writer hook.

- [ ] **Step 1: Add a helper that creates distinct pending terminal jobs**

Keep the existing helper as a wrapper and add this generalized helper, which appends real `job_started`, `job_finished`, and `job_notification_pending` events for each job:

```go
func appendPendingTerminal(t *testing.T, jm *jobManager, sessionID, jobID string, status jobstore.Status, reason, terminalGen string) {
	t.Helper()
	started := time.Unix(1000, 0).UTC()
	ended := time.Unix(1001, 0).UTC()
	for _, event := range []jobstore.Event{
		{
			Kind: jobstore.EventJobStarted, TS: started, JobID: jobID,
			Type: jobstore.JobShell, OwnerSessionID: sessionID,
			VisibleToSession: sessionID, StartedAt: &started,
		},
		{
			Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
			Status: status, Reason: reason, EndedAt: &ended, TerminalGen: terminalGen,
		},
		{
			Kind: jobstore.EventJobNotificationPending, TS: ended,
			JobID: jobID, TerminalGen: terminalGen,
		},
	} {
		if err := jm.appendEvent(event); err != nil {
			t.Fatalf("append %s for %s: %v", event.Kind, jobID, err)
		}
	}
}
```

- [ ] **Step 2: Add the coalescing and per-job-ledger tests**

Add the standard-library `bytes` and `encoding/json` imports plus `github.com/spf13/afero`, `primeradiant.com/serf/agent/transcript`, and `primeradiant.com/serf/fuzz/fault`; all are used only by the test below.

```go
func TestTerminalNotificationsCoalesceIntoOneDurableSteeringTurn(t *testing.T) {
	sess, adapter := newNotificationExcerptSession(t)
	appendPendingTerminal(t, sess.jobManager, sess.ID(), "job_A", jobstore.StatusCompleted, "exit_zero", "GEN_A")
	appendPendingTerminal(t, sess.jobManager, sess.ID(), "job_B", jobstore.StatusFailed, "exit_nonzero", "GEN_B")
	sess.enqueueJobNotification(jobNotification{JobID: "job_A"})
	sess.enqueueJobNotification(jobNotification{JobID: "job_B"})

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests = %d, want one coalesced notification turn", got)
	}
	text := deliveredNotificationText(t, adapter)
	for _, jobID := range []string{"job_A", "job_B"} {
		if strings.Count(text, `job_id="`+jobID+`"`) != 1 {
			t.Errorf("coalesced frame lost or duplicated %s:\n%s", jobID, text)
		}
	}
	recs, err := sess.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs["job_A"].NotifyState != jobstore.NotifyDelivered || recs["job_B"].NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("per-job delivery states: A=%s B=%s", recs["job_A"].NotifyState, recs["job_B"].NotifyState)
	}
}

func countCoalescedNotificationFrames(turns []schema.Turn, jobIDs ...string) int {
	count := 0
	for _, turn := range turns {
		if turn.Kind != schema.TurnSteering || !strings.Contains(turn.Message.Text(), "<job-notification") {
			continue
		}
		containsEveryJob := true
		for _, jobID := range jobIDs {
			if !strings.Contains(turn.Message.Text(), `job_id="`+jobID+`"`) {
				containsEveryJob = false
				break
			}
		}
		if containsEveryJob {
			count++
		}
	}
	return count
}

func transcriptTurnsFromFS(t *testing.T, fs afero.Fs, path string) []schema.Turn {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatal(err)
	}
	var turns []schema.Turn
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var entry transcript.Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode transcript line: %v", err)
		}
		if entry.Kind == "entry" {
			turns = append(turns, entry.Turn)
		}
	}
	return turns
}

func TestCoalescedTerminalTranscriptAppendFailureRetriesOneDurableFrame(t *testing.T) {
	sess, adapter := newNotificationExcerptSession(t)
	if err := sess.transcript.Close(); err != nil {
		t.Fatalf("close original transcript: %v", err)
	}

	baseFS := afero.NewMemMapFs()
	plan := bytes.Repeat([]byte{0x01}, 128)
	// NewWriterWithFS consumes operations 0-3 for mkdir/create/header write/sync.
	// AppendDurable seeks at operation 4, then this faults its entry write at 5;
	// the writer rolls the partial append back before appendTurnDurably can add history.
	plan[5] = 0x00
	const transcriptPath = "/session/transcript.jsonl"
	w, err := transcript.NewWriterWithFS(
		fault.FS(baseFS, fault.FromBytes(plan)),
		transcriptPath,
		transcript.Header{
			SessionID: sess.ID(), CreatedAt: time.Unix(0, 0).UTC(),
			ProfileID: "openai", Model: "gpt-5.2",
		},
	)
	if err != nil {
		t.Fatalf("new faultable transcript writer: %v", err)
	}
	sess.transcript = w

	appendPendingTerminal(t, sess.jobManager, sess.ID(), "job_A", jobstore.StatusCompleted, "exit_zero", "GEN_A")
	appendPendingTerminal(t, sess.jobManager, sess.ID(), "job_B", jobstore.StatusStopped, "runtime_lost", "GEN_B")
	sess.enqueueJobNotification(jobNotification{JobID: "job_A"})
	sess.enqueueJobNotification(jobNotification{JobID: "job_B"})

	if sess.acceptNotificationInput(context.Background()) {
		t.Fatal("notification input proceeded after durable frame append failed")
	}
	if len(adapter.Requests()) != 0 || sess.peekNotifications() != 2 {
		t.Fatalf("append failure requests=%d queued=%d", len(adapter.Requests()), sess.peekNotifications())
	}
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if got := countCoalescedNotificationFrames(history, "job_A", "job_B"); got != 0 {
		t.Fatalf("combined frames in history after failed append = %d, want 0", got)
	}
	if got := countCoalescedNotificationFrames(transcriptTurnsFromFS(t, baseFS, transcriptPath), "job_A", "job_B"); got != 0 {
		t.Fatalf("combined durable frames after failed append = %d, want 0", got)
	}
	recs, err := sess.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs["job_A"].NotifyState != jobstore.NotifyPending || recs["job_B"].NotifyState != jobstore.NotifyPending {
		t.Fatalf("append failure advanced delivery: A=%s B=%s", recs["job_A"].NotifyState, recs["job_B"].NotifyState)
	}

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("retry notification turn: %v", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests after retry = %d, want 1", got)
	}
	if got := sess.peekNotifications(); got != 0 {
		t.Fatalf("queued notifications after retry = %d, want 0", got)
	}
	text := deliveredNotificationText(t, adapter)
	for _, jobID := range []string{"job_A", "job_B"} {
		if strings.Count(text, `job_id="`+jobID+`"`) != 1 {
			t.Fatalf("retry lost or duplicated %s:\n%s", jobID, text)
		}
	}
	sess.mu.Lock()
	history = append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if got := countCoalescedNotificationFrames(history, "job_A", "job_B"); got != 1 {
		t.Fatalf("combined frames in history after retry = %d, want 1", got)
	}
	if got := countCoalescedNotificationFrames(transcriptTurnsFromFS(t, baseFS, transcriptPath), "job_A", "job_B"); got != 1 {
		t.Fatalf("combined durable frames after retry = %d, want 1", got)
	}
	recs, err = sess.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs["job_A"].NotifyState != jobstore.NotifyDelivered || recs["job_B"].NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("retry did not settle both ledgers: A=%s B=%s", recs["job_A"].NotifyState, recs["job_B"].NotifyState)
	}
}

func TestCoalescedTerminalDeliveryFailureSettlesIndependentlyWithoutFrameReplay(t *testing.T) {
	sess, adapter := newNotificationExcerptSession(t)
	appendPendingTerminal(t, sess.jobManager, sess.ID(), "job_A", jobstore.StatusCompleted, "exit_zero", "GEN_A")
	appendPendingTerminal(t, sess.jobManager, sess.ID(), "job_B", jobstore.StatusFailed, "exit_nonzero", "GEN_B")
	sess.enqueueJobNotification(jobNotification{JobID: "job_A"})
	sess.enqueueJobNotification(jobNotification{JobID: "job_B"})

	originalAppend := sess.jobManager.appendEvent
	deliveryAppends := 0
	sess.jobManager.appendEvent = func(event jobstore.Event) error {
		if event.Kind == jobstore.EventJobNotificationDelivered {
			deliveryAppends++
			if deliveryAppends == 2 {
				return errors.New("injected second delivery failure")
			}
		}
		return originalAppend(event)
	}
	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests after coalesced frame = %d, want 1", got)
	}
	recs, err := sess.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs["job_A"].NotifyState != jobstore.NotifyDelivered || recs["job_B"].NotifyState != jobstore.NotifyPending ||
		recs["job_A"].TerminalGen != "GEN_A" || recs["job_B"].TerminalGen != "GEN_B" {
		t.Fatalf("independent first delivery: A=%s B=%s", recs["job_A"].NotifyState, recs["job_B"].NotifyState)
	}

	// B's block is already in the durable combined frame. The retry settles its
	// ledger as already injected and must not issue another model request.
	if sess.acceptNotificationInput(context.Background()) {
		t.Fatal("already-injected B unexpectedly requested another notification turn")
	}
	recs, err = sess.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs["job_A"].NotifyState != jobstore.NotifyDelivered || recs["job_B"].NotifyState != jobstore.NotifyDelivered ||
		recs["job_A"].TerminalGen != "GEN_A" || recs["job_B"].TerminalGen != "GEN_B" {
		t.Fatalf("delivery retry did not settle independently: A=%s B=%s", recs["job_A"].NotifyState, recs["job_B"].NotifyState)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("durable combined frame replayed: model requests=%d", got)
	}
}
```

Add a pure rendering table for `completed`, `failed`, `stopped`, and `exhausted`; assert the `event` and `status` attributes contain the exact input and that no two outputs are equal. This is presentation coverage only: preserve Project 1's existing `StatusExhausted` and budget attributes without changing budget lifecycle semantics here.

- [ ] **Step 3: Run the tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestTerminalNotificationsCoalesce|TestCoalescedTerminalTranscriptAppendFailure|TestCoalescedTerminalDeliveryFailure|TestTerminalNotificationStatusesRenderDistinctly' -count=1 -v
```

Expected: PASS on the inspected implementation. A failure contradicts the characterized baseline: apply systematic debugging and stop to revise this plan with Jesse before changing production. This task remains test-only; do not change `acceptNotificationInput`, transcript writer code, jobstore event shapes, or watch delivery.

- [ ] **Step 4: Commit the contract tests**

```bash
git status --short
git add agent/job_notify_test.go
git commit -m "test(agent): lock terminal notification coalescing

Prove multiple terminal jobs share one durable steering frame while retaining
independent terminal generations and pending/delivered state. Cover both a
real durable transcript append failure with rollback/retry and a split
delivery-mark failure without frame replay."
```

---

### Task 5: Update model prompts and tool-fluency probes

**Files:**

- Modify: `agent/prompts/sections/background-jobs.md`
- Modify: `agent/prompts/sections/delegation.md`
- Modify: `agent/profile_test.go`
- Create: `tools/tool-fluency/cmd/serf-fluency/job_supervision_probe_test.go`
- Modify: `tools/tool-fluency/probes/jobs_control.yaml`
- Modify: `tools/tool-fluency/probes/job_watch.yaml`
- Modify: `tools/tool-fluency/README.md`

**Interfaces:**

- Consumes: `job_status(job_id)`, `read_transcript(transcript_ref, cursor, max_bytes, range)`, automatic terminal notifications, and unchanged `job_watch`.
- Produces: prompt/probe guidance with no callable or suggested `job_read_output` path.

- [ ] **Step 1: Write failing prompt and probe contract tests**

Extend the existing background-job prompt test in `agent/profile_test.go`:

```go
	for _, want := range []string{
		"terminal notification is automatic",
		"job_status",
		"read_transcript",
		"cursor",
		"Do not infer completion from quiet time",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("background-job prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "job_read_output") {
		t.Error("background-job prompt still advertises job_read_output")
	}
```

Create `tools/tool-fluency/cmd/serf-fluency/job_supervision_probe_test.go` with an offline contract test over the real probe directory:

```go
package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestJobSupervisionProbesUseCurrentSurface(t *testing.T) {
	probes, err := loadProbes(filepath.Join("..", "..", "probes"), "all")
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]probeFile, len(probes))
	for _, probe := range probes {
		byID[probe.ID] = probe
	}
	jobs, ok := byID["jobs.control_lifecycle"]
	if !ok {
		t.Fatal("jobs.control_lifecycle probe missing")
	}
	calls := make(map[string]bool, len(jobs.Expect.Calls))
	for _, call := range jobs.Expect.Calls {
		calls[call.Tool] = true
	}
	for _, want := range []string{"job_status", "read_transcript"} {
		if !calls[want] {
			t.Errorf("jobs control probe missing %q", want)
		}
	}
	for _, id := range []string{"jobs.control_lifecycle", "job_watch.observer_callback"} {
		probe, ok := byID[id]
		if !ok {
			t.Fatalf("probe %q missing", id)
		}
		parts := []string{probe.Prompt}
		for _, call := range probe.Expect.Calls {
			parts = append(parts, call.Tool)
		}
		parts = append(parts, probe.Expect.ForbiddenCalls...)
		if strings.Contains(strings.Join(parts, "\n"), "job_read_output") {
			t.Errorf("probe %q still names job_read_output", id)
		}
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'Test.*Background.*Prompt|TestCoordinatorWorkflowUsesCurrentJobSupervisionSurface' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./tools/tool-fluency/... -run '^TestJobSupervisionProbesUseCurrentSurface$' -count=1 -v
```

Expected: FAIL because the prompt lacks the exact bounded-cursor/quiet-time guidance and `jobs_control.yaml` still expects `job_read_output`.

- [ ] **Step 3: Replace the background-job decision guidance**

Ensure `agent/prompts/sections/background-jobs.md` contains this compact contract:

```markdown
Completion is notification-driven: the terminal notification is automatic.
Use `job_status(job_id)` once when you need lifecycle, phase, timing, terminal
reason, transcript reference, exit code, or delegate resumability. Do not infer
completion from quiet time and never poll `job_status`.

Use `read_transcript(transcript_ref)` for evidence. Job-log reads default to the
newest bounded bytes. Continue incrementally with the returned `next_cursor`, or
request an explicit `bytes:START-END` range; reusing an end-of-log cursor returns
no content. Use `job_watch` for a future output match. Transcript reading is not
a grep/wait primitive.
```

Keep the existing observer callback and self-watch guidance intact. In `delegation.md`, retain `delegate_id` versus `job_id` handle guidance and mention `read_transcript` only through the returned transcript ref.

- [ ] **Step 4: Update the probes exactly**

Replace `tools/tool-fluency/probes/jobs_control.yaml` with:

```yaml
schema: 1
id: jobs.control_lifecycle
tool: job_list
prompt: |
  Use shell with background=true to start: sh -c 'printf JOB_READY; sleep 30'
  Use job_list once to find the running job, job_status once to get its transcript_ref,
  read_transcript once to read JOB_READY, and job_stop with max_wait_ms 5000 to stop it.
  Do not poll. Finish with RESULT_JOBS.
expect:
  calls:
    - tool: shell
    - tool: job_list
    - tool: job_status
    - tool: read_transcript
    - tool: job_stop
    - tool: communicate
  final_contains: ["RESULT_JOBS"]
```

In `job_watch.yaml`, replace `job_read_output` in `forbidden_calls` with `read_transcript` so the probe still rejects redundant post-callback auditing without naming a removed tool.

Update the tool-fluency README Jobs/Transcripts rows to:

```markdown
| Jobs | `job_status`, `job_list`, `job_stop` | State/recovery/control without polling; completion is automatic. |
| Transcripts | `read_transcript`, `find_session_transcripts`, `read_session_transcript` | Bounded job evidence and explicit session archive/audit reads. |
```

- [ ] **Step 5: Run prompt/probe tests and audit current model guidance**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'Test.*Prompt|TestCoordinatorWorkflowUsesCurrentJobSupervisionSurface' -count=1
GOCACHE=/tmp/serf-gocache go test ./tools/tool-fluency/... -count=1
rg -n 'job_read_output' agent/prompts agent/internal/tool/definitions.go internal/bundled/plugins/coordinator-workflow/agents tools/tool-fluency/probes tools/tool-fluency/README.md
```

Expected: tests PASS; `rg` exits 1 with no matches.

- [ ] **Step 6: Commit**

```bash
git status --short
git add agent/prompts/sections/background-jobs.md agent/prompts/sections/delegation.md agent/profile_test.go tools/tool-fluency/cmd/serf-fluency/job_supervision_probe_test.go tools/tool-fluency/probes/jobs_control.yaml tools/tool-fluency/probes/job_watch.yaml tools/tool-fluency/README.md
git commit -m "docs(agent): teach notification-first job supervision

Route orientation to job_status, evidence to bounded transcript cursors, and
future matches to job_watch across the model prompt and fluency probes."
```

---

### Task 6: Rewrite current docs and manual scenarios around the new surface

**Files:**

- Modify the current docs and scenarios listed under “Current guidance, probes, and scenarios.”
- Delete/Create the two renamed scenarios listed there.

**Interfaces:**

- Consumes: the exact public contract under Shared Interfaces.
- Produces: current documentation and executable/manual examples with no removed-tool references.

- [ ] **Step 1: Replace the authoritative job-control output section**

In `docs/job-control.md`, remove the `### job_read_output` section and replace it with:

```markdown
### `job_status` and transcript evidence

`job_status(job_id)` is the compact orientation read. It reports job identity,
kind, lifecycle status, observable phase, terminal reason, running/duration/quiet
timing, start/last-event/end timestamps, transcript reference, shell exit code,
and delegate resumability. It does not return output, transcript turns, terminal
generations, or notification delivery state. Quiet time is not completion;
terminal notification is authoritative.

`read_transcript(transcript_ref)` reads evidence. A shell transcript ref is
`job:<job_id>`; a delegate ref names its session transcript. Job-log reads report
`total_bytes`, `dropped_bytes`, `returned_byte_range`, and `next_cursor`. The
default is the newest 8192 retained bytes. `cursor` reads forward without replay;
`range="bytes:START-END"` selects a half-open lifetime byte range. A cursor at
end-of-log returns empty content. Use `job_watch(output_match=...)` for a future
match; transcript reads never wait or grep.
```

Then make these exact conceptual substitutions throughout the same file:

| Old claim | Replacement |
|---|---|
| output/status via `job_read_output` | state via `job_status`; evidence via the returned transcript ref |
| `job_read_output(max_wait_ms/grep)` | `job_watch(output_match=...)` for future match, then `read_transcript` for evidence |
| delegate invocation log as authoritative result | delegate/session transcript plus returned result/terminal notification |
| observer grant extends output reader | observer grant allows only `read_transcript(job:<watched_job_id>)` |
| notification points to output reader | notification points to its `transcript_ref` when more evidence exists |
| tool matrices include the removed name | include `job_status` and `read_transcript` |

Do not change stop semantics, watch triggers, retention limits, nested job IDs, or shell launch/wait behavior while rewriting prose.

- [ ] **Step 2: Update testing/architecture/fluency guidance**

Apply these exact changes:

- `docs/agentic-testing.md`: doctor audits count `job_status` and `read_transcript`; healthy callback scenarios expect no polling before callback.
- `docs/architecture.md`: describe compact status plus transcript references; do not claim AppWire gained a new method.
- `docs/skills/tool-fluency/SKILL.md`: evidence block uses `parent job_status count` and `parent read_transcript count`.
- `docs/subagent-management/08-standalone-llm-calls.md`: replace output retrieval with the delegate's returned transcript ref.
- `docs/web-ui/ux-and-implementation-plan.md`: stale-running reconciliation names `job_status`/`job_list`/terminal events, not the removed reader.

- [ ] **Step 3: Migrate every current manual scenario**

For each exact scenario path listed under “Current guidance, probes, and scenarios,” use this mapping:

| Scenario intent | New action |
|---|---|
| inspect shell output now | call `job_status(job_id)`, then `read_transcript` with its `job:<job_id>` ref |
| inspect delegate work | call `read_transcript` with the delegate/session `transcript_ref` |
| wait for a token | create `job_watch(output_match=...)`, end the turn, then inspect transcript evidence after notification |
| assert no polling | count `job_status`/`job_list` loops and redundant `read_transcript` calls, never name the removed tool |
| observer reads watched job | use durable grant with `read_transcript(job:<watched_job_id>)` |
| verify repeated reads do not consume | use the same cursor at end and assert empty content |

Rename the two scenario files exactly as listed in File Structure and update `test/scenarios/INDEX.md` links/summaries. `job-transcript-cursor-and-output-watch.md` must cover one output-match notification, one bounded transcript read, cursor advancement, and end-cursor empty output. `subagent-list-and-transcript.md` must use `job_list` for inventory and the delegate `transcript_ref` for conversation evidence.

- [ ] **Step 4: Run documentation audits**

Run:

```bash
rg -n 'job_read_output' docs/job-control.md docs/agentic-testing.md docs/architecture.md docs/skills/tool-fluency/SKILL.md docs/subagent-management/08-standalone-llm-calls.md docs/web-ui/ux-and-implementation-plan.md test/scenarios
rg -n 'max_wait_ms|grep' docs/job-control.md test/scenarios/job-transcript-cursor-and-output-watch.md
```

Expected: first `rg` exits 1 with no matches. The second may find `max_wait_ms` only on still-supported `delegate`, `delegate_send`, or `job_stop`, and `grep` only in explicit statements that transcript reading does not grep; no transcript read uses either as an argument.

- [ ] **Step 5: Commit**

```bash
git status --short
git add -- \
  docs/job-control.md docs/agentic-testing.md docs/architecture.md \
  docs/skills/tool-fluency/SKILL.md docs/subagent-management/08-standalone-llm-calls.md \
  docs/web-ui/ux-and-implementation-plan.md test/scenarios/INDEX.md \
  test/scenarios/job-delegate-result-schema.md test/scenarios/job-delegate-wait-no-poll.md \
  test/scenarios/job-nested-visibility.md test/scenarios/job-notification-wake.md \
  test/scenarios/job-restart-durability.md test/scenarios/job-send-message-surface.md \
  test/scenarios/job-shell-lifecycle.md test/scenarios/job-stop-and-children.md \
  test/scenarios/job-watch-caller-notification-delivery.md \
  test/scenarios/job-watch-output-match-catchup.md \
  test/scenarios/job-watch-passive-observer-noop-filter.md \
  test/scenarios/job-watch-sidecar-observer.md \
  test/scenarios/recursion-coordinator-fanout.md \
  test/scenarios/recursion-deaf-coordinator-drivedown.md \
  test/scenarios/sidecar-approval-broker-communicate.md \
  test/scenarios/sidecar-handoff-packager-job-notification.md \
  test/scenarios/sidecar-memory-reminder-read-file.md \
  test/scenarios/sidecar-progress-digest-output-match.md \
  test/scenarios/sidecar-runbook-capture-output-match.md \
  test/scenarios/sidecar-test-triage-output-match.md \
  test/scenarios/transcript-subagent-audit-children-of.md \
  test/scenarios/job-read-output-blocking-grep.md \
  test/scenarios/job-transcript-cursor-and-output-watch.md \
  test/scenarios/subagent-list-and-output.md \
  test/scenarios/subagent-list-and-transcript.md
git commit -m "docs: replace job output reads with status and transcripts

Update the authoritative job-control contract and current manual scenarios to
use notification-first completion, compact status, bounded transcript cursors,
and job_watch for future matches."
```

---

### Task 7: Full verification and scope audit

**Files:**

- Verify only; modify no files unless a preceding scoped change is broken.

**Interfaces:**

- Consumes: all prior task commits.
- Produces: deterministic proof across agent, jobstore, server/AppWire, prompts, probes, and docs.

- [ ] **Step 1: Format changed Go files**

Run:

```bash
gofmt -w agent/internal/tool/definitions.go agent/session_tools_jobs.go agent/session_tools_transcript.go agent/session_tool_registry.go agent/job_watch.go agent/session_config.go agent/subagents.go agent/job_delegate.go agent/internal/jobstore/fold.go agent/internal/jobstore/output.go agent/jobs.go agent/job_output_digest.go agent/internal/tool/definitions_test.go agent/internal/tool/definitions_program_fuzz_test.go agent/job_supervision_test.go agent/transcript_tools_test.go agent/job_notify_test.go agent/jobs_test.go agent/internal/jobstore/output_test.go agent/profile_test.go agent/job_nested_test.go agent/job_watch_test.go agent/job_watch_loopguard_test.go agent/root_watch_tree_program_fuzz_test.go agent/job_runtime_recovery_program_fuzz_test.go agent/session_tools_shell_test.go agent/session_tools_jobs_test.go agent/session_tools_jobs_list_test.go agent/session_tools_jobs_stop_delegate_test.go agent/session_tools_jobs_fuzz_test.go agent/session_tools_jobs_lifecycle_fuzz_test.go agent/session_tools_jobs_seed100_more_test.go agent/session_tools_jobs_seed100_range_a_test.go agent/session_tools_jobs_seed100_range_b_test.go agent/job_transcript_recovery_grant_fuzz_test.go agent/job_transcript_projection_seed_coverage_fuzz_test.go tools/tool-fluency/cmd/serf-fluency/job_supervision_probe_test.go
gofmt -w agent/job_watch_timers_observe_fuzz_test.go agent/watch_grant_lifecycle_fuzz_test.go agent/jobs_seed100_fuzz_test.go agent/fuzz_fc2_dispatch_test.go agent/cov_s3_jobsfmt_test.go agent/cov_w2tail_jobs_helpers_test.go agent/shell_notify_digest_program_fuzz_test.go agent/session_tools_jobs_seed100_range_c_test.go agent/session_tools_jobs_seed100_range_d_test.go
```

Expected: exit 0 and no unrelated file changes.

- [ ] **Step 2: Run focused contract suites**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/jobstore -run 'TestOutputStoreFlush|TestFold.*Notification|Test.*Notification' -count=1
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestJobStatus|TestJobTools_Exhausted|TestJobNotification_Exhausted|TestReadTranscriptJobRef|Test.*Transcript.*Grant|TestFinalize|TestRestartRearm|TestReconcileLost|TestTerminalNotification|TestCoalescedTerminal' -count=1
GOCACHE=/tmp/serf-gocache go test ./agent ./agent/internal/tool -run 'ReadSessionTranscript|APILogSource|AttemptExpansion|OversizedExpansion' -count=1
GOCACHE=/tmp/serf-gocache go test ./tools/tool-fluency/... -count=1
GOCACHE=/tmp/serf-gocache go test ./internal/appprojector ./server -run 'TestProjectJobRecord_Exhausted|Test.*Exhausted|TestAppDiagnosticsFromDetailedStatus|TestServerAppWireThreadReadReturnsStatus' -count=1
make fuzz-registry-check
```

Expected: PASS. The server command is the AppWire no-protocol-change proof.

- [ ] **Step 3: Run default deterministic suites**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/... -count=1
GOCACHE=/tmp/serf-gocache go test ./server/... -count=1
make test
```

Expected: PASS with no provider credentials, network access, quota, live model behavior, sleeps, or ambient developer state required.

- [ ] **Step 4: Run static scope and hygiene audits**

```bash
rg -n 'DefJobReadOutput|func jobReadOutputTool|type jobReadOutputResult' agent
rg -n 'job_read_output' agent/prompts agent/internal/tool/definitions.go internal/bundled/plugins/coordinator-workflow/agents tools/tool-fluency/probes tools/tool-fluency/README.md docs/job-control.md docs/agentic-testing.md docs/architecture.md docs/skills/tool-fluency/SKILL.md docs/subagent-management/08-standalone-llm-calls.md docs/web-ui/ux-and-implementation-plan.md test/scenarios
rg -n 'FuzzJobOutputDigestSeedCoverage|FuzzJobtoolsExec|FuzzJobReadRecoveryGrant' scripts/run-fuzz.sh
git diff --check
git status --short
```

Expected: all three `rg` commands exit 1 with no matches; `git diff --check` exits 0. `git status` contains only scoped files and preserves Jesse's pre-existing unrelated untracked files.

Inspect the final diff and confirm it contains none of these out-of-scope changes:

```text
Superpowers behavior or files other than this committed plan
new gate-execution service
shell launcher/executor or retention-cap changes
provider API-log access from ordinary transcript reads
job_watch trigger/filter/delivery semantic changes
new AppWire methods or fields
definitions/accounting changes to `jobstore.StatusExhausted` or exhaustion metadata (preservation assertions are expected)
compatibility alias, deprecated registration, or fallback dispatch for job_read_output
```

Verification is read-only at this point. A failure returns to the task that owns the broken contract, where the red/green step and scoped commit are repeated; do not make an unplanned verification-cleanup commit.
