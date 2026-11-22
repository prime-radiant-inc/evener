# `communicate` Quiet Yield, `end_reason` Removal, and Root Bare-Text Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unify `communicate` across all agent roles onto `(message?, end_turn?: bool = true, output?)` where `communicate({})` quietly ends a turn without sending anything, delete `end_reason` / `waiting_on_work` / `needs_response` so `ask_user` alone owns `SessionAwaiting`, and add a Claude `--brief`-style bare-text fallback on untyped root `EntryUserInput` turns.

**Architecture:** `DefCommunicateNamed` exposes one 3-property schema (`strict: false`) everywhere; `WithCommunicateOutputSchema` and `WithAllowedDecisions` splice `properties.output` without force-requiring `"output"` at the top level so progress updates (`end_turn: false`) and quiet yields (`communicate({})`) pass schema validation while final replies (`end_turn: true` with non-empty `message`) enforce required structured output in the handler. Quiet yields record `communicate_quiet: true` in `ToolResult.ToolState`, suppressing `TurnCommunicate` emission, `apptranscript` reload rendering, `assistant.tool` / wildcard watch firing, and pre-drain subagent missing-report nudges while enforcing the dead-wait guard (`s.hasWakeableWork()` on roots, `s.treeHasOutstandingWork()` on report-owing subagents). `armAwaitingAtSettle` shrinks to `len(s.askPending) > 0`, and untyped root `EntryUserInput` `FinishReasonStop` prose settles through the same `Stop`-hook + accepted-reply completion path as `communicate({"message": ...})`.

**Tech Stack:** Go (`agent`, `agent/internal/tool`, `agent/provider`, `agent/doctor`, `internal/apptranscript`, `cmd/evener`), Go templates (`agent/prompts/system.md.tmpl`), Markdown product docs (`docs/product/`).

**Spec:** `docs/superpowers/specs/2026-11-22-communicate-quiet-yield-design.md`

## Global Constraints

- Never assert on human-readable error message phrasing, tool description prose, or UI copy in unit/behavior tests (`docs/developing-evener/testing.md`, "Copy Is Not a Test Oracle" and "Prompt Prose Is Not a Test Oracle"). Pin prompt and tool-description wording exclusively via whole-output golden files (`-update-prompt`), and assert behavior via `IsError`, `ToolState`, session state, emitted events, provider request count, and structured wire fields.
- `DefCommunicateNamed` must keep `Strict: &strictFalse` (`strict: false`) so OpenAI Responses and strict-capable providers do not force-require optional `message`, `end_turn`, or `output` properties.
- Preserve `--result-tool-name` (`SessionConfig.ResultToolName`), exact `json.Number` decoding in `unmarshalCommunicateArguments`, and `#570` atomic first-winner terminal capture.
- Do not advertise `end_reason` on `communicate`. Retain read-only decoding of historical `TurnCommunicate.EndReason` (`"waiting_on_work"`, `"needs_response"`, `"done"`) in `agent/job_watch_hub.go` so archived transcripts replay without false completions.
- Do not modify `hubapi/attention.go` or TypeScript `needsResponseRest` normalization, which continue normalizing `state: "awaiting", ask_pending: false` frames from older remote daemons to `"idle"`.
- Run all test and vet commands bare in foreground mode and check exit codes; run heavy gates (`make vet`, `make lint`) sequentially, never in parallel.
- End every git commit message with the session's `Co-authored-by` trailer.

## Review Focus

- Truncated root stream (`FinishReasonLength` with non-blank prose and 0 tool calls on `EntryUserInput`): must NOT be accepted as a finished root bare-text reply; must fall through to no-tool retry/truncation handling (covered in Task 3).
- Legacy empty default envelope on a quiet yield (`{"message": "", "end_turn": true, "output": {"message": "", "data": {}, "artifacts": []}}`): must execute as a quiet yield rather than failing structured-output-without-message validation (covered in Task 2).
- Custom `result_schema` accepting `{}` or falsy fields (`{"passed": false}`) on a final reply: key-presence check (`_, ok := args["output"]`) accepts `{"message": "Failed", "output": {}}` when valid against the schema and rejects when `"output"` is omitted (covered in Task 2).
- Post-report housekeeping notification on a `reportRequired` subagent calling `communicate({})`: after a subagent has already delivered its valid final report, a trailing job notification handled with `communicate({})` inside `drainJobTreeKeepingReport` succeeds and preserves the captured report and `structured_result` (covered in Task 2).
- `doctor_evener` `outcomeHint` and `finalEndTurnIndex` when a session ends with a trailing `communicate({})` ack or a root bare-text fallback: trailing `communicate({})` must neither clobber `outcomeHint` to `"none"` nor move `finalEndTurnIndex`, and root bare-text fallback must surface its prose in `outcomeHint` (covered in Task 3).

