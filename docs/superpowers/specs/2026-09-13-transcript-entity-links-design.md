# Transcript Entity Links

## Status

Approved in outline. Scope locked with Jesse on 2026-09-13, revised after
design reviews (roborev jobs 9855 and 9876):

- **Entities:** shell jobs (`job_…`) and stable delegates (`dlg_…`) get a link
  plus a hover card; evener watches (`watch_…`) get a hover card only.
- **Resolution:** client-side, from state the app already loads. No new
  backend or AppWire surface.
- **Surfaces:** message prose and structured id fields. Identified literal
  rendering surfaces — fenced and inline code, raw job output, ANSI logs, JSON,
  and copy targets — are never rewritten. Plain user-message text is the one
  accepted exception; see "User message prose".

This is a frontend-only change under `cmd/evener-hub/frontend/`.

## Evidence and problem

Entity ids already appear in transcripts as inert text: an agent writes
`job_034MY2rfMj2ho4Nj6J0jFB_…` in a report, a `job_list` row prints the
identity, a delegate card prints the id. The client already knows how to open
most of these things, and it already knows how to attach an open affordance to
prose: `AgentMarkdown` post-processes sanitized Markdown and hangs an
`OpenButton` on file links. Entity ids get none of that.

The pieces exist but are disconnected:

- `widgets/openbutton` is the standard affordance: the box-arrow glyph,
  tooltip "Open", one treatment everywhere.
- `panes/session/transcript/openTranscript.tsx` opens a read-only transcript
  pane beside; the `transcript` pane renders a `job:<id>` ref as a job log
  (`JobLog.tsx`) and a session ref as a thread transcript.
- `stores/threads.ts` exposes `listJobs(ref)` (`evener/jobs/list`) and
  `jobOutput(ref, jobId)`; `ThreadModel.delegates[]` carries live delegate
  projections.
- `protocol/activityData.ts` parses the full jobs+delegates activity tree,
  including per-entity state.
- `widgets/tooltip` and `widgets/popover` are the floating-layer primitives.

Nothing connects an id found in text to any of this.

## Goals

- Detect `job_`, `dlg_`, and `watch_` ids in transcript prose and structured id
  fields, using the same identity rules the server mints and validates.
- Render job and delegate ids as links that open the referenced entity beside,
  through the existing standard `OpenButton` affordance, when a verified open
  target exists.
- Render a hover card on every resolvable id summarizing the entity's current
  state, from client-side state.
- Leave unresolvable ids as plain text.
- Never rewrite identified literal surfaces: code, raw output, ANSI logs, or
  JSON. Plain user-message text is enhanced regardless of its apparent
  content, as a documented exception.

## Non-goals

- No Go, AppWire, or daemon changes. No cross-session entity index.
- No link affordance on watches; they get a hover card only.
- No linking of session ids, `ag_`, `att_`, `call_`, `wg_`, `wd_`, `plugin_`,
  or `item_` ids.
- No linkification inside fenced or inline code, raw tool output, ANSI logs,
  JSON payloads, or any copy/export string. This applies to surfaces that
  render those things as such; it does not apply to plain user-message text,
  which the client cannot classify.
- No job link when ownership cannot be verified (see "Open targets").
- No new pane type. Jobs open the existing `job:<id>` transcript pane;
  delegates open their child transcript pane.
- No change to reasoning text (`ThinkBlock`) or notice text
  (`SystemNoticeItem`, `NotificationCard`) in this delivery. They reuse the
  same enhancer later if wanted.

## Entity identity and detection

### Formats (`identifier/`)

| Kind | Shape | Length | Owner recoverable? |
| --- | --- | --- | --- |
| job | `job_` + 22 base62 owner session id + `_` + 12 base62 | 39 | yes, from the id |
| delegate | `dlg_` + 22 base62 UUIDv7 payload | 26 | no |
| watch | `watch_` + 22 base62 UUIDv7 payload | 28 | no |

Base62 is `0-9A-Za-z` (`identifier/uuid.go`). Ids are case-sensitive.

### Detection rules (full validator parity)

