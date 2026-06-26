# Task 1 Report: Backend event payload linkage

## Status
DONE

## Summary
Implemented backend job event payload linkage for delegate/subagent runs in the isolated worktree `/home/jesse/git/prime-radiant/serf/.worktrees/subagent-run-rendering`.

Changes made:
- Extended `events.JobStartedData` with linkage fields:
  - `delegate_id`
  - `task`
  - `transcript_ref`
  - `origin_turn_id`
  - `origin_tool_call_id`
  - `origin_item_id`
- Extended `events.JobFinishedData` with linkage fields:
  - `delegate_id`
  - `task`
  - `origin_turn_id`
  - `origin_tool_call_id`
  - `origin_item_id`
- Updated `jobManager.emitJobStarted` and `jobManager.emitJobFinished` to populate linkage from:
  - `jobstore.Event`
  - live `runningJob.rec`
  - delegate restore descriptor fallback for original delegate task/origin tool call linkage on resumed delegate runs
- Added focused create-flow event linkage coverage.
- Added focused resume-flow event linkage coverage.

## Files changed
- `agent/events/payloads.go`
- `agent/jobs.go`
- `agent/job_delegate_create_test.go`
- `agent/job_delegate_send_test.go`

## TDD evidence
Initial failing focused test was added first and run before implementation:

```bash
go test ./agent -run TestDelegateJobEventsCarrySubagentRunLinkage -count=1 -v
```

Expected failure observed before implementation:

```text
agent/job_delegate_create_test.go:58:35: got.DelegateID undefined (type events.JobStartedData has no field or method DelegateID)
agent/job_delegate_create_test.go:58:71: got.Task undefined (type events.JobStartedData has no field or method Task)
agent/job_delegate_create_test.go:58:104: got.TranscriptRef undefined (type events.JobStartedData has no field or method TranscriptRef)
agent/job_delegate_create_test.go:58:131: got.OriginToolCallID undefined (type events.JobStartedData has no field or method OriginToolCallID)
FAIL	primeradiant.com/serf/agent [build failed]
```

After implementation and adding the resume-flow test, the focused tests passed:

```bash
go test ./agent -run 'TestDelegateJobEventsCarrySubagentRunLinkage|TestDelegateResumeJobStartedKeepsOriginalOriginLinkage' -count=1 -v
```

Result:

```text
=== RUN   TestDelegateJobEventsCarrySubagentRunLinkage
--- PASS: TestDelegateJobEventsCarrySubagentRunLinkage (0.10s)
=== RUN   TestDelegateResumeJobStartedKeepsOriginalOriginLinkage
--- PASS: TestDelegateResumeJobStartedKeepsOriginalOriginLinkage (0.13s)
PASS
ok  	primeradiant.com/serf/agent	0.254s
```

## Package-level verification
Ran the required package-level covering test:

```bash
go test ./agent -count=1
```

Result:

```text
ok  	primeradiant.com/serf/agent	9.701s
```

Also ran:

```bash
git diff --check
```

Result: passed with no whitespace errors.

## Commit
Created commit:

```text
d7237f83 feat(agent): carry delegate linkage in job events
```

Commit summary:

```text
agent/events/payloads.go          | 35 +++++++++------
agent/job_delegate_create_test.go | 43 +++++++++++++++++++
agent/job_delegate_send_test.go   | 48 +++++++++++++++++++++
agent/jobs.go                     | 90 ++++++++++++++++++++++++++++++++-------
4 files changed, 188 insertions(+), 28 deletions(-)
```

## Self-review notes
- Scope stayed limited to Task 1 backend event payload linkage.
- Did not change AppWire/Codex wire methods.
- Did not modify the original checkout.
- Did not modify the unrelated `kimi-jobs-ux-cleanup.md` file.
- Only staged/committed the four requested task files.
- The resume-flow event assertion required emitted payloads to preserve the original delegate launch task/origin, not the later `delegate_send` message. The implementation therefore uses the delegate restore descriptor as an additional fallback source for `Task` and `OriginToolCallID` when available, while preserving the requested `jobstore.Event` and `runningJob.rec` sources.

## Concerns
None.

## Review fix: OriginItemID durable linkage

Status: DONE

