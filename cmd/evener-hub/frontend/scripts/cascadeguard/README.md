# Real-stack agent cascade guard

Run from the repository root:

```sh
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestAgentCascadeBrowser$' -count=1 -v
```

`make test-web-browser` also runs this guard. Chrome, Node and the production
frontend build are required. Missing prerequisites fail; this test never
substitutes dev markup or a fake AppWire handler.

## Boundary and journey

Go scripts only the external LLM provider. Actual delegate tools create 51
root siblings and a six-edge chain through the daemon. The public hub returns
52 direct root delegates and the real ordered ancestry. Chrome opens the
production SPA and activates those actual delegate rows.

Go passes the authenticated fixture JSON through Node's stdin pipe, keeping
its auth URL out of process arguments and environment variables.

`scripts/cascadeguard/attention.mjs` owns the floating Next-needs-you journey
and is imported by `run.mjs` after the cascade journeys. Go creates nothing for
it up front: the driver writes `{"command":"create-attention"}` to
`attention-control.jsonl`, and only then does Go start 52 real root sessions.
Each first model round fails with a non-retryable provider error, so each
session ends its turn errored - live, in the public needs_you tier, with no
pending question or approval. Go writes `attention-ready.json` only once the
public page carries every session. That is the only fixture state: the tier is
the hub's own `needs_you` section, one full 50-row page plus two later rows,
and the cascade journey above runs with no extra sessions and no extra daemons.

The guard checks:

- The mounted origin conversation never renders in the pane: no readable
  column and no spine. A first drill shows only the subagent's conversation,
  and that lone column fills the pane; depth six keeps two readable columns
  and four 52px ancestor spines, a 400px parent and a leaf with a 440px
  minimum. The pane body hosting the spines and columns never scrolls as a
  unit, each column's transcript viewport scrolls internally with its
  activity controls below the content, and the pane hosts no path nav or
  title heading.
- Compact ancestor status geometry with exact hover and screen-reader text on
  a rendered ancestor spine, stable through the origin's own live
  running-to-resting transition and under reduced motion, without changing the
  selected branch, focus or column geometry. The fixture's only busy
  conversation is the origin itself, which never renders; the running spine
  indicator family stays covered by the jsdom suites.
- Narrow overflow, selected-leaf visibility, independent transcript scrolling,
  parent selection, ancestor Tasks, one Escape, a keyboard pop through the
  pane's own footer breadcrumb crumb and native keyboard branching through the
  source-scoped sidebar's paging rows.
- Real cursor paging past the default 50 rows and two actual socket reconnects,
  additive membership, retained page extent and closed-peek demand release.
- Separate source and inspector panels, one reused secondary inspector, exact
  mounted center DOM and visible source row before and after entry and Return.
- Strict pending-image Return preserves the first intersecting source row,
  resolved entry, feasible within-entry progress and visible nonblank text.
  The baseline waits for useful, stable source reading geometry before Return,
  so a pending composer-height reflow does not become the oracle. After Return,
  its readiness barrier observes that reading position, since editor focus and
  saved workspace removal can precede transcript restoration.
- Width-only reflow in ordinary, cascade-column and phone-width browser
  readers preserves the same useful entry without opening or closing inspection.