`entityIds.ts` is a pure module that mirrors `identifier.ValidateJobID`,
`ValidateDelegateID`, and `ValidateWatchID`, including the UUID payload checks
that a length-and-alphabet test alone misses. For every id:

1. **Length and prefix** match the table exactly.
2. **Alphabet:** every payload character is in the base62 alphabet.
3. **Payload decodes to at most 128 bits.** `DecodeUUID` rejects a base62
   value whose bit length exceeds 128, so 22 valid base62 characters are not
   sufficient on their own.
4. **UUIDv7:** the decoded bytes carry version nibble `7` and RFC4122 variant
   bits (`10xxxxxx`). `ValidateUUIDv7Payload` enforces both.

Per kind:

- job: the 22-character owner portion must pass checks 1–4 as a session id
  (`ValidateJobID` calls `ValidateSessionID` on it); the 12-character suffix is
  base62 only, with no UUID check.
- delegate and watch: the payload must pass checks 1–4.

A candidate matches only at its exact length. The character immediately before
the match, when present, must not be `[0-9A-Za-z_]`; the character immediately
after, when present, must not be `[0-9A-Za-z_]`. This keeps `xjob_…`,
`job_…extra`, and a longer underscore-joined token from matching.
`findEntityIds(text)` returns `{ kind, id, start, end }[]` for a string.

### The provided example and the suffix-length question

Jesse's example, `job_034MY2rfMj2ho4Nj6J0jFB_q2qdqT99U6`, carries a
10-character suffix. The current mint (`identifier.NewJobID`) and validator
require exactly 12. This spec mirrors the server validator; that example would
not match.

Relaxing the suffix to 1–12 base62 is **not** part of this change. If real
transcripts turn up short-suffix ids, that is a separate compatibility
decision that must first establish whether the backend's job operations can
resolve those ids at all. Loosening detection alone would surface links to
jobs the backend cannot answer for.

## Client-side resolution

A single `entityIndex` resolves an id to a normalized summary. It reads only
state the app already has or can already fetch.

### Sources

1. **Current session's activity tree.** `threadsStore.listJobs(ref)`
   (`evener/jobs/list`), or `retainedActivityTree` from
   `stores/activityPanel.ts` when the activity panel already loaded one. The
   tree carries `ActivityJob` and `ActivityDelegate` records, including nested
   delegates.
2. **Current session's `ThreadModel.delegates[]`.** Live `EvenerDelegateInfo`
   projections, refreshed by the reducer on `evener/delegate/updated` and the
   jobs notifications.
3. **`job_watch` payloads in the session's loaded turns.** Evener watch state
   crosses the wire only inside a `job_watch` tool result; there is no watch
   field in `thread/read`, `evener/jobs/list`, or any store.
4. **Cross-session jobs.** A job id encodes its owner session id in
   characters 4–26. A job whose owner is not the current session resolves from
   the owner's own tree, fetched lazily (see "Lazy fetch, caching, errors").

### Reconciliation, not source priority

Sources are merged per entity id. Neither source outranks the other wholesale:

- **Delegates.** `ActivityDelegate.projectionRevision` is optional on the tree
  record; `EvenerDelegateInfo.projectionRevision` is required on the live
  projection. Precedence: when both are numeric, keep the higher revision and
  prefer the live `ThreadModel.delegates[]` record on a tie; when the tree
  record lacks a revision, prefer the live projection; when the live model has
  no such delegate, keep the tree record. A missing revision never drops a
  record.
- **Jobs.** The tree is the only structured source, and its freshness is not a
  bare revision equality. See "Freshness tokens".
- **Watches.** Ordered snapshots, last-present-wins; see "Watch snapshot
  ordering".

### Freshness tokens

The retained tree is reusable only when its fetch token still matches the
model. A token is `(connectionGeneration, jobsUpdatedAt, jobsTreeRevision)`
captured at fetch time. Revision equality alone is both too weak and too
strong (`protocol/reducer.ts`, `stores/threads.ts`):

- `evener/job/started` and `evener/job/finished` bump `jobsUpdatedAt` and leave
  `jobsTreeRevision` untouched, so an equality test on `jobsTreeRevision` would
  accept a tree that a job lifecycle already invalidated.
