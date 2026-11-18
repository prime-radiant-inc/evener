import type { AnyNotification } from "@evener/appwire-client";
import {
  applyNotification,
  applyReadResponse,
  compactDuration,
  hydrateThread,
  STUCK_AFTER_MS,
} from "@evener/appwire-client";
import { waitingForQuotaText } from "@evener/appwire-client/state/navigation";
import { testThread } from "@evener/appwire-client/testing/reducerTestKit";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../../stores/connection";
import { sessionActivitySnapshot } from "../../../../stores/sessionActivity";
import { activityClient, activitySummary } from "../../../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { seedCurrentDelegate } from "../tools/currentDelegate.testFixture";
import { resetSubagentModuleStoreForTests, turnScopeKey, upsertSubagentRow } from "../tools/subagentModuleStore";
import { LivenessLine } from "./LivenessLine";
import { describeLiveness, formatExactGap, formatQuietBucket, formatRetryWait } from "./liveness";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  resetSubagentModuleStoreForTests();
  resetThreadsStoreForTests();
  resetConnectionStoreForTests();
});

test("renders nothing while the thread is not active, regardless of gap", () => {
  render(<LivenessLine lastFrameAt={0} now={999_999} active={false} sessionRef={undefined} turnId={undefined} />);
  expect(screen.queryByTestId("liveness-line")).toBeNull();
});

test("renders nothing for a fresh/active thread (gap below the quiet threshold)", () => {
  render(<LivenessLine lastFrameAt={1_000} now={1_500} active={true} sessionRef={undefined} turnId={undefined} />);
  expect(screen.queryByTestId("liveness-line")).toBeNull();
});

test("renders the quiet phrase once the gap crosses the quiet threshold", () => {
  render(<LivenessLine lastFrameAt={0} now={30_000} active={true} sessionRef={undefined} turnId={undefined} />);
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quiet");
  expect(el.textContent).toContain(formatQuietBucket(30_000));
});

test("renders the stalled phrase once the gap crosses the stuck threshold, for an active turn tracking no children at all", () => {
  render(<LivenessLine lastFrameAt={0} now={STUCK_AFTER_MS + 5_000} active={true} sessionRef="s1" turnId="turn_0" />);
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("stalled");
  expect(el.textContent).toContain(formatExactGap(STUCK_AFTER_MS + 5_000));
});

// Cadence's own doc comment ("pure: no timers, no Date.now()") is the
// precedent this component follows for the same reason: the *caller* (which
// already owns a clock for Cadence - Session.tsx's useNowTick) controls
// decay, so a stalled/quiet thread shows honest, static text between
// renders rather than a self-ticking illusion of activity.
test("does not self-tick: the rendered text never changes without a new `now` prop, however much real time passes", () => {
  vi.useFakeTimers();
  render(<LivenessLine lastFrameAt={0} now={30_000} active={true} sessionRef={undefined} turnId={undefined} />);
  const before = screen.getByTestId("liveness-line").textContent;

  vi.advanceTimersByTime(10 * 60_000); // 10 real minutes, no re-render

  expect(screen.getByTestId("liveness-line").textContent).toBe(before);
});

// --- running children, scoped by session and turn -------------------------

function seedRunningChildren(count: number) {
  const thread = seedCurrentDelegate("s1", "dlg_1", "running");
  const stable = thread.delegates![0]!;
  thread.delegates = Array.from({ length: count }, (_, index) => ({ ...stable, delegateId: `dlg_${index}` }));
  threadsStore.setState({ threads: new Map([["s1", thread]]) });
  for (const delegate of thread.delegates) {
    upsertSubagentRow(turnScopeKey("s1", "turn_0"), {
      rowKey: `dlg:${delegate.delegateId}`,
      delegateId: delegate.delegateId,
      resultPreview: "",
    });
  }
}

