# Phase B (hooks): PreToolUse output schema + additionalContext/systemMessage delivery split

> Revised twice after adversarial review (`/par`). v2 fixed a Notification-hook
> recursion and a PostToolUse-stderr regression. v3 fixes a Stop/SubagentStop
> exit-2 stderr drop and removes the whole edge-case class by giving the parser a
> dedicated deny-reason field and an explicit, event-keyed error→model rule.

## Context

Serf's lifecycle-hook Phase 1 is shipped (`docs/hooks.md`): the nine fired events,
the matcher, `command`/`prompt` handlers, the input fields, the exit-code table.
The roadmap (`docs/subagent-management/07-lifecycle-hooks-claude-compat.md`)
reserves the rest in honest tiers.

This is the first slice of **Phase B**, scoped to *"fix the events we already
fire"* — making the **output contract** of the nine fired events match Claude,
without adding new events. Two reserved items, both for events serf already fires:

1. The **`PreToolUse` preferred output schema** (`permissionDecision`,
   `permissionDecisionReason`, the deprecated top-level `approve`/`block`
   mapping). Today serf honors only `permissionDecision: "deny"` and reads the
   reason from the wrong key (`hso.reason`).
2. A **distinct delivery channel for `additionalContext`** vs. `systemMessage`.
   Today both are delivered identically via `s.Steer(...)` — a bare `llm.User(text)`
   turn, as if the user spoke. Eight `TODO(phase-B)` anchors mark the sites.

Explicitly **out of scope** (stays reserved in `07`): new events, the approval
flow, `ConfigChange`, `UserPromptExpansion`, the `if` rule language,
async/`http`/`mcp_tool`/`agent` handlers, the **`ask`/`defer` interactive
decisions**, **`updatedInput` revalidation**, and **reworking the exit-code
error-stderr destinations** (current per-event behavior is preserved).

## Grounded Claude semantics

From <https://code.claude.com/docs/en/hooks>:

- `permissionDecision` ∈ `allow|deny|ask|defer`; reason field `permissionDecisionReason`.
- `additionalContext` → the model, *"wrapped in a system reminder."*
- `systemMessage` → the user only (*"warning message shown to the user"*).
- Plain-text stdout (exit 0, non-JSON) is model context only for `UserPromptSubmit`,
  `UserPromptExpansion`, `SessionStart`; for other events it is debug-log only.
- Exit-2 stderr destination is event-specific: `PostToolUse`/`PostToolUseFailure`
  add it to the **model**; `PreToolUse`/`Stop`/`SubagentStop` use it as the block
  reason / model-facing feedback; `Notification`/`SessionStart`/`SessionEnd` show
  it to the user.
- The deprecated top-level `decision: "approve"|"block"` form is documented as
  deprecated-but-supported; the exact `approve→allow`/`block→deny` mapping for
  `PreToolUse` is serf's interpretation. Serf accepts it as a fallback; `07`
  tracks it.

## Goals

- `PreToolUse` honors `permissionDecision: allow|deny` + `permissionDecisionReason`
  + the deprecated `approve`/`block` fallback.
- `additionalContext` reaches the model through a distinct, `<SYSTEM-REMINDER>`-wrapped
  channel.
- The exit-0 JSON `systemMessage` field becomes user-visible (not model context).
- All current model-facing delivery is preserved: `SessionStart`/`UserPromptSubmit`
  stdout-as-context; `PostToolUse`/`Stop`/`SubagentStop` exit-2 stderr to the model.
- `docs/hooks.md` becomes correct about delivery; `07` moves the shipped items out
  of "reserved (Phase B)" (splitting `updatedInput` revalidation, which stays reserved).

## Non-goals

- No new events, approval flow, `if` language, async/`http`/`mcp_tool`/`agent`,
  or `updatedInput` revalidation.
- `ask`/`defer` are **recognized but not honored** (no permission prompt).
- **No change to exit-code blocking or to where exit≠0 stderr is delivered** beyond
  preserving today's behavior. (The residual `Notification`/`SessionStart` exit-2
  stderr→model vs Claude's user-only is left as-is to keep the increment focused —
  a deliberate scope cut, not a constraint.)

## Design

