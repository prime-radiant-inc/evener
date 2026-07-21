# Web Rewrite Wave 4 — Report (M4 transcript)

Status: COMPLETE. T1 (sequential transcript core) + three parallel streams (T2 messages, T3 tools,
T4 flow) + three protocol-layer review rounds (R1/R2/R3, running alongside the streams against a
disjoint file manifest) + controller integration + a 13-item consolidated fix round + a Biome lint
migration + T5a (error-anchor pill, streaming caret) + T5b (this task: parity sweep, token-flood
benchmark, live proof, wave close), each independently gated green. Wave branch: `w4-transcript`,
HEAD `4097e73ef` before this task; this task adds one benchmark-harness commit (`3ee468f29`) and
one evidence+report commit (below) on top. Not yet merged to integration — outside this task's own
scope, per the brief.

## What shipped

- **T1 — transcript core** (`c1577ae56`..`afb420c01`): `SessionPane` replacing the Wave-3
  placeholder — `VirtualList` over turns (dynamic-height opt-in), `TurnBlock` skeleton, the
  item-renderer + tool-renderer registries with raw fallbacks, `StreamingText` (the imperative
  streaming leaf — appends only new chunks, surrogate-pair safe, TDD'd against chunk-sequence
  fixtures), `useTranscript` (model selection + `loadOlder` paging), threads-store `frameTimes`
  ring for Cadence.
- **T2 — message renderers** (merge `8ab55c5cc`): agentMessage (Markdown settled / StreamingText
  live), userMessage, steering-as-user-message, system/skill notices with quiet 3+-run coalescing,
  think blocks (open-while-live, collapse to a gist on settle), turn separators
  (duration/tokens/cost, all independently optional — never a fabricated placeholder).
- **T3 — tool renderers + agent-work surfaces** (merge `a8308b550`): descriptors for
  read/grep/ls/glob, shell (autoExpand on failure), edit/write/apply_patch (via the shared
  `DiffBlock` widget), web fetch/search, delegate, the job_* family, use_skill; the aggregated
  subagent module (leader-election-owned, spawn-ordered, done-rows-fold); watched-child live rows
  off `watchThread`'s additive subscription; ask_user question cards (read-only this wave); sandbox
  escalation cards (fully interactive — approve/deny wired to the real RPC).
- **T4 — scroll/liveness/paging/media** (merge `f28391b9d`): stick-to-bottom scroll geometry,
  the "↓ N new" / "↓ needs you" pill, `ImageGallery` (thumbnails + shared lightbox), the honest
  liveness line (quiet-bucket → may-be-stalled, driven by `lastFrameAt`), scroll-position
  persistence per ref.
- **R1/R2/R3 — protocol-layer fixes** (interleaved with T2-T4 on a disjoint `src/protocol/*`
  manifest): R1 (`3c71d3884`, `2e49410dd`, addendum `466d645f8`) — **the settle-wipe fix** (below)
  and live steering becoming a transcript item. R2 (`86034a432`..`ae7e12cc7`) — client-observed
  reasoning timing (the wire carries none), warnings reaching the model, settled tool calls keeping
  their `argumentsJSON` (the live settle site drops it, a Go-side gap — see Follow-ups). R3
  (`42ec2b59a`..`191c3161a`) — pending sandbox escalations reaching `ThreadModel` live.
- **Controller integration** (`e2216287f`): wires the three streams' registration barrels into
  `SessionPane`.
- **Fix round** (`3b6e79919`..`1c6b4231f`, 12 of 13 items landed, 1 correctly deferred): observed
  reasoning-timing fallback in `ThinkBlock`, empty-paragraph skip, a daemon-steering-images
  limitation pinned as already-correct, the `rememberedArgs` cache removed (superseded by R2's
  fix), `ToolCallItem` wired to `descriptor.autoExpand`, the sandbox-escalation rail moved onto
  `ThreadModel` instead of its own subscription, `type:"warning"` items rendered, a `loadOlder`
  reject-path pin, `ImageGallery` adopted for `item.images`/`outputImages`, duplicated
  clock/status helpers hoisted to `panes/session/liveness.ts`, an escalation-dedupe test
  strengthened, a `job_status` double-parse tidied. Item 9 (error-anchor pill) deferred with reason
  (`VirtualList` had no visible-range query yet) — built properly in T5a once that primitive
  existed.
