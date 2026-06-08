# Phase B (hooks): PreToolUse output schema + additionalContext/systemMessage delivery split

> Revised after an adversarial review (`/par`). The routing model below is the
> corrected one: error stderr keeps its current model-delivery (so PostToolUse
> stderr still reaches the model), only the exit-0 `systemMessage` field and
> exit-0 plain stdout are rerouted, and user-visible delivery uses the
> **non-recursing** `emitDiagnosticWarning` path.

## Context

Serf's lifecycle-hook Phase 1 is shipped (`docs/hooks.md`): the nine fired events,
the Claude-compatible matcher, `command`/`prompt` handlers, the input fields, and
the event-specific exit-code table. The roadmap
(`docs/subagent-management/07-lifecycle-hooks-claude-compat.md`) reserves the rest
in honest tiers.

This is the first slice of **Phase B**, scoped to *"fix the events we already
fire"* — making the **output contract** of the nine fired events match Claude,
without adding new events. Two reserved items, both for events serf already fires:

1. The **`PreToolUse` preferred output schema** (`permissionDecision`,
   `permissionDecisionReason`, the deprecated top-level `approve`/`block`
   mapping). Today serf honors only `hookSpecificOutput.permissionDecision: "deny"`
   and reads the reason from the wrong key (`hso.reason` instead of
   `permissionDecisionReason`).
2. A **distinct delivery channel for `additionalContext`** vs. `systemMessage`.
   Today both are delivered identically — every fired event calls `s.Steer(...)`,
   which injects a bare `llm.User(text)` turn (as if the *user* spoke). There are
   eight literal `TODO(phase-B)` anchors at these sites.

Explicitly **out of scope** (stays reserved in `07`): new events, the approval
flow (`PermissionRequest`/`PermissionDenied`), `ConfigChange`,
`UserPromptExpansion`, the `if` rule language, async/`http`/`mcp_tool`/`agent`
handlers, the **`ask`/`defer` interactive decisions** (no permission prompt
exists), and **reworking the exit-code error-stderr destinations** (the existing
per-event behavior is preserved unchanged — see below).

## Grounded Claude semantics

From <https://code.claude.com/docs/en/hooks> (verified against the live doc):

- `hookSpecificOutput.permissionDecision` ∈ `allow` (proceed), `deny` (block),
  `ask` (escalate to a user permission prompt), `defer` (defer to the normal
  permission flow). The reason field is **`permissionDecisionReason`**.
- **`additionalContext` → the model:** *"Claude Code wraps the string in a system
  reminder and inserts it into the conversation."*
- **`systemMessage` → the user only:** *"Warning message shown to the user"* — not
  sent to the model.
- **Plain-text stdout** (exit 0, non-JSON) is added to the **model** context only
  for `UserPromptSubmit`, `UserPromptExpansion`, and `SessionStart`; for other
  events it is not model context.
- **Exit-2 stderr destination is event-specific** (Claude's exit-code table,
  mirrored in `07` §"Exit-code semantics"): `PostToolUse`/`PostToolUseFailure`
  add stderr to the **model**; `PreToolUse`/`Stop`/`SubagentStop` use it as the
  block reason; `Notification`/`SessionStart`/`SessionEnd` show it to the user.
- Claude does not document cross-hook precedence among conflicting
  `permissionDecision`s.

**Not verified in the current doc (historical / deprecated):** the deprecated
top-level `decision: "approve"|"block"` → `permissionDecision` mapping for
`PreToolUse`. `07` already tracks it as a *deprecated compatibility mapping*
(`07` §"PreToolUse (preferred schema reserved)"); serf chooses to keep accepting
it for portability. It is implemented as a fallback, not cited as current-doc
behavior.

## Goals

- `PreToolUse` honors the preferred `permissionDecision` schema for the decisions
  serf can actually make (`allow`/`deny`), reads `permissionDecisionReason`, and
  accepts the deprecated top-level `approve`/`block` mapping as a fallback.
- `additionalContext` reaches the model through a channel distinct from
  `systemMessage`, framed as hook-provided context (`<SYSTEM-REMINDER>`).
- The exit-0 JSON `systemMessage` field becomes user-visible (not model context).
- `SessionStart`/`UserPromptSubmit` plain-stdout-as-context keeps working;
  `PostToolUse` exit-2 stderr keeps reaching the model (no regression).
