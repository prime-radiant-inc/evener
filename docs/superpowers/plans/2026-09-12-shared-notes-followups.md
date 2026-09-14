# Shared-notes follow-ups (split out of PR #1070)

Deferred review findings from PR #1070's roborev rounds, cheapest first. PR #1070 carries the
shared-notes/human-whiteboard and session-URL work itself; this branch picks up the findings that
were deliberately left out of it.

## Status

Closed on `shared-notes-followups` (PR #1249) unless noted; hashes are on that branch.

- **1** — fixed in `99fb9b8089`, completed in `0ce49e706` and `802276a97`: the per-row Remove button
  is disabled while its request is pending and the handler checks a synchronous ref, so two clicks
  inside one tick fire a single request. The finding's other half is implemented as well: a removal
  the server answers with "no URL entry with id" reads as success, because the outcome the user
  asked for is already true. An intermediate version held the guard until the `evener/urls/updated`
  push; roborev's fourth round showed that wedged a row whose id another client re-added before the
  push landed, so the guard is released when the request settles instead.
- **2, 7** — fixed in `24a4a433c3` (merged in `fe3c249fc0`): the delayed save samples the instance
  identity inside `save()` after hydration, so a rotation the client has already observed saves
  against the current instance. The daemon's `ExpectedInstanceID` fence remains the authority.
- **3** — not fixed: accepted limitation, per Jesse's ruling.
- **4** — fixed in `e113febe21` (merged in `0a9f86926c`): `urlsCarrierGeneration` split out of
  `notesCarrierGeneration`, so each carrier fences only the fields it wrote and neither can drop
  the other's newer sample.
- **5** — fixed in `53d83a21da`, completed by `bb4784854` and `efba9162e`: `normalizeNote` strips
  C0, DEL, and C1 **before** its whitespace collapse, so the human note, the agent note, and every
  URL label lose them while whitespace controls still collapse to one space (stripping after the
  collapse left a double space where a control sat between two spaces); raw URLs carrying any
  control rune are refused before parsing, because `url.Parse` accepts a C1 rune and keeps it
  verbatim in `RawQuery`; and both unknown-id error echoes quote the id. Values persisted before the
  strip are normalized on the way back in rather than left alone:
  `283157eaa` covers restored notes, the snapshot's canonical human note and the roster read, and
  `20a5a9592` adds the full strip for restored URLs and entry ids, a replayed response note, and the
  model copy of a persisted NOTES_CONTEXT turn.
- **6** — closed without a code change; see the note on the item below.
- **8, 9** — fixed in `a30b1ab8e3` (merged in `80747e1a0`): readers take one atomic load of a
  published committed notes cut (human note, agent note, URL list, ever-projected flag), and a
  notes mutator publishes only after its metadata save returns nil, so a tentative value can no
  longer reach `Meta()`, the projection snapshot, or `notes_read`, and no reader can straddle a
  mutation. The metadata write itself persists the live staged store, because that write is the
  mutation's durability point.
- **10, 11** — fixed in `655bac680f`: character references are parsed completely (any number of
  case-insensitive `amp;` layers, a required `;`, a named or numeric base, and a resolved value
  that really is an angle bracket) and rewritten to the canonical `&lt;`/`&gt;` spelling, so
  nesting depth stops buying anything and the pass is idempotent; text such as `?a=1&ltd=2` is
  left alone. Recorded residual: a reader that decodes one layer still sees `<` from the canonical
  spelling, exactly as it already did from a literally-typed `<`.
- **12** — fixed in `1f7c3f07e3`: `conversationSignals` excludes `schema.TurnNotesContext`
  alongside the hook-execution and environment turns.
- **13** — fixed in `481c34b2d8`: the past-session roster reads the top-level `human_note` through
  the lightweight projection reader (`ReadPersistedHumanNote`) instead of decoding and validating
  a journal-sized snapshot per entry; the strict reader remains the authority everywhere else.

## 1. `handleRemoveURL` in-flight guard (Low)

`cmd/evener-hub/frontend/src/panes/session/chrome/NotesPanel.tsx` — a double-click on Remove sends
two `urls/remove` requests; the second reports the entry as missing after the first succeeded and
surfaces a spurious "Couldn't remove link" toast. Disable the per-row button while its request is
pending, and treat an entry that is already absent from `model.sessionUrls` as success rather than
an error.

Implemented in both halves: the row is disabled while its request is pending (and a synchronous ref
keeps two clicks in one tick to one request), and a removal the server answers with "no URL entry
with id" reads as success rather than the spurious "Couldn't remove link" toast, since the entry is
already gone and the user's intent is satisfied.

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

Resolved without a code change: the rejection is the deliberate defense against `javascript:`,
`data:`, `mailto:` and friends (there is no exhaustive scheme list to check against), so review
round 14 kept the semantics and made the contract honest instead — `DefUrlsAdd` documents the colon
rule and the error names the `./` escape hatch. See Status.

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