test("reads as waiting on one subagent instead of quiet once a running child is tracked for the active turn", () => {
  seedRunningChildren(1);
  render(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("waiting");
  expect(el.textContent).toBe(describeLiveness(60_000, true, 1).text);
});

// A tracked running child keeps the line on "waiting" however long the
// silence: the shared stuck rule never fires while a subagent runs.
test("a tracked running child is never reported stalled, however long the silence", () => {
  seedRunningChildren(1);
  render(<LivenessLine lastFrameAt={0} now={STUCK_AFTER_MS * 3} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").getAttribute("data-level")).toBe("waiting");
});

test("counts every running child once past the quiet threshold, pre-empting the quiet phrase too", () => {
  seedRunningChildren(3);
  render(<LivenessLine lastFrameAt={0} now={30_000} active={true} sessionRef="s1" turnId="turn_0" />);
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("waiting");
  expect(el.textContent).toBe(describeLiveness(30_000, true, 3).text);
});

test("a done/failed row alone does not explain a wait - falls back to the ordinary stalled decision", () => {
  const scopeKey = turnScopeKey("s1", "turn_0");
  upsertSubagentRow(scopeKey, { rowKey: "dlg:1", receiptStatus: "done", resultPreview: "ok" });
  upsertSubagentRow(scopeKey, { rowKey: "dlg:2", receiptStatus: "failed", resultPreview: "boom" });
  render(<LivenessLine lastFrameAt={0} now={185_000} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").textContent).toBe(describeLiveness(185_000, true, 0).text);
});

test("an undefined turnId (no active turn yet) reads as zero running children even if rows exist for a real turn in the same session", () => {
  seedRunningChildren(1);
  render(<LivenessLine lastFrameAt={0} now={185_000} active={true} sessionRef="s1" turnId={undefined} />);
  expect(screen.getByTestId("liveness-line").textContent).toBe(describeLiveness(185_000, true, 0).text);
});

test("a historical running receipt cannot explain a current wait without owner evidence", () => {
  upsertSubagentRow(turnScopeKey("s1", "turn_0"), {
    rowKey: "dlg:1",
    delegateId: "dlg_1",
    receiptStatus: "running",
    resultPreview: "",
  });
  render(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").textContent).toBe("Quiet ~1m");
});

test("an explicitly in-flight launch can explain a current wait before owner evidence arrives", () => {
  upsertSubagentRow(turnScopeKey("s1", "turn_0"), {
    rowKey: "call:1",
    launching: true,
    resultPreview: "",
  });
  render(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").textContent).toBe("Waiting on 1 subagent");
});

// --- model-call retries (kata 4zn8): the daemon reports what it is waiting on,
// so the line stops guessing "May be stalled" at a provider that is merely rate
// limiting. The retry arrives on ThreadModel.modelRetry and is passed straight
// through; this component adds no clock and no inference of its own.

// A raw ModelRetryState as ThreadModel.modelRetry carries it (reducer.ts):
// unlike RetryWait, `model` is the retry's own model UNNARROWED, and
// `receivedAt` is the reducer's client-side stamp of when the notification
// landed - LivenessLine.tsx derives RetryWait's `model`/`inProgress` fields
// from these plus `now`/`lastFrameAt`/`primaryModel`, none of which the raw
// wire state carries an opinion on. Defaults here describe a retry reported
// at t=0, 5s into its call, still within its own delay - so most tests only
// need to override what they're actually pinning.
function rawRetry(overrides: Partial<Parameters<typeof LivenessLine>[0]["retry"]> = {}) {
  return {
    attempt: 9,
    maxAttempts: 11,
    attemptCap: 11,
    delayMs: 60_000,
    errorClass: "rate_limit",
    groupElapsedMs: 5_000,
    receivedAt: 0,
    ...overrides,
  } as NonNullable<Parameters<typeof LivenessLine>[0]["retry"]>;
}

test("renders the retry cause and wait instead of the quiet phrase", () => {
  render(
    <LivenessLine lastFrameAt={0} now={30_000} active={true} sessionRef="s1" turnId="turn_0" retry={rawRetry()} />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("retrying");
  expect(el.textContent).toBe("rate limited — attempt 9/11 — retrying in 60s — 5s on this call");
});

test("past the stuck threshold a retry still surfaces the silence rather than hiding it", () => {
  render(
    <LivenessLine lastFrameAt={0} now={630_000} active={true} sessionRef="s1" turnId="turn_0" retry={rawRetry()} />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("stalled");
  expect(el.textContent).toContain("9/11");
  expect(el.textContent).toContain(formatExactGap(630_000));
});

// Component 1's second honesty rule: the denominator must be the effective
// bound (AttemptCap), which can differ from the raw policy budget once a
// consume-phase failure drops it - a fail-fast group at attempt 3 of a
// cap-4 group, not the maxAttempts:11 the notification also still carries.
test("renders the attemptCap denominator, not maxAttempts", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={rawRetry({ attempt: 3, maxAttempts: 11, attemptCap: 4, delayMs: 5_000, receivedAt: 30_000 })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).toBe(
    "rate limited — attempt 3/4 — retrying in 5s — 5s on this call",
  );
});

// A hub too old to send AttemptCap reports it as 0 (attemptCap is a required
// wire field, but an old daemon's payload omits it, so the reducer stores the
// JSON zero value) - rendering "attempt 3/0" would promise a denominator that
// was never honest, so the chip drops the "/N" entirely rather than
// substituting maxAttempts or any other budget.
test("omits the denominator entirely when attemptCap is 0 (a hub too old to send it)", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={rawRetry({ attempt: 3, maxAttempts: 11, attemptCap: 0, delayMs: 5_000, receivedAt: 30_000 })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).toBe(
    "rate limited — attempt 3 — retrying in 5s — 5s on this call",
  );
});

test("shows the retry's model tag when a fallback chain walk switched off the session's primary model", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      primaryModel="claude-opus-4"
      retry={rawRetry({ attempt: 1, attemptCap: 4, delayMs: 5_000, model: "gpt-5", receivedAt: 30_000 })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).toBe(
    "rate limited (gpt-5) — attempt 1/4 — retrying in 5s — 5s on this call",
  );
});

test("omits the model tag when the retry's model matches the session's primary model - same model, still failing", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      primaryModel="claude-opus-4"
      retry={rawRetry({ attempt: 1, attemptCap: 4, delayMs: 5_000, model: "claude-opus-4", receivedAt: 30_000 })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).toBe(
    "rate limited — attempt 1/4 — retrying in 5s — 5s on this call",
  );
});

// A model switch is exactly what wakes a quota wait, and the zero-delay wake
// still carries the model that HIT the limit rather than the one now running.
// If the resumed call outlasts the quiet threshold, a tag taken from that frame
// labels a live call with the provider that failed and is no longer in use.
test("omits the model tag on a zero-delay quota wake, which names the model that failed rather than the one running", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      primaryModel="claude-opus-4"
      retry={rawRetry({
        attempt: 1,
        attemptCap: 4,
        delayMs: 0,
        errorClass: "quota_exceeded",
        model: "gpt-5",
        receivedAt: 30_000,
      })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).not.toContain("gpt-5");
});

// The wait's own announcement keeps its tag: there the named model is the one
// that failed and is still the one waiting.
test("keeps the model tag on a quota wait that is still counting down", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      primaryModel="claude-opus-4"
      retry={rawRetry({
        attempt: 1,
        attemptCap: 4,
        delayMs: 5_000,
        errorClass: "quota_exceeded",
        model: "gpt-5",
        receivedAt: 30_000,
      })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).toContain("gpt-5");
});

// The production path a stamped wait takes: a real quota announcement names
// its turn and its daemon-issued stamp, so the component hands the wait to
// the quota branch - which must still carry the live announcement's model
// tag and the call's elapsed time beside the countdown. A snapshot-only wait
// (a re-hydrated pane with no announcement) keeps the countdown free of both:
// the snapshot cannot establish a model or an elapsed time.
test("a stamped same-turn quota wait keeps its model tag and elapsed time on the quota line", () => {
  const now = 30_000;
  // The pane re-hydrated while the wait ran, so the store carries the daemon
  // deadline the countdown counts to; the live announcement carries the tag
  // and the call's elapsed time.
  reloadWithQuotaWait(60 * 60_000, now);
  render(
    <LivenessLine
      lastFrameAt={0}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      primaryModel="claude-opus-4"
      retry={rawRetry({
        attempt: 1,
        attemptCap: 4,
        delayMs: 60 * 60_000,
        errorClass: "quota_exceeded",
        model: "gpt-5",
        groupElapsedMs: 14 * 60_000,
        turnId: "turn_0",
        waitStartedAt: 10_000,
        receivedAt: now,
      })}
    />,
  );
  const line = screen.getByTestId("liveness-line");
  expect(line.textContent).toContain("gpt-5");
  expect(line.textContent).toContain(formatExactGap(14 * 60_000));
  // The snapshot-only wait stays free of both: a re-hydrated pane with no
  // live announcement cannot establish a model or an elapsed time.
  cleanup();
  reloadWithQuotaWait(60 * 60_000, now);
  render(
    <LivenessLine
      lastFrameAt={0}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      primaryModel="claude-opus-4"
    />,
  );
  const snapshot = screen.getByTestId("liveness-line");
  expect(snapshot.textContent).not.toContain("gpt-5");
});

test("shows the retry group's elapsed time from groupElapsedMs", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={rawRetry({ groupElapsedMs: 14 * 60_000 })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).toContain(formatExactGap(14 * 60_000));
});