---

### Task 1: Unified `communicate` Tool Definition & Optional Top-Level `output` Override

**Files:**
- Modify: `agent/internal/tool/definitions.go:605-665,830-965`
- Modify: `agent/internal/tool/definitions_test.go`
- Modify: `agent/provider/profile_overrides.go:17-85,110-235`
- Modify: `agent/provider/profile_overrides_test.go`

**Interfaces:**
- Consumes: `llm.ToolDefinition`, `llm.ValidateSchemaDocument`
- Produces:
  - `tool.DefCommunicateNamed(name string) llm.ToolDefinition`: `Parameters.properties` has `"message"` (`type: "string"`), `"end_turn"` (`type: "boolean", default: true`), and `"output"` (`type: "object"` with optional `"message"`, `"data"`, `"artifacts"` — no inner or outer `"required"` slice); `Strict` is `&false`.
  - Deletes `tool.CommunicateEndReasonDone`, `tool.CommunicateEndReasonNeedsResponse`, `tool.CommunicateEndReasonWaitingOnWork`, `tool.CommunicateEndReasons`, and `tool.WithCommunicateEndReason`.
  - Updates `tool.EndTurnToWait(resultTool string) string`, `tool.DefJobStatusNamed(resultTool string) llm.ToolDefinition`, and `tool.DefJobListNamed(resultTool string) llm.ToolDefinition` (dropping the `hasEndReason bool` parameter — check and update call sites in `agent/session_tools_jobs.go` / `agent/session_tool_definitions.go` so the tree compiles cleanly at the end of Task 1!).
  - Updates `tool.ObserverReporting(resultTool string) string` and `tool.WithCommunicateDelegateUpdates(def llm.ToolDefinition) llm.ToolDefinition` to describe `end_turn=false` updates, final reports (`message` with `end_turn=true` or omitted), and quiet yields (`{}`).
  - `provider.WithCommunicateOutputSchema` and `provider.WithAllowedDecisions`: replace/augment `properties.output` without adding `"output"` to top-level `Parameters["required"]` (keeping inner `output.required` intact so `t.Schema.Validate` validates `output` whenever present).

- [ ] **Step 1: Write failing schema tests in `agent/internal/tool/definitions_test.go` and `agent/provider/profile_overrides_test.go`**

Add/update tests asserting:
1. `DefCommunicate()` has `Strict != nil && !*Strict`, properties `{"message", "end_turn", "output"}`, `end_turn.default == true`, no `"end_reason"` property, and empty/absent top-level `"required"` so `{}`, `{"message": "hi"}`, and `{"message": "hi", "end_turn": false}` all pass JSON-schema validation while every property retains a non-empty `"description"` (`TestAllBuiltinParametersCarryDescriptions`).
2. `WithCommunicateOutputSchema(p, customSchema)` and `WithAllowedDecisions(p, []string{"approve", "reject"})` splice `properties.output` (and `decision` in `output.required`), leave `"output"` **absent** from top-level `Parameters["required"]` (so `{}` and `{"message": "progress", "end_turn": false}` pass JSON-schema validation), and reject an invalid `output` object when `"output"` **is** provided.

- [ ] **Step 2: Run targeted tests to verify RED**

Run: `go test -count=1 ./agent/internal/tool/... ./agent/provider/...`
Expected: FAIL on the new schema required/property assertions.

- [ ] **Step 3: Implement the unified `DefCommunicateNamed`, helper signature cleanups, and `profile_overrides.go` updates**

