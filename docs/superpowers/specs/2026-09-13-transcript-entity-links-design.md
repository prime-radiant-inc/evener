# Transcript Entity Links

## Status

Approved in outline. Scope locked with Jesse on 2026-09-13, revised after two
design reviews (roborev jobs 9855 and 9876) and a simplify pass over four
angles (reuse, simplification, efficiency, altitude):

- **Entities:** shell jobs (`job_…`) and stable delegates (`dlg_…`) get a link
  plus a hover card; evener watches (`watch_…`) get a hover card only.
- **Resolution:** client-side, from the current session's already-loaded
  state. No backend, no AppWire change, no cross-session index.
- **Surfaces:** message prose and structured id fields. Identified literal
  surfaces (fenced and inline code, raw job output, ANSI logs, JSON, copy
  targets) are never rewritten. Plain user-message text is the one accepted
  exception.

Frontend-only, under `cmd/evener-hub/frontend/`.

## Evidence and problem

Entity ids already appear in transcripts as inert text. The client can already
open most of what they name, and it already knows how to attach an open
affordance to prose: `AgentMarkdown` post-processes sanitized Markdown and
hangs an `OpenButton` on file links. Entity ids get none of that.

The pieces exist and are disconnected:

- `widgets/openbutton` is the standard affordance (box-arrow glyph, tooltip
  "Open").
- `panes/session/transcript/openTranscript.tsx` opens a read-only transcript
  pane beside; the `transcript` pane renders a `job:<id>` ref as a job log and
  a session ref as a thread transcript.
- `protocol/activityList.ts` and `stores/activityPanel.ts` already own per-ref
  `evener/jobs/list` requests, dedup, invalidation, parsing, and a retained
  tree.
- `protocol/activityRows.ts` already normalizes the tree into
  `ActivityJobRow`/`ActivityDelegateRow` (with `transcriptRef`/`parentRef`),
  and `ThreadModel.delegates[]` carries live delegate projections.
- `widgets/tooltip` is the floating-label primitive; `AgentMarkdown.tsx:42-74`
  is the prose-enhancement precedent.

Nothing connects an id found in text to any of this.

## Goals

- Detect `job_`, `dlg_`, and `watch_` ids in transcript prose and structured id
  fields, using the identity rules the server mints and validates.
- Link job and delegate ids to the referenced entity beside, through the
  existing `OpenTranscriptButton`/`openTranscript` path, when a verified open
  target exists.
- Show a hover card on every resolvable id, summarizing state the client
  already holds.
- Leave unresolvable ids as plain text.
- Never rewrite identified literal surfaces. Plain user-message text is the
  documented exception.

## Non-goals

- No Go, AppWire, or daemon changes.
- No cross-session resolution. An id that names an entity outside the current
  session stays plain text. This is what makes the client-side-only choice
  coherent.
- No link affordance on watches; they get a hover card only.
- No linking of session ids, `ag_`, `att_`, `call_`, `wg_`, `wd_`, `plugin_`,
  or `item_` ids.
- No linkification inside fenced or inline code, raw tool output, ANSI logs,
  JSON, or copy/export strings. Plain user-message text is exempt because a
  plain-text div gives the client no way to classify it.
- No new cache, request engine, pane type, or floating-layer lifecycle. The
  design reuses what exists.
- No change to reasoning text (`ThinkBlock`) or notice text (`SystemNoticeItem`,
  `NotificationCard`) in this delivery.

## Entity identity

| Kind | Shape | Length |
| --- | --- | --- |
| job | `job_` + 22 base62 owner session id + `_` + 12 base62 | 39 |
| delegate | `dlg_` + 22 base62 UUIDv7 payload | 26 |
| watch | `watch_` + 22 base62 UUIDv7 payload | 28 |

Base62 is `0-9A-Za-z` (`identifier/uuid.go`). Ids are case-sensitive.

`entityIds.ts` mirrors `identifier.ValidateJobID`, `ValidateDelegateID`, and
`ValidateWatchID`, including the UUID payload checks a length-and-alphabet test
misses: base62 decode to at most 128 bits, version nibble `7`, and RFC4122
variant bits. The job owner portion is a session id and gets the same checks;
its 12-character suffix is base62 only.

This module is genuinely new (no client-side validator exists; `types.gen.ts`
is types only), so it is pinned to `identifier/*.go` and its tests use golden
vectors generated from the Go package. Its closest client precedent is
`steeringClassify.isValidTranscriptRef`, which mirrors `appwire/refs.go`.

A candidate matches only at its exact length, and neither the character before
nor after may be `[0-9A-Za-z_]`. `findEntityIds(text)` returns
`{ kind, id, start, end }[]`. Detection prefilters on prefix and length before
decoding, so a text node costs one scan and no allocation per non-match.

Relaxing the job suffix to 1-12 base62 is out of scope; it is a compatibility
decision that must first establish backend resolvability.

