# Shared-notes follow-ups (split out of PR #1070)

Deferred review findings from PR #1070's roborev rounds, cheapest first. PR #1070 carries the
shared-notes/human-whiteboard and session-URL work itself; this branch picks up the findings that
were deliberately left out of it.

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

## 8. Tentative notes state can reach the cached envelope (Medium)

`agent/session_notes_rpc.go` (`mutateHumanNote`, `mutateAgentNoteSerialized`,
`mutateSessionURLAddSerialized`, `RemoveSessionURL`), `agent/session_state.go` (`Meta`),
`server/thread_envelope.go` (`assign`) — the mutators write live state under `Session.mu`, then
persist meta.json holding `metaSaveMu` but not `Session.mu`, and on a persistence failure restore
the previous value with no emission. `Meta()` reads the live notes without `metaSaveMu`, so a
concurrent `refreshFacets(facetGoal)` sample can install the tentative value into the cached
envelope, where `thread/read` serves it until the next facet sample (turn end is `facetAll`).
Durability is not affected: the save gates the success journal, so no false success is recorded and
the RPC returns the error for retry. Fix alongside the notes-carrier authority rework (item 4) — make
sampling read a committed notes snapshot, reorder the save seam to persist-before-publish, or emit a
rollback repair; the last needs the client outbox semantics checked first, since a push that
contradicts a pending optimistic mutation may be discarded. `cfg.testOnly.notesAutoSaveFault` makes
the failure path deterministically testable.

## 9. `notes_read` can return a non-atomic notes snapshot (Low)

`agent/session_tools_notes.go:85` through `Session.notesSnapshotAll` and `notesProjectionSnapshot` — the
read takes the agent note, the URL list, and `notesEverProjected` under `Session.mu`, then the canonical
human note under the mutation store's `stateMu`, so it is not one atomic cut of the four. The function's
own comment documents that gap and says callers holding `notesUpdateMu` are what keeps a mutation from
landing inside the read, which is how `notesContextBlock` uses it; the `notes_read` tool is a caller
that does not hold it, so a concurrent mutation can return one field from before it and another from
after. Each returned value is internally valid, so the impact is a transient mixed read in an
agent-facing text blob, not durability or display corruption.

Fix with item 8, which needs an atomically published notes snapshot that readers take without blocking.
`Meta()` cannot simply take `notesUpdateMu`: the mutators hold it across the meta.json persistence I/O
that the sampling path exists to stay off. Raised in roborev round 18 and deferred by Jesse's ruling
alongside item 8.

## 10. Doubly-encoded framing references reach the model copy (Medium, security)

`agent/session_notes_rpc.go` (`notesAngleBracketReference`, used by `neutralizeNotesFraming`) — the
pattern `&(?:amp;)?(?:#|lt|gt)` reaches exactly one `amp;` layer, so `&amp;amp;lt;` matches nowhere and
passes through into the model-facing copy; repeated entity decoding re-arms it into `<`, which is what
the framing escape exists to prevent. Item 11 constrains the same pattern from the other side, so the
two need one design: parse a complete character reference with a terminator, follow nested `amp;` layers
to whatever depth appears, and decide what the model copy should hold when the resolved value is an
angle bracket. Note that escaping the leading `&` only buys one more decode layer rather than closing
the class; closing it means canonicalizing the copy, which changes what the model reads.

## 11. Framing neutralizer rewrites innocent references (Low)

`agent/session_notes_rpc.go` (`neutralizeNotesFraming`) — the reference match requires no terminator or
validity check, so text such as a URL query containing `&lt` is rewritten in the model copy while
`notes_read` and the UI keep the original, and the model reasons over text the user never sees. Require
the full reference shape before escaping the `&`. Design with item 10, not as a separate patch.

## 12. `conversationSignals` counts injected notes context as conversation (Low)

`agent/session_init.go:2050` — the loop excludes `TurnHookCompleted` and `TurnEnvironment` but not
`schema.TurnNotesContext`, so a session whose first history turn is `NOTES_CONTEXT` is classified as
already carrying a conversation. Exclude it and cover the case behaviourally.

## 13. Past-session roster decodes a whole mutation snapshot per entry (Low, performance)

`cmd/evener-hub/app_threadread.go` (`pastEntryThreadForList`, via `agent.ReadCanonicalHumanNote`) — the
canonical human-note read loads and JSON-decodes the session's entire client-mutation snapshot once per
past entry inside the roster-building loop, adding O(past sessions × journal size) synchronous I/O and
decoding to every past-session listing. Expose a lightweight reader that decodes only the human note, or
memoize the canonical note alongside the existing per-entry caches.