- **Biome** (`cb06fb1a9`..`fb1002462`): full eslint→Biome migration, 84 rules promoted
  warn/info→error, 158 findings resolved (102 code fixes, 7 config-level, 49 justified
  suppressions with inline reasoning), zero `--unsafe` fixes, zero behavior changes outside three
  deliberate hook-dependency corrections (each independently re-verified against its own test
  suite).
- **T5a** (`d0d31dd3f`..`4097e73ef`): `VirtualList.getVisibleRange()`, `useTranscriptScroll`'s
  first-failed-turn error anchor, `NewContentPill`'s danger variant ("Failed turn," turn-level not
  tool-call-level), `StreamingText`'s CSS-only blinking caret. A coordinator review caught two
  defects against the real wire's bare-stamp turn-failure shape; both fixed same-day, addendum
  `4097e73ef`.
- **T5b — wave close** (this task): parity sweep (530 items across both source docs), a
  token-flood benchmark harness (commit `3ee468f29`), a 12-scenario live proof against a real hub
  and a real `openai/gpt-5.5` session, this report.

## The settle-wipe story

**What was wrong.** The live wire's `turn/completed` settle stamp is, for every ordinary turn
ending (`EventUserInput`, `EventGoalContinuation`, `EventError`, `EventSessionEnd` — confirmed
against `internal/appprojector/appwire_projection.go`'s four live settle sites), a **bare**
`{id, status[, error]}` — no `items` field at all (`itemsView: ""`). The reducer's original
`turn/completed` handler didn't discriminate this from the (different, `itemsView:"full"`)
snapshot-hydration shape: it treated the settle payload's absent `items` as authoritative and
replaced the turn's item list wholesale — wiping every item the model had spent the whole turn
accumulating via `item/started`/deltas/`item/completed`. A turn that streamed a full assistant
response, ran two tool calls, and settled normally would end with **zero items** — the exact
content the reader had just watched arrive, gone the instant the turn finished.

**How it hid.** Every one of the wave's own hand-built test fixtures (`basic-turn.jsonl`,
`streaming-with-reset.jsonl`, `tool-and-jobs.jsonl`) happened to smuggle the turn's items back in
through the settle payload itself — a wire-*false* shape (the real projector never does this) that
accidentally re-populated what the bug had just deleted, so every existing test passed. The bug was
invisible until R1 rewrote the fixtures to match the real wire shape and watched the same tests
fail for real.

**The fix.** `reducer.ts`'s `turn/completed` case now discriminates on `itemsView`: `"full"` (a
genuine snapshot replace) maps the settle payload's own items through the merge helpers as before;
anything else folds every item the model *already has* through a new `settleItem` — joining any
still-pending delta chunks into `text`, promoting a stray `inProgress` status to `completed` (a
turn cannot legitimately end with one of its own items mid-stream), and otherwise passing the item
through untouched. The turn's own scalar fields (status, timing, cost) still come from the wire
stamp; only the *items* are now preserved-not-replaced on a bare settle.

**The double verification.** R1's own RED-first cycle caught the bug once, rewriting the reducer's
own cross-thread test to the real wire shape and watching it fail with `expected an item at index 0
Error` against the unfixed code. Independently, the exact same wire-false assumption turned up a
**second** time, in a completely different file the fix's own manifest didn't touch —
`src/stores/threads.test.ts`'s "sibling immunity" test, which smuggled its own turn's item through
a settle payload the same way. R1's fix (correctly) didn't change that file's behavior, so the
identical class of bug in that file's *test data* was now provably exposed as a real failure by the
corrected reducer — confirmed by re-deriving the expected fix by hand (stream the item in via
`item/completed` first, then a genuine bare stamp) before touching anything, then watching it go
green. Two independently-authored test suites had encoded the same wrong assumption about the wire;
fixing the reducer once caught both, and the second catch (via a controller-authorized one-file
manifest extension, `466d645f8`) was itself independent proof the fix was principled rather than
fixture-fitted.