- `docs/hooks.md` becomes correct about delivery; `07` moves the shipped items out
  of "reserved (Phase B)".

## Non-goals

- No new events, approval flow, `if` language, or async/`http`/`mcp_tool`/`agent`.
- No permission prompt — `ask`/`defer` are **recognized but not honored**.
- **No change to the exit-code table or to error-stderr (exit≠0) routing.** The
  current per-event behavior (mostly model-delivery for non-blocking events; block
  reason for blocking events) is preserved as-is. Fixing the residual
  `Notification`/`SessionStart` exit-2-stderr-to-model divergence is a separate
  item.
- No change to matcher semantics, discovery, or the input contract.

## Design

### Part 1 — PreToolUse preferred output schema

`agent/internal/hooks/hooks.go` — `parseHookOutput` + `RunPreToolUse`:

- Parse the full `permissionDecision` vocabulary (`allow|deny|ask|defer`) and read
  `permissionDecisionReason` (replacing the current `hso.reason` read, which is
  neither the Claude field nor documented).
- Parse the deprecated top-level form: `decision: "approve"` and `decision: "block"`
  with top-level `reason`. The existing parser already sets `Blocked` on top-level
  `decision == "block"` (used by `Stop`/`SubagentStop`). `RunPreToolUse` must
  **newly** treat that `Blocked` as a deny for `PreToolUse` (today it reads only
  `Denied`/exit-2 — the mapping does not exist yet). The preferred
  `hookSpecificOutput.permissionDecision` wins when both are present.
- Runtime effect in `RunPreToolUse`:
  - `deny` (or top-level `block`, or exit 2 where `BlockOnExit2`) → `Denied`. Deny
    precedence unchanged: **any** deny wins.
  - **`DenyMessage`** = `permissionDecisionReason` if present, else the deprecated
    top-level `reason`, else the exit-2 stderr (`SystemMessage`) fallback. (Keeping
    the stderr fallback preserves `TestHookRunner_PreToolUse_ExitCode2Denies`,
    which asserts the exit-2 stderr appears in `DenyMessage`.)
  - `allow` → recognized as an explicit non-deny; serf has no gate to short-circuit,
    so it does **not** override another hook's `deny` (documented divergence;
    Claude is silent on cross-hook precedence). A non-deny `permissionDecisionReason`
    has no Claude-defined destination and is **recognized but not surfaced**;
    `additionalContext` (if present) still routes to the model.
  - `ask` / `defer` → **recognized but not honored**: serf has no interactive
    permission prompt (the missing primitive that keeps `PermissionRequest`
    reserved). The tool **proceeds** (non-blocking), and a one-time user-visible
    diagnostic names the unsupported decision.

### Part 2 + 3 — model-context vs. user-visible delivery

The root issue is that `parsedHookOutput.SystemMessage` is overloaded — it carries
(a) exit-0 plain stdout, (b) the exit-0 JSON `systemMessage` field, and (c) exit≠0
error stderr — which have different correct destinations. We distinguish them in
the parser and route in the **Runner** (which knows the event), so the session
delivery sites stay uniform.

**Parser** (`parseHookOutput`): add `SystemMessageIsJSONField bool`, set **true
only** in the JSON `systemMessage`-field branch (`hooks.go:441`). It is left
**false** in the plain-stdout branch (`hooks.go:430`) and in the exit≠0 early
return (`hooks.go:415-419`, where `IsError` is already set). `AdditionalContext`
is already separate.

**Result structs** (`RunResult`, `PreToolUseResult`, `StopResult`): replace the
overloaded `SystemMessages` + `AdditionalContext` with two routed buckets:

- `ModelContext []string` — delivered to the model.
- `UserMessages []string` — shown to the user.

`Denied`/`DenyMessage`, `Blocked`/`BlockReason`, and `TerminalSequences` are kept
(the last gains a one-line comment noting it is parsed-but-unrouted today, since it
has no consumer at any delivery site — renaming/removing it is out of scope).

**Routing** is computed where the event is known. `collectSystemMessages` (shared
by `RunPostToolUse`/`RunUserPromptSubmit`/`RunSessionStartFor`/`RunPreCompact`/
`RunNotification`) gains an **`event` parameter** (all five callers updated);
`RunPreToolUse` and `runStopEvent` already know their event. The rule, per hook
output `o`:

```
isContextEvent := event ∈ {SessionStart, UserPromptSubmit}
ModelContext += o.AdditionalContext                       // JSON additionalContext → model, always
if o.IsError:
    // exit≠0 stderr: UNCHANGED behavior.
    //  - blocking events (PreToolUse/Stop/SubagentStop): consumed as Deny/Block reason (not here)
    //  - non-blocking events: → ModelContext (preserves today's model delivery, incl. PostToolUse)
    ModelContext += o.SystemMessage          // (skipped by blocking runners, which use the reason)
else if o.SystemMessageIsJSONField:
    UserMessages += o.SystemMessage          // exit-0 JSON systemMessage field → user
else if o.SystemMessage != "":               // exit-0 plain stdout
    if isContextEvent: ModelContext += o.SystemMessage    // SessionStart/UserPromptSubmit → model
    else:              UserMessages += o.SystemMessage    // others → user
```

For `PreToolUse`/`Stop`/`SubagentStop` the exit-2 stderr is consumed as the
`DenyMessage`/`BlockReason` (existing behavior), so those runners do not also push
it to `ModelContext`.

**Session delivery** — two shared helpers replace the eight `TODO(phase-B)` pairs:

- `ModelContext` → `s.deliverHookContext(text)`: wraps the text in
  `<SYSTEM-REMINDER>…</SYSTEM-REMINDER>` and enqueues it via `Steer` (the queue,
  so `Stop`/`SubagentStop` context survives to the next model turn). This **adds**
  the system-reminder envelope — today's hook `Steer` text is unwrapped; the
  wrapper is the deliberate change toward Claude's "wraps the string in a system
  reminder."
- `UserMessages` → `s.deliverHookUserMessage(source, text)`: emits via
  **`emitDiagnosticWarning`** (`WarningData{Source: "hook", Title: <event>, Message: text}`),
  the path that renders in CLI (`[warning] <Message>`), TUI, and hub/web **without**
  firing the Notification hook. Using plain `emit` here would re-enter the
  Notification hook and recurse (`session_events.go` documents this invariant);
  `emitDiagnosticWarning` is the existing non-recursing escape hatch. The hook text
  goes in `Message` (CLI renders only `Message`); `Source`/`Title` are labels.

Of the eight sites, **seven** use `Steer` today (`PreToolUse`, `PostToolUse`,
`Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionStart`, `Notification`). The
**eighth**, `PreCompact` (`session_compaction.go`), is different: it appends to a
`messages []string` slice consumed by `appendSteeringMessagesToHistory` (direct
history append, mid-compaction, no live turn). There, `ModelContext` flows into
that existing slice path; `UserMessages` use `deliverHookUserMessage`
(`emitDiagnosticWarning` is a pure stream send — safe to call during compaction).

### Behavior changes (intended, documented)

- The exit-0 JSON `systemMessage` field, and exit-0 plain stdout from non-context
  events, become **user-visible** (not model context). Authors who want the model
  to see context use `additionalContext`.
- `additionalContext` is now `<SYSTEM-REMINDER>`-wrapped model context.
- **Unchanged:** `SessionStart`/`UserPromptSubmit` plain stdout still reaches the
  model; exit≠0 stderr keeps its current destination (so `PostToolUse` stderr still
  reaches the model, and blocking events still use it as the reason).

## Files touched

- `agent/internal/hooks/hooks.go` — parser (`permissionDecision`,
  `permissionDecisionReason`, deprecated mapping, `SystemMessageIsJSONField`),
  result structs (`ModelContext`/`UserMessages` + kept fields), `RunPreToolUse`
  (allow/deny/ask/defer, `Blocked`→deny, `DenyMessage` fallback, ask/defer
  diagnostic), `runStopEvent`, `collectSystemMessages(event, outputs)`.
- Session delivery + two helpers: `agent/session_tools.go`,
  `agent/session_tool_round.go`, `agent/subagents.go`, `agent/session_lifecycle.go`,
  `agent/session_init.go`, `agent/session_events.go`, `agent/session_compaction.go`.