// Web-only difference from the TUI (task 11 brief): once a delta has landed
// since the retry was reported, the wait it described is over, so the line
// says "in progress" instead of leaving a stale "retrying in 60s" countdown
// sitting there next to live output.
test("renders 'in progress' once a frame has landed since the retry was reported, instead of clearing", () => {
  render(
    <LivenessLine
      lastFrameAt={5_000}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={rawRetry({ receivedAt: 0 })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).toBe(
    "rate limited — attempt 9/11 — in progress — 5s on this call",
  );
});

// The other half of the same rule (spec: "delay expired, or a delta
// arrived"): even with no frame yet, once the reported delay has elapsed the
// wait itself is over, so the line stops counting down a delay that's
// already spent.
test("renders 'in progress' once the reported delay has elapsed, even with no new frame yet", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={70_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={rawRetry({ receivedAt: 0, delayMs: 60_000 })}
    />,
  );
  expect(screen.getByTestId("liveness-line").textContent).toBe(
    "rate limited — attempt 9/11 — in progress — 5s on this call",
  );
});

// --- a usage-limit wait (issue #6657) ---------------------------------------
//
// A model call that meets a usage limit naming its reset waits inside itself.
// The daemon samples that wait into the thread read's failure summary, so a
// client that RELOADS can still say when the call resumes; the live
// evener/thread/modelRetry that also described it is ephemeral and gone. These
// cases seed the store with a model built by hydrateThread from a read - the
// same way a reload rebuilds one - rather than by setting the field.