1. Update `DefCommunicateNamed`, `WithCommunicateDelegateUpdates`, `ObserverReporting`, `EndTurnToWait`, `DefJobStatusNamed`, and `DefJobListNamed` in `agent/internal/tool/definitions.go`, removing `CommunicateEndReason*` and `WithCommunicateEndReason`.
2. Update `replaceCommunicateOutputSchema` and `addDecisionToSchema` in `agent/provider/profile_overrides.go` so neither appends `"output"` to top-level `params["required"]`.
3. Update callers of `DefJobStatusNamed` / `DefJobListNamed` / `EndTurnToWait` in `agent/` (and stub/remove references to `tool.CommunicateEndReason*` / `tool.WithCommunicateEndReason` only if needed for `./agent/internal/tool` + `./agent/provider` compilation; `./agent` callers are completed in Task 2).

- [ ] **Step 4: Run `tool` and `provider` tests (including fuzz compile check)**

Run: `go test -race -count=1 ./agent/internal/tool/... ./agent/provider/... && go test -tags evenerfuzz -run '^$' ./agent/internal/tool/... ./agent/provider/...`
Expected: PASS (exit 0).

- [ ] **Step 5: Commit Task 1**

Stage exact touched files under `agent/internal/tool/` and `agent/provider/` and commit with `feat(tool,provider): make communicate message and output optional and drop end_reason`.

---

### Task 2: `communicate({})` Quiet Yield, Dead-Wait Guard, Subagent Drain, and `SessionAwaiting` Cleanup

**Files:**
- Modify: `agent/session_tools_communicate.go`
- Modify: `agent/session.go` (`communicateResult`, `acceptCommunicateTerminal`, `quietlyYielded`, remove `communicateEndReason`)
- Modify: `agent/session_tool_registry.go` (remove `offersEndReason`; wire `canQuietlyYield` / `recordQuietYield` deps)
- Modify: `agent/session_state.go` (`armAwaitingAtSettle` shrinks to `len(s.askPending) > 0`; remove `needsResponseQuietPeriodDefault`, `restMu`, `restOwedGeneration`, `resumeQuietRest`, `settleTerminalState` dead branches)
- Modify: `agent/session_tools_ask.go` (remove `endedOnNeedsResponse` and `communicateEndReasonOf`)
- Modify: `agent/session_testhooks.go` (remove `needsResponseQuietPeriod` and `needsResponseRestBeforeAnnounce`)
- Modify: `agent/session_tool_definitions.go` & `agent/session_tools_jobs.go` (remove `WithCommunicateEndReason` and `hasEndReason` args)
- Modify: `agent/session_tool_round.go` (`deliverIfCommunicated`: handle `s.comm.quietEnd` without `Stop` hooks or `acceptTerminalCommunicate`; clear `s.comm` when `stopResult.Blocked` is `true`)
- Modify: `agent/session_lifecycle.go` (`routeNoToolCalls`: remove notification bare-text carve-out, keeping only `kind == EntryNotification && afterTerminalCommunicate` for `#329`; pass active `EntryKind` into tool context or session field so `communicate` knows `EntryNotification` / `EntryDelegateAttention` vs initiating turns)
- Modify: `agent/subagents.go` (`(*subagent).run`: skip pre-drain `deliverCommunicateNudge` when `a.sess.quietlyYielded()` and `treeHasOutstandingWork()` holds, preserving post-drain `deliverCommunicateNudge`)
- Modify/Create/Delete tests:
  - Create: `agent/session_communicate_quiet_yield_test.go` (replaces `agent/session_communicate_end_reason_test.go`)
  - Delete: `agent/session_communicate_end_reason_test.go`, `agent/session_needs_response_quiet_test.go`, `agent/session_needs_response_restore_test.go`, `agent/session_needs_response_child_stop_test.go`
  - Modify: `agent/session_ask_test.go`, `agent/session_awaiting_test.go`, `agent/session_rest_equivalence_test.go`, `agent/session_waiting_on_work_test.go`, `agent/session_waiting_root_wake_test.go`, `agent/notification_bare_text_test.go`, `agent/delegate_resource_supervision_test.go`, `agent/subagent_owned_job_drain_test.go`, `agent/result_tool_schema_test.go`, `agent/fuzz_lf_roundcontent_test.go`, `agent/session_lifecycle_tail_coverage_fuzz_test.go`, `agent/session_tools_misc_contract_fuzz_test.go`

