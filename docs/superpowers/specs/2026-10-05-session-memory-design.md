# Session memory

## Intent

Evener memory has two scopes. Personal memory holds what applies beyond any one project. Project memory holds knowledge about the project. Neither has a place for working notes about the current work: its plan, what has been tried and what has been found. Today that knowledge either lives only in the transcript, where compaction loses it, or gets written to project memory, where it clutters the project with task-local detail.

Add a third scope, **session**, for working notes about the current work.

The session whiteboard (`notes_agent_set`) stays a status channel to the human partner. Improving its prompting is a separate item, outside this spec.

Success means agents keep working notes (plan, what they tried and ruled out) in session memory during longer work. Those notes survive compaction, delegates can read them, and anything that holds beyond the work gets promoted to project or personal memory. Plans, constraints and decisions the partner states go to project memory. The memory prompt lab measures this (see Verification).

## Decisions

| Question | Decision |
|---|---|
| Who shares a session's memory | The root session owns it. Delegates read it and cannot write it. |
| Do delegates get their own session memory | No. A delegate reports findings to its parent, and the parent decides what to save. |
| Fork | The fork gets a copy of the parent's session memory as of the fork's first use, and the two diverge. |
| Session deletion | Session memory is kept, like the other scopes. Removal is manual for now (see #3748). |
| Promotion | Before its final report, the agent copies anything that outlasts the work into project or personal memory. |
| Mechanism | A third scope in the existing memory machinery. Session notes and the history state directory are not used. |

## Storage, identity and lifetime

- **Location:** `<state-root>/memory/sessions/<root-session-id>/`, under the same memory root as personal and project memory. History overrides (`--state-dir`, `EVENER_STATE_DIR`) do not move it.
- **Owner:** the root session. A delegate resolves `scope: "session"` to its root session's id through the existing `delegateRootSessionID`.
- **Resume:** keeps the session id, and with it the memory.
- **Compaction:** the session index is re-projected afterward, as the other scopes are.
- **Fork, and `--resume-with`:** the fork gets a copy of the source session's session memory as of the fork's first use (its first session-memory access, normally its first model call). The copy is made in the forked session's own process, because memory belongs to the host that runs the session; after that the two diverge. A missing or empty source directory gives an empty start. The copy takes only regular files and directories within a size limit; anything else means a warning and an empty start. A copy failure is reported as a session warning and leaves an empty session scope; it never blocks the fork.
- **Deletion:** deleting a session leaves its session memory in place. No native memory tool reaches another session's session directory, so removal is manual for now (see #3748, which tracks cleanup and directory accumulation).
- **Opt-out:** `--disable-memory` disables session memory along with the other scopes. There is no separate switch.

## Tools, context and enforcement

- **Tools:** the five native memory tools' `scope` enum becomes `personal | project | session`. There are no new tools. Paths stay relative and confined to the scope root, as today.
- **Delegate writes:** `memory_write`, `memory_edit` and `memory_delete` with `scope: "session"` return a tool error from a delegate: "session memory belongs to the root session; report this to your parent instead." The error goes back to the model so it can redirect. Nothing else in the delegate's session is affected. Delegates can still call `memory_read` and `memory_search` on session scope.
- **Index injection:** a third projection, named `memory_session`. It uses the same 8 KiB bound, quoting, currentness framing and refresh points as the other scopes: startup, resume, after compaction, and model boundaries. A delegate receives its root's session index, framed as read-only.
- **Gating:** the session scope is available whenever memory is enabled. For delegates, the save predicate that gates write-side guidance treats session scope as read-only. Guidance names only tools and scopes the session can use.

## Prompting

These changes are added to the existing memory guidance (`memoryGuidance()` and the Finishing rows). Each part is shown only when the session can act on it. A delegate with no root session id gets neither the scope line nor the delegate line, since it has no usable session scope.

- **Scope line:** "Session memory holds working notes about the current work: its plan, what you tried and what you found."
- **Save trigger (root sessions):** "you are partway through longer work: keep its plan, what you tried and what you ruled out in session memory, so you could pick it up again after compaction or an interruption."
- **Partner-told plans, constraints and decisions go to project memory.** Session memory holds working notes during longer work.
- **Skip rule (root sessions):** working notes about the current work belong in session memory, not project memory (", not project memory" only when project memory is bound).
- **Promotion:** a Finishing row for root sessions. "The work is done and session memory has notes." → "Before you report, copy anything that holds beyond this work into project or personal memory."
- **Delegates:** "Session memory belongs to your root session. Read it, and report what you learn to your parent."

## Verification

**Unit tests:**
- scope resolution, for root and delegate
- a delegate's session write refused with the redirect error
- fork and `--resume-with` copy the directory, and a copy failure doesn't block the fork
- resume keeps the same memory
- the session index is projected, and re-projected after compaction
- guidance gating for root and delegate sessions

**Memory prompt lab:** the sequential-session harness used for #3740. Each scenario is measured against the current prompt as the baseline, with interviews (resuming the session and asking why) where agents pick the wrong scope.
1. Longer work leaves working notes in session memory.
2. A project fact first noted in session memory gets promoted before the final report.
3. A delegate reads the root's session memory, and reports instead of writing.

## Documentation

In the same change:
- update `docs/product/memory.md` (storage roots, scopes, lifetime, delegate rule, fork copy)
- update the tool reference in `docs/tools/memory.md`
- update the S24 memory row in `docs/product/subsystems.md` if its responsibilities text changes
- update `internal/bundled/skills/gardening-memory/SKILL.md` so it covers session scope and promoting session notes

## Out of scope

- Whiteboard prompting.
- Automatic cleanup of session memory for deleted sessions (#3748).
- Delegate-private session memory.