// A usage-limit wait as a reload rebuilds it: a thread read whose failure names
// the reset instant, hydrated into a ThreadModel. `resumesInMs` is measured from
// the test's own clock, so the model carries the same instant a real read of
// that daemon would have carried at that moment.
function reloadWithQuotaWait(resumesInMs: number, nowMs: number): void {
  const thread = testThread({
    status: { type: "active" },
    evener: {
      failure: { title: "Usage limit reached", resumesAt: nowMs + resumesInMs },
    },
  });
  // Hydrated under the ref the pane looks the session up by.
  const model = hydrateThread({ thread }, "s1", nowMs);
  threadsStore.setState({ threads: new Map([["s1", model]]) });
}

test("a reloaded session says when its usage-limit wait resumes, without any live retry frame", () => {
  const now = 1_000_000;
  reloadWithQuotaWait(6 * 60 * 60_000, now);
  render(<LivenessLine lastFrameAt={0} now={now} active={true} sessionRef="s1" turnId="turn_0" />);

  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  // The shared wait words, from the one source both this pane and the rail
  // read - asserted through the same function, never against a literal.
  expect(el.textContent).toBe(waitingForQuotaText(6 * 60 * 60_000));
});

// The common trigger, and the one this issue was filed from: a reader has the
// pane open when the limit hits. Nothing forces a read then - the retry
// notification never invalidates history - so ThreadModel.quotaResumesAt is
// never set, and before this the line stayed on the retry's own seconds
// countdown and then fell to unexplained silence for hours. The live retry's
// delay, measured from the client-side receipt stamp, is the remaining wait.
test("a usage-limit retry with no read at all still says when the wait resumes", () => {
  const now = 1_000_000;
  const receivedAt = now - 5_000;
  // Hydrated clean: no failure, so the read has told the model nothing about a
  // wait and only the live notification below can.
  const clean = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now);
  expect(clean.quotaResumesAt).toBeUndefined();
  const sixHours = 6 * 60 * 60_000;
  const live = applyNotification(
    clean,
    {
      method: "evener/thread/modelRetry",
      params: {
        threadId: "thr_1",
        ref: "s1",
        turnId: "turn_0",
        attempt: 1,
        maxAttempts: 0,
        delayMs: sixHours,
        errorClass: "quota_exceeded",
        groupElapsedMs: 5_000,
        attemptCap: 0,
      },
    },
    receivedAt,
  );
  expect(live.quotaResumesAt).toBeUndefined();
  threadsStore.setState({ threads: new Map([["s1", live]]) });

  render(
    <LivenessLine
      lastFrameAt={receivedAt}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={live.modelRetry}
    />,
  );

  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  expect(el.textContent).toBe(waitingForQuotaText(sixHours - 5_000));
});

