# Shared-notes follow-ups (split out of PR #1070)

Deferred review findings from PR #1070's roborev rounds, cheapest first. PR #1070 carries the
shared-notes/human-whiteboard and session-URL work itself; these are deliberately out of it.

PR #1070 carries the shared-notes/human-whiteboard and session-URL work. Four findings from its
review rounds are deliberately not in that PR; this branch picks them up, cheapest first.

## 1. `handleRemoveURL` in-flight guard (Low)

`cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.tsx` — a double-click on Remove sends
two `urls/remove` requests; the second reports the entry as missing after the first succeeded and
surfaces a spurious "Couldn't remove link" toast. Disable the per-row button while its request is
pending, and treat an entry that is already absent from `model.sessionUrls` as success rather than
an error.

## 2. Stale `expectedInstanceId` at debounce schedule time (Medium)

`cmd/evener-hub/frontend/src/stores/humanNoteDrafts.ts` — the delayed save captures
`expectedInstanceId` when the debounce is scheduled and passes it to `setHumanNote` 10s later,
so an instance rotation inside the window turns a saveable note into a "Session instance changed"
error. Decide the intent — fence against writing to a *different* session instance, or target the
current one — and either sample the identity inside `save()` or make the refusal recoverable in the
UI. The fencing semantics are the whole question here; do not weaken them by accident.

## 3. `urls/remove` durable outbox routing (Medium, accepted limitation)

`cmd/evener-hub/frontend/src/stores/threads.ts` and `cmd/evener-tui/hub_commands.go` — removals are
sent directly with a fresh mutation id, so they have no offline queue, no idempotent retry, and no
receipt. The backend already journals `urls/remove` by outer id, so the work is client-side: return
a mutation receipt from `urls/remove`, route both clients through `enqueueMutationIntent` with one
persisted id and payload, and settle the receipt. The response carries no state (the
`evener/urls/updated` push is the authority), so the receipt/ack wiring is the real design work.

## 4. Notes and URLs share one carrier generation (Medium)

`server/thread_envelope.go` (`RecordAppEvent`, `assign`), `server/appwire_runtime.go` —
a single `notesCarrierGeneration` bumps for both notes and URL carriers, so a notes commit landing
during a facet sample can drop a newer URL list and leave `thread/read` reporting stale URLs until
the next push or full sample. Give notes and URLs independent generations (mirroring
`goalCarrierGeneration` vs `taskCarrierGeneration`), or have `assign` drop only the fields the
winning carrier actually wrote.

## 5. Terminal injection via unstripped control sequences (Medium, security)

`agent/session_notes.go` (`normalizeNote`), `cmd/evener-tui/details_drawer.go` — `normalizeNote`
collapses whitespace but keeps ANSI escapes and C0/C1 controls, so an agent-supplied note, label,
URL, or id reaches the terminal raw and can emit OSC/CSI sequences. Strip `U+001B`, C0/C1 controls,
and `U+007F` during normalization, or sanitize before rendering notes content in the TUI. Cover a
malicious control-sequence input.

## 6. URI scheme rejection blocks filenames with colons (Low)

`agent/session_notes.go` (`canonicalSessionURL`) — a bare `a:b.md` is rejected as a URI scheme
before the in-scope path fallback runs. Try `canonicalFilePath` before rejecting a colon name.

## 7. `blurHumanNote` captures the instance before hydration (Low)

`cmd/evener-hub/frontend/src/stores/humanNoteDrafts.ts` — `expectedInstanceId` is captured before
`ensureThread` resolves, so a blur on an unhydrated thread saves with `""` and trips the
`Session instance changed` fence even though hydration succeeded. Capture the identity after
`await retained`, or pass `undefined` to use the post-hydration one. Overlaps item 2.
