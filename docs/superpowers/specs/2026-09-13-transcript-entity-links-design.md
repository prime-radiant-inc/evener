# Transcript Entity Links

## Status

Approved in outline. Scope locked with Jesse on 2026-09-13, revised through five
design-review passes (roborev jobs 9855, 9876, 9885, 9890, 9919) and a simplify
pass over four angles (reuse, simplification, efficiency, altitude):

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
- `stores/activitySummary.ts` coordinates per-ref `evener/jobs/list` requests
  (dedup, request fencing, queued follow-up) and publishes into
  `stores/activityPanel.ts`, which retains the parsed tree.
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
- No resolution outside the current session's loaded activity tree. Scope is
  that tree (recursive: nested delegates and child-owned jobs), the live
  `ThreadModel.delegates[]`, and loaded `job_watch` payloads. An id naming
  anything else stays plain text. This is what makes the client-side-only
  choice coherent.
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
adds no fetch engine, no cache layer, and no concurrency policy.

### Ownership and initial discovery

The view is a read-only derived consumer of `activityPanelStore`'s retained
tree, `ThreadModel.delegates[]`, and the loaded turns. It adds no second cache
and no freshness value of its own.

Initial discovery is an explicit opt-in at live-session chrome mounts.
`SessionChrome.discoverActivity` defaults off and is forwarded to the hidden
`ActivityPanel` as `discoverWhenHidden`; the live composer mount and the local
notLoaded/send-disabled menu fallback opt in. `useActivityRefresh` shares the
complete freshness effect between that background owner and `ActivityPanelBody`:
while a body is mounted it owns freshness, and a hidden trigger without
`refreshWhenHidden` remains suppressed. An opted-in chrome owner may establish
an unestablished summary, so its first retained tree is available for job
resolution without requiring the Activity sheet to have been opened. Isolated
or read-only consumers retain the default-off behavior and do not initiate
initial discovery.

This ownership intentionally follows the chrome that is actually mounted. A
freshly opened saved notLoaded/send-enabled session does not mount composer
chrome until its follow-up composer is engaged, so job ids may remain unresolved
until that engagement. The menu-only Session fallback covers the corresponding
local notLoaded/send-disabled case; universal saved-session discovery is not the
shipped scope.

Forced refreshes are skipped while the summary is loading. This keeps
co-mounted owners from queuing a duplicate forced follow-up after the initial
request completes.

Read-only panes: a read-only `transcript` pane initiates no loading, but
`activityPanelStore` is keyed by ref, so it consumes any tree already retained
for that ref (for example while the owning session pane is open). Its
resolution then matches the session pane's.

### Sources

1. **`ThreadModel.delegates[]`** (live `EvenerDelegateInfo`) for delegates.
2. **The retained tree**
   (`retainedActivityTree(activityPanelStore.entries.get(ref))`) for jobs and
   nested delegates. It is a raw `ActivityTree`, not a disclosure-filtered row
   list.
3. **`job_watch` payloads in loaded turns** for watches. There is no watch
   field anywhere else on the wire.

### id to entity map

Index the raw tree, not `buildActivityRows`: that builder takes `expandedFolds`
and collapses inactive entries behind fold rows, so it would leave completed
jobs and delegates unresolvable and would change transcript links whenever the
user opened an activity fold.

Write one `indexActivityEntities(tree)` that walks every loaded entry (no fold
logic) and reuses `activityNodeID`. Factor the `transcriptRef`/`parentRef`
derivation out of `buildActivityRows` into a shared function so the index and
the panel build identical row fields. Resolution must be identical before and
after a fold change.

### Derivation ownership

`TranscriptBody` is the shared owner: it calls `useEntityView` once and places
the resulting `entities` map in its transcript render context. Every
`EntityRef` in that body reads the same map; inline references neither subscribe
to the source stores nor flatten the tree. A separately mounted
`TranscriptBody` owns its own derivation. The map is memoized over the session
ref, retained tree, delegate projection, watch-fold key, and stale/ended state.

The watch fold keys on its actual inputs, not on the turns array: `ThreadModel`
has no turns version, and agent prose deltas replace that array, so keying on it
would re-fold every loaded `job_watch` result on each delta. Key on the ordered
watch-relevant inputs: each `job_watch` item's position, arguments, output,
error, completion state, and parsed raw payload content. The raw value is
serialized into the key so equal content remains stable across object identity
changes. Prose deltas do not change those; tool completion, output or raw data
merged during paging, and full-snapshot replacement do, which is correct. Ids
plus completion state alone are not sufficient, because a snapshot or a paging
merge can enrich a completed item without changing either.

### State

Cards consume the already-normalized `ActivityJobRow` / `ActivityDelegateRow`
and the existing formatters and classifiers: `activityDelegateState`,
`classifyJobStatus`, `stableDelegateDisplayStatus`, `isActivityFailure`,
`formatElapsed`, `formatClockTime`, `formatQuietAge`, `quietAnchorMillis`,
`formatUsagePair`, `formatByteCount`, `clipJobID`. Do not define new
`JobState`/`DelegateState` shapes or re-derive any of these.

### Delegate selection

A delegate may resolve from the live `delegates[]` array alone, with no tree
row. That is a first-class branch, not a failure: because
`EvenerDelegateInfo.transcriptRef` is required, a live-only delegate both earns
a card and navigates. Its card normalizes through the existing helpers
(`stableDelegateDisplayStatus`, `classifyJobStatus`), since an
`EvenerDelegateInfo` is not an `ActivityDelegateRow`.