// A stamped wait has one time basis: the daemon's. Its instant is the start
// the daemon issued plus the delay, and the receipt-based arm must not stand
// behind it - the two differ by this reader's clock skew. This pane's clock
// runs an hour fast, so the receipt says the two-hour wait began only an hour
// ago while the daemon's instant has already passed. Mixing the bases made
// the countdown reach zero and then leap back to the receipt's hour.
//
// Past the deadline the wait is still the pane's to show: the countdown's time
// basis is the daemon's deadline, and its lifetime is the daemon's WAIT - not
// this client's clock crossing a reset. Reading the crossing as the wait's end
// is how an hours-long scheduled wait (a delegate that reached its reset with
// every execution slot taken keeps the wait published until it reacquires one
// and emits the wake) renders as a stall warning while the daemon still says
// it is waiting. So the line keeps the quota level and says which half of the
// wait it is in, and the two things it must never claim are the ones the old
// reading fell through to: the receipt-basis duration, and a stall marker.
test("a stamped usage-limit wait past its daemon deadline holds the wait and claims no retry duration or stall", () => {
  const waitStartedAt = 1_000_000;
  const twoHours = 2 * 60 * 60_000;
  const skew = 60 * 60_000;
  // The daemon's instant passed by `skew`, and this pane received the
  // announcement an hour ago on its own fast clock.
  const now = waitStartedAt + twoHours + skew;
  const model = hydrateThread(
    {
      thread: testThread({
        status: { type: "active" },
        evener: { failure: { title: "Usage limit reached", resumesAt: waitStartedAt + twoHours, waitStartedAt } },
      }),
    },
    "s1",
    waitStartedAt,
  );
  threadsStore.setState({ threads: new Map([["s1", model]]) });
  const announced = applyNotification(
    model,
    {
      method: "evener/thread/modelRetry",
      params: {
        threadId: "thr_1",
        ref: "s1",
        turnId: "turn_0",
        attempt: 1,
        maxAttempts: 0,
        delayMs: twoHours,
        errorClass: "quota_exceeded",
        groupElapsedMs: 5_000,
        attemptCap: 0,
        waitStartedAt,
      },
    } as AnyNotification,
    now - skew,
  );

  render(
    <LivenessLine
      lastFrameAt={now - skew}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={announced.modelRetry}
    />,
  );

  const el = screen.queryByTestId("liveness-line");
  // The defect this guards is subtler than a wrong level: suppressing the
  // quota chip while still handing the retry to the generic formatter renders
  // the RECEIPT duration - "retrying in 7200s" - and past the stuck threshold
  // hangs it off a stall warning, for a wait the daemon's own instant already
  // ended. The pane then states a duration on one clock basis beneath a
  // verdict the other clock basis produced. Both are absent here: the line
  // says what it says about the silence with the retry contributing nothing.
  //
  // Asserted as the two things the line must not claim, computed from the same
  // formatters the component uses rather than pinned as copy: the retry's own
  // line text appears nowhere, and the decision equals the retry-free one.
  const gap = now - (now - skew);
  const retryLine = formatRetryWait({
    attempt: 1,
    attemptCap: 0,
    delayMs: twoHours,
    errorClass: "quota_exceeded",
    groupElapsedMs: 5_000,
    inProgress: false,
  });
  expect(el?.textContent ?? "").not.toContain(retryLine);
  // Not the silence-derived level: the wait is still recorded, so the line
  // states the wait rather than inferring anything from the silence.
  expect(el?.getAttribute("data-level")).toBe("quota");
  expect(el?.getAttribute("data-level")).not.toBe(describeLiveness(gap, true, 0).level);
  expect(el?.className ?? "").not.toContain("stalled");
  // And the wait's own state survives the crossing: the level stays the
  // quota one (not silence-derived, not stalled), the countdown's duration is
  // gone (the reset is behind it), and a matching wake then clears the wait
  // entirely. Asserted as behavior - which states the line occupies and which
  // facts remain - never as copy.
  expect(el?.getAttribute("data-level")).toBe("quota");
  expect(el?.textContent ?? "").not.toContain(compactDuration(twoHours));
  expect(el?.className ?? "").not.toContain("stalled");
  // The line still states a wait (not silence): its level differs from the
  // retry-free, wait-free reading of the same gap.
  expect(el?.getAttribute("data-level")).not.toBe(describeLiveness(gap, true, 0).level);
});

