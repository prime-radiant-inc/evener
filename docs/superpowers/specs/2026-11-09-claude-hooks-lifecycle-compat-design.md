# Claude Hook Lifecycle Compatibility — Design

Date: 2026-11-09 (rev 2, after adversarial review)
Status: draft for approval
Compatibility source of truth: <https://code.claude.com/docs/en/hooks> (read 2026-11-09)
Existing evener hook docs: [`docs/hooks.md`](../../hooks.md),
[`docs/subagent-management/07-lifecycle-hooks-claude-compat.md`](../../subagent-management/07-lifecycle-hooks-claude-compat.md),
[`docs/subagent-runtime-contracts.md`](../../subagent-runtime-contracts.md) (the evergreen pointer `07` names), [`docs/subagent-management/10-runtime-contracts.md`](../../subagent-management/10-runtime-contracts.md) §Contract 4 (the tier vocabulary).

## 1. Intent

A plugin that declares a hook for a real Claude Code event must have it run.
Today `toolsmith` declares `SubagentStart`, evener recognizes the name, warns

> plugin "toolsmith" declares a hook for the reserved event "SubagentStart",
> which evener does not yet fire; this hook will not run

and drops it. That is the failure this change fixes, plus the four other
lifecycle events that have a real evener boundary and are still reserved.

### Goals

1. Fire five more Claude events at their correct evener boundary, with the
   Claude-documented input fields, decision control, and exit-code behavior:
   `SubagentStart`, `PostToolUseFailure`, `PostToolBatch`, `StopFailure`,
   `PostCompact`.
2. Correct the events that already fire where this change touches their Claude
   contract: `SubagentStop` gains the agent-type matcher target and the two
   documented input fields; `Stop` gains the same two input fields;
   `PreCompact` gains its `manual`/`auto` matcher target and its two input
   fields; `SessionEnd` gains a Claude-vocabulary reason; `Notification` gains
   `notification_type` and `title`. Explicitly **not** part of this goal:
   `PreCompact` decision control (see non-goals).
3. Partition tool events the way Claude does: `PostToolUse` fires on success,
   `PostToolUseFailure` on an execution failure, and neither fires for a call
   rejected before execution.
4. Honor the universal `continue: false` / `stopReason` output on the events
   Claude honors it on (and keep discarding it where Claude discards it).
5. Give every event a truthful `agent_id`/`agent_type` when it fires inside a
   subagent, and Claude's `effort` object shape.
6. Fix the recognized-event list: Claude now documents `DirectoryAdded`,
   `PreModelSwitch`, and `PostModelSwitch`. A plugin declaring one gets a
   "likely a typo" warning today, which is wrong.
7. Align the matcher exact-match set with Claude's, which now also accepts
   `-`, `,`, and spaces, and multi-value lists separated by either `|` or `,`.

### Non-goals

- `TaskCreated`, `TaskCompleted`, `WorktreeCreate`, `WorktreeRemove`,
  `UserPromptExpansion`.
- **`PreCompact` decision control.** Claude blocks compaction on exit 2 or
  `decision: "block"`; evener's `RunPreCompact` has no block path and the hook
  runs mid-fold, where aborting would mean unwinding an in-flight strategy.
  `PreCompact` therefore keeps its parse-and-aggregate behavior and its exit-2
  is non-blocking. This is the one place the spec does *not* claim parity, and
  `docs/hooks.md` must say so where the events table says `PreCompact` fires.
- `PermissionRequest`, `PermissionDenied`, `ConfigChange`, `CwdChanged`,
  `DirectoryAdded`, `FileChanged`, `InstructionsLoaded`, `MessageDisplay`,
  `Setup`, `TeammateIdle`, `Elicitation`, `ElicitationResult`,
  `PreModelSwitch`, `PostModelSwitch` firing.
- `http`, `mcp_tool`, and `agent` handler types; `async`/`asyncRewake`; `if`
  rule evaluation; `once`.