When both sources have the delegate, selection is revision-aware, because a
tree response can carry a higher `projectionRevision` than the live array
(which nothing here updates):

- both numeric: higher wins, live record on a tie;
- tree record lacks `projectionRevision`: live record;
- no live record: tree record.

`ActivityDelegate.projectionRevision` is optional;
`EvenerDelegateInfo.projectionRevision` is required.

## Rendering

### `EntityRef`

By default the id text is rendered as a focusable, non-opening trigger with the
hover card. Whether a verified open target exists determines the standard
branches:

- **Unresolved:** plain text. No trigger, no card, no control.
- **Resolved:** the trigger plus its card.
- **Resolved with an open target:** the above, plus the standard `OpenButton`
  as the sole open control. The id text is never a second button for the same
  action.

`EntityRef` also has two composition props. `triggerOnly` keeps the trigger and
card but suppresses its `OpenButton` when the structured surface already owns
the sole open control. `embedded` deliberately omits the trigger's tab stop when
the id sits inside the watch row's disclosure button: that surrounding button is
the focusable control, and its expanded detail carries the same information.

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

Open targets come from the activity row contract, the same fields
`ActivityTree`'s own `OpenTranscriptButton` already uses (`transcriptTarget(row)`
and `row.parentRef`):

| Id | Ref | parentRef |
| --- | --- | --- |
| job | `row.transcriptRef` when present, else `job:<jobId>` | `row.parentRef` |
| delegate | `row.transcriptRef` | `row.parentRef` |
| watch | none | — |

Field notes: `ActivityJobRow.parentRef` is required and its `transcriptRef` is
optional, with ownership on `row.job.ownerRef`. `ActivityDelegateRow.transcriptRef`
and `.parentRef` are both required, and the row's `transcriptRef` is normalized
from `delegate.childRef` (the underlying `ActivityDelegate.transcriptRef` is
optional). Use the row fields, never the underlying record's optional ones.

Only rows from the current session's loaded tree are eligible. `JobLog` fetches
output through `parentRef`; a guessed owner is worse than no link, so a **job**
id with no eligible row stays plain text. That rule is jobs-only: a delegate
resolves and navigates from a live `delegates[]` record without any tree row.

## Failure modes

- Unresolvable id (no delegate projection, no tree, no loaded `job_watch`):
  plain text, no link, no card.
- Malformed or server-invalid id: not detected.
- Job id with no eligible tree row: plain text, no link, no card. A job's state
  and target both come from the row.
- No tree yet (initial state): job entities are unresolved; delegates and
  watches still resolve from their own sources.
- Refresh failure with a tree already retained: `activityPanelStore` keeps it as
  `{kind: "ready", tree, staleError}` and `retainedActivityTree` still returns
  it, so already-shown cards remain and are marked stale from `staleError`. A
  first-load failure leaves no tree and no card.
- A retained `ended` tree carries no `staleError`. Treat it as ended, not
  current: previously running activity must not read as live.
- Staleness needs its own subscription. `staleError` can change while the tree
  and delegates are byte-identical, so the index key cannot carry it; a
  separate selector reads the load state for the indicator.
- Malformed or unparseable payload: no card, no link, no error surface.
- Truncated tree without the id: unresolved.

## Accessibility

- The id trigger is focusable (`tabindex="0"`) and non-opening, with
  `aria-describedby` pointing at the card while shown; focus reveals the
  description, as `Tooltip` triggers already do. The `embedded` watch-row
  exception is out of the tab order because the surrounding disclosure button
  owns focus and its expanded detail carries the same information.
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

**Stage 2 — resolution (`protocol/activityRows.ts` gains
`indexActivityEntities`, `protocol/watchRows.ts`, `protocol/entityView.ts` +
tests).**
Acceptance: resolution from loaded fixtures for all three kinds; resolution
identical before and after a fold change (disclosure-independent index);
revision-aware delegate selection including the tree-lacks-revision case; the
live-only delegate branch yields a card entity and an open target from
`EvenerDelegateInfo`; one shared `entities` map per `TranscriptBody`, consumed
by every `EntityRef` in that body without leaf store subscriptions; the watch
fold keys on ordered watch-relevant inputs, including raw payload content, and
does not re-run on prose-only deltas; retained refresh failure yields stale
metadata and a retained `ended` tree yields ended metadata (normalized, not
rendered); operation-aware watch normalization with field presence preserved;
positional ordering across older-history paging; no resolution outside the
current session's tree. An
integration case starts with empty activity stores and mounted session chrome
and asserts the chosen initial-discovery behavior. `jobWatch`'s existing tests
still pass after the extraction.

**Stage 3 — shared interaction (`panes/session/transcript/EntityRef.tsx`,
`panes/session/transcript/EntityText.tsx`, `widgets/hovercard/index.tsx`, the
extracted Tooltip lifecycle hook + tests).**
Acceptance: unresolved plain text; resolved trigger plus card; navigable entity
adds exactly one `OpenButton`; a refresh-failed entity renders a stale card and
an ended-tree entity renders ended, both without a live indicator;
`aria-describedby` on trigger and control; card shows/hides on
hover/focus/leave/blur and portals; activating the `OpenButton` opens the pane
with the correct `ref` and `parentRef` and reuses an already-open pane (assert
against `workspaceStore`, as `agentFileLinks.test.tsx` does), while activating
the id trigger does not navigate; `Tooltip`'s existing tests still pass after
the lifecycle extraction.

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