// The regression this guards: over a six-hour wait plenty of frames land that
// have nothing to do with the waiting call, and every thread-scalar one
// restamps lastFrameAt - here a background job finishing. RetryWait's usual
// rule ("any later frame means the call moved") is right for a short backoff
// and wrong for a wait this long: keying on it dropped the chip and fell back
// to "in progress" and then to unexplained silence, which is exactly the
// reading issue #6657 forbids.
test("a background job finishing during a usage-limit wait does not end the wait", () => {
  const now = 1_000_000;
  const receivedAt = now - 5_000;
  const sixHours = 6 * 60 * 60_000;
  let model = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", receivedAt);
  model = applyNotification(
    model,
    {
      method: "evener/thread/modelRetry",
      params: {
        threadId: "thr_1",
        ref: "s1",
        turnId: "turn_0",
        attempt: 1,
        maxAttempts: 0,
        delayMs: sixHours,
        errorClass: "quota_exceeded",
        groupElapsedMs: 5_000,
        attemptCap: 0,
      },
    },
    receivedAt,
  );
  // A job ending mid-wait: scalar-only, so it restamps lastFrameAt and never
  // touches modelRetry.
  model = applyNotification(
    model,
    {
      method: "evener/job/finished",
      params: {
        threadId: "thr_1",
        ref: "s1",
        job: { jobId: "job_1", jobType: "shell", status: "finished", outputBytes: 0 },
      },
    },
    now - 1_000,
  );
  expect(model.lastFrameAt).toBeGreaterThan(model.modelRetry?.receivedAt ?? 0);
  expect(model.modelRetry?.errorClass).toBe("quota_exceeded");
  threadsStore.setState({ threads: new Map([["s1", model]]) });

  render(
    <LivenessLine
      lastFrameAt={model.lastFrameAt}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={model.modelRetry}
    />,
  );

  expect(screen.getByTestId("liveness-line").getAttribute("data-level")).toBe("quota");
});