**Interfaces:**
- Consumes: Task 1 unified `DefCommunicateNamed` and `WithCommunicateOutputSchema` / `WithAllowedDecisions`
- Produces:
  - `communicateEndState` (`agent/session_tools_communicate.go`):
    ```go
    type communicateEndState struct {
        Accepted *bool `json:"communicate_accepted,omitempty"`
        EndTurn  *bool `json:"communicate_end_turn,omitempty"`
        Quiet    *bool `json:"communicate_quiet,omitempty"`
    }
    ```
  - `(*Session).quietlyYielded() bool`: true when the current turn accepted a quiet `communicate({})` yield.
  - Validation & execution contract in `registerCommunicateTool`:
    - `end_turn` defaults to `true` when omitted.
    - `!endTurn && message == ""` -> error.
    - `endTurn && message != ""` -> if custom `result_schema` or `AllowedDecisions` is bound (`requiresStructuredFinalOutput(resultToolDef)`), require `_, ok := args["output"]` (`ok == true`); deliver `TurnCommunicate` + `EventCommunicate`, call `acceptCommunicateTerminal`, return `{"accepted": accepted, "end_turn": true, "inbox": inbox}` (+ `warning` if running jobs).
    - `endTurn && message == "" && meaningfulOutputPresent` -> error (note: legacy default empty envelope `{"message":"", "data":{}, "artifacts":[]}` has `meaningfulOutputPresent == false` and routes to quiet yield).
    - `endTurn && message == "" && !meaningfulOutputPresent` (quiet yield):
      - Dead-wait guard:
        - Root (`!s.reportsToParent()`): allowed if `kind == EntryNotification` OR `s.hasWakeableWork()` OR `s.communicated()`; otherwise return tool error.
        - Subagent (`s.reportsToParent()`): allowed if `treeHasOutstandingWork()` OR `s.communicated()` OR (`kind == EntryDelegateAttention` and `recordAttentionNoAction(lease)` records `true`); otherwise return tool error.
      - Sets `s.comm.quietEnd = true`, emits NO `TurnCommunicate` / `EventCommunicate`, leaves `s.latestReport` untouched, omits `runningJobsEndTurnWarning`, returns `tool.StateResult{Output: json({"accepted":true,"end_turn":true,"quiet":true,"inbox":inbox}), State: communicateEndState{Accepted:&tr, EndTurn:&tr, Quiet:&tr}}`.

- [ ] **Step 1: Write failing behavior tests in `agent/session_communicate_quiet_yield_test.go` and `agent/subagent_owned_job_drain_test.go`**