Findings addressed:
- Added `OriginItemID` to durable job linkage (`jobstore.Event`, `jobstore.JobRecord`, and `DelegateRestoreDescriptor`) so `origin_item_id` can round-trip through job start folding and delegate resume restore metadata.
- Preserved the canonical provider/tool item identity by adding `llm.ToolCallData.ItemID`, carrying OpenAI Responses `item_id` through streaming tool-call events/accumulation, and threading it through tool execution context into delegate/subagent job creation.
- Populated `OriginItemID` in both `emitJobStarted` and `emitJobFinished`, preferring live `runningJob.rec` / restore descriptor linkage when available and falling back to durable terminal/start event fields.
- Extended focused delegate create/resume event tests to seed an origin item ID and assert `JobStartedData.OriginItemID` is non-empty and preserved.
- Added the requested comment documenting why restored original delegate task overrides resumed job record task for linkage payloads.

Tests run:

```bash
go test ./agent -run 'TestDelegateJobEventsCarrySubagentRunLinkage|TestDelegateResumeJobStartedKeepsOriginalOriginLinkage' -count=1 -v
```

Result:

```text
=== RUN   TestDelegateJobEventsCarrySubagentRunLinkage
--- PASS: TestDelegateJobEventsCarrySubagentRunLinkage (0.10s)
=== RUN   TestDelegateResumeJobStartedKeepsOriginalOriginLinkage
--- PASS: TestDelegateResumeJobStartedKeepsOriginalOriginLinkage (0.13s)
PASS
ok  	primeradiant.com/serf/agent	0.251s
```

```bash
go test ./agent -count=1
```

Result:

```text
ok  	primeradiant.com/serf/agent	8.826s
```

```bash
git diff --check
```

Result: passed with no whitespace errors.

Concerns: None.
## Re-review fix: OpenAI Responses final function_call ItemID propagation

Status: DONE

Findings addressed:
- Parsed OpenAI Responses final `function_call` output item identity into `llm.ToolCallData.ItemID` in `fromResponses`, using output item `id` and falling back to `item_id` if present.
- Preserved the existing naming distinction: `ToolCallData.ID` remains the Responses `call_id`; `ToolCallData.ItemID` carries the provider output item id.
- Extended deterministic OpenAI provider tests so a completed Responses `function_call` with `id`/`call_id` reaches `ToolCallData.ItemID`, including the `Complete`/`StreamAccumulator.Response()` path where final response content replaces streamed accumulated content.
- Added a focused `llm.StreamAccumulator` regression test documenting that when a final response with content is accepted, its tool-call `ItemID` must be present in `Response().ToolCalls()`.
- Corrected an accidental edit made in the original checkout during the fix attempt; verified the original checkout no longer has these modifications and all final changes are in the requested worktree only.

Tests run:

```bash
go test ./llm -run TestStreamAccumulator_FinishWithContentResponsePreservesFinalToolCallItemID -count=1 -v
```

Result:

```text
=== RUN   TestStreamAccumulator_FinishWithContentResponsePreservesFinalToolCallItemID
--- PASS: TestStreamAccumulator_FinishWithContentResponsePreservesFinalToolCallItemID (0.00s)
PASS
ok  	primeradiant.com/serf/llm	0.005s
```

```bash
go test ./llm/providers/openai -run 'TestAdapter_Complete_OAuthTransportPreservesStreamedToolCallsWhenCompletedOutputIsEmpty|TestFromResponses_FunctionCallPreservesOutputItemID|TestAdapter_Complete_OAuthTransportTracksItemIDAndFragmentedToolArguments' -count=1 -v
```

Result:

```text
=== RUN   TestAdapter_Complete_OAuthTransportPreservesStreamedToolCallsWhenCompletedOutputIsEmpty
--- PASS: TestAdapter_Complete_OAuthTransportPreservesStreamedToolCallsWhenCompletedOutputIsEmpty (0.00s)
=== RUN   TestAdapter_Complete_OAuthTransportTracksItemIDAndFragmentedToolArguments
--- PASS: TestAdapter_Complete_OAuthTransportTracksItemIDAndFragmentedToolArguments (0.00s)
=== RUN   TestFromResponses_FunctionCallPreservesOutputItemID
--- PASS: TestFromResponses_FunctionCallPreservesOutputItemID (0.00s)
PASS
ok  	primeradiant.com/serf/llm/providers/openai	0.011s
```

```bash
go test ./agent -count=1
```

Result:

```text
ok  	primeradiant.com/serf/agent	9.242s
```

```bash
go test ./llm/providers/openai -count=1
```

Result:

```text
ok  	primeradiant.com/serf/llm/providers/openai	0.316s
```

```bash
git diff --check
```

Result: passed with no whitespace errors.

Concerns: None.

