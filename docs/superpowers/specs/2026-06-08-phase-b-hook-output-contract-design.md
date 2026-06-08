# Phase B (hooks): PreToolUse output schema + additionalContext/systemMessage delivery split

## Context

Serf's lifecycle-hook Phase 1 is shipped (`docs/hooks.md`): the nine fired events,
the Claude-compatible matcher, `command`/`prompt` handlers, the input fields, and
the event-specific exit-code table. The compatibility roadmap (`docs/subagent-management/07-lifecycle-hooks-claude-compat.md`)
reserves the rest in honest tiers.

This is the first slice of **Phase B**, scoped to *"fix the events we already
fire"* — making the **output contract** of the nine fired events match Claude,
without adding new events. Two reserved items, both for events serf already
fires:

1. The **`PreToolUse` preferred output schema** (`permissionDecision`,
   `permissionDecisionReason`, the deprecated top-level `approve`/`block`
   mapping). Today serf honors only `hookSpecificOutput.permissionDecision: "deny"`
   and reads the reason from the wrong key (`hso.reason` instead of
   `permissionDecisionReason`).
2. A **distinct delivery channel for `additionalContext`** vs. `systemMessage`.
   Today both are delivered identically — every site calls `s.Steer(...)`, which
   injects an `llm.User(text)` turn (as if the *user* spoke). There are eight
   literal `TODO(phase-B)` anchors in the code at these sites.

Explicitly **out of scope** (stays reserved in `07`, blocked on missing serf
primitives): new events (`PostToolUseFailure`, `SubagentStart`, `PostCompact`,
…), `PermissionRequest`/`PermissionDenied` (no approval flow), `ConfigChange`,
`UserPromptExpansion`, the `if` rule language, async handlers, `http`/`mcp_tool`/`agent`
handler types, and the `ask`/`defer` *interactive* permission decisions (no
permission prompt exists — see below).

## Grounded Claude semantics (source of truth)

From <https://code.claude.com/docs/en/hooks> (verified, not assumed):

- `hookSpecificOutput.permissionDecision` ∈ `allow` (proceed), `deny` (block),
  `ask` (escalate to a user permission prompt), `defer` (defer to the normal
  permission flow). The reason field is **`permissionDecisionReason`**.
- Deprecated top-level form for `PreToolUse`: `decision: "block"` + `reason` →
  `permissionDecision: "deny"` + `permissionDecisionReason`; `decision: "approve"`
  → `allow`; omitting `decision` / exit 0 with no JSON → allow/defer.
- **`additionalContext` → the model:** *"Claude Code wraps the string in a system
  reminder and inserts it into the conversation."* (This is exactly serf's
  `<SYSTEM-REMINDER>` + `schema.TurnSteering` idiom.)
- **`systemMessage` → the user only:** *"Warning message shown to the user"* — not
  sent to the model.
- **Plain-text stdout** (exit 0, non-JSON) is added to the **model** context only
  for `UserPromptSubmit`, `UserPromptExpansion`, and `SessionStart`; for every
  other event it is **not** model context.
- Claude does not document cross-hook precedence when several hooks return
  conflicting `permissionDecision`s.

## Goals

- `PreToolUse` honors the preferred `permissionDecision` schema for the decisions
  serf can actually make (`allow`/`deny`), reads `permissionDecisionReason`, and
  accepts the deprecated top-level `approve`/`block` mapping.
- `additionalContext` reaches the model through a channel distinct from
  `systemMessage`, framed as hook-provided context (`<SYSTEM-REMINDER>`), not as
  user speech.
- `systemMessage` becomes user-visible (not model context), via serf's existing
  user-visible warning channel.
- `SessionStart` / `UserPromptSubmit` bootstrap hooks (plain-text stdout as
  context) keep working.
- `docs/hooks.md` becomes correct and unhedged about output delivery; `07` moves
  the now-shipped items out of "reserved (Phase B)".

## Non-goals

- No new events, no approval/permission-request flow, no `if` rule language, no
  async/`http`/`mcp_tool`/`agent` handlers.
- No interactive permission prompt — so `ask`/`defer` are **recognized but not
  honored** (see below). Building a permission UI is a separate, larger effort.
- No change to the exit-code table (`exitcode.go`) — exit-2 blocking is unchanged.
- No change to matcher semantics, handler discovery, or the input contract.

## Design

### Part 1 — PreToolUse preferred output schema

`agent/internal/hooks/hooks.go`:

- `parseHookOutput` learns the full `permissionDecision` vocabulary. Add a
  `PermissionDecision string` field (`""|allow|deny|ask|defer`) and read
  `permissionDecisionReason` for the reason (the current code reads `hso.reason`,
  which is neither the Claude field nor documented — that read is replaced).
- Deprecated top-level mapping is parsed too: top-level `decision: "approve"` →
  treated as `allow`, `decision: "block"` → `deny`, top-level `reason` → the
  reason. The preferred `hookSpecificOutput.permissionDecision` **wins** when both
  are present. (Top-level `decision: "block"` still also sets the existing
  `Blocked` field used by `Stop`/`SubagentStop`; `RunPreToolUse` is what maps it
  to a deny, so there is no cross-event conflict.)
- Runtime effect in `RunPreToolUse`:
  - `deny` (or exit 2 where `BlockOnExit2`) → `Denied`, with
    `DenyMessage = permissionDecisionReason`. Deny precedence unchanged: **any**
    deny wins.
  - `allow` → recognized as an explicit non-deny. Serf has no permission gate to
    short-circuit, so `allow` does not override another hook's `deny` (documented
    divergence; Claude does not specify cross-hook precedence). Its
    `permissionDecisionReason`/`additionalContext` are still delivered.
  - `ask` / `defer` → **recognized but not honored**: serf has no interactive
    permission prompt (the same missing primitive that keeps `PermissionRequest`
    reserved). The tool **proceeds** (non-blocking), and the runner adds a
    user-visible diagnostic naming the unsupported decision. This keeps the honest
    line rather than fabricating a gate or silently denying.

### Part 2 + 3 — model-context vs. user-visible delivery (the split)

The root problem is that `parsedHookOutput.SystemMessage` is overloaded — it
carries exit-0 plain stdout, the JSON `systemMessage` field, and exit≠0 error
stderr, which have different correct destinations. Rather than re-deriving the
routing at each of the eight call sites (not DRY), the **Runner** owns
routing-by-event and returns two pre-routed buckets; the session owns the two
delivery channels.

**Parser** (`parseHookOutput`): tag the origin of `SystemMessage` so the runner
can route it. Add `SystemMessageFromStdout bool` — true when `SystemMessage` came
from exit-0 **non-JSON** stdout (context-eligible), false when it came from the
JSON `systemMessage` field or from an error (exit≠0 stderr). `AdditionalContext`
is already a separate field.

**Result structs** (`RunResult`, `PreToolUseResult`, `StopResult`) expose two
routed buckets instead of the current `SystemMessages` + `AdditionalContext`:

- `ModelContext []string` — destined for the model (the `additionalContext`
  field, always; plus exit-0 plain stdout on the **context events**
  `SessionStart` / `UserPromptSubmit`).
- `UserMessages []string` — destined for the user (the JSON `systemMessage`
  field; exit≠0 error stderr; and plain stdout on non-context events).

`Denied`/`DenyMessage` (PreToolUse) and `Blocked`/`BlockReason` (Stop) are
unchanged in role; the reason text is also surfaced as a `UserMessage`. The
existing `TerminalSequences` field is retained unchanged — it currently has no
consumer at any delivery site, so it is left exactly as-is (removing it is
unrelated cleanup, out of scope).

**Routing rule** (applied in the per-event aggregation, which knows the event):

```
isContextEvent := event ∈ {SessionStart, UserPromptSubmit}
for each hook output o:
    ModelContext += o.AdditionalContext
    if o.SystemMessage != "":
        if isContextEvent && o.SystemMessageFromStdout:   // exit-0 plain stdout, context event
            ModelContext += o.SystemMessage
        else:                                             // JSON systemMessage field, error, or non-context stdout
            UserMessages += o.SystemMessage
```

**Session delivery** — two shared helpers, used uniformly at every site (DRY),
replacing the eight `s.Steer(...)` pairs:

- `ModelContext` → a `<SYSTEM-REMINDER>`-wrapped `schema.TurnSteering` (serf's
  existing model-context idiom; matches Claude's "wraps the string in a system
  reminder"). A small helper, e.g. `s.deliverHookContext(text)`.
- `UserMessages` → an `events.EventWarning` (`WarningData{Source, Title, Message}`),
  which already renders in CLI (`[warning] …`), TUI, and hub/web. Claude itself
  calls `systemMessage` a "warning message shown to the user", so this is the
  semantically correct channel and needs **no new event plumbing**. A small
  helper, e.g. `s.deliverHookUserMessage(source, text)`.

This makes the eight sites (`PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`,
`UserPromptSubmit`, `SessionStart`, `Notification`, `PreCompact`) identical:
deliver `ModelContext` one way, `UserMessages` the other. `PreCompact` keeps its
existing "append to the post-compaction steering messages" path for `ModelContext`.

### Behavior changes (intended, documented)

- `systemMessage` (and exit≠0 stderr) no longer reaches the model for any event;
  it is shown to the user. (Docs already warned this was coming.)
- Plain-text stdout from non-context events (e.g. a `PostToolUse` logger) becomes
  user-visible instead of model context. Authors who want the model to see it use
  `additionalContext`. Serf surfaces it to the user rather than Claude's
  debug-log-only behavior — a deliberate, more-transparent divergence so hook
  output never silently vanishes.
- `SessionStart` / `UserPromptSubmit` plain-stdout-as-context is unchanged (still
  reaches the model).

## Files touched

- `agent/internal/hooks/hooks.go` — parser (`permissionDecision`, reason,
  deprecated mapping, `SystemMessageFromStdout`), result structs (`ModelContext`/
  `UserMessages`), `RunPreToolUse`/`runStopEvent`/`collectSystemMessages` routing,
  `ask`/`defer` diagnostic.
- `agent/internal/hooks/*_test.go` — parser + runner tests for the above.
- Session delivery sites + two helpers: `agent/session_tools.go`,
  `agent/session_tool_round.go`, `agent/subagents.go`, `agent/session_lifecycle.go`,
  `agent/session_init.go`, `agent/session_events.go`, `agent/session_compaction.go`.
- `agent/*_test.go` — update session-level hook tests that assert the old
  (both-to-model) behavior to the new routed behavior. **Update, not delete.**
- `docs/hooks.md` — output section, exit-codes notes, plain-text-stdout rules, the
  complete example, the `additionalContext`/`systemMessage` description.
- `docs/subagent-management/07-lifecycle-hooks-claude-compat.md` — move the
  now-shipped items out of "reserved (Phase B)"; trim the PreToolUse reserved
  schema and the reserved universal-fields notes to reflect what shipped; leave
  `ask`/`defer`, `PermissionRequest`, and the rest reserved with honest reasons.

`agent/internal/hooks/exitcode.go` — **unchanged.**

## Testing (red-green TDD)

- Parser: `permissionDecision` allow/deny/ask/defer parsed; `permissionDecisionReason`
  read; deprecated top-level `approve`/`block` + `reason` mapped; preferred form
  wins over deprecated; `SystemMessageFromStdout` set correctly for plain stdout
  vs JSON field vs error.
- `RunPreToolUse`: deny denies (decision and exit 2); allow does not override a
  co-occurring deny; ask/defer proceed and emit a user diagnostic;
  `permissionDecisionReason` becomes `DenyMessage`.
- Routing: `additionalContext` → `ModelContext`; JSON `systemMessage` → `UserMessages`;
  context-event plain stdout → `ModelContext`; non-context plain stdout →
  `UserMessages`; error stderr → `UserMessages`.
- Session-level: `additionalContext` produces a `<SYSTEM-REMINDER>` steering turn;
  `systemMessage` produces an `EventWarning` and **no** model turn (non-context);
  `SessionStart`/`UserPromptSubmit` plain stdout still reaches the model.
- Existing live scenario card (`test/scenarios/hooks-claude-compat-matcher.md`)
  stays green; extend or add a card only if a behavior it covers changed.

## Acceptance criteria

- `PreToolUse` honors `permissionDecision: allow|deny` + `permissionDecisionReason`
  and the deprecated `approve`/`block` mapping; `ask`/`defer` are recognized,
  non-blocking, and diagnosed.
- `additionalContext` is delivered to the model as a system-reminder, distinct
  from `systemMessage`.
- `systemMessage` (and error stderr) is user-visible and not sent to the model;
  context-event stdout still reaches the model.
- `make test` and `make lint` pass across all four modules.
- `docs/hooks.md` describes the shipped delivery accurately with no hedging; `07`
  no longer lists these items as reserved.

## Risks / divergences

- **Behavior change** to `systemMessage`/plain-stdout delivery — mitigated by
  updating docs and the affected tests, and by the docs' prior warning.
- **`ask`/`defer` proceed (fail-open)** rather than deny — deliberate: serf has no
  gate, and `defer` means "use the normal flow" (proceed) in Claude; `ask` cannot
  be honored without a prompt. The diagnostic makes the non-support loud.
- **Cross-hook `allow` vs `deny`** precedence is serf-defined (deny wins); Claude
  is silent here. Documented as a caveat.
