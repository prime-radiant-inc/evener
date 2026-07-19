# Iteration-5 fix report

Branch `webui-joy`, worktree `webui-joy`. Scope: `cmd/serf-hub/assets/renderer.js` + `cmd/serf-hub/jstest/`. Red-first: every test below was run against the pre-fix renderer (via `git stash push -- cmd/serf-hub/assets/renderer.js`) and failed; all pass after the fix.

## F1 (Important): stale hydration's `finally` clobbers the successor's scroll-settle suppression

**Trace (confirmed in code).** `connectAppwire`'s `readThread().then(async …)` runs a chunked replay (`HYDRATION_CHUNK = 150`, `await setTimeout(0)` between slices). H1 suspends at its first yield; a reconnect starts H2, which sets `hydrationInProgress`/`suppressScrollSettle` and suspends mid-replay itself. H1 resumes, fails the `this.liveStream !== appwireStream` staleness check (~line 915 pre-fix) and `return`s — but its unconditional outer `finally` still ran `this.suppressScrollSettle = false` while H2 owned the stream, stripping H2's O(N²)-avoidance suppression mid-replay and re-enabling replay-time scroll/pill side effects.

**Audit of sibling flags in the same block.** `hydrationInProgress = false` (line ~943) and `replayingBufferedNotifications = false` (inner finally, ~941) sit after the last staleness check with no intervening `await`, so a stale H1 cannot reach them today — but they are the same shared-flag pattern and were hardened with the same ownership guard. The `.catch` path already guards (`if (this.liveStream !== appwireStream) return;`). Converse hygiene: since a stale finally no longer clears `suppressScrollSettle`, `init()` now resets it (it already reset `hydrationInProgress`) so the flag can never leak into a session whose hydration never ran (a non-appwire view would otherwise suppress stick-to-bottom and the new-content pill forever).

**Red evidence** (`jstest/test-renderer-hydration-stale-overlap.js`, new section (c); H1 = 300 events, H2 = 600 events, H2 started at H1's first yield; `suppressScrollSettle` instrumented with an accessor to catch any true→false transition landing while H2 owns the replay):

```
FAIL: (c) H1's stale finally did NOT clear suppressScrollSettle under H2's mid-replay ownership
```

**Change** (`assets/renderer.js`, hydration completion block): all three flag clears are now ownership-guarded —

```js
} finally {
  if (this.liveStream === appwireStream) this.replayingBufferedNotifications = false;
}
if (this.liveStream === appwireStream) this.hydrationInProgress = false;
} finally {
  // Stale-overlap guard: a superseded hydration that returned at one of the
  // staleness aborts above still runs THIS finally … Only the stream owner
  // clears the flag; the successor clears it in its own finally.
  if (this.liveStream === appwireStream) this.suppressScrollSettle = false;
}
```

plus `this.suppressScrollSettle = false;` in `init()` next to the existing `hydrationInProgress` reset.

**Green evidence:** full file passes — `PASS: stale overlapping readThread completion/rejection is guarded before any state mutation` (sections (a) stale-.then, (b) stale-.catch, (c) mid-replay clobber; section (c) also asserts H2 completes with both flags cleared and the transcript holding exactly H2's 600 events).

## F2 (Minor): `init()` zeros the prepend-settle counters but doesn't cancel the keyed frame callbacks

**Trace.** `init()` zeroed `programmaticScrollDepth`/`prependSettleHolds` but left session A's keyed `"prepend-settle"`/`"prepend-settle-release"` frame callbacks scheduled. A stale release firing into session B drains B's hold accumulator (`programmaticScrollDepth = Math.max(0, depth - holds)`), so B's correction-window scroll event lands with depth 0 and reads as reader intent (`readerScrolledDuringHydration`).

**Red evidence** (`jstest/test-renderer-prepend-settle-release.js`, new section (e); keyed-cancellation mirrored into the test's `scheduleFrame` stub; session B is a fresh conversation element because `init()` is idempotent per element):

```
FAIL: (e) init also cancelled session A's keyed prepend-settle frames
FAIL: (e) the stale session-A release callback is inert after init
FAIL: (e) firing the stale callback left B's holds untouched (holds=0)
FAIL: (e) firing the stale callback left B's depth untouched (depth=0)
```

**Change** (`assets/renderer.js`, `init()`): alongside the zeroing —

```js
this.cancelFrame("prepend-settle");
this.cancelFrame("prepend-settle-release");
```

**Green evidence:** `PASS: prepend-settle depth release is frame-chained (ordering, hold window, accumulator drain)` — sections (a)–(d) unchanged and passing; (e) asserts init zeroes the counters, cancels both keyed frames, the stale callback is inert, B's holds/depth stay at 1, and B's own settle/release drains normally.

## F3 (Minor): transient "↓ 0 new" pill visible for ~300ms

**Trace.** `renderNewContentPill`'s plain-count branch paints the *debounced* `newContentPaintedCount`, which is 0 on the hidden→visible first paint while `newContentCount` is already positive; the trailing 300ms debounce in `scheduleNewContentCountPaint` then commits the real count. Net effect: the pill badges "↓ 0 new" for the entire first debounce window — exactly what the browser probe caught. (`noteNewContent(0)` is not reachable: its only call site guards `added > 0`.)

**Red evidence** (`jstest/test-renderer-new-content-pill.js`, new "never renders a zero count" section):

```
FAIL: first paint never reads '↓ 0 new' (got ↓ 0 new)
FAIL: first paint shows a positive count (got ↓ 0 new)
```

**Change** (`assets/renderer.js`):

1. `renderNewContentPill`: capture `wasHidden` before un-hiding; on the hidden→visible transition seed `newContentPaintedCount = newContentCount`. Repaints while already visible keep the debounced value, so the existing anti-jitter guarantee ("count does not repaint to the final total mid-burst") is preserved.
2. `scheduleNewContentCountPaint`: the trailing commit bails to `clearNewContentPill()` when `newContentCount <= 0`, so no path can ever paint 0.

**Green evidence:** `PASS: new-content pill counts, clears, and goes attention-aware` — all pre-existing sections (including the mid-burst anti-jitter assertions) plus the new zero-count section pass: first paint shows a positive count immediately, trailing commit never shows 0.

## Gate

```
$ cd cmd/serf-hub/jstest && ./run-all.sh
OK      test-abbreviate-model.js
…
OK      test-transcript-windowing.js
OK      test-turn-meta-badge.js
jstest: all tests passed          # 179 OK, 0 FAIL/TIMEOUT/MISSING

$ make build-hub
LDFLAGS="…" scripts/build-runtime-pair.sh   # exit 0

$ GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub
ok  	primeradiant.com/serf/cmd/serf-hub	21.027s
```

Note: two in-harness background invocations of `run-all.sh` were reaped mid-suite
(the suite runs ~7 minutes; the job wrapper did not survive). The final gate run
was executed detached (`nohup ./run-all.sh`) and completed end-to-end with
"jstest: all tests passed"; the mid-run prefix (169 tests) and a manual run of
the remaining 10 test files had also already passed independently.