**Live re-confirmation this task (T5b).** The 10,000-delta token-flood benchmark
(`src/protocol/tokenFlood.test.tsx`) re-proves this exact invariant at 100x the fixture scale — a
bare `turn/completed` after 10,000 real deltas preserves the item's full, exact text — and the
12-scenario live proof watched it hold against a real daemon (scenario 2: "the settle persists").

## Parity sweep

Full item-by-item sweep of both source docs (`docs/web-ui/parity/parity-m4-transcript.md`, 261
checkboxes across 18 sections; `docs/web-ui/parity/contracts-transcript-scroll-liveness.md`, 269
bullets across 19 sections) — the complete, section-by-section table lives in
`.superpowers/sdd/t5b-parity-sweep-full.md` (the raw trail); this is its compact summary.

| Category | Approx. count | Share |
|---|---:|---:|
| Shipped | ~122 | 23% |
| Modernized | ~151 | 28% |
| Deferred | ~206 | 39% |
| Dead-in-legacy | 0 | 0% |

(Counts are directionally solid, not claimed exact to the single item — see the raw sweep's own
methodology note. Zero **dead-in-legacy** is a real finding, not an omission: every checklist item
that sits beside one of the parity doc's own named-dead constructs (`SUBAGENT_SEVERITY`,
`SUBAGENT_VISIBLE_ROWS`, `renderLivePlan`/`taskFoldGroup`) describes the REAL behavior next to it,
which this rewrite does implement — there was never a "please port this dead code" row to defer.)

**The sweep's most load-bearing finding: no task/plan-card feature exists anywhere in this
rewrite** — confirmed by direct grep against both the frontend (`grep -rn
"task_list\|taskList\|TaskCard\|task-card\|plan-item\|PlanCard" src/` → 0 matches) and the Go wire
layer (`grep -rn "task_list\|TaskList" internal/appprojector/*.go` → 0 matches). A `task_list` tool
call renders as a bare generic tool-call row (`DEFAULT_DESCRIPTOR`: raw JSON dump) instead of the
dedicated task-update card legacy shows. This accounts for the largest single cluster of `deferred`
verdicts (parity §9's task-card items, contracts §11's ~25 task/plan-card items, one bullet each in
contracts §1/§3/§4/§19). It is not a stream dropping the ball — the wave-4 plan's own T1-T4 task
descriptions never name `task_list`/a task-card at all; this feature area was never scoped into the
wave. Two sub-behaviors are worse than "missing": an `action:"view"` task_list call and a
`task-nudge` steering message are both explicitly *suppressed* in legacy (no card, no divider, no
row) and both now render *visible* chrome (a generic tool-call row; a collapsed-but-present
steering divider) — a genuine small regression, not just an absent feature.

**Second-largest cluster: no steering/notification classification exists.** `SteeringItem.tsx` is
a 2-way split (`source==="user"` vs. everything else) with zero content parsing — every
daemon-originated steering message, of any legacy "kind" (current-task, task-nudge, full-list,
notification, loop, …), renders identically as one generic collapsed "Steering injected" divider
showing the raw text. This accounts for parity §8's ~8 deferred items and contracts §17's entire
21-item in-transcript-job-notification section.

**Third: the turn-failure diagnostic system is entirely unbuilt** — a red end-cap with a raw-error
detail, a taxonomy badge, and Retry/Reconnect recovery actions (parity §9's 4 end-cap items;
contracts §10's 11-bullet diagnostic-actions cluster; contracts §9's matching 4 bullets). Confirmed
by direct grep (`grep -rln "Retry turn\|Reconnect & retry\|diagnostic" src/panes/session/` → 0
hits) after this exact gap slipped through all 6 of the sweep's own parallel research packages
(each independently flagged it out-of-scope for itself) — closed by a direct coordinator check
rather than left as a silent hole, per the sweep's own "no row silently skipped" mandate.