- Hydration sets both fields to `null`, and `threadsStore.listJobs()` updates
  neither, so a tree fetched before hydration would never satisfy
  `tree.revision === model.jobsTreeRevision` and would be rejected forever.

Rules:

- Reuse a retained tree only when the model's `jobsUpdatedAt` and connection
  generation equal the token's, and, when both `jobsTreeRevision` and the
  tree's `revision` are non-null, those two are equal as well.
- A null `jobsUpdatedAt` on the model means freshness is unknown. Unknown is
  not fresh: mark the entry stale and make it eligible for a refetch under the
  lazy-fetch rules, rather than accepting or perpetually rejecting it.
- `evener/jobs/treeUpdated` bumps `jobsTreeRevision` (monotonic, ignoring
  non-increasing revisions) and `jobsUpdatedAt`. Invalidations never refresh;
  they only mark state for the next fetch.

### Refresh ownership and reconnect

- The index owns its fetches. It never depends on the activity panel to fetch
  for it, though it may read the panel's retained tree when its freshness token
  matches.
- Every fetch carries a monotonic request ID; a response is applied only if it
  is still the newest for its ref and the connection generation is unchanged.
  This mirrors the existing request fencing in `stores/activitySummary.ts`.
- One in-flight request per ref; a second request for the same ref joins it
  rather than issuing a duplicate.
- Joining must not swallow a newer invalidation. Each fetch records the
  invalidation token it was issued for; if a newer token arrives while it is in
  flight, the entry is marked stale and exactly one follow-up fetch is queued
  and issued after settlement. This mirrors `activitySummary.ts`'s `pendingBump`
  drain and prevents an old response from publishing as newest after a job
  finished mid-fetch.
- On a connection generation change or reconnect, drop cached **foreign**
  trees and re-resolve. Foreign state must never be served across a generation
  boundary. Current-session state rehydrates through the normal thread path.

### Freshness: current session vs. foreign

- **Current session** entities update live: `jobsUpdatedAt`/`jobsTreeRevision`
  bumps and `delegates[]` patches flow through the reducer, so a card for a
  current-session entity is current whenever its token matches.
- **Foreign** entities have no live push. They are fetched on demand, carry a
  TTL, and are cleared on reconnect or generation change. A foreign card shows
  its fetch time and is labeled possibly stale.
- When `jobsUpdatedAt` is null (pre-hydration), the entry is treated as stale
  and re-resolved once the model hydrates, per "Freshness tokens".

## Normalized summary

```ts
type EntitySummary =
  | { kind: "job"; id: string; label: string; state: JobState; open?: OpenTarget }
  | { kind: "delegate"; id: string; label: string; state: DelegateState; open?: OpenTarget }
  | { kind: "watch"; id: string; label: string; state: WatchState; open?: undefined };
```

- `JobState`: status, type, description/command, started/ended, exit code,
  output bytes, live flag.
- `DelegateState`: lifecycle status, outcome, mandate (clamped), agent type,
  model, run start/end, quiet age, usage, resumable/attention.
- `WatchState`: one of watching, pending, missing, ended, cleared, or terminal
  catch-up; condition, deliveries, source, and a last-known snapshot flag.
- `OpenTarget`: `{ ref: string; parentRef?: string }` — exactly what
  `openTranscript` takes.

State and navigation are separate. A job or delegate with resolved state but
no verified open target still renders a hover card and plain (unlinked) text;
`open` is optional for that reason. A watch never has an open target.

## Rendering

### `EntityRef`

`<EntityRef id={…} />` is the shared component for structured fields. It has
exactly three rendering branches, and every surface uses whichever applies:

1. **Unresolved** (invalid, unknown, or failed lookup): plain text. No card,
   no link, no button.
2. **Resolved, no open target** (a job or delegate without a verified owner,
   and every watch): information-only. The id text is a focusable trigger with
   a hover card and no open action.
3. **Resolved with an open target** (job or delegate with a verified owner):
   the id text is a button that opens, followed by the standard `OpenButton`.

Accessible names stay specific ("Open job log", "Open delegate transcript"),
matching the `OpenButton` contract.

### Hover card

