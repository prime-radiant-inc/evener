# Iteration-4 fix report — renderer pipeline (branch webui-joy)

Scope: F1–F5 in `cmd/serf-hub/assets/{renderer.js,renderer-tools.js,style.css}` +
`cmd/serf-hub/jstest/`. Red-first: every behavioral fix has a failing test
captured before the change.

## F1 (Important) — stale overlapping readThread completion wedges hydration

**Red evidence** — new `cmd/serf-hub/jstest/test-renderer-hydration-stale-overlap.js`,
two overlapping readThread deferrals (H1 superseded by a reconnect's H2; H2
resolved fully, then stale H1 resolved / H3 rejected after H4):

```
FAIL: stale H1 completion did not wedge hydrationInProgress ...
FAIL: stale H1 completion did not clear appwireHydrated
FAIL: stale H1 did not reset/re-render the transcript (got 150 messages)
FAIL: transcript is still exactly H2's events
FAIL: stale H3 rejection did NOT clear the new stream (clearAppwireStream would kill live delivery)
FAIL: stale H3 rejection raised no connection banner over a healthy stream
```

Root cause confirmed by the red run: the stale H1 passed the
session/conversation entry check (`renderer.js` old line 845), reset H2's
transcript, replayed 150 events (one chunk), then aborted on the *later*
`this.liveStream !== appwireStream` check (old line 899) — leaving
`hydrationInProgress` stuck true.

**Change** (`renderer.js`, `connectAppwire`):
- Top of the readThread `.then` (before ANY state/DOM mutation):
  `if (this.liveStream !== appwireStream) return;` — mirrors the existing
  mid-chunk/post-paging check, using the same stream token captured at
  connect time (`const appwireStream = this.liveStream;`).
- Top of the `.catch`: same guard, so a stale rejection can no longer
  `clearAppwireStream()` the healthy new stream or raise a false
  "Connection lost" banner + reconnect loop.

**Green evidence**: `PASS: stale overlapping readThread completion/rejection
is guarded before any state mutation` (transcript stays H2's 3 events;
`hydrationInProgress === false`; `appwireHydrated === true`; stale rejection
leaves stream/hydration/banner untouched).

## F2 (Important) — hydration's final scrollToBottom yanks a mid-replay reader

**Red evidence** — new `cmd/serf-hub/jstest/test-renderer-hydration-scroll-intent.js`
(jsdom, `scrollHeight` stubbed to 5000, scroll event dispatched by hand
mid-replay):

```
FAIL: reader scrolled mid-replay — final settle did NOT yank to bottom (scrollTop=5000, want 1200)
```

**Change** (`renderer.js`):
- Init: `this.readerScrolledDuringHydration = false; this.programmaticScroll = false;`
- Scroll listener (`bindScrollAffordance`): sets
  `readerScrolledDuringHydration = true` when a scroll event fires while
  `this.hydrationInProgress && !this.programmaticScroll`; the affordance tick
  is untouched.
- `scrollToBottom()` wraps the `scrollTop` write in a
  `programmaticScroll` try/finally marker so programmatic scrolls can never be
  read as reader intent.
- Hydration start: flag cleared alongside `hydrationInProgress = true`;
  also cleared in `resetTranscriptReplay()`.
- Hydration-end settle:
  `if (!this.readerScrolledDuringHydration) this.scrollToBottom(); this.readerScrolledDuringHydration = false;`
  Programmatic paging scrolls can't fire mid-hydration anyway
  (`maybeLoadOlderTurns` is guarded), so only genuine reader scrolls set intent.

**Green evidence**: `PASS: hydration-end settle honors reader scroll intent
(no yank; sticks when untouched)` — case (a) no scroll → settle parks at
bottom (scrollTop 5000); case (b) mid-replay scroll to 1200 → stays 1200;
case (c) later hydration with no scroll → sticks at bottom again (flag reset).

## F3 (Important) — >8KB tail-fold silently drops bytes behind a bare "…\n"

**Red evidence** — `test-tool-streaming-output.js` updated first:

```
FAIL: bodyEnd prefixes an HONEST not-retained note (not a bare ellipsis masquerading as ordinary output)
FAIL: the bare … prefix is gone — elision is stated, not implied
FAIL: expandable content begins with the honest not-retained note
FAIL: read bodyEnd prefixes the honest not-retained note
```

**Change** (`renderer-tools.js`, `tailFoldOutput`): the `"…\n"` prefix is
replaced with the drop-note idiom's honest statement, rendered as a text line
(the string lands in a `<pre>`):
`earlier output not retained — showing the last 8,000 chars\n` + the same
surrogate-safe `tailSlice(text, max)`. Fold behavior otherwise unchanged.
`test-renderer-surrogate-tail.js` was updated to the new prefix (it asserted
the old `"…\n"`), slicing past the note line via its length.

**Green evidence**: both files pass; the multi-line >8KB case still ends with
`line-0899`, oldest lines still elided, fold chrome still builds past 5 lines.

## F4 (Minor) — streaming caret never animates

**Red evidence** — stylesheet-text assertion in `test-renderer-streaming-tail.js`:

```
FAIL: caret breathes via the think-breathe keyframe on --pulse-cycle (got: color: var(--accent);)
```

**Change** (`style.css`): `.assistant-message .streaming-tail .streaming-caret`
gains `animation: think-breathe var(--pulse-cycle) infinite;` — the same
sanctioned keyframe/token as `.think.streaming .think-glyph`. Coverage under
`prefers-reduced-motion` comes from the existing universal collapse
(§1.5: `animation-duration: 1ms !important; animation-iteration-count: 1`),
which the test asserts is present.

**Green evidence**: caret-rule + reduced-motion assertions pass.

## F5 (Minor) — missing parse-failure fallback test

The fallback already existed (`renderAssistantMessage`: `try { innerHTML =
marked.parse(text) } catch { el.textContent = text }`); coverage was missing.
Added to `test-renderer-streaming-tail.js`: `marked.parse` throws at
finalization → `ASSISTANT_TEXT_END` does not throw, the message stays as plain
text (`textContent === "plain fallback text"`, zero element children). Passed
immediately (pure coverage test, no source change needed).

## Gate outputs

- `cd cmd/serf-hub/jstest && ./run-all.sh` → **178/178 OK**, `jstest: all tests passed`, rc=0
  (includes the two new test files; captured in /tmp/iter4-jstest.log during the run).
- `make build-hub` → success (build-runtime-pair.sh).
- `GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub` → `ok primeradiant.com/serf/cmd/serf-hub 21.099s`.

## Files changed

- `cmd/serf-hub/assets/renderer.js` (F1 guards, F2 intent tracking/settle/marker)
- `cmd/serf-hub/assets/renderer-tools.js` (F3 honest tail-fold note)
- `cmd/serf-hub/assets/style.css` (F4 caret breathe)
- `cmd/serf-hub/jstest/test-renderer-hydration-stale-overlap.js` (new, F1)
- `cmd/serf-hub/jstest/test-renderer-hydration-scroll-intent.js` (new, F2)
- `cmd/serf-hub/jstest/test-tool-streaming-output.js` (F3 assertions)
- `cmd/serf-hub/jstest/test-renderer-surrogate-tail.js` (F3 prefix update)
- `cmd/serf-hub/jstest/test-renderer-streaming-tail.js` (F4 + F5 assertions)

## Concerns

- The `programmaticScroll` marker is synchronous; browser scroll events fire
  async, so it guards intent only because the hydration-end `scrollToBottom`
  runs after `hydrationInProgress` is cleared and all mid-hydration programmatic
  scrolls are suppressed (`suppressScrollSettle`) or guarded
  (`maybeLoadOlderTurns`). If a future change introduces an unguarded
  programmatic scroll inside the replay window, it must use the marker.
- `tailFoldOutput`'s note formats the budget with `toLocaleString("en-US")`
  (deterministic under Node ≥13 full-ICU and browsers).