// modelRetry is not cleared on Stop, so a new turn can still carry the dead
// wait's six-hour retry. The guard is the retry's own turn identity: it
// speaks only for the turn it named, so nothing needs a frame clock to tell.
test("a usage-limit retry left over from an earlier turn does not explain a new turn", () => {
  const now = 1_000_000;
  const sixHours = 6 * 60 * 60_000;
  let model = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now - 5_000);
  model = applyNotification(
    model,
    {
      method: "evener/thread/modelRetry",
      params: {
        threadId: "thr_1",
        ref: "s1",
        turnId: "turn_stopped",
        attempt: 1,
        maxAttempts: 0,
        delayMs: sixHours,
        errorClass: "quota_exceeded",
        groupElapsedMs: 5_000,
        attemptCap: 0,
      },
    },
    now - 5_000,
  );
  threadsStore.setState({ threads: new Map([["s1", model]]) });

  // The pane is now running a different turn.
  render(
    <LivenessLine
      lastFrameAt={model.lastFrameAt}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_new"
      retry={model.modelRetry}
    />,
  );

  const el = screen.queryByTestId("liveness-line");
  expect(el?.getAttribute("data-level") ?? "none").not.toBe("quota");
});

// The reconnect shape: this pane dropped while the limit held, another client
// switched models, and the same turn resumed and went on to a long tool. The
// read now correctly names no resume time, but a read omits what has ended and
// the merge keeps every field it omits - so the retained retry survived with
// its turnId still matching and its countdown still running, and revived a
// quota chip over work that had resumed. The read is the authority on whether a
// wait remains; retry it here, in the reconcile.
test("a read showing the turn resumed retires a quota retry retained from before the reconnect", () => {
  const now = 1_000_000;
  const sixHours = 6 * 60 * 60_000;
  let model = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now - 5_000);
  model = applyNotification(
    model,
    {
      method: "evener/thread/modelRetry",
      params: {
        threadId: "thr_1",
        ref: "s1",
        turnId: "turn_0",
        attempt: 1,
        maxAttempts: 0,
        delayMs: sixHours,
        errorClass: "quota_exceeded",
        groupElapsedMs: 5_000,
        attemptCap: 0,
      },
    },
    now - 5_000,
  );
  expect(model.modelRetry?.errorClass).toBe("quota_exceeded");

  // The reconnect read: the same turn, still running, and NO wait - the
  // failure summary names no resume time because there is none.
  const resumed = applyReadResponse(
    model,
    {
      thread: testThread({ status: { type: "active" }, evener: {} }),
      requestGeneration: (model.history?.issuedGeneration ?? 0) + 1,
    },
    now,
  );
  expect(resumed.quotaResumesAt).toBeUndefined();

  threadsStore.setState({ threads: new Map([["s1", resumed]]) });
  render(
    <LivenessLine
      lastFrameAt={resumed.lastFrameAt}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={resumed.modelRetry}
    />,
  );

  const el = screen.queryByTestId("liveness-line");
  expect(el?.getAttribute("data-level") ?? "none").not.toBe("quota");
});