### Parser — stop overloading `SystemMessage`

`parseHookOutput` (`agent/internal/hooks/hooks.go`) currently funnels four
distinct things into one `SystemMessage` field: exit-0 plain stdout
(`hooks.go:430`), the exit-0 JSON `systemMessage` field (`hooks.go:441`), exit≠0
error stderr (`hooks.go:415-419`, early return, `IsError=true`), and the
PreToolUse deny reason (`hooks.go:452-456`, currently read from the wrong key
`hso.reason`). They have different destinations, so the parser must keep them
distinct. `parsedHookOutput` gains:

- `PermissionDecision string` — `""|allow|deny|ask|defer` (from
  `hookSpecificOutput.permissionDecision`).
- `PermissionReason string` — from `permissionDecisionReason` (and, for the
  deprecated form, top-level `reason`). The deny reason no longer goes into
  `SystemMessage`.
- `SystemMessageIsJSONField bool` — true **only** in the JSON `systemMessage`-field
  branch (`hooks.go:441`); false in the plain-stdout branch (`hooks.go:430`) and
  the exit≠0 early return.

`AdditionalContext`, `IsError`, `Blocked`/`BlockReason` (from top-level JSON
`decision:"block"`), `UpdatedInput`, and `TerminalSequence` are unchanged.

With this, `SystemMessage` now holds exactly one of: plain stdout
(`!IsError && !IsJSONField`), the JSON `systemMessage` field
(`!IsError && IsJSONField`), or error stderr (`IsError`).

### Result structs — two routed buckets

`RunResult`, `PreToolUseResult`, `StopResult` replace `SystemMessages` +
`AdditionalContext` with:

- `ModelContext []string` — delivered to the model.
- `UserMessages []string` — shown to the user.

`Denied`/`DenyMessage`, `Blocked`/`BlockReason`, and `TerminalSequences` are kept
(`TerminalSequences` gains a comment: parsed-but-unrouted today, no delivery-site
consumer; renaming/removing it is out of scope).

### Routing (Runner owns it; it knows the event)

`collectSystemMessages` gains an `event` parameter (its five callers —
`RunPostToolUse`, `RunUserPromptSubmit`, `RunSessionStartFor`, `RunPreCompact`,
`RunNotification` — are updated). `RunPreToolUse` and `runStopEvent` already know
their event. Per hook output `o`:

```
isContextEvent := event ∈ {SessionStart, UserPromptSubmit}

ModelContext += o.AdditionalContext                 // JSON additionalContext → model, always

if o.IsError:                                       // exit≠0 stderr
    if event == PreToolUse && <this output denies>:
        // consumed as the deny reason (see RunPreToolUse), NOT pushed to a bucket
    else:
        ModelContext += o.SystemMessage             // model — preserves today's behavior for
                                                    // PostToolUse, Stop, SubagentStop, and
                                                    // PreToolUse non-deny (exit 1) errors
else if o.SystemMessageIsJSONField:
    UserMessages += o.SystemMessage                 // exit-0 JSON systemMessage field → user
else if o.SystemMessage != "":                      // exit-0 plain stdout
    if isContextEvent: ModelContext += o.SystemMessage   // SessionStart/UserPromptSubmit → model
    else:              UserMessages += o.SystemMessage   // other events → user (divergence; see Risks)
```

The only event where error stderr is **not** pushed to `ModelContext` is a
**denying** `PreToolUse` output, where it becomes the deny reason instead. For
`Stop`/`SubagentStop`, error stderr always → `ModelContext` (exactly today's
model delivery; `Blocked` is signalled independently). This closes the v2
Stop/SubagentStop drop.

### Part 1 — PreToolUse decision aggregation (`RunPreToolUse`)

- `deny` (or deprecated top-level `decision:"block"` → the already-parsed
  `Blocked`, which `RunPreToolUse` must **newly** treat as a deny for PreToolUse —
  today it reads only `Denied`/exit-2), or exit 2 where `BlockOnExit2` → `Denied`.
  Any deny wins.
- `DenyMessage` = `PermissionReason` if set, else the exit-2 stderr
  (`SystemMessage` when `IsError`), else empty. (Preserves
  `TestHookRunner_PreToolUse_ExitCode2Denies`, which asserts the exit-2 stderr in
  `DenyMessage`.) The deny reason is **not** also routed to `UserMessages`.