- A height-only round trip in the ordinary and cascade-column readers keeps
  the reading line exactly where it was (#3899).
- Trusted native Shift-Space, wheel and current-state pill input during genuine held target
  measurements, followed by release that retains the newer reading point or
  live result. The keyboard checkpoint focuses the unchanged source scrollport
  before trusted input, without injecting a tab index or replacing a handler.
- Original panel identity, draft, a catalog skill and exact processed PNG
  bytes, with every readable column embedding its own scope's live
  conversation composer and nothing else in the pane mounting one.
- Native canvas completion held across inspection, both successful settlement
  and explicit failure, while preserving a newer source draft.
- Same-spelling project command, skill and inert prose with exact UTF-16 atom
  offsets and persisted metadata after mounted-source image failure and Return.
  Two retained PNGs surround the atoms, then a held native IndexedDB receipt
  strips each submitted marker independently from a newer mounted draft.
  Actual command expansion uses empty arguments, and the provider and public
  transcript independently verify the original mixed input arrives once.
- An actual committed IndexedDB transaction with its application acknowledgement
  held, and an actual held provider call with queued input. Inspection and Return
  retain the original recipient and mutation IDs; the provider and public
  transcript independently verify each submitted input arrives once.
- Close-only Return, exact original editor focus and persisted inspector removal.
- A real page reload restoring separate ordinary source and cascade
  inspector, exact selected ref, six saved edges and origin locator rendered
  above the removed origin, while an independent pane keeps its identity,
  grid, tab order and draft. Opening the already-open child conversation
  from the root transcript's delegate row focuses its own pane without
  closing inspection; the original root conversation never renders in the
  pane, so the old root-column Open shortcut that closed inspection is gone
  with the column's link.
- Actual leaf ancestry and source location replies held at native WebSocket
  delivery, then released unchanged, with exact inspector focus retained and no
  column style mutations.
- A desktop layout reload followed by a phone-width host switch, showing only
  the selected cascade column's embedded conversation - runtime state and its
  composer in the column's bottom bar, the home-indicator band reserved below
  the activity controls - with no path nav or pane title and a usable
  close-only Return, plus the ordinary phone Agents row opening the child's
  live conversation.
- The floating attention control against that real tier: a structured draft
  with a catalog skill atom and a real staged PNG held at native encoder
  completion, an away-reading source, then Next, Next, real browser Back,
  Forward and Back to the origin. After every hop `location.pathname`, the
  focused pane's ref and the history entry's runtime attention token must
  agree, and Back restores the exact origin pane record with its draft, image
  and reading point. A hop leaves the editor unfocused. Each attention session
  ends its held turn on a non-retryable provider failure, so it needs you
  without a pending question; a pending question deliberately hides the
  control and cannot be a hop target.
- A completed 500ms native pointer hold opens the list without navigating and
  its release click is consumed; movement past the 10px tolerance cancels the
  hold and releases outside the button, so no click navigates. Escape restores
  the More trigger.
- Actual geometry at 320, 390, 700, 899, 900, 1024 and 1400 CSS pixels in both
  themes: the control, Next and More fit the viewport, Next and More are
  distinct hit areas, neither overlaps the composer nor the Send/Stop button,
  the end clearance equals the measured stack, and the last text row is
  hit-testable above the floating stack's bottom edge at the end. The matrix
  records `control:false` for any width where the control is not offered and
  fails at the end naming every such width.

  The matrix passed at all seven widths in both themes on 2026-10-22 after the
  driver began dismissing the source activity sidebar before measuring (on the
  phone host it is a full-screen overlay that otherwise covers the control).
  Native hold and cancel pass. The already-presented target is recorded as
  blocked: an ordinary rail open replaces the primary session pane here, so no
  source pane survives to compare an in-place focus against. A long fixture
  title is not asserted: `ThreadStartParams` carries no title, and a session
  rename never reached the hub's needs_you summary, so the control's title
  truncation has no real provider path yet.
- The measured clearance row cannot toggle New Content: asserted by the unit
  suite (`Session.test.tsx`, clearance driven 0 → 96 → 0 with the pill and the
  reader's position checked at each step), not yet reached by this browser leg.
- A real response held at native delivery: Go starts one root on the driver's
  command and parks its first provider call, so the reply exists but is not
  delivered. The reader opens that session, hops Next to a needs-you target,
  returns with Back, and the reply is then released unchanged. The delivered
  text must appear in place, and `location.pathname`, the focused pane ref, its
  pane ID, the history entry's attention token and `history.length` must all be
  unchanged afterwards, so a late response navigates nothing. The fixed
  hold/cancel and encoder holds are untouched: only the provider reply waits.
- A transport-boundary failure on a later read: the open hub socket refuses the
  write of the app's own `needs_you` page-two read, so no frame reaches the hub
  and that page errors without a reconnect. The list stays open with its first
  50 rows intact, and a deliberate second press of Load more issues exactly one
  later-page read at offset 50, which the hub answers. Both pages then carry 52
  rows in the hub's order, and an earlier-page row still opens with URL and pane
  agreeing. The refused frame is injected in the page at the socket write, is
  recorded as an attempt, and is reported as such; no frontend store is seeded.
- The hub's public two-page tier (50 + 2 rows, exact remaining counts) checked
  from Go, and the attention tier's absence from every earlier cascade
  assertion because the provider holds each first round until the driver
  releases it.

The attention module runs at desktop widths in headless Chrome. Safari,
physical-phone touch and keyboard behavior, and the native app are not
qualified by it.

The center legitimately retains shared root demand. Closed-peek release is
checked on an ancestor with no independent holder, rather than treating every
root read as an inspector leak. Detached-source ownership and continuation cases
remain in the actual `sourceState.test.ts` suite; this browser journey keeps the
center mounted. Missing, malformed and reused origin locators are exercised by
the real Dockview restore and lifetime-owner suites.

Provider holds use filesystem-backed control acknowledgements. Browser holds
wrap only native completion delivery and return the actual event or encoded
blob. No test seeds a frontend store, manufactures a delegate row, replaces an
encoder or adds a production scheduling hook. Context and runtime changes must
preserve focus and geometry. Unexpected browser errors or warnings fail.

The driver awaits actual screenshot completion before clicking the Verbosity
menu. Mounted DOM can precede Chromium's updated scrollbar hit-test regions.
Tab activation uses the saved pane ID and actual group order while checking the
observed label, so equal session names remain distinct native targets.

Owner tests in `transcriptAnchors.test.tsx`, `useTranscriptScroll.test.ts`,
`useTranscriptScrollKeys.test.tsx` and `virtuallist.test.tsx` supply DOM geometry
and observer delivery at the browser boundary while running the real reader and
virtual viewport. They cover committed measurements, filtered zero-height rows,
later viewport-height settlement, clamping, input precedence and lifetime
isolation. The native guard checks actual text rectangles, painted geometry and
trusted input in desktop Chrome, including phone-width browser scenes. Safari,
physical phones, the native app and live provider behavior require separate
qualification.

The independent Go activity tests cover real archive decisions, nested socket
membership, runtime release and an initially empty incomplete retained jobs page:

```sh
go test ./agent -run '^TestSessionActivityRealDelegateTree$' -count=1
go test ./cmd/evener-hub -run '^TestSessionActivity' -count=1
```

## Evidence and retention

Go owns the real provider, hub and daemon lifecycles. The driver emits
condition-backed milestones in `milestones.jsonl` and receives controls through
`control.jsonl`. DOM, PNG, measured geometry, console events and actual RPC frames
are kept on failure. Set `CASCADEGUARD_KEEP_ARTIFACTS=1` to retain a passing run;
the Go output names its private evidence directory.

The authenticated fixture URL and raw RPC artifacts are private test inputs.
Keep token values out of reports. Copy retained evidence before inspecting it.
The shared hub fixture redacts auth URLs in failure diagnostics while retaining
raw private logs for daemon cleanup.

PNG preservation belongs to the original panel lifetime. Layout reload tests
must not expect memory-only image bytes to survive a page reload or put them in
layout JSON/localStorage. See the [product contract](../../../../../docs/product/session-activity.md#automatic-agent-cascade)
and [approved secondary amendment](../../../../../docs/superpowers/specs/2026-10-04-secondary-agent-cascade-design.md),
which retains the other [original cascade contracts](../../../../../docs/superpowers/specs/2026-10-01-automatic-agent-cascade-design.md).