- `terminalSequence` delivery. Parsed, no delivery site, unchanged caveat.
- `prompt_id`, `scratchpad_dir`, `permission_mode` common input fields.
- `Stop`/`SubagentStop` `background_tasks`, `session_crons`, and
  `agent_transcript_path`. Evener has jobs but no cron concept and no
  per-subagent transcript path an agent-facing hook can read; populating a
  half-truthful list is worse than omitting it. Named in `docs/hooks.md`.
- `CLAUDE_CODE_STOP_HOOK_BLOCK_CAP` and `CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS`.
  The SessionEnd budget stays 10 seconds. The 8-consecutive-continuation cap is
  also deferred: evener already bounds the loop with
  `MaxToolRoundsPerInput`, and a second counter would need its own restore
  story.

### Anti-goals

- Advertising an event as fired while its matcher target or a documented
  decision it accepts is dead. "Fires" means the whole contract in §4.
- A new hook event that can widen policy: no hook may bypass tool policy, the
  sandbox, session cancellation, or a pending question.

## 2. Constraints

- Tests are deterministic: no provider credentials, network, or model behavior
  in default tests ([`testing.md`](../../developing-evener/testing.md)). Hooks
  are evener plumbing — real `bash -c` hook scripts plus a scripted
  `llm.ProviderAdapter`.
- No copy assertions. Assert which hook ran, what the model or user received,
  and what state changed.
- Additive wire changes except the deliberate ones in §5.5.
- Every feature carries one tier from `10-runtime-contracts.md` §Contract 4.
  The five new events ship `claude-compatible-subset` with their caveats named.
- Go RE2, not JavaScript regex, stays the documented matcher caveat.
- Claude keeps adding events. The recognition list must not become a typo
  oracle again; this change adds the three that exist today (goal 6,
  `agent/plugin/hooks.go`).

## 3. Approaches

**A. One shared lifecycle hook layer, per-event runners (chosen).** Extend
`agent/internal/hooks` with five runner methods plus the shared pieces they
need (agent identity, batch payload, error taxonomy, the continuation counter),
and fire each from the one place in the session lifecycle where that boundary
already exists. Diagnostics, `/status`, and docs all read the same
`validHookEvents`/`recognizedClaudeEvents` sets, so they cannot drift.

**B. Translate Claude events into existing evener events.** Fire `SubagentStart`
as a synthetic `SessionStart` on the child, `PostToolUseFailure` as
`PostToolUse`. Cheap, but the matcher target, input shape, and decision control
differ per event; `SubagentStart` would inherit `SessionStart`'s startup-only
matcher vocabulary. Rejected.

**C. Shell out to a Claude-compatible hook host.** A separate process speaking
the whole contract, including `http`/`mcp_tool`/`agent` handlers. Buys full
parity at the cost of a second runtime, a second sandbox boundary, and a second
place for policy to leak. Rejected: too much for five events.

## 4. Event contracts

Each event gets: a constant, an entry in `validHookEvents`, its tier in
`hookEventTier`, a runner method, an exit-code row, an input-shape row, and a
section in `docs/hooks.md`. Shared plumbing first.

### 4.1 Shared plumbing

**Agent identity.** `Input.AgentID`/`AgentType` exist and are never set. In
`Session.hookInput` (`agent/session_events.go:604`), when
`cfg.spawn.parentSessionID != ""`: `AgentID = s.id`, `AgentType =
cfg.spawn.agentType`.

Add `agentType` to `spawnConfig` (`agent/session_config.go:804`) and set it from
one helper, `subagentHookAgentType(requested, agentName)`: the trimmed
`requested` key when non-empty (already the plugin-scoped catalog key, e.g.
`toolsmith:toolsmith`, since that is what `pluginAgents` is keyed by), else
`agentName` — which is `subagent` for a default subagent
(`agent/subagents.go:967-986`). That keeps the value truthful where
`subagent.agentType` today is empty for a default subagent
(`agent/subagents.go:106`), so the value must never be empty inside a subagent.
On restore, a descriptor for an unnamed delegate carries
`Descriptor.AgentType == "default"` (`agent/delegate_runtime.go:2350-2352`), so
the helper maps `default` to `frozen.Config.AgentName`. `docs/hooks.md` names
`subagent` as Evener's default agent type, with no `general-purpose` alias.