A new `widgets/hovercard` primitive, built on the same show/hide and
`computeTooltipPosition` mechanics as `widgets/tooltip` but rendering rich
children. It shows on hover and focus after the same delay, hides on
leave/blur/scroll/resize, portals to `document.body`, uses `role="tooltip"`,
and is non-interactive (no focus trap).

Correction to the outline: this is **not** `Popover`. `Popover` traps focus
and closes on outside click, which is wrong for a hover affordance. The
mechanics come from `Tooltip`; only the content is richer.

The card body is per kind:

- **job** — status, type, command/description, start or duration, exit code,
  output size.
- **delegate** — status chip, mandate first line, agent/model, duration or
  quiet age, usage, child ref.
- **watch** — watching/ended, condition sentence, deliveries, source, and a
  "last known" caveat.

### Watch snapshot ordering

A session's loaded turns can contain several `job_watch` results describing
the same watch. Order them by the transcript's own position — turn order, then
item order — and let the latest snapshot win regardless of load order, so
paging older history in never resurrects an older watching state over a newer
ended one.

Normalization must be **operation-aware**, because the four result shapes carry
different fields (`agent/session_tools_jobs.go`, mirrored by `jobWatch.tsx`):

- **create:** `watching:true`, `source`, and structured trigger fields
  (`output_match`, `after_seconds`, `repeat_seconds`, `progress_interval_ms`,
  `events`, `event_filter`), plus `note`, `watch_id`, and the
  `replaced_existing`/`fired` markers. A terminal catch-up is a create mode
  carrying `terminal_catchup:true`, `watching:false`, and `status`.
- **clear:** `watching:false` with `watch_id` and a `replaced_existing` or
  `fired` marker. It carries no `end_reason`.
- **inspect:** `watching`, `source`, `condition`, `note`, `end_reason`,
  `deliveries`, `created_at`. A miss is `{watch_id, watching:false}` with no
  other marker.
- **list:** `watches[]` / `recent_watches[]` of inspect-shaped rows.

Classification uses the operation and the payload, not a single `end_reason`
test. The display state is one of: watching, pending, missing, ended, cleared,
terminal catch-up.

Field presence is preserved before merging: an absent field stays absent and
never clears a present one, and an absent `watching` is not coerced to `false`.
The existing `normalizeRow` does coerce it and reads only inspect/list
`condition`/`end_reason`, so it is insufficient as-is.

Fold rules:

- The latest positional snapshot wins; older snapshots never overwrite newer
  ones.
- Present fields overwrite; absent fields do not.
- A later clear, terminal catch-up, or ended inspect wins over an earlier
  create.
- A later create carrying `replaced_existing` wins over an earlier create.
- The fold is deterministic and idempotent for equal positions.

Extract this to `src/protocol/watchRows.ts` and use it from both
`jobWatch.tsx` and `entityIndex`. The renderer's visible behavior must not
change; the extraction is verified by `jobWatch`'s existing tests.

### Prose enhancement (agent Markdown)

`EntityText.tsx` owns the prose integration in two forms:
`useEntityTextEnhancement(rootRef, deps)` walks a rendered Markdown root, and
`<EntityText text={…} />` splits a plain string directly.

For Markdown roots, after the sanitized DOM settles:

1. Walk text nodes under the Markdown root with a `TreeWalker`.
2. Skip any node inside `code`, `pre`, `script`, `style`, or an existing `<a>`.
3. Skip nodes under elements this enhancer previously mounted (marked with a
   data attribute) so repeated passes are idempotent.
4. For each match, split the text node and insert a portal host rendering
   `<EntityRef>`.
5. Record the original text of every split node. On teardown, restore the
   original text and remove the inserted hosts before disconnecting, so an
   effect replay with an unchanged source leaves the prose byte-identical and
   never drops the id it replaced.
6. Rebind on every `source`/`live` change, matching the file-link effect.

This is the riskiest part. The file-link precedent inserts a sibling after an
existing anchor; text-node splitting is more invasive, and a streaming message
re-renders its DOM repeatedly. Preservation, not just duplication, is the
required contract.

### User message prose