- **Tests to update (rename + semantics; update, not delete):**
  - `agent/internal/hooks/exitcode_test.go` — PostToolUse exit-2 asserts
    `result.SystemMessages`; becomes `result.ModelContext` (stderr → model).
  - `agent/internal/hooks/hooks_test.go` — `.SystemMessages`/`.AdditionalContext`
    consumers (incl. `RunSessionStartFor`, PreToolUse) → new bucket names +
    routing; add the new parser/runner cases.
  - `agent/plugin_integration_live_test.go`, `agent/plugin_real_test.go` —
    `.SystemMessages`/`.AdditionalContext` consumers (live tests; must at least
    compile, and the PreToolUse-deny / SessionStart-context assertions move to the
    correct bucket / `DenyMessage`).
  - Session-level hook tests asserting old both-to-model delivery.
- `docs/hooks.md` — "What your hook returns" (systemMessage → user; additionalContext
  → model system-reminder; plain-stdout rule), the `allow|deny` + `ask`/`defer`
  wording (drop "full allow|ask|defer schema reserved"; state `ask`/`defer` are
  recognized, non-blocking, diagnosed), the exit-codes notes, the complete example
  (PostToolUse logger stdout now user-visible).
- `docs/subagent-management/07-lifecycle-hooks-claude-compat.md` — move the shipped
  items out of "reserved (Phase B)"; keep `ask`/`defer`, `PermissionRequest`, and
  the rest reserved with honest reasons.

`agent/internal/hooks/exitcode.go` — **unchanged.**

## Testing (red-green TDD)

- Parser: `permissionDecision` allow/deny/ask/defer; `permissionDecisionReason`
  read; deprecated top-level `approve`/`block` + `reason` mapped; preferred wins;
  `SystemMessageIsJSONField` true only for the JSON field, false for plain stdout
  and errors.
- `RunPreToolUse`: deny denies (decision, top-level `block`, exit 2); `DenyMessage`
  prefers `permissionDecisionReason`, falls back to top-level `reason`, then exit-2
  stderr; `allow` does not override a co-occurring deny; ask/defer proceed and emit
  a user diagnostic.
- Routing: `additionalContext` → `ModelContext`; JSON `systemMessage` → `UserMessages`;
  context-event plain stdout → `ModelContext`; non-context plain stdout →
  `UserMessages`; **exit≠0 stderr → `ModelContext` for non-blocking events (e.g.
  PostToolUse) — explicit regression guard**; blocking-event exit-2 stderr →
  `DenyMessage`/`BlockReason`.
- Session-level: `additionalContext` → a `<SYSTEM-REMINDER>` steering turn;
  JSON `systemMessage` → an `emitDiagnosticWarning` and **no** model turn and
  **no** Notification-hook re-entry (recursion guard, mirroring `recursion_test.go`);
  `SessionStart`/`UserPromptSubmit` plain stdout still reaches the model.
- Existing live scenario card stays green.

## Acceptance criteria

- `PreToolUse` honors `permissionDecision: allow|deny` + `permissionDecisionReason`
  and the deprecated `approve`/`block` mapping; `ask`/`defer` are recognized,
  non-blocking, diagnosed; `DenyMessage` keeps its exit-2 fallback.
- `additionalContext` is delivered to the model as a distinct system-reminder.
- The exit-0 JSON `systemMessage` field is user-visible (via the non-recursing
  diagnostic-warning path) and not sent to the model; `PostToolUse` exit-2 stderr
  still reaches the model.
- `make test` and `make lint` pass across all four modules.
- `docs/hooks.md` describes delivery accurately; `07` no longer lists these items
  as reserved.

## Risks / divergences

- **Behavior change** to `systemMessage`-field / non-context plain-stdout delivery
  — mitigated by updating docs and the affected tests.
- **`ask`/`defer` proceed (fail-open)** — deliberate: `defer` means "use the normal
  flow" (proceed) in Claude, and `ask` cannot be honored without a prompt; the
  diagnostic makes the non-support loud.
- **Cross-hook `allow` vs `deny`** precedence is serf-defined (deny wins); Claude is
  silent. Documented caveat.
- **Residual divergence (out of scope):** non-blocking exit-2 stderr for
  `Notification`/`SessionStart` still reaches the model (Claude: user only). Not
  introduced here; preserving current behavior keeps the increment focused and
  avoids regressing `PostToolUse`.