**One value feeds both the `agent_type` input and the matcher target.** Claude
matches on exactly the `agent_type` field and does not match a type matcher
against an empty `agent_type`. Root sessions leave both empty.

**Effort.** Claude documents `effort` as an object whose `level` is the level.
Change `Input.Effort` from `string` to `*hooks.Effort` with
`json:"effort,omitempty"` and `Level string \`json:"level"\``; a nil pointer
emits no field (a non-pointer struct would serialize `{"level":""}` on every
event, since `omitempty` never omits a struct). `CLAUDE_EFFORT` stays the bare
`Level`, built in `agent/internal/hooks/command_runtime.go:242-243`, which must
switch to `input.Effort.Level`.

**`continue: false`.** `parsedHookOutput.Continue` is parsed and discarded; add
`StopReason`. Add `Stopped bool` + `StopReason string` to `RunResult`,
`PreToolUseResult`, and `StopResult`, filled by one shared helper run before the
event-specific aggregation. Claude honors it on most events and **discards it**
on `StopFailure`, `Notification`, `PreCompact`, `PostCompact`, `SessionEnd`;
honor it everywhere else evener fires (including `SessionStart`,
`SubagentStart`, and both tool events) and discard it on those five. A stop
takes precedence over any event-specific decision: deliver `StopReason` as a
user-visible warning, then end the action the way that site ends a turn.

**Matcher targets.** Add the target to each runner and to
`docs/hooks.md` §"Matcher targets by event":

| Event | Target |
| --- | --- |
| `PreToolUse`, `PostToolUse`, `PostToolUseFailure` | Claude tool name |
| `SessionStart` | `startup`/`resume`/`clear`/`compact` |
| `SubagentStart`, `SubagentStop` | child agent type — the same value as `agent_type` |
| `StopFailure` | Claude error name (§4.5) |
| `PreCompact`, `PostCompact` | `manual`/`auto` |
| `Notification` | `notification_type` |
| `SessionEnd` | Claude reason (`clear`/`other`, §4.7) |
| `Stop`, `UserPromptSubmit`, `PostToolBatch` | no target (§4.4 bypasses matching) |

**Matcher exact-match set.** `isExactMatcher` (`agent/internal/hooks/matcher.go:52`)
accepts `[A-Za-z0-9_|]`. Claude accepts letters, digits, `_`, `-`, spaces, `,`,
`|` in exact mode, with `|` or `,` separating alternatives and optional
surrounding whitespace. Widen Evener to match: exact mode splits on `|` and `,`
and trims each alternative. Without this, `Edit, Write` is treated as a regex
and never matches, and `code-reviewer` only matches by regex accident.

**Amendment (Task 9).** The widened set applies to **every** event, not to all
but two. `isExactMatcher` takes no event, so a hyphenated `FileChanged` matcher
and a hyphenated `StopFailure` matcher are exact in Evener where Claude treats
them as regexes. That divergence is accepted and documented: `StopFailure`'s own
error names (`model_not_found`, `rate_limit`) contain only letters and `_`, so
exact mode reproduces Claude's behavior for every name Evener emits, and
`FileChanged`'s file events are matched by the same widened list. A per-event
discriminator would have to be threaded through `matchTarget`, `MatchHooks`,
`hookCanRun` and `Validate` to buy nothing for either event's real vocabulary.
`docs/hooks.md` names the difference where it lists the exact-match set.

### 4.2 `SubagentStart`

