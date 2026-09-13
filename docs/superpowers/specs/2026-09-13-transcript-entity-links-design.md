# Transcript Entity Links

## Status

Approved in outline. Scope locked with Jesse on 2026-09-13:

- **Entities:** shell jobs (`job_…`) and stable delegates (`dlg_…`) get a link
  plus a hover card; evener watches (`watch_…`) get a hover card only.
- **Resolution:** client-side, from state the app already loads. No new
  backend or AppWire surface.
- **Surfaces:** message prose and structured id fields. Code, raw job output,
  ANSI logs, JSON, and copy targets are never rewritten.

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
  through the existing standard `OpenButton` affordance.
- Render a hover card on every resolvable id summarizing the entity's current
  state, from client-side state.
- Leave unresolvable ids as plain text.
- Never rewrite code, raw output, ANSI logs, JSON, or copy targets.

## Non-goals

- No Go, AppWire, or daemon changes. No cross-session entity index.
- No link affordance on watches; they get a hover card only.
- No linking of session ids, `ag_`, `att_`, `call_`, `wg_`, `wd_`, `plugin_`,
  or `item_` ids.
- No linkification inside fenced or inline code, raw tool output, ANSI logs,
  JSON payloads, or any copy/export string.
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

### Detection rules

`entityIds.ts` is a pure module that mirrors `identifier.ValidateJobID`,
`ValidateDelegateID`, and `ValidateWatchID` exactly:

- A candidate matches only at its exact length with a valid base62 payload.
- The character immediately before the match, when present, must not be
  `[0-9A-Za-z_]`. The character immediately after, when present, must not be
  `[0-9A-Za-z_]`. This keeps `xjob_…`, `job_…extra`, and a longer
  underscore-joined token from matching.
- `findEntityIds(text)` returns `{ kind, id, start, end }[]` for a string.

### The provided example

Jesse's example, `job_034MY2rfMj2ho4Nj6J0jFB_q2qdqT99U6`, carries a 10-character
suffix. The current mint (`identifier.NewJobID`) and validator require exactly
12. This spec mirrors the server validator; that example would not match. If
real transcripts turn up ids with other suffix lengths, loosen the job suffix to
1–12 base62 in one place (`entityIds.ts`) and add a case.

## Client-side resolution

A single `entityIndex` resolves an id to a normalized summary. It reads only
state the app already has or can already fetch.

### Sources, in priority order

1. **Current session's activity tree.** `threadsStore.listJobs(ref)`
   (`evener/jobs/list`). When the activity panel has already loaded a tree,
   reuse `retainedActivityTree` from `stores/activityPanel.ts` rather than
   refetching. The tree carries `ActivityJob` and `ActivityDelegate` records,
   including nested delegates.
2. **Current session's `ThreadModel.delegates[]`.** Live
   `EvenerDelegateInfo` projections, refreshed by the reducer on
   `evener/delegate/updated` and the jobs notifications.
3. **`job_watch` payloads in the session's loaded turns.** Evener watch state
   crosses the wire only inside a `job_watch` tool result; there is no watch
   field in `thread/read`, `evener/jobs/list`, or any store. The index extracts
   watch rows from `item.raw` using the same normalizer the `job_watch`
   renderer uses.
4. **Cross-session jobs.** A job id encodes its owner session id in
   characters 4–26. When the owner is not the current session, derive the owner
   ref (`local:<ownerSessionID>`, or `ActivityJob.ownerRef` when a retained tree
   supplied it) and fetch `evener/jobs/list` for it lazily, with a cache. A miss
   stays plain text.

### Normalized summary

```ts
type EntitySummary =
  | { kind: "job"; id: string; label: string; state: JobState; open: OpenTarget }
  | { kind: "delegate"; id: string; label: string; state: DelegateState; open: OpenTarget }
  | { kind: "watch"; id: string; label: string; state: WatchState; open?: undefined };
```

- `JobState`: status, type, description/command, started/ended, exit code,
  output bytes, live flag.
- `DelegateState`: lifecycle status, outcome, mandate (clamped), agent type,
  model, run start/end, quiet age, usage, resumable/attention.
- `WatchState`: watching/ended/pending, condition, deliveries, source,
  last-known snapshot flag.
- `OpenTarget`: `{ ref: string; parentRef?: string }` — exactly what
  `openTranscript` takes.

### Reuse, not duplication

`jobWatch.tsx` already normalizes watch rows out of `job_watch` payloads
(`WatchRow`, `normalizeRow`). Extract that pure normalizer to
`src/protocol/watchRows.ts` and use it from both the renderer and
`entityIndex`. Do not write a second parser.

### Freshness

`entityIndex` subscribes to the threads store and to the activity tree
revision. A hover card renders from the index, so a state change re-renders an
open card. Watches are explicitly last-known: a `job_watch` payload is a
snapshot, and nothing live updates it. The card says so.