**Genuine architectural wins, not gaps** (the ~28% "modernized" share, concentrated in the
scroll/hydration/streaming machinery): this rewrite's single-shot snapshot hydration (one
`thread/read` RPC returning an already-folded `ThreadModel`, vs. legacy's chunked-replay-of-a-raw-
event-log) eliminates the entire class of chunked-replay races the legacy contracts doc spends
~20 bullets guarding against (mid-chunk reconnect, mid-chunk settings toggle, mid-chunk scroll) —
there is no replay loop left for them to race against. The reducer's `id`-keyed item/turn updates
(`mapItem`/`mapTurn`) make the DOM-position-heuristic bug class behind legacy's "idempotent
late-END replacement" (~6 bullets) structurally impossible rather than merely handled. `communicate`
tool-call dedup moved server-side (`appwire_projection.go:329-360`) — the frontend needs no
client-side interception at all.

## Token-flood benchmark

Wave plan's binding gate: *"recorded 10k-delta stream replayed through the store; frame budget
documented, no dropped-chunk correctness failures."* Harness: `src/protocol/testing/tokenFlood.ts`
(shared generator + timed fold); `src/protocol/tokenFlood.test.tsx` (correctness, part of `vitest
run`); `src/protocol/tokenFlood.bench.ts` (timing profile, `vitest bench` only).