// A six-hour reset is not 21,600 seconds of a wedged session, and the chip's
// own silence gate is for unexplained silence: a wait whose reset is known
// shows at once, and keeps saying hours rather than counting seconds down.
test("a usage-limit wait shows immediately, in hours rather than a seconds countdown", () => {
  const now = 1_000_000;
  reloadWithQuotaWait(21_600_000, now);
  // 3s of quiet: far under the 20s gate that guards inferred silence.
  render(<LivenessLine lastFrameAt={now - 3_000} now={now} active={true} sessionRef="s1" turnId="turn_0" />);

  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  expect(el.textContent).not.toContain("21600s");
  expect(el.textContent).toBe(waitingForQuotaText(21_600_000));
});

// The instant passing is not the wait ending. The daemon publishes the wait
// until it can resume the call - a delegate at its provider reset with every
// execution slot taken stays waiting, possibly for hours - and the pane has no
// evidence the wait is over until a wake, a turn boundary, or an authoritative
// read says so. So past the instant the line keeps the wait and changes what it
// promises: no longer a future countdown, but the reset that has passed and
// the retry that has not yet gone out.
test("a usage-limit wait whose instant has passed keeps explaining the silence as pending", () => {
  const now = 1_000_000;
  reloadWithQuotaWait(60_000, now);
  render(<LivenessLine lastFrameAt={now} now={now + 60_000} active={true} sessionRef="s1" turnId="turn_0" />);

  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  expect(el.textContent).not.toContain("resumes in");
  expect(el.textContent).toContain("reset passed");
});

// --- the hub's count (#3797) ------------------------------------------------

// The hub's count of running subagents, known only from the subtree activity
// summary, so a line that says it proves the subtree read was the source.
function hubCounting(known: boolean, active: number) {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    delegates: { known: known && scope === "subtree", total: active, active, failed: 0, completed: 0 },
  }));
  connectionStore.getState().connect(client);
  return client;
}

// The hub counts a session's running subagents one way everywhere: those whose
// run is open, at every depth (the subtree activity summary, the Live row's
// tally, the phone's Board). Once that count is known it is the one the line
// says, whatever this pane's transcript rows hold - here two subagents a level
// down that the active turn's own rows never show.
test("says the hub's subtree count of running subagents once it is known", async () => {
  hubCounting(true, 2);
  render(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  await waitFor(() => expect(screen.getByTestId("liveness-line").textContent).toBe("Waiting on 2 subagents"));
});

// Once known, the hub's count wins over the turn's rows downward too: a row
// still marked running for a child whose run the hub has seen end explains
// nothing.
test("a known hub count of zero overrides a turn row still marked running", async () => {
  seedRunningChildren(1);
  hubCounting(true, 0);
  render(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  await waitFor(() => expect(screen.getByTestId("liveness-line").textContent).toBe("Quiet ~1m"));
});

// Until the hub knows its count (an unindexed journal, an older hub), the
// active turn's rows stand in.
test("an unknown hub count falls back to the active turn's rows", async () => {
  seedRunningChildren(1);
  const client = hubCounting(false, 0);
  render(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  await waitFor(() => expect(sessionActivitySnapshot(client, "s1", "subtree")?.summary).toBeTruthy());
  expect(screen.getByTestId("liveness-line").textContent).toBe("Waiting on 1 subagent");
});

// The line holds the hub's count for its session whether or not a turn is
// running, so when a turn starts the count is already there: its first
// words are the hub's, never a flash of the turn's own rows first.
test("a turn's first words are the hub's count, read before the turn started", async () => {
  seedRunningChildren(1);
  const client = hubCounting(true, 2);
  const view = render(<LivenessLine lastFrameAt={0} now={60_000} active={false} sessionRef="s1" turnId="turn_0" />);
  await waitFor(() => expect(sessionActivitySnapshot(client, "s1", "subtree")?.summary).toBeTruthy());
  view.rerender(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").textContent).toBe("Waiting on 2 subagents");
});