## Rendering

### `EntityRef`

`<EntityRef id={…} />` is the shared component for structured fields. It:

- validates the id through `entityIds.ts` and renders nothing linkable for an
  invalid or unresolvable id;
- renders the id text as a button (no in-app URL exists for these panes) and,
  for jobs and delegates, the standard `OpenButton` after it;
- wraps the group in a hover-card trigger;
- for watches, renders plain text plus the hover card, with no button and no
  `OpenButton`.

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
5. Rebind on every `source`/`live` change, matching the file-link effect, and
   clean up listeners and inserted nodes on teardown.

This is the riskiest part. The file-link precedent inserts a sibling after an
existing anchor; text-node splitting is more invasive, and a streaming message
re-renders its DOM repeatedly. Deduplication across stream and settlement is a
required test case.

### User message prose

`UserMessageItem` renders plain text in a div, not Markdown, so it has no code
semantics to skip and needs no DOM walk. Render its text through
`<EntityText text={…} />`, which splits the string on detected ids and renders
`<EntityRef>` inline. A pasted raw log in a user message therefore links ids
inside it; that is accepted, since a plain-text div gives the client no way to
tell pasted output from prose.

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
| job | `job:<jobId>` (or `ActivityJob.transcriptRef` when known) | `ActivityJob.ownerRef`, else the current session ref, else `local:<ownerSessionID>` |
| delegate | `delegate.transcriptRef` (child session ref) | the session ref whose tree/model supplied the delegate |
| watch | none | — |

Jobs and delegates route through
`openTranscript(ref, parentRef)`, which already handles beside placement, pane
dedup, and navigation context. Job logs additionally require `parentRef`, since
`JobLog` fetches output through the owning session.

## Failure modes

- Unresolvable id (foreign delegate, watch with no loaded `job_watch` call,
  unknown owner): plain text, no link, no card.
- Malformed id (wrong length, bad alphabet, bad boundary): not detected at all.
- Missing session ref for a job: no link; the card still renders if the tree
  supplied state.
- A job whose owner is not local: link only when the owner ref is derivable or
  loaded; otherwise plain text.

## Accessibility and interaction

- The primary id control is keyboard reachable and opens on Enter/Space.
- The hover card shows on focus as well as hover and is wired with
  `aria-describedby`, matching `Tooltip`.
- Hidden on touch, where there is no hover, via the same CSS gate `Tooltip`
  uses.

## Testing

Pure unit:

- `entityIds.test.ts` — valid ids for all three kinds; wrong lengths; bad
  alphabet; boundary cases (`xjob_…`, `job_…extra`, trailing `_`); multiple
  matches; no matches.

Resolution:

- `entityIndex.test.ts` — job, delegate, and watch resolution from fixtures;
  cross-session job owner derivation; watch extraction from a `job_watch` raw;
  miss returns undefined; `OpenTarget` values.

Components:

- `EntityRef.test.tsx` — link plus `OpenButton` for job and delegate; click
  opens the pane (assert against `workspaceStore`, as `agentFileLinks.test.tsx`
  does); watch renders a card with no button.
- `HoverCard` tests for show/hide, focus trigger, portaling.
- Prose tests: an id in agent Markdown links; an id in inline/fenced code and
  in an existing link does not; stream-then-settle keeps one affordance per id;
  user message prose links.
- Structured tests for the three renderers.

Follow TDD. Do not mock resolution logic; use real fixtures and the real
stores. Default tests stay deterministic.

## Gates

Run before the frontend gate, per `AGENTS.md`:

- `npx biome check --write` on touched files under `src/`.
- `make test-web` (unit + typecheck + Biome).
- `make test-web-browser` on a Chrome-capable host.

## Deliverables

New:

- `src/protocol/entityIds.ts` + test
- `src/protocol/watchRows.ts` (extracted from `tools/jobWatch.tsx`)
- `src/stores/entityIndex.ts` + test
- `src/widgets/hovercard/index.tsx` + test
- `src/panes/session/transcript/EntityRef.tsx` + test
- `src/panes/session/transcript/EntityText.tsx` (the prose text-node
  enhancer) + test

Edited:

- `messages/AgentMarkdown.tsx`
- `messages/UserMessageItem.tsx`
- `tools/jobTools.tsx`, `tools/delegateStatus.tsx`, `tools/jobWatch.tsx`

## Follow-ups

- Loosen job suffix detection if real ids diverge from 12 characters.
- Extend to reasoning and notice text with the same enhancer.
- Add watch state to the AppWire surface for live watch cards and a real open
  target, if watches earn a pane.
- Consider whether the activity tree should expose watches, which would remove
  the `job_watch`-payload dependency.