Cover all of:
1. Unified schema across interactive root, headless root, and subagent (identical properties, no `end_reason`).
2. `communicate({"message": "done"})` defaults `end_turn` to `true`, emits one `EventCommunicate`, and rests `SessionIdle` (never `SessionAwaiting`).
3. `communicate({})` on an initiating root turn: refused (`IsError == true`, turn stays open) when `!hasWakeableWork()`; succeeds (`quiet: true`, zero `EventCommunicate`, rests `SessionIdle`) when a background job, delegate, or self-watch is active; legacy empty default `output: {"message":"","data":{},"artifacts":[]}` with `message: ""` also succeeds as a quiet yield (Review Focus #2).
4. `communicate({})` on a root `EntryNotification` wake with `0` jobs left: succeeds quietly and preserves an active `ask_user` `SessionAwaiting` if applicable.
5. `communicate({})` on a `reportRequired` subagent with a `result_schema`:
   - Refused when only an unfired standing watch exists (`treeHasOutstandingWork() == false`).
   - Succeeds after starting a background `shell` job (`go test`), enters `drainForFinalization` **without** scolding or emitting a premature parent `<delegate-notification>`, wakes when the shell job completes, and delivers **one** final report when the subagent calls `communicate({"message": "passed", "output": {}})` (including falsy/empty-object `output` allowed by `result_schema`, and rejecting final reply if `"output"` is omitted — Review Focus #3).
   - Post-report housekeeping notification wake calling `communicate({})` inside `drainJobTreeKeepingReport` preserves the earlier captured report and `structured_result` (Review Focus #4).
6. `communicate({})` on an attention-only observer (`EntryDelegateAttention`) records `recordAttentionNoAction` and settles without waking its parent.
7. `Stop` hook returning `Blocked: true` clears `s.comm` so the continued round can capture a fresh terminal reply cleanly.

- [ ] **Step 2: Run targeted `./agent` tests to verify RED**

Run: `go test -count=1 ./agent -run 'TestCommunicate|TestSubagentOwnedJobDrain'`
Expected: FAIL (compile errors on removed `CommunicateEndReason*` and failing new tests).

- [ ] **Step 3: Implement Task 2 across `agent/session_tools_communicate.go`, `session.go`, `session_state.go`, `session_tools_ask.go`, `session_tool_round.go`, `session_lifecycle.go`, and `subagents.go`, and update affected tests**

Update existing notification/supervision tests (`TestDelegateResourceSupervision_NotificationDriveIsAnUnleasedInputTurn`, `notification_bare_text_test.go`, `session_waiting_root_wake_test.go`, `fuzz_lf_roundcontent_test.go`, `session_lifecycle_tail_coverage_fuzz_test.go`, etc.) to use `communicate({})` instead of bare-text notification acknowledgements or `end_reason`, and delete the retired `needs_response` timer test files.

- [ ] **Step 4: Run `./agent` targeted tests and fuzz-tag compile check**

Run: `go test -race -count=1 ./agent -run 'TestCommunicate|TestSessionHasWakeableWork|TestWaitingRoot|TestNotification|TestDelegateResourceSupervision|TestAskUser|TestAwaiting|TestRest|TestSubagentOwnedJobDrain|TestResultTool' && go test -tags evenerfuzz -run '^$' ./agent`
Expected: PASS (exit 0).

- [ ] **Step 5: Commit Task 2**

Stage exact modified/created/deleted files in `agent/` and commit with `feat(agent): add communicate({}) quiet yield and retire end_reason and needs_response`.

---

### Task 3: Root Bare-Text Fallback, `apptranscript` & Watch Silence on `communicate({})`, `hubCompletion`, and `doctor` Parity

**Files:**
- Modify: `agent/schema/turn.go` (add `AcceptedReply bool` `json:"accepted_reply,omitempty"` on `Turn` for root bare-text fallback `TurnAssistant` entries)
- Modify: `agent/session_lifecycle.go` & `agent/session_tool_round.go` (accept clean `FinishReasonStop` prose on untyped root `EntryUserInput` turns through `deliverIfCommunicated`, stamping `AcceptedReply: true` on the recorded `TurnAssistant` entry after `Stop` hooks pass, or recording `TurnCommunicate` replacement if `Stop` hook `Stopped: true`)
- Modify: `internal/apptranscript/apptranscript.go` & `internal/apptranscript/notice.go` (skip `agentMessage` and `communicateRawFallbackText` when `ToolResult` has `communicate_quiet: true` or valid JSON args with empty `message`/`output`; attach `SessionObservationMetadata{TurnID, EndTurn: true, TerminalAccepted: &true, CausalKnown, Causal}` on `AcceptedReply` `TurnAssistant` `agentMessage` items; stamp `TerminalAccepted` on `TurnCommunicate` only when accepted and not blocked)
- Modify: `agent/transcript_render.go` (`writeResultToolMessage`: render nothing when `communicate` arguments are a valid JSON object carrying no non-blank `message` or `output.message`)
- Modify: `agent/watch_fire.go` (`evaluateWatchEvent` / `watchEventFilterMatches`: return miss without incrementing `eventCount` when `ev.Kind == events.EventToolCallEnd` has `Error == ""` and `ToolState` contains `"communicate_quiet": true`)
- Modify: `agent/job_watch_hub.go` (ensure `hubCompletion` completes on both accepted final `TurnCommunicate` and `AcceptedReply` root `TurnAssistant` items, never on `communicate({})`, while keeping historical `EndReason == "waiting_on_work" | "needs_response"` non-terminal on replay)
- Modify: `cmd/evener/run.go` (ensure one-shot `evener run` prints the accepted root bare-text fallback reply to `stdout`)
- Modify: `agent/doctor/sessions.go` (`outcomeHint`) & `agent/doctor/health.go` (`finalEndTurnIndex`) (ignore quiet `communicate({})` calls with empty `message`; recognize `AcceptedReply` root `TurnAssistant` prose)
- Create/Modify tests:
  - Create: `agent/session_root_bare_text_fallback_test.go`
  - Modify: `internal/apptranscript/apptranscript_test.go`, `agent/job_watch_events_test.go`, `agent/job_watch_hub_test.go`, `agent/transcript_render_test.go`, `agent/doctor/sessions_test.go`, `agent/doctor/health_test.go`, `cmd/evener/run_test.go`

**Interfaces:**
- Consumes: Task 2 `communicateEndState{Quiet: &true}` in `ToolResult.ToolState` and `deliverIfCommunicated`
- Produces:
  - `schema.Turn.AcceptedReply bool` (`json:"accepted_reply,omitempty"`)
  - `apptranscript.IsQuietCommunicate(result *llm.ToolResultData) bool`
  - Root `EntryUserInput` bare-text fallback across live session, `Stop` hooks, `evener run`, `apptranscript`, `job_watch_hub`, and `doctor_evener`.

- [ ] **Step 1: Write failing tests across `apptranscript`, `agent`, `agent/doctor`, and `cmd/evener`**

1. `internal/apptranscript/apptranscript_test.go`: a turn carrying `communicate({})` + `ToolResult{ToolState: {"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}}` projects **zero** `agentMessage` items (no `"{}"` raw fallback!), whereas malformed unparseable raw bytes still trigger `communicateRawFallbackText`; an `AcceptedReply: true` `TurnAssistant` projects one `agentMessage` carrying `Observation{EndTurn: true, TerminalAccepted: &true}`.
2. `agent/job_watch_events_test.go`: neither an `assistant.tool` watch nor a wildcard `["*"]` watch fires on a successful `communicate_quiet: true` `EventToolCallEnd`, while a failed `communicate({})` (`Error != ""`) still fires `assistant.tool`.
3. `agent/job_watch_hub_test.go`: watched root calling `communicate({})` with 3 running delegates does **not** complete `hubCompletion`; watched root calling `communicate({"message": "Server up"})` with a running background job **does** complete `hubCompletion`; watched root finishing via root bare-text fallback completes `hubCompletion` both live and on replay; historical `EndReason: "waiting_on_work"` and `"needs_response"` records on disk remain non-completing on replay.
4. `agent/session_root_bare_text_fallback_test.go`:
   - Untyped root `EntryUserInput` with `FinishReasonStop` and non-blank prose finishes `SessionIdle` in 1 round (`ProcessInput` returns prose, `Stop` hook runs and can `Block` or `Stop`/replace, zero `no-tool-calls` retry).
   - `FinishReasonLength` (truncated stream — Review Focus #1), empty response (`noContent`), `EntryNotification` bare text, subagent bare text, and `--output-schema` / `AllowedDecisions` root bare text are **not** accepted as bare-text fallback replies.
5. `agent/doctor/sessions_test.go` & `health_test.go` (Review Focus #5): trailing `communicate({})` after a final reply leaves `outcomeHint` and `finalEndTurnIndex` anchored on the final reply; root bare-text fallback `AcceptedReply` populates `outcomeHint` and `finalEndTurnIndex`.
6. `cmd/evener/run_test.go`: one-shot `evener run` outputs the root bare-text fallback answer on `stdout`.

- [ ] **Step 2: Run targeted tests to verify RED**

Run: `go test -count=1 ./internal/apptranscript/... ./agent/doctor/... ./cmd/evener/... ./agent -run 'TestRootBareTextFallback|TestWatch|TestHub'`
Expected: FAIL on the new assertions.

- [ ] **Step 3: Implement `AcceptedReply`, `apptranscript` & `transcript_render` quiet suppression, `watch_fire` filter, `job_watch_hub`, `doctor`, and `cmd/evener/run.go`**

Keep the root bare-text fallback gate exact: `!s.reportsToParent() && kind == EntryUserInput && s.usesDefaultCommunicateOutputEnvelope() && resp.Finish.Reason == llm.FinishReasonStop && !noContent && strings.TrimSpace(lastText) != ""`. Ensure `Stop` hook `Blocked: true` clears `s.comm` without setting `AcceptedReply`, and `Stopped: true` writes the hook's replacement reply.

- [ ] **Step 4: Run targeted package suites**

Run: `go test -race -count=1 ./internal/apptranscript/... ./agent/doctor/... ./cmd/evener/... ./agent -run 'TestRootBareTextFallback|TestWatch|TestHub|TestTranscriptRender'`
Expected: PASS (exit 0).

- [ ] **Step 5: Commit Task 3**

Stage exact touched files and commit with `feat(agent,apptranscript,doctor): add root bare-text fallback and suppress quiet yield projections`.

---

### Task 4: System Prompt, Messaging Goldens, Evergreen Product Docs, and Full Gate Verification

**Files:**
- Modify: `agent/prompt_data.go` & `agent/session_prompts.go` (delete `HasEndReason`)
- Modify: `agent/prompts/system.md.tmpl:95-150,330-336`
- Modify: `agent/testdata/messagingprompt/*.md` (regenerated via `-update-prompt`)
- Modify: `docs/product/lifecycles.md`, `docs/product/attention-navigation.md`, `docs/product/session-activity.md`, `docs/product/subsystems.md`

**Interfaces:**
- Consumes: Tasks 1–3 unified `communicate` schema, `communicate({})` quiet yield, retired `end_reason` / `needs_response`, and root bare-text fallback.
- Produces: Updated system prompt, regenerated messaging prompt goldens, evergreen product docs, and a clean pass on `go test`, `make vet`, and `make lint`.

- [ ] **Step 1: Update `agent/prompt_data.go`, `agent/session_prompts.go`, and `agent/prompts/system.md.tmpl`**

1. Delete `HasEndReason` from `PromptData` (`agent/prompt_data.go`) and `session_prompts.go`.
2. In `agent/prompts/system.md.tmpl`:
   - Under `## Background work` (around line 100): instruct ending a wait turn with `{{ .ResultToolName }}({})` when nothing needs to be sent (or sending a short status message when useful), removing `end_reason="waiting_on_work"`.
   - Under `## Reporting` (around line 132): remove `"The one exception is a notification or watch frame that needs nothing from you: note that in one line of plain text and call no tool."` and remove the `{{ if .HasEndReason }}` `end_reason` sentence. Replace with clear guidance to call `{{ .ResultToolName }}({})` with no `message` when a notification or watch frame needs nothing from you, and to pass `end_turn=false` only when sending a progress update before immediate continued work.

- [ ] **Step 2: Regenerate messaging prompt goldens and inspect every line of the golden diff**

Run:
```bash
go test ./agent -run '^(TestDelegateMessagingGuidanceGoldens|TestManagerRequestAllBranches)$' -count=1 -update-prompt
git diff agent/testdata/messagingprompt/
```
Verify every golden diff reflects only the intended `communicate` schema/description, `communicate({})` quiet yield, and removed `end_reason` / bare-text `"Noted:"` exception, and check `TestShippedPromptsOnlyNameToolsTheSessionHas` (`go test -count=1 ./agent -run 'TestShippedPrompts|TestBundledPrompt'`).

- [ ] **Step 3: Update evergreen product docs (`docs/product/`)**

Update current behavior (no historical PR narrative) in:
- `docs/product/lifecycles.md` (`Session`: `awaiting` is entered for a pending `ask_user` question; remove current-daemon `needs_response` claim while noting wire normalization keeps handling legacy/remote frames)
- `docs/product/attention-navigation.md` & `docs/product/session-activity.md` (reflect that `ask_user` and approvals own `awaiting`, and legacy `awaiting` without `askPending` remains normalized to `Idle`)
- `docs/product/subsystems.md` (`S09 Session execution and goals`: replace `waiting_on_work` / `needs_response` quiet-period description and notification bare-text carve-out with `communicate({})` quiet yield, `ask_user`-owned `SessionAwaiting`, and root `EntryUserInput` bare-text fallback)

- [ ] **Step 4: Run full Go package tests and sequential repo static gates**

Run sequentially (one at a time, bare foreground):
1. `go test -count=1 ./agent/... ./internal/apptranscript/... ./internal/transcriptindex/... ./cmd/evener/... ./server/...`
2. `make vet`
3. `make lint`
4. `make docs-check`
Expected: ALL PASS (exit 0), pristine output, zero lint or doc errors.

- [ ] **Step 5: Commit Task 4**

Stage exact touched prompt, golden, and `docs/product/` files (plus any final test fixes surfaced in Step 4) and commit with `docs(prompts,product): teach communicate({}) quiet yield and update lifecycle docs`.
