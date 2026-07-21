# Wave 4 Task 2 report — MESSAGE renderers

Branch `w4-messages`, off T1's tip `afb420c01`. 6 commits, all in `transcript/messages/**` only
(`git diff --stat afb420c01..HEAD -- . ':!cmd/serf-hub/frontend/src/panes/session/transcript/messages/'`
is empty — zero touches outside the owned directory). Full suite green (1109 tests = 985 baseline
+ 112 of my own + 12 auto-generated `token-contract.test.ts` checks for my 6 new CSS files, 79
files), `tsc --noEmit` clean, `eslint src` clean, `npm run build` clean — each verified twice in a
row for stability (see Verification).

## Commits

| Commit | Unit |
|---|---|
| `c45eb88a5` | shared formatting helpers (`format.ts`, `turnMeta.ts`, `reasoningFormat.ts`, `systemGrouping.ts`) |
| `6aa14978e` | userMessage + steering renderers |
| `3464e4010` | agentMessage renderer (streaming fast path) |
| `7873494d9` | reasoning (think block) renderer |
| `821a81647` | system/skill notice renderer with quiet grouping |
| `77c319a0d` | turn separator + `messages/index.ts` registration barrel |

## TDD evidence

Every unit: test file written first, run to confirm `Failed to resolve import` (red — the
implementation module doesn't exist yet), implementation added, run again to confirm green.
Verified explicitly for all 11 test files, not just claimed. One genuine test-authoring bug caught
and fixed mid-flight, both instructive:

- **`ThinkBlock.test.tsx`'s reset test initially called `ThinkBlock` directly** (`render` +
  `rerender` on the component itself with a new item id), expecting stale streamed text to vanish.
  It didn't — because the "no residue on reset" guarantee is a property of **`TurnBlock`'s
  `key={item.id}`** forcing a full remount (a fresh `StreamingText` instance), not of anything an
  individual item renderer does on its own. Calling `ThinkBlock` directly bypasses that key
  entirely, so the second `StreamingText` paragraph (both renders map array index `0`) saw
  `chunks.length` no bigger than what it already had and correctly did nothing — exposing my test
  as testing the wrong layer, not a product bug. Fixed by rendering through `TurnBlock` instead
  (mirroring `RawItemView.test.tsx`'s own reset test exactly), plus setting `status: "inProgress"`
  on the items (`TurnBlock` derives `live` from `item.status`, not from a prop the direct-render
  tests use) — both fixes needed before the test correctly exercised the real system.
- **`reasoningFormat.test.ts`'s clip-length test picked a string whose 20-char cut landed exactly
  on a space** (`"word ".repeat(4)` == 20 chars, trailing space), so `firstLine`'s `trimEnd()`
  correctly ate that trailing space before appending the ellipsis, making my hand-computed expected
  length off by one. Not an implementation bug — fixed by switching to an unbroken run of
  non-space characters so the clip boundary can't coincidentally land on whitespace.

## Per-renderer parity mapping

Legend: **shipped** (built, tested) / **deferred** (real legacy behavior, not built this wave,
reason given) / **dead** (legacy code the parity doc's own Highlights section already flags as
unused) / **superseded** (the underlying problem is solved differently by this stream's React
architecture, so the specific legacy mechanic doesn't apply).

### agentMessage (parity §6, contracts §7)
- Settled → one `Markdown` parse of the full text; live → `StreamingText` over `pendingText`,
  plain text, no markdown parse per delta. **Shipped.**
- 4096-char head-freeze, surrogate-safe backoff, DOM-text-node-before-caret insertion, per-settle
  coalesced re-parse. **Superseded** — `StreamingText` (T1) never re-parses markdown mid-stream at
  all (the raw/parsed split is unconditional for the whole stream, not threshold-triggered), so
  there is no head/tail boundary to manage in the first place.
- Idempotent late-END-replace-in-place (detach/reattach `.turn-meta`, "is this the last block and
  are all siblings quiet followers" check). **Superseded** — React's own keyed reconciliation
  (`key={item.id}` in `TurnBlock`) updates the same DOM node in place for a re-render of the same
  item id; there is no "duplicate vs. replace" ambiguity to resolve by inspecting siblings.
- `appendAssistantBlock`/`lastElementIsAssistantText` communicate-tool dedup. **Superseded at the
  wire, not by me** — confirmed via `internal/appprojector/appwire_projection.go` (live) and
  `internal/apptranscript/apptranscript.go` (historical): a `communicate` tool call is *already*
  projected as a plain `agentMessage` item server-side, with the duplicate-vs-already-streamed-text
  dedup done server-side too (`apptranscript.go`'s `lastAssistantText` comment: *"mirrors the live
  projector's own dedup"*). I never see a raw tool call to intercept.
- `resetAssistantMessage`/`ASSISTANT_TEXT_RESET`. **Shipped implicitly** — `reducer.ts`'s
  `item/agentMessage/reset` removes the item from `turn.items` outright; `TurnBlock`'s keyed map
  then naturally unmounts/remounts on any later re-add, so no reset-specific code is needed in this
  renderer.
- `streamingCaret()` blinking cursor. **Not shipped — flagged as a concern below** (design-system.md
  budgets a "streaming caret blink" motion that, as far as I can find, nothing in the app claims
  yet).
- Empty finalize removes the block. **Shipped** (`!item.text` while settled → `null`).
- Sub-second/rapid-settle streaming windows (the T1 domain finding). **Shipped and specifically
  tested** — `AgentMessageItem.test.tsx`'s two "rapid-settle" cases.

### userMessage (parity §5)
- Quiet "You"-tag treatment, tag as a sibling of the text (not mixed in). **Shipped.**
- Image/attachment thumbnails, single-card vs. contact-sheet, `imageSrc` resolution order.
  **Deferred to T4** per this task's own scope note; shipped an honest `[image]`/`[N images]`
  count line instead (`data-testid="user-message-image-placeholder"`, a stable hook for T4 to
  replace).
- Non-image attachment chips (`♪`/`📄` for audio/document, excluded from the image count).
  **Deferred, not fixable by me** — `reducer.ts`'s `imagesToStrings` (T1-owned) flattens every
  `InputItem` (images *and* audio/document attachments) into one untyped `string[]`, discarding
  the wire's own `type` discriminator before `ItemModel` ever reaches me. I can't tell an image
  apart from a document at this layer.
- Copy/edit/fork actions. **Deferred** — not named in this task's scope, and no other wave/task in
  the plan claims them either; my best guess is they're composer-adjacent (wave 5, per the
  `ask_user` note in T3's own scope: *"answered via the composer in Wave 5"*).

### steering (parity §8, contracts §9 steering-source tests)
- `source==="user"` → real user message, never the divider (issue #24). **Shipped**, reusing
  `UserMessageView` verbatim.
- Daemon-sourced steering never gets image thumbnails (placeholder-only). **Shipped** (the divider
  branch never touches `item.images`; the backend already bakes a placeholder into `item.text`
  when text is otherwise empty — confirmed in `apptranscript.go`).
- `classifySteering`'s sub-kind suppression (`current-task`/`task-nudge`/`full-list` render
  *nothing*; `notification` blocks render structured cards). **Deferred, deliberately** — this
  needs client-side regex classification of the raw text that I chose not to reimplement: (a) it's
  not named in this task's scope (only "quiet groups" and "user-sourced" are), (b) I found no
  evidence a task/plan-card surface exists anywhere in Wave 4 to make suppressing this content
  safe (legacy suppresses it *because* a task-card elsewhere already shows it) — showing it via the
  generic divider, worst case, is extra-but-honest information rather than a silent gap, and (c)
  `notification` (`<job-notification>`) parsing is squarely T3's subagent/job territory. My divider
  shows every daemon-steering item uniformly, verbatim.

### reasoning / think block (parity §7, contracts §8)
- Live: open, "Thinking…" label, streams the full body (not collapsed). **Shipped.**
- One independent `StreamingText` per `reasoningSummaries` `summaryIndex`, not a flattened single
  stream. **Shipped, and load-bearing** — flattening would corrupt output the moment an earlier
  index receives a delta after a later index has already started (worked through in
  `ThinkBlock.tsx`'s own comment and proven by `ThinkBlock.test.tsx`'s interleaving-growth test).
- Settle: empty thought removed entirely. **Shipped.**
- Settle: collapse to "Thought for Ns" + gist preview, duration-tier CSS class, teleprompter-tail
  live preview (`.pv`, last-400-then-200-chars). **Partially shipped / deliberately simplified** —
  I ship "Thought [for Ns] · first line" (first-line, not `reasoningGist`'s last-sentence +
  filler-stripping heuristic; no tier class; no separate rolling live preview, since the full body
  streams inline already). Matches this task's own scope wording ("first-line preview") rather than
  the richer legacy heuristic.
- **No fabricated "Ns" duration — see Concerns.** Confirmed via both `internal/appprojector/
  appwire_projection.go` and `internal/apptranscript/apptranscript.go` that a reasoning item never
  gets `StartedAt`/`CompletedAt` from the wire, in either the live or historical path. My
  `thoughtSeconds()` only ever computes from real timestamps (currently always `undefined` in
  practice) rather than measuring a client wall clock — a deliberate deviation from the literal
  scope text "Thought for Ns", made in favor of this codebase's own repeated, explicit "never
  synthesize from the client's wall clock" rule (see e.g. parity §16's tool-hover-meta and
  liveness sections).

### system/skill notices (parity §9, contracts §12)
- Quiet one-liner per item; 3+ consecutive coalesce into one collapsed disclosure naming the count
  and the first event; a non-lifecycle item in between breaks the run. **Shipped**, generically,
  for every `systemMessage` item (compaction, model switch, skill activation, plugin loads, hook
  completions, round timings — confirmed via `internal/appprojector` that the new wire projects
  *all* of these as `type: "systemMessage"`, unlike legacy's separate `SYSTEM_MESSAGE`/`SYSTEM_LINE`
  wire-level distinction, which the new backend has already collapsed into one type).
- Separate coalescing tracks for plugin-loaded vs. generic lifecycle runs; per-sub-kind identity
  (skill-activated vs. round-timings vs. ...); `localStorage` visibility preferences
  (`roundTimings`/`hookExitsAll`/`hookExitsNormal`/`promptLoaded`); rich structured compaction
  delta (turns-before→summary, tokens-before→after). **Deferred, not fixable by me** —
  `ItemModel` (`protocol/model.ts`, T1-owned) does not carry the wire's `eventKind`/`description`/
  `raw` fields (confirmed present on `types.gen.ts`'s `ThreadItem` but dropped by `reducer.ts`'s
  `wireItemToModel`), so this renderer has no signal to distinguish sub-kinds at all. Every
  `systemMessage` item gets the same honest, quiet treatment instead of a fabricated identity.

### turn separators (parity §4, contracts §9 turn-meta-badge tests)
- Legacy has **no real turn-separator element at all** — it's a `.turn-meta` badge glued onto the
  last assistant message of the turn, built/updated in place on `turn/completed`. This task's own
  scope explicitly calls for a standalone "compact ink-mid row per turn" instead — a deliberate
  beyond-parity redesign, not a port. **Shipped** as `TurnSeparator`: duration/tokens/cost from
  `TurnModel`, each segment shown only when present, joined with the same `" · "` convention
  legacy's own `formatTurnMetaText` uses, sans-serif chrome throughout (no `--font-mono`, per this
  task's own "no chrome mono" note).
- `show-cost` visibility preference (hide the cost segment, no dangling separator).
  **Deferred** — no preference/settings plumbing reaches `TurnModel`/`ItemModel` this wave; cost
  always shows when the wire provides it.
- **Not wired into the render tree yet — see the wiring instructions below.** `TurnBlock.tsx`
  (T1-owned) has no per-turn extension point today; placing `TurnSeparator` needs one line added
  there at merge.

## Wiring instructions for the controller

Two separate additions, both outside my owned tree:

1. **`SessionPane` (`cmd/serf-hub/frontend/src/panes/session/Session.tsx`)** — per this task's own
   instruction, add a side-effect import so all five item renderers self-register (mirrors
   `TurnBlock.tsx`'s own `import "./ToolCallItem"` for `commandExecution`):

   ```ts
   import "./transcript/messages";
   ```

   Suggested placement: alongside the existing `transcript/` imports, e.g. right after
   `import { TurnBlock } from "./transcript/TurnBlock";`.

2. **`TurnBlock.tsx` (`cmd/serf-hub/frontend/src/panes/session/transcript/TurnBlock.tsx`)** — needs
   `TurnSeparator` actually rendered once per turn, after the items map:

   ```tsx
   import { TurnSeparator } from "./messages";
   // ...
   export function TurnBlock({ turn }: TurnBlockProps) {
     return (
       <div className={CLASS.turn} data-testid="turn-block" data-turn-id={turn.id}>
         {turn.items.map((item) => {
           const ItemRenderer = itemRendererFor(item.type);
           return <ItemRenderer key={item.id} item={item} turn={turn} live={isItemLive(item)} />;
         })}
         <TurnSeparator turn={turn} />
       </div>
     );
   }
   ```

   Note: `./messages` re-exports `TurnSeparator` from the same `index.ts` that runs the
   registration side effects, so wiring #2 alone would transitively satisfy #1 too (ES module
   imports are cached/idempotent, so having both is harmless, just slightly redundant) — I kept
   both instructions since #1 is what this task explicitly asked me to document, and #2 is the
   one genuinely new requirement (`TurnBlock` needs a JSX change, not just an import).

## Files

Created (all under `cmd/serf-hub/frontend/src/panes/session/transcript/messages/`):
- `format.ts` / `.test.ts` — `imagePlaceholder`, `formatTokenCount`, `formatDurationMs`, `firstLine`
- `turnMeta.ts` / `.test.ts` — `turnMetaParts` (narrows `TurnModel.usage`/`.cost`'s `unknown` type)
- `reasoningFormat.ts` / `.test.ts` — `joinedReasoningParagraphs`, `reasoningPreview`, `thoughtSeconds`
- `systemGrouping.ts` / `.test.ts` — `systemRunFor`, `shouldGroup`
- `UserMessageItem.tsx` / `.test.tsx` / `usermessageitem.module.css`
- `SteeringItem.tsx` / `.test.tsx` / `steeringitem.module.css`
- `AgentMessageItem.tsx` / `.test.tsx` / `agentmessageitem.module.css`
- `ThinkBlock.tsx` / `.test.tsx` / `thinkblock.module.css`
- `SystemNoticeItem.tsx` / `.test.tsx` / `systemnoticeitem.module.css`
- `TurnSeparator.tsx` / `.test.tsx` / `turnseparator.module.css`
- `index.ts` / `.test.ts` — the registration barrel

1465 lines total (implementation + tests + CSS). Zero files touched outside this directory.

## Self-review

- **`format.ts`/`turnMeta.ts`/`reasoningFormat.ts`/`systemGrouping.ts` are plain, dependency-free
  functions (no React)**, committed as one foundational batch before any component. Each renderer
  then only wires them to JSX — kept the red→green cycles short and the components themselves thin.
- **`turnMeta.ts` narrows `TurnModel.usage`/`.cost` locally** rather than widening the shared
  `ItemModel`/`TurnModel` types (which are `protocol/`, forbidden). Documented in the file's own
  header comment exactly which `types.gen.ts` shape (`SerfUsage`/`string`) the guard targets, so a
  future reader isn't left guessing why a `TurnModel` field typed `unknown` gets treated as an
  object with `inputTokens`/`outputTokens`.
- **`UserMessageView` is exported standalone specifically for `SteeringItem` to reuse**, rather than
  duplicating the "You" tag + placeholder markup in two files — one real behavioral requirement
  (parity issue #24) implemented once.
- **`SystemNoticeItem`'s grouping is stateless**, recomputed fresh from `turn.items` on every
  render via `systemRunFor` — no persisted "which run am I in" bookkeeping to desync as items
  stream in, at the cost of an O(run length) scan per grouped item per render (turns realistically
  hold a handful of system notices at most, so this is not a real perf concern).
- **Every renderer's live/settled transition test explicitly mirrors `RawItemView.test.tsx`'s own
  canonical pattern** (render live → rerender settled → assert no leftover streaming testid, no
  duplicated text), per this task's binding constraint — not just "has tests," but the *same
  shape* of test T1 established.
- Checked for accidental sentence-case/mono violations by hand across all five renderers' visible
  copy ("You", "Steering injected", "Thinking…", "Thought for Ns", "N system events", "System
  event") — none are ALL-CAPS, none use `--font-mono` for chrome text.

## Concerns

- **Live steering is currently invisible in the transcript — a gap in T1's reducer, not mine to
  fix, but important context for whoever reviews or merges this.** `reducer.ts`'s
  `applyNotification` case for `"serf/steering/injected"` only updates `lastFrameAt`; it never
  folds the notification into any turn's `items` array (unlike `item/started`/`item/completed`,
  which do). `SteeringItem` is fully built and tested and *will* render correctly the moment a
  `type: "steering"` item reaches it, but today that only happens for **hydrated/historical**
  turns (`apptranscript.go` does produce `type: "steering"` items for reload) — a steering message
  injected into a *live* session won't appear until `reducer.ts` is extended to insert an item for
  it, which is outside `transcript/messages/**`.
- **`streamingCaret()` (design-system.md's "streaming caret blink" motion) is not implemented
  anywhere I can find** — not in `StreamingText` (T1, checked directly: no caret element), not
  here. It's one of only three motions the whole design system budgets for, and `agentMessage`'s
  live path is the most natural place for it, but (a) it isn't named in this task's explicit scope
  bullets, (b) no blink-rate/timing token exists in `tokens.css` to source a duration from without
  inventing one, and (c) it's ambiguous whether it should live in `StreamingText` itself (T1,
  universal to every streaming leaf) or be layered on per-renderer (me, `agentMessage`-specific).
  Flagging rather than guessing at ownership or a timing value.
- **No settings/preference plumbing reaches `ItemModel`/`TurnModel` this wave** — legacy's
  `roundTimings`/`hookExitsAll`/`hookExitsNormal`/`promptLoaded`/`show-cost` `localStorage`
  preferences have no equivalent here, so `SystemNoticeItem` always renders every lifecycle event
  (mitigated somewhat by the 3+ grouping) and `TurnSeparator` always shows cost when present.
- **`Markdown`'s `md.parse()` call has no try/catch** (checked directly in
  `widgets/markdown/index.tsx`) — contracts §7 documents legacy falling back to plain text if
  `marked.parse` throws. This is a pre-existing property of the `Markdown` *widget* (`widgets/`,
  forbidden for me to edit), not something `AgentMessageItem` introduces; flagging since I rely on
  it directly for the settled path.
- **Fork/copy/edit actions on user/assistant messages are entirely absent** (see the userMessage
  parity mapping above) — not in this task's scope, and I could not find another Wave 4 task that
  claims them either. Worth a decision at wave-close about which task (or a later wave) owns them.

## Verification

```
npx vitest run   → EXIT=0  (1109 passed, 79 files; 2 full reruns, identical both times)
npx tsc --noEmit → EXIT=0  (no output, both runs)
npx eslint src    → EXIT=0  (no output, both runs)
npm run build     → EXIT=0  (tsc --noEmit && vite build; bundle sizes UNCHANGED from baseline,
                              confirming nothing in transcript/messages/** is reachable/bundled
                              yet — expected, since the wiring in this report hasn't landed)
```

`dist/PLACEHOLDER` (a deliberately-tracked file `vite build`'s `emptyOutDir` deletes) was restored
via `git checkout` after every `build` run — confirmed clean (`git status --short`) before each
commit.
