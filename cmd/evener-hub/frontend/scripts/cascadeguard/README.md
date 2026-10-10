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

The guard checks:

- A first drill whose immediate parent is the mounted origin conversation
  collapses that parent to a 52px spine, keeping only the leaf readable; two
  readable columns and five 52px ancestor spines at depth six, a 400px
  parent and a leaf with a 440px minimum.
- Compact ancestor status geometry with exact hover and screen-reader text,
  including a real running-to-idle transition and a still spinner under reduced
  motion, without changing the selected branch, focus or column geometry.
- Narrow overflow, selected-leaf visibility, independent transcript scrolling,
  parent selection, ancestor Tasks, one Escape and native keyboard branching.
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
- Width-only reflow in ordinary, read-only cascade and phone-width browser
  readers preserves the same useful entry without opening or closing inspection.
- A height-only round trip in the ordinary and read-only cascade readers keeps
  the reading line exactly where it was (#3899).
- Trusted native Shift-Space, wheel and current-state pill input during genuine held target
  measurements, followed by release that retains the newer reading point or
  live result. The keyboard checkpoint focuses the unchanged source scrollport
  before trusted input, without injecting a tab index or replacing a handler.
- Original panel identity, draft, a catalog skill and exact processed PNG bytes,
  with no composer or file picker inside the read-only inspector.
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
- A real page reload restoring separate ordinary source and read-only inspector,
  exact selected ref, six saved edges and origin locator, while an independent
  pane keeps its identity, grid, tab order and draft.
- Actual leaf ancestry and source location replies held at native WebSocket
  delivery, then released unchanged, with exact inspector focus retained and no
  column style mutations.
- A desktop layout reload followed by a phone-width host switch, showing only
  the selected read-only cascade transcript and usable close-only Return, plus
  ordinary phone Agents transcript entry.

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