## Resolution

Resolution is a derived view over state the current session already has. It
adds no fetch engine, no LRU/TTL/negative-cache layer, and no concurrency cap.

### Sources

1. **`ThreadModel.delegates[]`** (live `EvenerDelegateInfo`) for delegates.
2. **The retained activity tree** (`retainedActivityTree` in
   `stores/activityPanel.ts`) for jobs and nested delegates. When the current
   session has no retained tree and a job id needs one, issue one coalesced
   `threadsStore.listJobs(ref)` and memoize the parsed tree for that ref until
   its `(hydrations, jobsUpdatedAt)` pair changes. That single memo is the
   whole cache.
3. **`job_watch` payloads in loaded turns** for watches. There is no watch
   field anywhere else on the wire.

A retained tree is reused only while it matches the current
`(threadsStore.hydrations.get(ref), model.jobsUpdatedAt)`. Those are the same
two signals the activity panel already uses (`ActivityPanel.tsx:74-97`); the
design adds no third freshness concept.

### id to entity map

Build the map by reusing `buildActivityRows` (which already yields jobs and
delegates with `transcriptRef` and `parentRef`) over `activityNodeID`. Export a
small flatten helper from the existing activity modules if one is needed. Do
not write a second tree walker.

### State

Cards consume the already-normalized `ActivityJobRow` / `ActivityDelegateRow`
and the existing formatters and classifiers: `activityDelegateState`,
`classifyJobStatus`, `stableDelegateDisplayStatus`, `isActivityFailure`,
`formatElapsed`, `formatClockTime`, `formatQuietAge`, `quietAnchorMillis`,
`formatUsagePair`, `formatByteCount`, `clipJobID`. Do not define new
`JobState`/`DelegateState` shapes or re-derive any of these.

### Delegate precedence

Prefer the live `ThreadModel.delegates[]` record when present; else the tree
record. The live projection is the controller fold, so a revision comparison
earns nothing.

## Rendering

### `EntityRef`

The id text is always rendered the same way: a focusable, non-opening trigger
with the hover card. The only variable is whether a verified open target
exists:

- **Unresolved:** plain text. No trigger, no card, no control.
- **Resolved:** the trigger plus its card.
- **Resolved with an open target:** the above, plus the standard `OpenButton`
  as the sole open control. The id text is never a second button for the same
  action.

### Hover card

Reuse `Tooltip`. Extract its show/hide lifecycle (delay timer, scroll/resize
dismiss, portal, `ResizeObserver` re-measure, touch gate) from
`widgets/tooltip/index.tsx` into a shared hook that both `Tooltip` and the
card use, and reuse `computeTooltipPosition`. Do not build a second floating
lifecycle, and do not use `Popover` (it traps focus and closes on outside
click).

The card is non-interactive, `role="tooltip"`, associated by
`aria-describedby`. Body per kind, built from the reused row shapes:

- **job:** status, type, command/description, start or duration, exit code,
  output size.
- **delegate:** status, mandate first line, agent/model, duration or quiet age,
  usage.
- **watch:** state, condition sentence, deliveries, source, marked last-known.

### Watch normalization

Extract `jobWatch.tsx`'s row parsing and the text/state helpers the card needs
(`sourceLabel`, `humanizeSeconds`, `humanizeInterval`, `conditionSpec`,
`parseConditionText`) into `protocol/watchRows.ts`; both `jobWatch.tsx` and the
card import them. Nothing re-derives them.

Normalization stays operation-aware, because the result shapes differ: create
carries structured trigger fields; clear and terminal catch-up carry no
`end_reason`; inspect carries `condition`/`end_reason`; list nests
inspect-shaped rows. Field presence is preserved (an absent `watching` is not
coerced to `false`; an absent field never clears a present one).

Snapshots of the same watch are ordered by transcript position (turn, then
item), latest wins regardless of load order, and a later clear, terminal
catch-up, or ended inspect wins over an earlier create.

### Prose enhancement (agent Markdown)

`useEntityTextEnhancement(rootRef, deps)` reuses the `AgentMarkdown` seam: walk
text nodes with a `TreeWalker`, skip `code`/`pre`/`script`/`style`/existing
`<a>` and previously mounted hosts, split matched text nodes, and mount
`<EntityRef>` portals. Record each split node's original text and restore it on
teardown, so an effect replay with an unchanged source leaves the prose
byte-identical. Rebind on `source`/`live` changes.

This is the riskiest part. Preservation, not just duplication, is the contract.

### User message prose

`UserMessageItem` renders a plain-text div, so no DOM walk is needed. Its
`<EntityText text={…} />` string form follows the existing
`webTools.linkifyLine` matchAll-and-cursor idiom rather than inventing a third
splitter.