**Boundary.** Once per subagent run launch, from the parent's runner. The funnel
is `Session.launchSubagentRun` (`agent/subagents.go:1713`), the one function all
five launch paths call (`subagents.go:1361` legacy `spawnAgent`,
`subagents.go:1476` `startOrSteerSubagentRun`, `delegate_runtime.go:2314`
`delegate` create, `delegate_runtime.go:1867` `delegate_send` start,
`delegate_runtime.go:900` idle-attention drive). Firing there — not in
`trackAndLaunchPreparedSubagent`, which only the legacy path calls — is what
makes the `delegate` tool fire the hook at all.

Fire immediately before the run goroutine starts, so the child's first model
request follows the hook. Fire for the idle-attention drive too; the delivery
rule below makes that a no-op when the child already holds the context.

**Matcher target.** The child's agent type, the same value its `agent_type`
input carries: the plugin-scoped catalog key (`toolsmith:toolsmith`) when the
spawn named a plugin agent, else `subagent`. A colon places the matcher on the
regex path, matching Claude's guidance to anchor plugin-scoped names
(`^my-plugin:reviewer$`).

**Input.** Common fields plus `agent_id` (child session id) and `agent_type`.

**Output.** `hookSpecificOutput.additionalContext` is injected into the child's
context before its first request. No blocking: exit 2 and `decision: "block"`
show stderr to the user only. `continue: false` is honored (Claude's universal
field).

**Delivery and dedupe.** Deliver with the child's `deliverHookContext`. Claude
re-injects only when the child's context no longer holds an earlier copy: scan
the child's current history for a `TurnSteering` with
`events.SteeringKindHookContext` whose text equals the wrapped context, and skip
delivery when present. A compaction that discarded the copy therefore re-injects
on the next launch, with no extra durable state.

**Failure.** A failed hook must not fail the spawn: it becomes a user-visible
warning on the `sessionStartFailureWarning` pattern, never model context.

**Exit code.** 2 shows stderr to the user; nothing blocks.

### 4.3 `PostToolUseFailure`

**Boundary.** `Session.execTool` (`agent/session_tools.go:765`), after
`TOOL_CALL_END` is emitted, when the tool **executed and failed**:

`res.IsError && !res.PrevalOnly && !res.Rejected`.

`Rejected` is new: `tool.ExecResult` gains `Rejected bool`, set by the registry's
pre-execution refusals — the breaker park, unknown tool, invalid registered
arguments JSON, `ValidateRawArguments` failure, and `PreValidate` failure
(`agent/internal/tool/registry.go:741-770` and its neighbours). `PrevalOnly`
covers the session-side repair rejection (`session_tools.go:939`). Together they
are Claude's "rejected before execution": unknown tool, validation failure.
A `PreToolUse` deny and every cancel/skip path return before this point.

**Partition (deliberate change, §5.5).** `PostToolUse` fires only when
`!res.IsError`. Today it fires for every completion
(`session_tools.go:1017-1035` has no guard), so a plugin that registered only
`PostToolUse` saw failures too; Claude does not do that, and its docs tell
authors to register `PostToolUseFailure` for failures. Pre-execution rejections
fire neither.

**Matcher target.** Claude tool name.

