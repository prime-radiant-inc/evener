# Resume Hook Ordering Design

Date: 2026-06-25

## Context

Serf resumes an existing session through `RestoreSessionFromMetaWithConfig`. The restore path currently sets `SessionStartKindResume`, initializes session state, initializes plugins, and runs `SessionStart` hooks during restore. Hook results can inject model context and user messages before any new post-resume user prompt exists.

A restored session can also re-arm durable terminal job notifications. Those notification wakes are autonomous `EntryNotification` turns, not user prompts. If resume hook output is injected during restore, or if a notification turn observes newly injected resume context before the user's new task, the model can continue stale work instead of responding to the new post-resume prompt.

The intended contract is:

- preserve `SessionStart` hooks with matcher/source `resume`;
- do not deliver their injected context or user messages during restore;
- deliver resume hook output exactly once with the first real post-resume user input;
- do not let notification, continuation, watch, or other autonomous entries consume resume hook output.

Codex follows this general model. In `inspo/codex/codex-rs/core/src/session/session.rs`, session initialization stores a pending `SessionStartSource::Resume` in session state for resumed histories. In `inspo/codex/codex-rs/core/src/session/turn.rs`, turn execution calls `run_pending_session_start_hooks()` before `UserPromptSubmit`; that helper uses `take_pending_session_start_source()` so the hook runs exactly once from the first turn rather than during session construction.

## Goal

Make resume `SessionStart` hook output inert until the first real post-resume user prompt, then inject it exactly once before that prompt enters model context.

## Non-goals

- Do not remove or rename `SessionStart` hooks.
- Do not change hook matcher names or plugin configuration syntax.
- Do not change startup-session hook behavior except where shared helper code needs to distinguish resume from startup.
- Do not change durable job notification semantics in this design.
- Do not change transcript storage format beyond whatever existing turn recording naturally does when the first user turn drains pending hook output.

## Current Serf behavior

Relevant Serf path:

1. `RestoreSessionFromMetaWithConfig` sets `cfg.SessionStartKind = plugin.SessionStartKindResume`.
2. `initSessionState` restores transcript/session state and arms pending terminal job notifications.
3. `initPlugins` calls `runSessionStartHooks`.
4. `runSessionStartHooks` currently delivers hook `ModelContext` and `UserMessages` immediately.
5. Separately, restored terminal job notifications can enqueue a textless `EntryNotification` before the next user prompt reaches the session.

This makes resume-time hook injection happen too early. It also allows autonomous notification turns to run before the new user task while resume hook output is already present.

## Design

### Architecture

Add explicit pending resume-session-start state to `agent.Session`.

For resumed sessions, plugin initialization should register that resume session-start hooks are pending instead of immediately delivering their model-facing output. The pending state is drained only by the first accepted `EntryUserInput` after resume.

The preferred implementation is Codex-shaped lazy execution:

- at restore/plugin initialization time, store enough information to run `SessionStartKindResume` later;
- on first real user input, take the pending state exactly once and run resume `SessionStart` hooks in that user turn's context;
- deliver hook model context and hook user messages before the current user prompt.

This is preferable to running hooks during restore and buffering their outputs because hooks can use current turn/session paths and because resumes that never receive another user prompt never run model-facing hook logic unnecessarily.

### Data flow

#### Restore

On restore:

1. Load session metadata/transcript as today.
2. Keep `SessionStartKindResume` as the selected hook source/matcher.
3. Initialize plugin registries as today.
4. Do not append resume hook `ModelContext` or `UserMessages` to model context.
5. Store pending resume-hook state on the session.

The pending state should be private to the session actor and protected by the same concurrency discipline as other session input state.

#### Autonomous entries

For these inputs, do not drain pending resume hooks:

- `EntryNotification`;
- continuation entries;
- watch-origin entries;
- any system/internal entry that is not a real user prompt.

If one of those entries runs before the first post-resume user input, the pending resume hook state remains unchanged.

#### First user input

When the first post-resume `EntryUserInput` is accepted for processing:

1. Atomically take the pending resume-hook state.
2. Run the matching `SessionStart` hooks with source/kind `resume`.
3. If hooks produce model context, append it before the user's prompt.
4. If hooks produce user messages, append them before the user's prompt in the same relative order used by current hook delivery.
5. Append/process the current user prompt.
6. Clear the pending state only after the user input is accepted into the turn path.

The resulting model-facing order is:

```text
[restored conversation]
[resume hook model context]
[resume hook user messages]
[current post-resume user prompt]
```

#### Later user inputs

After the pending state has been taken and successfully processed, later user prompts do not rerun or redeliver resume hooks.

### Error handling

Use existing hook error behavior. This design changes ordering, not the policy for failed hooks.

Important edge cases:

- If a user input is rejected before becoming an accepted turn input, the pending resume hook state must remain pending.
- If hook execution fails according to existing semantics, preserve those semantics. Do not silently drop the error or pretend the hook succeeded.
- If a notification turn arrives before a user input, it must not take or clear pending resume hook state.
- If no user input ever arrives, pending resume hook state remains inert.
- If the process exits after restore but before first user input, the next restore may recreate pending resume hook state from restore configuration; it should still run at most once for the first accepted user prompt in that process.

## Testing

Tests should use deterministic scripted providers and real Serf plumbing below the LLM boundary, consistent with `docs/testing.md`.

Add regression coverage for these cases:

1. **Restore does not inject immediately**
   - Configure a resume `SessionStart` hook that emits recognizable model context and a recognizable user message.
   - Restore a session.
   - Assert the restored model context/transcript does not contain those hook outputs before any user input.

2. **Notification does not drain**
   - Restore a session with pending resume hook state and a pending terminal job notification.
   - Let/process the notification-only entry.
   - Assert the hook outputs are still not delivered to that autonomous turn.
   - Assert the first later user input still receives the hook outputs.

3. **First user input drains in order**
   - Restore with a resume hook that emits context/message markers.
   - Submit one real user prompt.
   - Assert model-facing input order is restored history, hook context, hook user message, current user prompt.

4. **Second user input does not duplicate**
   - After the first user input drains pending hook output, submit another user prompt.
   - Assert hook markers appear only once across both turns.

5. **Rejected input preserves pending state**
   - Exercise the earliest available rejection path before a user input becomes an accepted turn.
   - Assert pending resume hook state remains available for the next accepted user input.

## Acceptance criteria

- Resume `SessionStart` hooks with matcher/source `resume` still run.
- Their model-facing output is not visible during restore or autonomous notification turns.
- Their model-facing output is delivered exactly once before the first accepted post-resume user prompt.
- Existing startup hook behavior remains unchanged.
- Regression tests cover immediate restore, notification-before-user, first-user ordering, no duplicate delivery, and rejected-input preservation.