`UserMessageItem` renders plain text in a div, not Markdown, so it has no code
semantics to skip and needs no DOM walk. Render its text through
`<EntityText text={…} />`, which splits the string on detected ids and renders
`<EntityRef>` inline.

This is the accepted exception to the literal-surface rule: a pasted raw log or
JSON blob in a user message links ids inside it. A plain-text div gives the
client no way to tell pasted output from prose, and guessing would be worse
than a consistent rule.

### Structured id fields

Use `<EntityRef>` directly, no DOM walking:

- `tools/jobTools.tsx` — `job_list` row identities.
- `tools/delegateStatus.tsx` — the delegate status header id.
- `tools/jobWatch.tsx` — watch row ids, passing the full id as the resolution
  key and the clipped id as display text.

Optional extensions, same component: the `job_status` / `job_stop` summaries in
`jobTools.tsx`.

## Open targets

| Id | Ref | parentRef |
| --- | --- | --- |
| job | `ActivityJob.transcriptRef` when known, else `job:<jobId>` | verified owner only (below) |
| delegate | `delegate.transcriptRef` (child session ref) | the session ref whose tree/model supplied the delegate |
| watch | none | — |

Job navigation must use a verified owner. In order:

1. `ActivityJob.ownerRef` from a tree record (authoritative).
2. Otherwise the owner derived from the id, as `local:<ownerSessionID>`.
3. The current session ref **only** when it equals the job's encoded owner.

The previous draft put the current session before the encoded owner. That was
wrong: `JobLog` fetches output through `parentRef`, so a foreign job linked
with the current session would read another job's log. When no verified owner
is available, the job renders an unlinked id with its hover card.

Jobs and delegates route through `openTranscript(ref, parentRef)`, which
already handles beside placement, pane dedup, and navigation context.

## Lazy fetch, caching, errors

- **Discovery.** An unresolved job or delegate id on screen makes the current
  session's tree eligible to load, because delegate ids carry no owner and
  nested delegates are otherwise undiscoverable. Job ids additionally derive a
  foreign owner (`local:<ownerSessionID>`).
- **Refresh.** A resolved summary becomes eligible again when its freshness
  token changes, its foreign TTL expires, or its negative-cache TTL expires.
- **Dedup.** One in-flight request per ref; concurrent requests join it, and a
  newer invalidation queues one follow-up fetch (see "Refresh ownership").
- **Concurrency.** A global cap (start at 4) bounds requests across distinct
  owner refs, with excess requests queued. Per-ref dedup alone does not bound a
  transcript full of distinct owner ids.
- **Cache.** Bounded LRU keyed by `ref` + freshness token. A foreign entry is
  valid until evicted, its TTL expires, or the connection generation changes.
- **Cancellation.** A fetch whose surface unmounts or is no longer visible is
  abandoned and its result is not published. It may seed the cache only if its
  token is still current.
- **Negative cache.** A miss is remembered briefly (short TTL) and evicted
  under memory pressure, so a page full of unknown ids cannot produce a
  request per render.
- **Errors.** A disconnected, unsupported, malformed, or failed lookup renders
  no card and no link. It never surfaces a transcript-level error, and it
  never retries in a loop: retry happens only after the connection store
  returns to ready. This matches the silent-failure precedent in
  `ActivityRowDetail`'s `JobOutputPreview`.

## Activity tree truncation

Activity trees support truncation and continuation (`ActivityBranchState`).
The entity index does not paginate for a hover card: if the current tree is
truncated and an id is not present, the id stays unresolved. A foreign
owner fetch reads at most one page. This bounds work and avoids a request
storm from a transcript full of old ids.

## Accessibility and interaction

- **Branch 3 (navigable job or delegate).** The id button opens on
  Enter/Space. Both the id button and the `OpenButton` are focus targets and
  each carries `aria-describedby` pointing at the hover card while it is
  shown. Do not rely on `Tooltip`'s single-child `cloneElement`; `EntityRef`
  wires the association onto both controls explicitly.
- **Branch 2 (information-only job, delegate, or watch).** The trigger is
  focusable and non-opening: a span with `tabindex="0"` and
  `aria-describedby` pointing at the card. Focus reveals the description, as
  `Tooltip` already does for its own triggers, and introduces no open action.