**Correctness — all asserted, all green.** A 10,000-delta wire-shaped stream (`turn/started` →
`item/started` → 10,000 `item/agentMessage/delta`, chunk lengths uniform-random 2-40 chars → an
`item/completed` carrying the wire's own authoritative text → a **bare** `turn/completed` stamp)
folded sequentially through `applyNotification`. Mid-stream `pendingText` is the exact
concatenation of all 10,000 chunks (no drops, no reorders); the bare settle preserves the item
(text, count, status) — the R1 fix, re-proven at 100x fixture scale. Proven RED-first: a
deliberately-corrupted replay (one chunk dropped, two swapped) failed with `expected [...(9999)] to
have a length of 10000 but got 9999` before the fix was reverted to correct.

**Timing (measured, not asserted — the frame-budget deliverable):**

| Metric | Value |
|---|---|
| Total fold time, 10,000 deltas | ~32ms |
| Mean per-delta | ~0.0032ms |
| p99 per-delta | ~0.0055ms |
| First-10% mean / Last-10% mean | ~0.0005ms / ~0.0055ms (**~11x**) |
| vitest bench: fold(1k)→fold(5k)→fold(10k) total-time ratio | 1x → **22.7x** → **70.7x** (linear-expected: 1x→5x→10x) |

**Flagged punch item: super-linear (consistent with O(n²)) growth in the delta-accumulation path.**
Both measurements agree: a truly O(1)-per-delta fold would show a flat ~1x early/late ratio and
linear 5x/10x scaling; the observed ~11x per-delta slowdown and ~23x/~71x total-time growth are
consistent with `reducer.ts`'s `item/agentMessage/delta` case —
`pendingText: [...(item.pendingText ?? []), params.delta]` — which spreads (copies) the *entire*
accumulated chunk array on every single delta, an O(current-length) cost repeated n times. Absolute
cost is still small at realistic stream sizes (~32ms for 10,000 deltas), but the curve is real and
would compound badly on a much longer flood (a naive O(n²) extrapolation puts 100,000 deltas around
3 seconds of pure reducer time). Not fixed here (out of this task's remit) — a straightforward fix
exists (track a start-index instead of always copying, or accumulate into a mutable buffer) for a
future task.

**Streaming fast path (500-delta flood through a mounted `Session`, jsdom, real `FakeClient` +
store):** correctness confirmed (settled sibling's DOM content stays exactly correct throughout).
**Flagged punch item, the more operationally significant of the two:** a render-count probe
(mirroring `TurnBlock.test.tsx`'s own established synthetic-item-type pattern) proved a
**already-settled sibling item in the same turn re-renders exactly once per delta** — 500 deltas,
500 re-renders of unrelated, unchanged content. Root cause, confirmed by direct inspection: no
component in the item-render tree (`TurnBlock`, any item renderer) is wrapped in `React.memo`
(zero `memo(` call sites under `panes/session/` or `widgets/virtuallist/`), and `TurnBlock` passes
the *whole* enclosing `TurnModel` object through to every item renderer as a prop
(`<ItemRenderer item={item} turn={turn} .../>`, T1's own locked `ItemRenderProps`) — a fresh
`turn` object reference on every delta (`reducer.ts`'s `mapTurn` always returns `{...turn,
items: mapItem(...)}}`), so React re-invokes every sibling's render function even though its own
`item` prop never changed. This is wasteful, not wrong (content stays correct) — but it means the
binding constraint's own "per-delta work never re-renders the settled transcript" is not currently
true at the React level, only at StreamingText's own imperative-leaf level. A fix (memoize with a
comparator that ignores `turn` identity, or scope the store subscription per-turn) is a real,
scoped follow-up.

## Live proof

Real hub (`make build-hub`, `SERF_HUB_WEB=new`, dedicated port 19280) + a freshly-built `serf` CLI
(`--model openai/gpt-5.5`, no `.env` needed) + Chrome, driven end to end. Evidence:
`.superpowers/sdd/t5b-evidence/`.

| # | Scenario | Verdict | Evidence |
|---|---|---|---|
| 1 | Streaming text live + caret | **Pass** | `03-*.png` — caret visible mid-stream on a think block |
| 2 | Settle persists | **Pass** | streamed content survived settle across every session tested |
| 3 | Think block: live+caret / settled "Thought for Ns" / reload | **Pass (live) / Finding (settled)** | `03,04,05-*.png` — live streaming confirmed; **the reasoning item never actually reaches "Thought for Ns" on a still-live daemon** — see Go follow-ups |
| 4 | Tool calls: failure auto-expands, success collapses | **Pass** | `02-*.png` |
| 5 | Live steering | **Pass** | second attempt (clean timing) — steer rendered as a 5th "You"-tagged message, content genuinely influenced the model's reply |
| 6 | Turn separators | **Pass** | `06-*.png` (duration-only) and a later duration+tokens+cost row, both correct honest-degradation variants |
| 7 | Prepend (older turns), no viewport jump | **Pass, measured drift** | `11-*.png` — pill correctly did not inflate (null); a real ~70px/one-row drift measured, consistent with the previously-flagged single-pass rAF concern |
| 8 | New-content pill (needs-you + error-anchor) | **Pass (needs-you) / Not provoked (error-anchor)** | `08,10-*.png` — needs-you variant + click-jumps-clears confirmed; two careful `interrupt` attempts both cancelled cleanly before producing a visible failed turn — not cheaply provokable in this environment |
| 9 | Images | **Pass** | `01-*.png` — user-attached image rendered as a gallery thumbnail |
| 10 | `.top` overlay screenshot | **Captured, mixed verdict** | `12-*.png` — overlay elements (LoadOlderRow/LivenessLine) themselves are minimal/acceptable; the view is dominated by the VirtualList overlap bug (below), making a clean read hard |
| 11 | Two browsers, same live stream | **Pass** | `13-*.png` — both tabs confirmed receiving the same live updates |
| 12 | Warnings | **Not provoked** | two careful `interrupt` attempts both cancelled cleanly with no visible warning — consistent with the reducer's own documented behavior (a warning needs an active turn to attach to); not forced |

**Critical live-proof finding #1 — `VirtualList`'s dynamic-height remeasurement is structurally
broken.** `widgets/virtuallist/index.tsx` gives the *same* DOM element both `ref={dynamic ?
virtualizer.measureElement : undefined}` **and** an inline `style={{height: item.size, ...}}` set
from the virtualizer's own cached estimate. A `ResizeObserver`-based remeasurement (what
`measureElement` uses) can only fire when the *observed element's own box* actually changes size —
but here that box's height is externally pinned by the same inline style the virtualizer itself
controls, so it never "resizes" from the observer's perspective no matter how much its content
overflows. Measured live: `turn_1`'s row wrapper stayed at the 96px `ESTIMATED_TURN_HEIGHT`
fallback while its real content measured 337px — a 241px overflow rendering directly on top of the
next row, for a turn that had been fully settled for over 15 minutes (not a streaming-lag
artifact). This affects nearly every real turn (multi-paragraph content trivially exceeds 96px) and
is the dominant visual defect in the live-proof screenshots. Evidence: `09-*.png` (with numeric
proof: `wrapperRectHeight: 96` vs `realContentHeight: 337`, `overflow: visible`). Not fixable within
this task's remit; a high-priority punch item.

**Finding #2 — reasoning items never receive a wire status transition on a live daemon.** See
scenario 3 and the Go follow-ups below.

No Critical (content loss / crash) surfaced — both findings above are real, significant rendering
defects, not data loss, so the live proof continued through all 12 scenarios per the brief's own
STOP threshold.

## Go follow-ups for Jesse

1. **Reasoning items never receive a wire status-completing event.** Confirmed by direct
   inspection of `internal/appprojector/appwire_projection.go`: a reasoning `ThreadItem` is minted
   once, via `item/started` (`Status: inProgress`) on the first `EventReasoningSummaryDelta`
   (:257-272) — there is no `EventReasoningSummaryEnd` or equivalent anywhere in the event
   vocabulary (`agent/events/events.go`), and none of this file's 7 `NotifyItemCompleted` call
   sites construct a `Type:"reasoning"` item. A reasoning item's status is stuck at `"inProgress"`
   for the rest of a live session — confirmed live (T5b scenario 3): the think block never
   collapses from "Thinking…" to "Thought for Ns," even minutes after the turn completed, even
   across a page reload (since a still-running daemon's hydrate path shares the same in-memory
   projector state as the live path). `internal/apptranscript/apptranscript.go` *does* correctly
   hardcode `Status:"completed"` on true disk-backed reload (a closed/reopened session) — the gap
   is specific to any session whose daemon is still alive. Sharpens the wave's own previously
   anticipated "reasoning timestamps on the wire" item: the gap is the status transition itself,
   not just timestamps.
2. **Steering as `item/completed` server-side**, so the frontend doesn't need its own live-item
   construction for steering (currently reducer-side, `src/protocol/reducer.ts`'s
   `serf/steering/injected` case, per R1 Part B).
3. **Settled tool items should carry `ArgumentsJSON`.** `EventToolCallEnd`
   (`appwire_projection.go:414-442`) resolves `argsJSON` but only uses it to derive `Description`,
   never attaching it to the emitted `ThreadItem` — a live-settle-only gap the frontend reducer
   already works around (R2's `mergeArguments`, preserving the streamed item's own value), but the
   wire itself should just carry it.
4. **Shell exit-code as a wire contract.** `ItemModel` has no `.error`/exit-code field at all
   (confirmed: `wireItemToModel` never maps one) — this is the root cause behind roughly a dozen
   deferred parity items in this sweep (binary detection, drop notes, the shell terminal footer's
   bad-exit styling, the MCP-namespaced error/ok marker, `cheapToolBodyEnd`'s error-preferred
   rendering). A single wire field would resolve all of them at once.
5. **Escalation `resolved` broadcast, for multi-client card clear.** Per R3's own finding: the wire
   has `serf/sandbox/escalation/requested` but no matching `resolved` notification, so a second
   client watching the same session stays stale after another client resolves the escalation — a
   known, disclosed limitation, not a regression.

## Process lessons

- **vitest silently excludes a parse-broken file while still exiting 0.** A gate ordering of
  "`vitest run` alone" can pass green even when a test file fails to even parse, if vitest's own
  reporting doesn't hard-fail the run for a single broken file under some configurations — this
  wave's own gate discipline (`tsc --noEmit` *always first*, then `vitest run` with the test-file
  count compared against the prior run at every step) exists specifically to catch this
  structurally rather than trust a green exit code alone. Every task report in this wave, including
  this one, ran both in that order and recorded the file count every time.
- **A diff3 union-resolution merge can silently keep a stale "closer" value.** Worth a standing
  caution for any future merge-conflict resolution on this branch: a mechanical union-style
  resolution of two divergent edits to the same boolean/enum "closer" field can pick the wrong
  side's value without producing a visible conflict marker, if both sides independently touched
  adjacent-but-not-identical lines. Verify semantically, not just syntactically, when a merge
  touches state-machine-shaped code.

## Deferred / punch items (owners TBD — Jesse's call)

1. **`VirtualList` dynamic-height remeasurement is broken** (live-proof finding #1, above) — the
   single highest-priority item from this whole close: it affects the visual legibility of nearly
   every real transcript turn. Fix is scoped (stop double-controlling the measured element's own
   height) but is real production code, out of this task's remit.
2. **Reducer's `pendingText` accumulation is super-linear** (benchmark finding) — real but low
   urgency at realistic stream sizes; a straightforward fix exists.
3. **No React.memo anywhere in the item-render tree** — 1:1 re-render cost per delta on every
   mounted sibling item, confirmed via the benchmark's render-count probe. Related to #1 but a
   distinct fix (memoization / subscription scoping vs. the height-measurement bug).
4. **Task/plan-card feature is entirely unbuilt** (parity sweep's largest finding) — not scoped
   into any wave-4 task; needs an owner and a wave.
5. **Steering/notification classification is entirely unbuilt** (parity sweep's second-largest
   finding) — same status.
6. **Turn-failure diagnostic system (end-cap, recovery actions) is entirely unbuilt** — slipped
   through all 6 sweep packages' own scope before being caught by direct verification; needs an
   owner.
7. **Two small legacy regressions**: an `action:"view"` task_list call and a `task-nudge` steer now
   render *visible* chrome where legacy explicitly suppressed both — worth fixing before/alongside
   #4-#5, not on their own.
8. **New-content pill's plain-count label has no debounce** — will visibly flicker-count during a
   burst of distinct turns rather than freeze-then-settle (parity §15 / contracts §5).
9. **`--workspace-visible-height`/`visualViewport` mobile-keyboard-avoidance is entirely absent** —
   the shell currently uses a static `100vh`, the exact mobile-Safari address-bar-clipping failure
   mode the legacy mechanism existed to prevent. Worth a look given `StackHost` shows mobile is a
   live concern for this app.
10. **`ImageGallery` never distinguishes single- from multi-image layout** (no contact-sheet grid,
    no filename captions, though the wire data for captions exists and is silently dropped).
11. **`marked.parse` has no try/catch at finalization** — a throwing parse would crash the render
    instead of falling back to plain text like legacy did.
12. **`loadOlderTurnsUntilPrimaryDialogue` has no equivalent** — a session whose latest 40 turns
    are all housekeeping opens looking sparse, with no auto-backfill.
13. Error-anchor pill's bidirectional arrow (↓/↑) — T5a's own disclosed, still-open judgment call.

## Verification

```
cmd/serf-hub/frontend:
  npx tsc --noEmit  → EXIT=0
  npx vitest run    → EXIT=0  (104 files / 1509 tests — baseline 103/1504 + this task's
                                tokenFlood.test.tsx: +1 file, +5 tests; tokenFlood.bench.ts is
                                correctly NOT included, confirmed — `vitest bench` only)
  npm run lint      → EXIT=0
  npm run build     → EXIT=0  (dist/PLACEHOLDER restored via `git restore` immediately after,
                                confirmed clean via `git status`)

go build ./...                  → EXIT=0  (repo root)
go test ./cmd/serf-hub/...      → EXIT=0  (11 packages, all ok)
```

Live proof: real hub (`SERF_HUB_WEB=new`, `serf-hub -addr 127.0.0.1:19280 -serf <fresh serf
binary>`) + a real `openai/gpt-5.5` session, driven via the Chrome skill. Evidence:
`.superpowers/sdd/t5b-evidence/` (13 files). All spawned sessions shut down, hub process killed,
scratch state directories and built binaries removed, browser tabs closed — confirmed via `pgrep`
and a port-listen check before closing out this task.