- `allow` → explicit non-deny; does not override another hook's `deny` (Claude is
  silent on cross-hook precedence; serf keeps deny-wins). A non-deny
  `PermissionReason` has no Claude-defined destination and is recognized but not
  surfaced.
- `ask`/`defer` → recognized but not honored (no permission prompt). The tool
  proceeds, and `RunPreToolUse` appends a `UserMessages` diagnostic naming the
  unsupported decision (per occurrence; no new field — it rides the existing
  `UserMessages` bucket).

### Session delivery — two helpers replace the eight `Steer` pairs

- `s.deliverHookContext(text)`: wraps `text` in `<SYSTEM-REMINDER>…</SYSTEM-REMINDER>`
  and enqueues it via `Steer` (the queue, so `Stop`/`SubagentStop` context survives
  to the next model turn). This **adds** the wrapper — today's hook `Steer` text is
  bare; the wrapper is the deliberate move toward Claude's "wrapped in a system
  reminder."
- `s.deliverHookUserMessage(text)`: emits via **`emitDiagnosticWarning`** — the
  path that renders in CLI (`[warning] <Message>`), TUI, and hub/web **without**
  firing the Notification hook (plain `emit` re-enters the Notification hook and
  recurses; `session_events.go` documents this). The hook text goes in `Message`
  (CLI renders only `Message`). To label it without it being reclassified by
  message content, add a `SourceHook` value to `agent/internal/diagnostic`
  (`normalizeSource`/`Classify` only recognize `provider|serf|hub|ui` today, so
  `Source:"hook"` would otherwise be overwritten by `enrichWarningData`).

Seven of the eight sites use `Steer` (`PreToolUse`, `PostToolUse`, `Stop`,
`SubagentStop`, `UserPromptSubmit`, `SessionStart`, `Notification`). The eighth,
`PreCompact` (`session_compaction.go`), appends `ModelContext` to its existing
`messages []string` slice (consumed by `appendSteeringMessagesToHistory`, a direct
history append mid-compaction); those strings must be `<SYSTEM-REMINDER>`-wrapped
too (so the wrapper is uniform across all events), and its `UserMessages` use
`deliverHookUserMessage` (a pure stream send — safe mid-compaction).

### Behavior changes (intended)

- Exit-0 JSON `systemMessage`, and exit-0 plain stdout from non-context events,
  become user-visible (not model context).
- `additionalContext` (and `SessionStart`/`UserPromptSubmit` stdout context) is now
  `<SYSTEM-REMINDER>`-wrapped — including the bootstrap text a `SessionStart` hook
  injects (so its delivered text changes shape).
- **Unchanged:** exit≠0 stderr still reaches the model for `PostToolUse`/`Stop`/
  `SubagentStop` (and PreToolUse non-deny errors); blocking still works.

## Files touched

- `agent/internal/hooks/hooks.go` — parser fields (`PermissionDecision`,
  `PermissionReason`, `SystemMessageIsJSONField`; `permissionDecisionReason`
  replacing `hso.reason`; deprecated mapping), result structs, `RunPreToolUse`
  (deny aggregation + `DenyMessage` fallback + ask/defer diagnostic + `Blocked`→deny),
  `runStopEvent`, `collectSystemMessages(event, outputs)`.
- `agent/internal/diagnostic/diagnostic.go` — add `SourceHook`.
- Session delivery + two helpers: `agent/session_tools.go`,
  `agent/session_tool_round.go`, `agent/subagents.go`, `agent/session_lifecycle.go`,
  `agent/session_init.go`, `agent/session_events.go`, `agent/session_compaction.go`.