- Hidden on touch, where there is no hover, via the same CSS gate `Tooltip`
  uses.

## Failure modes

- Unresolvable id (foreign delegate, watch with no loaded `job_watch` call,
  unknown owner): plain text, no link, no card.
- Malformed or server-invalid id: not detected at all.
- Job with resolved state but no verified owner: hover card, unlinked text.
- Lookup failure: no card, no link, no error surface, no retry loop.
- Truncated tree without the id: unresolved until a later refresh includes it.

## Implementation sequence and acceptance

Each stage is reviewable on its own, in dependency order. The invasive Markdown
enhancement is last and lands as its own change.

**Stage 1 — identity (`src/protocol/entityIds.ts` + test).**
Acceptance: valid ids for all three kinds; wrong lengths; bad alphabet; decoded
payload over 128 bits; payload that is not UUIDv7 (wrong version or variant);
boundary cases (`xjob_…`, `job_…extra`, trailing `_`); multiple matches; no
matches.

**Stage 2 — watch rows and index (`src/protocol/watchRows.ts`,
`src/stores/entityIndex.ts` + tests).**
Acceptance: operation-aware watch normalization for create, clear, inspect, and
list, including terminal catch-up and the inspect-miss shape; field presence
preserved (absent `watching` stays absent); job, delegate, and watch resolution
from fixtures; delegate reconciliation when the tree record lacks a
`projectionRevision`; retained tree accepted only on a matching freshness
token, with a null `jobsUpdatedAt` treated as stale; an invalidation arriving
during an outstanding fetch queues exactly one follow-up; cross-session job
owner derivation; watch ordering by turn and item position, including
older-history paging; request dedup, the global concurrency cap, unmount
cancellation, and stale-response rejection; negative-cache behavior;
`OpenTarget` values, including the no-verified-owner case.

**Stage 3 — shared interaction (`src/widgets/hovercard/index.tsx`,
`src/panes/session/transcript/EntityRef.tsx` + tests).**
Acceptance: the three branches render as specified: unresolved plain text;
resolved-without-owner information-only (focusable, card, no button); resolved
job/delegate link plus `OpenButton` that opens the pane (assert against
`workspaceStore`, as `agentFileLinks.test.tsx` does). `aria-describedby` lands
on both branch-3 controls and on every branch-2 trigger; the card shows/hides
on hover/focus/leave/blur and portals.

**Stage 4 — structured fields (`tools/jobTools.tsx`,
`tools/delegateStatus.tsx`, `tools/jobWatch.tsx`).**
Acceptance: `jobTools` and `delegateStatus` render a branch-3 link when an open
target exists and a branch-2 trigger otherwise, resolving the right entity;
`jobWatch` rows are branch 2 only (never linked) and use the full id as the
resolution key with the clipped id as display text. `jobWatch`'s existing tests
still pass after the normalization extraction.

**Stage 5 — prose enhancement, separate change (`EntityText.tsx`, then
`messages/AgentMarkdown.tsx` and `messages/UserMessageItem.tsx`).**
Acceptance: an id in agent Markdown links; an id in inline or fenced code, or
in an existing link, does not; stream-then-settle keeps one affordance per id;
an effect replay with unchanged source preserves visible text byte-for-byte;
unresolved-to-resolved transitions add the affordance without altering
surrounding text; file-link affordances still work alongside entity links;
user message prose links.

## Gates

Run before the frontend gate, per `AGENTS.md`:

- `npx biome check --write` on touched files under `src/`.
- `make test-web` (unit + typecheck + Biome).
- `make test-web-browser` on a Chrome-capable host.

Follow TDD. Do not mock resolution logic; use real fixtures and the real
stores. Default tests stay deterministic.

## Follow-ups

- Loosen job suffix detection only if real ids diverge from 12 characters, and
  only after establishing backend resolvability — treat it as a compatibility
  decision, not a parser tweak.
- Extend to reasoning and notice text with the same enhancer.
- Add watch state to the AppWire surface for live watch cards and a real open
  target, if watches earn a pane.
- Consider whether the activity tree should expose watches, which would remove
  the `job_watch`-payload dependency.