**Input.** `tool_name`, `tool_input`, `tool_use_id`, `error` = `res.Output` (the
model-facing text, per Claude's "the same text Claude receives as the failed
tool's result"), `is_interrupt` (`false`; a cancel never reaches here),
`duration_ms` = `res.DurationMS`.

**Output.** `additionalContext` → model. Exit 2 is non-blocking and its stderr
reaches the model, as the existing tool-event routing does.

### 4.4 `PostToolBatch`

**Boundary.** The round tail in `agent/session_lifecycle.go`, immediately after
`persistToolResults` (`:2496`) and before `injectPostToolSteering` (`:2512`),
once per round that executed tools. Firing **before** `injectPostToolSteering`
is load-bearing: that function is where `injectDrainedSteering` runs
(`agent/session_tool_round.go:507`), and it is the last drain before the next
request. Steered after it, the hook's context would reach the model one request
late.

**Ordering with the turn boundary.** The hook's block must not pre-empt a reply
the same round already produced, and must not strand a pending question. So:
record the block, keep going, and apply it only after `deliverIfCommunicated`
(`:2532`) returns `done == false`. If the round communicated or posted an ask,
that boundary wins and the block is discarded (its `additionalContext` was
already delivered).

**Matcher target.** None. Claude documents "There is no matcher for this event",
so the runner ignores a configured matcher entirely rather than passing it
through `matchTarget` (which would silently skip a hook whose matcher named a
tool, and would also miscount it in `SupportedSummary`).

**Input.** `tool_calls`: an array of `{tool_name (Claude name), tool_input,
tool_use_id, tool_response}` where `tool_response` is `res.Output`, the
serialized content the model receives.

**Output.** `additionalContext` → model, in the same drain as other steering.
`decision: "block"`, `continue: false`, or exit 2 stop the round loop before the
next model call: the reason (JSON `reason`/`stopReason`, else exit-2 stderr) is
delivered as a user-visible warning and appended as a steering turn, then the
turn ends through `finishProcessingAtFailureBoundary`. History stays well-formed
because the tool results are already persisted.

**Exit code.** 2 blocks (stops the loop).

`PostToolBatch` is the only new blocking row. `PreCompact` stays non-blocking
(non-goal), so `exitBehavior` keeps its current answer for it.

### 4.5 `StopFailure`

**Boundary.** `Session.handleModelError` (`agent/session_model_call.go:864`),
in its terminal branch: after `settleFailedRound`, next to `emitTurnFailure`
(`:898-916`). Not on a user cancel and not on the content-filter retry branch.
Runs instead of `Stop`: a failure turn never reaches `deliverIfCommunicated`, so
`Stop` already does not fire.

**Matcher target.** The error name.

**Input.** `error`, `error_details`, `last_assistant_message` = the failure text
the conversation shows: `errorDataFromError(err).Error`, i.e. the same string
`emitTurnFailure` records as `TurnFailureInfo.Message`. The diagnostic `Title` and
`Hint` are separate fields on that record, not part of this string.

Mapping from `llm.Kind` plus status/code, only where provable:

| evener | Claude `error` |
| --- | --- |
| `KindRateLimit` | `rate_limit` |
| `KindServer` with 529 or code `overloaded_error` | `overloaded` |
| `KindAuthentication` | `authentication_failed` |
| `KindAccessDenied` | `oauth_org_not_allowed` only when the code says so, else `unknown` |
| `KindQuotaExceeded` | `billing_error` |
| `KindNotFound` | `model_not_found` |
| `KindInvalidRequest` | `invalid_request` |
| `KindServer` | `server_error` |
| everything else | `unknown` |

`error_details` is the status/code pair when either is known.

**Output.** All output and the exit code are ignored except `terminalSequence`
(parsed, no delivery site — existing caveat). Fire and discard.

### 4.6 `PostCompact`

**Boundary.** `stageCompactionEffects`'s `flush` (`agent/session_compaction.go:725`),
which runs only for a winning fold and outside `attentionMu` (released at
`:333`, `flush` at `:344`), so no deadlock against `deliverHookContext`/`Steer`.
All fold publishers pass through it: `/compact`, request-time `ManageContext`,
`applyPendingForceCompact`, `forceCompactForModelRecovery`.

**Gate (the revision-1 bug).** A request-time fold publishes on every model
request whether or not a layer folded anything
(`session_model_call.go:281-283`, "publishes a fold whether or not a layer
folded anything"). Firing from `flush` unconditionally would fire `PostCompact`
on every request. Gate on `commit.stagedCompactionCount() > 0` — the same signal
that tells `PreCompact` a real compaction started.

**Matcher target.** `trigger`: `manual` from `Compact` (`/compact`), `auto` from
every other publisher, including `compact_context` (Claude's `manual` means
`/compact`).

**Input.** `trigger`, `compact_summary` (the text of the fold's last `TurnSummary`
turn, else its `TurnCheckpoint`, else empty).

**Output.** No decision control; `systemMessage` and `continue` are discarded,
per Claude. Exit 2 shows stderr to the user only.

### 4.7 Corrections to already-fired events

- **`Stop`**: add `stop_hook_active` (true when this dispatch continues a turn a
  Stop hook already blocked) and `last_assistant_message` (`s.comm.reply`). The
  flag is a per-turn boolean on `Session`, set when a Stop hook blocks and
  cleared when a tool runs; the round cap stays `MaxToolRoundsPerInput` (the
  8-consecutive-continuation cap is a non-goal).
- **`SubagentStop`**: same two fields, with the same per-turn flag reset in
  `resetSubagentForRunLocked`. `last_assistant_message`
  is the child's last `TurnAssistant` text, not the delivered report (`res`) —
  Claude's field is the closing text, explicitly not the report. Matcher target
  becomes the child's agent type (a `RunSubagentStopFor(ctx, input, agentType)`
  variant; today's `runStopEvent` matches on an empty target).
- **`PreCompact`**: matcher target becomes the `trigger`; input gains `trigger`
  and `custom_instructions` (the fold's instructions, `null` otherwise). The
  trigger is threaded into `stageCompactionEffects` (`session_compaction.go:575`)
  and `foldWithForceCompact` (`:379`), which today carry none; `stageCompactionEffects`
  also needs the instructions, which `foldWithForceCompact` already takes.
  **Decision control stays dead** (non-goal), and the docs say so.
- **`SessionEnd`**: matcher target becomes Claude's reason vocabulary. Evener
  can distinguish a thread clear and nothing else, so `closeOptions` gains a
  `reason`, `CloseWithReason("clear")` is called from the clear handler
  (`cmd/evener/serve.go`'s `closeSupersededSession` call site); `Close` and a
  `CloseForShutdown` that cannot prove idleness send `other`; an idle
  `CloseForShutdown` sends `prompt_input_exit` (see the amendment below). A
  retirement close skips
  `SessionEnd` hooks entirely (`session_lifecycle.go`'s `if !retirement` guard),
  so that stays true and is documented. `resume` and `logout` are never sent.
  The input `reason` carries the same value.
  **Amendment:** `prompt_input_exit` therefore *is* sent, and it is the one
  shutdown reason Evener claims. "Idle" means no turn in flight, no running job,
  and no turn killed mid-stream; a shutdown that finds any of those sends `other`
  instead. Its helper is
  `sessionEndPromptInputExit`/`idleForPromptExit`, and `docs/hooks.md` documents
  the three-value vocabulary (`clear`, `prompt_input_exit`, `other`).
- **`Notification`**: input gains `notification_type` and `title`; the matcher
  target becomes `notification_type`. Evener emits only one kind of
  notification, a model-facing diagnostic warning, so the value is the
  evener-native `warning`. Claude's types (`permission_prompt`, `idle_prompt`,
  …) have no evener equivalent; a matcher naming one never matches, and
  `docs/hooks.md` says so.

### 4.8 Event-field additions

`Input` gains, all `omitempty`: `Trigger`, `CustomInstructions *string`,
`CompactSummary`, `StopHookActive *bool`, `LastAssistantMessage`, `Error`,
`ErrorDetails`, `IsInterrupt *bool`, `DurationMS *int64`, `ToolCalls
[]BatchToolCall`, `NotificationType`, `Title`. Pointers where `false`/`0` is
meaningful and absence is not the same as zero.

## 5. Implementation shape

### 5.1 New/changed files

| File | Change |
| --- | --- |
| `agent/plugin/hooks.go` | five constants, `validHookEvents`, three recognized names, tier registry |
| `agent/internal/hooks/hooks.go` | `Input` fields (§4.8), `Effort`, result `Stopped`/`StopReason`, five runners, target-taking variants, batch matcher bypass |
| `agent/internal/hooks/exitcode.go` | `PostToolBatch` blocking row; comment refresh |
| `agent/internal/hooks/errorname.go` (new) | `llm.Kind`/status → Claude `error` name |
| `agent/internal/hooks/matcher.go` | exact-match set and `,` splitting |
| `agent/internal/hooks/command_runtime.go` | `CLAUDE_EFFORT` from `input.Effort.Level` |
| `agent/internal/tool/registry.go` | `ExecResult.Rejected` at the pre-execution refusals |
| `agent/session_config.go` | `spawnConfig.agentType` |
| `cmd/evener/serve.go` | pass `"clear"` on the clear path |
| `agent/session_events.go` | `hookInput` agent identity + event fields |
| `agent/session_tools.go` | `PostToolUse` success gate, `PostToolUseFailure` dispatch |
| `agent/session_lifecycle.go` | `PostToolBatch` dispatch before `injectPostToolSteering`, block applied after `deliverIfCommunicated`, `closeOptions.reason`/`CloseWithReason`, `SessionEnd` reason |
| `agent/session_model_call.go` | `StopFailure` dispatch |
| `agent/session_compaction.go` | trigger + instructions threading, `PreCompact` target, gated `PostCompact` in `flush` |
| `agent/subagents.go` | `SubagentStart` in `launchSubagentRun`, `agentType` plumbing, `SubagentStop` target + field + flag reset |
| `docs/hooks.md` | events table, matcher table, input table, exit-code table, caveats |
| `docs/subagent-runtime-contracts.md` | "Nine events fire" list, matcher list |
| `docs/subagent-management/07-lifecycle-hooks-claude-compat.md` | event table, tier lists, the "nine" occurrences |
| `docs/product/subsystems.md` | only if the S17–S19 hooks row's claim changes |

### 5.2 Dispatch-site invariants

- Hooks never run under `s.mu`. Every new dispatch reads what it needs first.
- `PostCompact` fires after the publish transaction releases `attentionMu`, only
  for a winning fold that actually compacted.
- `SubagentStart` fires before the child's first model request and is not part of
  the child's own `SessionStart` result.
- `PostToolBatch`'s context is steered before the last pre-request drain; its
  block is applied after the turn-boundary decision.
- No new event may block a pending question's resolution.

### 5.3 Diagnostics

`unsupportedPluginHookEvents` shrinks by five automatically through
`validHookEvents`; the five gain a `/status` row at tier
`claude-compatible-subset`. `DirectoryAdded`, `PreModelSwitch`, `PostModelSwitch`
become `reserved-placeholder` instead of "likely a typo".

### 5.4 Failure modes for the new events

A hook is a user subprocess. For each new event: a spawn failure or timeout is a
non-blocking error whose stderr becomes user-visible (`SessionStart`'s
summarized-warning pattern), except where the event blocks, where the block
stands on exit 2 alone. Output over the cap is truncated by the existing
`cappedOutput`. A hook still running at session close is cancelled by the
existing close budget. Hooks run in the session's sandbox wrapper as today.

### 5.5 Deliberate behavior changes

1. **`PostToolUse` no longer fires for a failed tool** (only `PostToolUseFailure`
   does). Gives Claude's partition; a plugin relying on the old double-fire must
   add `PostToolUseFailure`.
2. **`continue: false` starts being honored** — except on the five events Claude
   discards it on.
3. **`effort` becomes an object** (`{"level": "high"}`, absent when unset). A
   plugin reading the old bare string sees nothing.
4. **Matcher exact mode accepts `,` and spaces as separators**, so `Edit, Write`
   now matches where it previously fell through to regex and matched nothing.

Each gets a line in `docs/hooks.md` beside the existing misconfiguration notes.

## 6. Test plan

| Event | Runner tests | Boundary tests |
| --- | --- | --- |
| `SubagentStart` | matcher on agent type (exact, plugin-scoped with colon, `.*`), additionalContext routing, exit 2 → user only, failure → warning | real spawn through the **`delegate` tool** (not `spawnAgent`): the child's first model request carries the context; a second launch of the same child does not duplicate it; `toolsmith:toolsmith` does not match another agent; a **default (unnamed) subagent's hook receives `agent_type: subagent` and a `subagent` matcher matches it**; idle-attention drive fires the hook but delivers nothing new |
| `PostToolUseFailure` | input shape, `error` = model-facing text, additionalContext, exit 2 → model | a failing `bash -c` fires it and `PostToolUse` does not; a succeeding one fires `PostToolUse` and not this; an unknown tool, a schema rejection, a breaker park and a `PreToolUse` deny fire neither |
| `PostToolBatch` | `tool_calls` shape from two results, additionalContext, block via JSON and exit 2, a configured matcher does not skip it | a two-call round fires it once and the **same** next request carries the context; a block ends the turn with both results in history; a round with `communicate` keeps its reply and discards the block; a round posting `ask_user` stays `awaiting` |
| `StopFailure` | error-name mapping table, matcher by name, output and `continue:false` discarded | a scripted 429 ends the turn, fires `StopFailure` once with `error: rate_limit`, fires no `Stop`, and keeps a salvaged draft |
| `PostCompact` | trigger matcher, `compact_summary` source | `/compact` fires `manual`; an automatic fold fires `auto`; a request-time fold that compacted nothing fires nothing; a losing fold fires no `PostCompact` (and may re-run `PreCompact`, as today) |
| `PreCompact` | trigger matcher, `trigger`/`custom_instructions` in the input | `/compact` matches `manual` and a pressure fold matches `auto`; `custom_instructions` carries the fold's instructions for a manual/`compact_context` fold and is absent for an automatic one |
| `continue:false` | aggregation per event | one dispatch site per honoring event proves the action stopped; each of the five discarding events keeps going |
| `Stop`/`SubagentStop` | new input fields, `last_assistant_message` from the reply/last assistant turn | `stop_hook_active` is set by a block and cleared by a tool call; `SubagentStop` matches on agent type |
| Matcher | `,`/space lists, hyphenated names, `FileChanged`-style narrow set | — |
| `SessionEnd` | reason matcher (`clear`, `other`) | a clear runs the `clear` hook and not the `other` one; a plain close runs the `other` hook and not the `clear` one; a retirement close runs neither |
| `Notification` | `notification_type`/`title` in the input, matcher on the type | a diagnostic warning fires the `warning`-matched hook and not an `idle_prompt`-matched one |
| Identity/effort | — | a hook inside a real subagent sees its `agent_id`/`agent_type`; a root hook sees neither; `effort` is an object with `level`, absent when unset; `CLAUDE_EFFORT` still set |
| Registry | — | declaring `DirectoryAdded`/`PreModelSwitch`/`PostModelSwitch` yields a reserved warning, not "likely a typo"; declaring `SubagentStart` yields no warning |

Plus a scenario card update (`test/scenarios/hooks-claude-compat-matcher.md` or a
sibling) that loads the real `toolsmith` plugin via `--plugin-dir` and asserts its
`SubagentStart` warning is gone and its hook ran.

## 7. Review focus

1. A `SubagentStart` hook that exits 1: the spawn still happens and the child
   still answers.
2. A `PostToolUseFailure` hook that itself fails: the tool's failure result
   reaches the model unchanged.
3. `PostToolBatch` on a round whose tools were cancelled: no hook, or one with
   the partial batch, never a hang.
4. A `PostCompact` hook on two compactions back to back: no duplicate fire.
5. `StopFailure` on a turn that already streamed text: the salvaged draft stays
   and the hook fires once.

## 8. Risks

- **Deadlock on `attentionMu`** if `PostCompact` fires inside the publish
  transaction. Mitigated by firing from `flush`, tested with a hook that steers.
- **Double `SubagentStart`** across the five launch paths. Mitigated by the
  single-funnel placement plus the delivery dedupe.
- **`PostToolBatch` context arriving late.** Mitigated by the before-drain
  placement, pinned by a test that inspects the next request's messages.
- **Error-name mapping overclaiming.** Anything not provable maps to `unknown`.
- **The `delegate`-tool boundary is the one that matters** and is the furthest
  from `spawnAgent`; its test must go through the real tool.
