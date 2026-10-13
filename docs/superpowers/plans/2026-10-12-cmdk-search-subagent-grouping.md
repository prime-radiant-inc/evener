# cmd-k search: group subagent hits under their top-level session

Date: 2026-10-12 · Lane: `search-subagent-grouping` (worktree, base `ea31304e2e`)

## Problem

Jesse's report, verbatim: "When doing a full text search of previous sessions
with cmd-k in the webui, subagent sessions are surfaced at the same priority
and with the same visual treatment as primary sessions. Subagent sessions
should be grouped under their top level session. If the text shows up in the
subagent but not its top level session, the top level session should be greyed
out."

## Current behavior (verified in code)

- `evener/search` (`cmd/evener-hub/app_search.go`, `hubSearch`) answers three
  groups: `live`, `past`, `inSessions`.
- The Live group already omits subagent sessions (#3082, #2574).
- The Past group admits every matched ended session, including ended
  subagent sessions (their metas live in the same project state dir the past
  index scans), and renders each flat, identical to a primary session. This is
  the complaint.
- The web palette (`cmd/evener-hub/frontend/src/shell/palette/`) renders only
  `live` and `past`. `inSessions` is rendered by no client today.
- The native Board search (`mobile-native/src/board/boardSearch.ts`) consumes
  the same response and renders flat `live`/`past`.
- The past index matches name / original prompt / ID / working dir. The
  message-index (`inSessions`) is a separate group, unrendered by any client.

## Design

The hub owns lineage and annotates; the palette renders groups. The wire
change is additive and optional so every existing client is unchanged.

### Wire (`appwire/types.go`, regenerate via `go generate ./appwire/...`)

New struct:

```go
// SearchTopLevel names the top-level session a matching subagent renders
// under: the first non-subagent ancestor (walking ParentSessionID,
// cycle-guarded), then through fork supersessions to the row the navigation
// tree would show (the tree's own top-level rule, tree.go's rollup walk).
type SearchTopLevel struct {
    ID              string `json:"id"`
    Ref             string `json:"ref"`
    Title           string `json:"title"`
    Project         string `json:"project,omitempty"`
    State           string `json:"state"`
    Age             string `json:"age,omitempty"`
    AskPending      bool   `json:"askPending,omitempty"`
    ApprovalPending bool   `json:"approvalPending,omitempty"`
}
```

`SearchResult` gains `TopLevel *SearchTopLevel json:"topLevel,omitempty"`,
present only on rows whose session is a subagent. Live parents carry their
live state and `age: "now"`; ended parents carry `ended` and their age.
`AskPending` rides along for native's `searchResultMark`, which reads it. A
client synthesizing a row from `TopLevel` fills the generated TS type's
required fields (id, title, project, state) with the anchor's values or
empty strings.

`SearchTopLevel` nests inside `SearchResult` and is never a method's own
Params/Result, so `internal/appwiredoc/main.go` must register it beside
`SearchResult` (its own comment on SearchResult, lines 158-166, says an
unregistered element type gets named but none of its fields documented —
and the lint freshness gate cannot catch that). The wire section of the
test plan pins the doc output by content.

### Hub (`cmd/evener-hub/app_search.go`, `hubcore` helper)

- `past` stays exactly the list of matching sessions, in the same order —
  the native Board is unchanged. `live` unchanged. `inSessions` unchanged.
- Each admitted past match that the RootIndex marks a subagent gets its
  `TopLevel` resolved hub-side. Unresolvable ancestry (orphan, cycle) leaves
  the row unannotated — it degrades to today's flat row; no match is lost.
- One implementation of the walk: the tree's inline ancestor-chain and
  fork-supersession walk (tree.go:1329-1358) is extracted into a `hubcore`
  helper (returning the chain and the resolved top-level id, with the
  parent-lookup and is-subagent rules injected), `tree.go` is rewired onto
  it, and a `hubcore` agreement test pins search's resolution and the
  tree's against the same corpus (chains, forks, cycles, orphans). Existing
  tree tests must stay green unchanged — they pin the tree's behavior.
- The tree's live-reported (`reportedBy`) parent fallback is deliberately
  out of scope for search: a past match's own meta is indexed by definition,
  and the fallback exists for live children the Live group already excludes.
- An ancestor the past index has not folded in yet but the roster lists as
  live resolves from the roster entry (title falls back to `ShortID`), so a
  fresh live parent still anchors its ended subagent's hit.

### Shared grouping helper (`appwire-client/typescript`)

A new root-exported `searchGrouping.ts` module (the `activityData.ts` pattern:
colocated tests, re-exported from `index.ts`, listed in `tsconfig.build.json`,
a README paragraph) owns the grouping decision once:

```ts
interface GroupedSearchRow {
  result: SearchResult;   // the row to render (anchor or match)
  nested: boolean;        // a matching subagent, render indented under its anchor
  indirect: boolean;      // a synthesized anchor whose own session did not match
}
groupSearchResults(live: SearchResult[], past: SearchResult[]): {
  live: GroupedSearchRow[];  // live rows, each followed inline by its nested past children
  past: GroupedSearchRow[];  // past rows, grouped under anchors
}
```

Rules (shared, one implementation):
- Children group under the top-level session's own match row when it is
  present in `live` or `past` (not greyed — it matched itself).
- When the top-level session did not match, a greyed anchor row is
  synthesized from the child's `TopLevel` info at the group's position. The
  operative condition is "the top-level session has no row of its own in the
  response" — which also covers a session that matched but was cut from the
  20-row past page.
- When the top-level session is live and matched, its past children nest
  directly under its Live row (one row per session, no duplicates).
- A group renders contiguously at its first (most recent) member's
  position; children keep match order. Standalone matches render as today.
- A synthesized anchor may name a session outside the requested scope (an
  archived subagent's unarchived parent under `scope=archived`): grey is
  de-emphasis, not scope membership.
- Unannotated rows (older hub, orphan lineage) render exactly as today; the
  helper adds no dedup of its own (a current hub's past fetch already
  excludes live sessions, and native keeps its existing `sessionResults`
  dedup unchanged).

### Web palette (`CommandPalette.tsx`, `search.ts`, CSS module)

- `buildView` renders `groupSearchResults` output: nested rows indent
  (tree's `--space-2` per level) with the palette's existing `↳` glyph and
  a "subagent" hint; greyed anchors use the same `--ink-low` as unavailable
  rows, in a separate class.
- Greyed anchors and nested rows keep full keyboard navigation; Enter/click
  opens each row's own session by its `ref` (greyed anchors stay clickable —
  Jesse's ruling; grey is de-emphasis, not disabled).
- The "Past · N" header counts the rows rendered in the Past section,
  excluding synthesized anchors; children pulled under a live anchor count
  toward that Live rendering, not toward Past's N.
- Data attributes for tests: nested rows `data-nested`, greyed anchors
  `data-indirect`.

### Native Board search (`mobile-native/src/board`)

- `SearchResults.tsx`'s Sessions group renders the same grouped rows via the
  shared helper; `sessionResults` becomes the grouped shape (scope "live"
  keeps today's meaning: live rows only, so past children appear only under
  "All").
- Visual treatment: nested rows indent (the StateMark column's width) and
  drop the title weight to read subordinate; greyed anchors render their
  title in `inkLow` but stay pressable and open their session.
- The `SESSIONS · N` band counts the rows it renders, excluding synthesized
  anchors (native renders live and past rows in one band, so pulled-in
  children still count there).
- `onOpen` is unchanged: every row, nested or greyed, opens by its own ref.

## Non-goals

- `inSessions` (message search) untouched; no client renders it.
- Live subagent sessions stay excluded from the Live group (#3082).
- No ranking change beyond grouping; scope, archived flags, pinning and the
  needs-you empty-query view unchanged. Native "Live" scope keeps today's
  meaning (live rows only).
- The web demo fleet (`demoFleet.ts`) keeps answering `evener/search` with
  flat rows: it doubles as the older-hub shape, and demoing grouped Board
  results would need demo data this change does not plan.

## Recovery / preservation

- An older hub answers without `topLevel`: every row renders exactly as
  each client renders that response today (the web palette verbatim, the
  native Board with its existing live-dedup). A newer hub's extra fields
  are ignored by older clients.
- Unresolvable lineage degrades to a flat row, never a dropped match.
- No new failure path: the annotation is computed from the already-loaded
  RootIndex and past metas; it cannot fail the query.

## Test plan (TDD; red first for every case)

Backend (`cmd/evener-hub/app_search_test.go`, hubcore):
1. An ended subagent match carries `TopLevel` naming its ended parent
   (id/ref/title/state `ended`/age).
2. A subagent of a live parent carries live state and `age: "now"`.
3. A nested delegate chain (A → B → C) resolves to the root A.
4. A subagent under a fork-superseded original anchors to the continuation.
5. An orphan subagent (parent meta absent, not live) gets no `TopLevel`.
6. A parent that matches too still appears in `past` itself, unannotated,
   beside its annotated child.
7. A subagent whose live parent's meta is not yet in the past index still
   anchors to that parent, from the roster entry.
8. The extracted walk helper's unit tests in hubcore (cycle guard, missing
   metas, fork chains), plus the agreement test: search's resolution and
   `BuildTree` agree on a corpus with chains, forks, cycles and orphans.

Shared client (`appwire-client/typescript/searchGrouping.test.ts`):
9. Real-anchor grouping; both synthesized-anchor flavors as separate named
   assertions (an ended parent absent from the response, and an unmatched
   live parent carrying its live state and dot); multiple children
   contiguous; children pulled to a live anchor; orphan flat; no duplicate
   anchor row; match order preserved; a topLevel-less response returns rows
   exactly as given (old-hub shape).

Web palette (`CommandPalette.test.tsx`):
10. Nested rows carry the hint and indent class; greyed anchor carries the
    dim class; keyboard order walks anchor then children; Enter on a child
    (fixture under a live anchor, so the pulled-into-Live layout is what the
    keyboard assertion pins) navigates to the child ref; Enter on a greyed
    anchor navigates to the parent ref; "Past · N" excludes synthesized
    anchors and children pulled under Live; a topLevel-less response renders
    exactly today's flat rows.

Native Board (`SearchResults.test.tsx`, `boardSearch.test.ts`):
11. Grouped rendering: nested rows indented with lighter weight; greyed
    anchor dimmed but pressable; taps open the row's own session ref; the
    SESSIONS count excludes synthesized anchors; "live" scope shows live
    rows only; an unmatched live parent's greyed anchor carries its live
    dot; a topLevel-less response renders exactly today's flat rows.

12. `fetchSearch` unchanged (additive wire). The new module gets a
    hand-written root smoke call in
    `appwire-client/typescript/scripts/qualify-package.mjs` (the runner
    asserts every shipped module is reachable from a published specifier).

Wire + gates:
13. `go generate ./appwire/...` — `types.gen.ts` and
    `docs/appwire-protocol.md` carry the new type, its field table present
    (pinned by a doc-contains test after the `register(appwire.SearchTopLevel{})`
    call in `internal/appwiredoc/main.go`); freshness tests pass.
14. `make test`, `make test-web`, `make test-native`, `make test-api-package`,
    `make vet`, `make lint` (one at a time; the colocated helper tests run
    under `make test-web`, which includes the appwire-client sources).

## Product docs (same change)

- `docs/product/subsystems.md` S06 row: search's past results name each
  subagent's top-level session (hub-owned lineage, additive field).
- `docs/product/subsystems.md` S02 row: the palette groups subagent hits
  under their top-level session, greying an anchor that only its subagents
  matched.
- `docs/product/subsystems.md` S03 row: the Board search renders the same
  grouping through the shared client helper.
- `appwire-client/typescript/README.md`: the new root export's contract.
- `docs/appwire-protocol.md` regenerates automatically.

## Jesse's rulings (2026-10-12, spec approved with these answers)

- Greyed anchors stay clickable and open their session; grey means "the
  match came from descendants", not disabled.
- One row per session for live parents: matched parents nest children under
  their Live row; unmatched live parents get a greyed synthesized anchor in
  Past with their live status dot.
- The native Board renders the same grouping in this change, not webui only.

## PAR record (2026-10-12; /par not installed, adapted: two competing reviewers)

Both reviewers verified the current-behavior claims against the code and
returned "approve with changes". Adopted findings, all resolved above: the
appwiredoc registration (reviewer A, significant — verified against
internal/appwiredoc/main.go:158-166, which already registers SearchResult
for exactly this reason; reviewer B's "regenerates automatically" claim is
wrong on this point), the old-hub duplicate rule contradiction (reviewer B,
significant — resolved by deleting the rule rather than extending native's
dedup to the web, the smaller change that keeps the "exactly today's
rendering" promise for old hubs, since a current hub cannot produce the
duplicate at all), the single-implementation mandate for the lineage walk
(reviewer B, significant), AskPending on the wire, the Past-count and
scope wording, the two-anchor-flavor / live-anchor / old-hub-flat tests,
the `qualify-package.mjs` smoke call, and the `make test-api-package` gate.
Reviewer B found the most legitimate significant findings. The demo fleet
(question) stays flat, recorded as a non-goal.