- **Tests (update, not delete):**
  - `agent/internal/hooks/exitcode_test.go` — PostToolUse exit-2 asserts
    `SystemMessages` → `ModelContext`.
  - `agent/internal/hooks/hooks_test.go` — `.SystemMessages`/`.AdditionalContext`
    consumers → buckets; `TestParseHookOutput_PreToolUseDeny` (reads the old
    `hso["reason"]` key) → `permissionDecisionReason`/`PermissionReason`; new
    parser/runner cases.
  - `agent/plugin_integration_test.go` — the SessionStart bootstrap assertions
    (`:112`, `:127`) now expect the `<SYSTEM-REMINDER>`-wrapped text.
  - `agent/plugin_integration_live_test.go`, `agent/plugin_real_test.go` —
    `.SystemMessages`/`.AdditionalContext` consumers → buckets / `DenyMessage`.
  - Session-level hook tests asserting old both-to-model delivery.
- `docs/hooks.md` — "What your hook returns" (systemMessage→user; additionalContext→model
  wrapped; plain-stdout rule), `allow|deny` + `ask`/`defer` wording, exit-code
  notes, the complete example (PostToolUse logger stdout now user-visible).
- `docs/subagent-management/07-lifecycle-hooks-claude-compat.md` — move shipped
  items out of "reserved (Phase B)", **splitting `updatedInput` revalidation**
  (line 101/660) so it stays reserved; keep `ask`/`defer`/`PermissionRequest`/etc.
  reserved with honest reasons.

`agent/internal/hooks/exitcode.go` — **unchanged.**

## Testing (red-green TDD)

- Parser: `permissionDecision` allow/deny/ask/defer; `permissionDecisionReason` →
  `PermissionReason`; deprecated top-level `approve`/`block` + `reason` mapped;
  preferred wins; `SystemMessageIsJSONField` true only for the JSON field.
- `RunPreToolUse`: deny denies (decision, top-level `block`, exit 2); `DenyMessage`
  prefers `PermissionReason`, falls back to exit-2 stderr; deny reason **not** in
  `UserMessages`; `allow` doesn't override a co-occurring deny; ask/defer proceed
  and emit one `UserMessages` diagnostic; non-deny PreToolUse error stderr →
  `ModelContext`.
- Routing/regression guards: `additionalContext` → `ModelContext`; JSON
  `systemMessage` → `UserMessages`; context-event plain stdout → `ModelContext`;
  non-context plain stdout → `UserMessages`; **`PostToolUse` exit-2 stderr →
  `ModelContext`**; **`Stop`/`SubagentStop` exit-2 stderr → `ModelContext`** (the
  v3 regression guard).
- Session-level: `additionalContext` → a `<SYSTEM-REMINDER>` steering turn; JSON
  `systemMessage` → an `emitDiagnosticWarning`, no model turn, **no Notification-hook
  re-entry** (recursion guard, mirroring `recursion_test.go`); `SessionStart`
  bootstrap stdout still reaches the model (wrapped).

## Acceptance criteria

- `PreToolUse` honors `permissionDecision: allow|deny` + `permissionDecisionReason`
  + deprecated `approve`/`block`; `ask`/`defer` recognized, non-blocking, diagnosed;
  `DenyMessage` keeps its exit-2 fallback; deny reason not double-delivered.
- `additionalContext` → model as a distinct `<SYSTEM-REMINDER>`; exit-0 JSON
  `systemMessage` → user (non-recursing); `PostToolUse`/`Stop`/`SubagentStop` exit-2
  stderr still reach the model.
- `make test` and `make lint` pass across all four modules.
- `docs/hooks.md` accurate; `07` no longer lists these (except `updatedInput`
  revalidation) as reserved.

## Risks / divergences

- **Behavior change** to `systemMessage`-field / non-context plain-stdout / wrapped
  context — mitigated by updating docs + tests.
- **Non-context plain stdout → user** diverges from Claude (which debug-logs it to
  *neither* model nor user). Deliberate: serf has no surfaced debug-log sink, so
  user-visibility beats silent drop. Documented in `docs/hooks.md`.
- **`ask`/`defer` proceed (fail-open)** — `defer` = "use the normal flow"; `ask`
  cannot be honored without a prompt; the diagnostic makes it loud.
- **Cross-hook `allow` vs `deny`** is serf-defined (deny wins); Claude is silent.
- **Residual (out of scope):** non-blocking exit-2 stderr for
  `Notification`/`SessionStart` still reaches the model (Claude: user only). A
  deliberate scope cut to keep the increment focused — `deliverHookUserMessage`
  makes a later fix trivial.