This is the accepted exception to the literal-surface rule: pasted output in a
user message links ids. Guessing at content classification would be worse than
a consistent rule.

### Structured id fields

- `tools/jobTools.tsx` `job_list` rows: `<EntityRef>` per identity.
- `tools/jobWatch.tsx` rows: `<EntityRef>` (resolved-without-target; watches
  never link), full id as key, clipped id as text.
- `tools/delegateStatus.tsx`: the card already has an `OpenTranscriptButton`
  footer. Reuse that single control; do not add a second open affordance on the
  header id. The header may carry the hover card trigger only.

Open actions route through the existing `openTranscript` /
`OpenTranscriptButton` path, and where a tool renderer already declares an open
target (`openTranscriptRef` / `openTranscriptInline`, consumed by
`ToolCallItem`), reuse that descriptor rather than adding a parallel path.

## Open targets

| Id | Ref | parentRef |
| --- | --- | --- |
| job | the tree row's `transcriptRef` when known, else `job:<jobId>` | the tree row's `ownerRef` |
| delegate | the delegate's `transcriptRef` | the current session ref |
| watch | none | — |

Job navigation requires an owner from the retained tree. A job whose owner is
not the current session is outside the client-side scope and stays plain text.
`JobLog` fetches output through `parentRef`, so a guessed owner is worse than
no link.

## Failure modes

- Unresolvable id (no delegate projection, no tree, no loaded `job_watch`):
  plain text, no link, no card.
- Malformed or server-invalid id: not detected.
- Job with state but no tree row: no link; card only if the tree supplied it.
- Lookup or parse failure: no card, no link, no error surface, no retry loop.
- Truncated tree without the id: unresolved.

## Accessibility

- The id trigger is focusable (`tabindex="0"`) and non-opening, with
  `aria-describedby` pointing at the card while shown; focus reveals the
  description, as `Tooltip` triggers already do.
- On a navigable entity, the `OpenButton` is the single focusable open
  control; it also carries `aria-describedby`. Do not rely on `Tooltip`'s
  single-child `cloneElement`; wire the association explicitly.
- Hidden on touch, via the same CSS gate `Tooltip` uses.

## Implementation sequence and acceptance

Each stage is reviewable alone; the Markdown enhancement lands separately.

**Stage 1 — identity (`protocol/entityIds.ts` + test).**
Acceptance: valid ids for all three kinds; wrong lengths; bad alphabet; decode
over 128 bits; wrong UUIDv7 version or variant; boundaries (`xjob_…`,
`job_…extra`, trailing `_`); multiple matches and none; golden vectors match
the Go identifier package.

**Stage 2 — resolution (`protocol/watchRows.ts`, `protocol/entityView.ts` +
tests).**
Acceptance: job, delegate, and watch resolution from loaded fixtures; delegate
preference for the live projection; retained tree gated on
`(hydrations, jobsUpdatedAt)`; one coalesced `listJobs` per ref; operation-aware
watch normalization with field presence preserved; positional ordering with
older-history paging; no cross-session resolution. `jobWatch`'s existing tests
still pass after the extraction.

**Stage 3 — shared interaction (`panes/session/transcript/EntityRef.tsx`,
`panes/session/transcript/EntityText.tsx`, `widgets/hovercard/index.tsx`, the
extracted Tooltip lifecycle hook + tests).**
Acceptance: unresolved plain text; resolved trigger plus card; navigable entity
adds exactly one `OpenButton`; `aria-describedby` on trigger and control; card
shows/hides on hover/focus/leave/blur and portals; `Tooltip`'s existing tests
still pass after the lifecycle extraction.

**Stage 4 — structured fields (`tools/jobTools.tsx`, `tools/jobWatch.tsx`,
`tools/delegateStatus.tsx`).**
Acceptance: `job_list` and `jobWatch` rows resolve through the shared view;
`delegateStatus` keeps its single footer control; no renderer gains a second
open affordance.

**Stage 5 — prose enhancement, separate change (`messages/AgentMarkdown.tsx`,
`messages/UserMessageItem.tsx`).**
Acceptance: an agent-Markdown id links; an id in inline or fenced code, or in
an existing link, does not; stream-then-settle keeps one affordance per id;
effect replay with unchanged source preserves visible text byte-for-byte;
unresolved-to-resolved adds the affordance without altering surrounding text;
file links still work; user-message text links via the string form.

## Gates

Per `AGENTS.md`: `npx biome check --write` on touched files, `make test-web`,
and `make test-web-browser` on a Chrome-capable host. Follow TDD; use real
fixtures and real stores; no mocked resolution logic.

## Follow-ups

- Cross-session job/delegate resolution, if it ever earns a backend lookup.
- Watch state on the AppWire surface, which would remove the `job_watch`
  dependency and allow a real open target.
- Shared golden-vector generation wired into both the Go and TS test suites.
- Extend the enhancer to reasoning and notice text.
