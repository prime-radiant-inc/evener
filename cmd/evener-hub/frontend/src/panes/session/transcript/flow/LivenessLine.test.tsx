import { applyNotification, applyReadResponse, hydrateThread, STUCK_AFTER_MS } from "@evener/appwire-client";
import { quotaWaitState } from "@evener/appwire-client/state/navigation";
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
import { describeLiveness, formatExactGap, formatQuietBucket } from "./liveness";

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
      failure: { title: "Usage limit reached", resumesAt: nowMs + resumesInMs, waitStartedAt: 1 },
    },
  });
  // Hydrated under the ref the pane looks the session up by.
  const model = hydrateThread({ thread }, "s1", nowMs);
  threadsStore.setState({ threads: new Map([["s1", model]]) });
}

// A stamped quota-wait announcement as the pane's subscription delivers it:
// the wait's identity (the daemon's sequence stamp), its absolute resume
// instant, and the chip's metadata. Overrides carry only what a case varies;
// the delay defaults to a six-hour reset's runway.
function quotaRetryFrame(
  waitStartedAt: number,
  overrides: { resumesAt?: number; delayMs?: number; model?: string; groupElapsedMs?: number; turnId?: string } = {},
) {
  return {
    method: "evener/thread/modelRetry" as const,
    params: {
      threadId: "thr_1",
      ref: "s1",
      turnId: overrides.turnId ?? "turn_0",
      attempt: 1,
      maxAttempts: 0,
      delayMs: overrides.delayMs ?? 21_600_000,
      errorClass: "quota_exceeded" as const,
      groupElapsedMs: overrides.groupElapsedMs ?? 5_000,
      attemptCap: 0,
      waitStartedAt,
      ...(overrides.resumesAt !== undefined ? { resumesAt: overrides.resumesAt } : {}),
      ...(overrides.model !== undefined ? { model: overrides.model } : {}),
    },
  };
}

test("a reloaded session says when its usage-limit wait resumes, without any live retry frame", () => {
  const now = 1_000_000;
  reloadWithQuotaWait(6 * 60 * 60_000, now);
  render(<LivenessLine lastFrameAt={0} now={now} active={true} sessionRef="s1" turnId="turn_0" />);

  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  // The wait's computed runway as a fact: the level says the wait stands,
  // this says the instant it promises. No copy is asserted.
  expect(el.getAttribute("data-remaining-ms")).toBe(String(6 * 60 * 60_000));
  expect(el.getAttribute("data-elapsed-ms")).toBeNull();
});

// The anti-installation contract's pane half: a daemon that predates the
// stamp announces the wait with no stamp, and that announcement renders
// NOTHING at the quota level — no receipt-clock countdown, no chip, no
// elapsed. The pane shows no wait on an older daemon, period; its row still
// shows the wait from the owning root's roster sample (a different producer).
// What the line falls back to is the ordinary retry text, exactly as before
// the quota-wait stack existed.
test("an unstamped announcement shows no quota wait in the pane", () => {
  const now = 1_000_000;
  // Hydrated clean: no failure, no stamp — only the live notification below
  // can say anything, and (an older daemon) it carries no stamp.
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
    now - 5_000,
  );
  expect(live.quotaResumesAt).toBeUndefined();
  expect(live.quotaWaitStartedAt).toBeUndefined();
  threadsStore.setState({ threads: new Map([["s1", live]]) });

  render(
    <LivenessLine
      lastFrameAt={now - 5_000}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={live.modelRetry}
    />,
  );

  // The frame installed no wait and no chip, so under the quiet gate the
  // pane renders no line at all — exactly the pre-stack reading of an older
  // daemon's wait: silence, never a receipt-clock countdown.
  expect(screen.queryByTestId("liveness-line")).toBeNull();
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
        waitStartedAt: 1,
        resumesAt: receivedAt + sixHours,
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

// The wait's own wake (its retry going out) leaves a zero-delay progress
// chip, and that chip is not cleared on Stop, so a new turn can still carry
// the dead turn's progress state. A progress state is not a wait: it renders
// ordinary retry text, never the quota level, and the chip's own turn
// identity still speaks only for the turn it named.
test("a usage-limit retry left over from an earlier turn does not explain a new turn", () => {
  const now = 1_000_000;
  const sixHours = 6 * 60 * 60_000;
  let model = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now - 5_000);
  // The wait starts on the turn that is about to end, then its wake goes out:
  // the wait's fields clear, the progress chip stays.
  model = applyNotification(
    model,
    quotaRetryFrame(2, { resumesAt: now + sixHours, delayMs: sixHours, turnId: "turn_stopped" }),
    now - 5_000,
  );
  model = applyNotification(model, quotaRetryFrame(2, { delayMs: 0, turnId: "turn_stopped" }), now - 4_000);
  expect(model.quotaResumesAt).toBeUndefined();
  expect(model.modelRetry?.delayMs).toBe(0);
  expect(model.modelRetry?.turnId).toBe("turn_stopped");
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
        waitStartedAt: 3,
        resumesAt: now + sixHours,
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
  // The units are the renderer's; the test pins the computed runway and that
  // the line never degrades to a raw-seconds countdown of the whole reset
  // (the promise's own level, not its phrasing).
  expect(el.getAttribute("data-remaining-ms")).toBe(String(21_600_000));
  expect(el.textContent).not.toContain("21600s");
});

// Contract point 3: the wait's lifetime is the daemon's, never the client's
// clock. Crossing the reset instant changes the line from countdown to
// pending; it does not end the wait - the daemon keeps serving the wait's
// summary with its original instant while the wait stands (a delegate held
// at its reset while every execution slot is occupied can stay waiting for
// hours), so the pane keeps explaining the silence until the wake, a turn
// boundary, or a read showing no wait ends it.
test("a usage-limit wait past its reset instant reads pending, not silence", () => {
  const now = 1_000_000;
  reloadWithQuotaWait(60_000, now);
  render(<LivenessLine lastFrameAt={now} now={now + 60_000} active={true} sessionRef="s1" turnId="turn_0" />);

  // The phase, asserted structurally: the level stays quota and the line
  // carries the pending marker, never a rendered sentence as the oracle.
  // The model-level half goes through the shared selector.
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  expect(el.getAttribute("data-passed-reset")).toBe("true");
  const model = threadsStore.getState().threads.get("s1");
  expect(quotaWaitState(model?.quotaResumesAt, now + 60_000)).toEqual({ phase: "pending" });
});

// Contract point 4 + scenario 13's rendering half: a wait established by a
// live announcement carries the call's model tag and elapsed time, and both
// survive the reset crossing unchanged - the crossing changes the promise,
// never the facts beside it. The elapsed advances from the announcement's
// receipt instant (sample + time since receipt).
test("a stamped wait's model tag and advancing elapsed ride both halves of the line", () => {
  const now = 1_000_000;
  const sixHours = 6 * 60 * 60_000;
  let model = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now - 30_000);
  model = applyNotification(
    model,
    quotaRetryFrame(2, {
      resumesAt: now + sixHours,
      delayMs: sixHours,
      model: "wait/model",
      groupElapsedMs: 14 * 60_000,
    }),
    now - 30_000,
  );
  expect(model.quotaWaitStartedAt).toBe(2);
  threadsStore.setState({ threads: new Map([["s1", model]]) });

  // Countdown half: the tag and the advanced elapsed ride beside the promise.
  const { rerender } = render(
    <LivenessLine
      lastFrameAt={now - 30_000}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={model.modelRetry}
      primaryModel="primary/model"
    />,
  );
  const counting = screen.getByTestId("liveness-line");
  expect(counting.getAttribute("data-level")).toBe("quota");
  // The tag and the advanced elapsed ride beside the promise - asserted as
  // facts, not as the sentence that carries them.
  expect(counting.getAttribute("data-model")).toBe("wait/model");
  expect(counting.getAttribute("data-elapsed-ms")).toBe(String(14 * 60_000 + 30_000));

  // The reset crosses: same facts, pending promise.
  rerender(
    <LivenessLine
      lastFrameAt={now - 30_000}
      now={now + sixHours + 5_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={model.modelRetry}
      primaryModel="primary/model"
    />,
  );
  const pending = screen.getByTestId("liveness-line");
  expect(pending.getAttribute("data-level")).toBe("quota");
  // The crossing changed the promise, never the facts: the phase marker
  // flipped while the tag and the advanced elapsed ride identically -
  // asserted as segments the renderer computed, never as the whole claim.
  expect(pending.getAttribute("data-passed-reset")).toBe("true");
  // The elapsed keeps advancing from the same receipt instant: the sample
  // (14m) plus everything since receipt (30s + the six hours and 5s the
  // crossing itself took), in the exact-gap formatter's own units.
  const advanced = formatExactGap(14 * 60_000 + 30_000 + sixHours + 5_000);
  expect(pending.textContent).toContain("(wait/model)");
  expect(pending.textContent).toContain(`${advanced} on this call`);
  expect(quotaWaitState(model.quotaResumesAt, now + sixHours + 5_000)).toEqual({ phase: "pending" });
});

// Scenario 13's clamp half: the elapsed advance can land marginally negative
// across the pane's tick boundary (the tick fires just before the receipt
// stamp a later frame recorded); it clamps at zero, never reads negative.
test("a stamped wait's elapsed advance clamps at zero across the tick boundary", () => {
  const now = 1_000_000;
  let model = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now);
  // A re-announcement arriving at `now`: the tick below reads at now - 500,
  // before the receipt stamp, so the advance would go negative without the
  // clamp.
  model = applyNotification(
    model,
    quotaRetryFrame(3, { resumesAt: now + 60_000, delayMs: 60_000, groupElapsedMs: 0 }),
    now,
  );
  threadsStore.setState({ threads: new Map([["s1", model]]) });

  render(
    <LivenessLine
      lastFrameAt={now}
      now={now - 500}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={model.modelRetry}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  // The clamp's whole point, as a fact: the elapsed never goes negative
  // across the tick boundary, and the runway is the announcement's own
  // instant from the receipt stamp.
  expect(el.getAttribute("data-elapsed-ms")).toBe("0");
  expect(el.getAttribute("data-remaining-ms")).toBe(String(60_500));
  expect(el.textContent).not.toContain("-");
});

// Contract point 4 / scenario 14: a snapshot-only wait (deadline from a read,
// no live announcement) carries no model tag and no elapsed - the snapshot
// cannot attribute them. reloadWithQuotaWait is exactly that shape: the
// rehydrated model holds the wait and no chip at all.
test("a snapshot-only wait renders the promise alone, without tag or elapsed", () => {
  const now = 1_000_000;
  reloadWithQuotaWait(21_600_000, now);
  render(<LivenessLine lastFrameAt={now - 3_000} now={now} active={true} sessionRef="s1" turnId="turn_0" />);
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  // Snapshot-only: the runway is the read's instant; no tag, no elapsed -
  // the snapshot cannot attribute them. All three as facts.
  expect(el.getAttribute("data-remaining-ms")).toBe(String(21_600_000));
  expect(el.getAttribute("data-model")).toBeNull();
  expect(el.getAttribute("data-elapsed-ms")).toBeNull();
});

// A daemon that predates the stamp never reaches the elapsed question at all
// (the frame installs no wait), so the discriminator the stamped gate exists
// for is exercised by a STAMPED announcement's measured zero below. What
// remains to pin here: the unstamped frame renders nothing at the quota
// level, so no time-since-announcement can dress itself up as elapsed.
test("an unstamped announcement renders no quota line at all", () => {
  const now = 1_000_000;
  const sixHours = 6 * 60 * 60_000;
  let model = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now - 30_000);
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
        groupElapsedMs: 0,
        attemptCap: 0,
      },
    },
    now - 30_000,
  );
  expect(model.quotaWaitStartedAt).toBeUndefined();
  threadsStore.setState({ threads: new Map([["s1", model]]) });

  render(
    <LivenessLine
      lastFrameAt={now - 30_000}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={model.modelRetry}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).not.toBe("quota");
  // The unstamped frame installs no wait, so no quota facts exist to assert:
  // the level alone carries the contract (the copy's absence is implied by
  // the level, not asserted as a phrase).
  expect(el.getAttribute("data-remaining-ms")).toBeNull();
  expect(el.getAttribute("data-elapsed-ms")).toBeNull();
});

// The other side of the discriminator: a stamped announcement's sample IS
// measured - including a valid zero - so a stamped producer's zero keeps its
// place on the line and advances like any sample. (A stamped start always
// names its absolute instant; the frame below carries one.)
test("a stamped announcement's measured-zero elapsed still displays", () => {
  const now = 1_000_000;
  const sixHours = 6 * 60 * 60_000;
  let stamped = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now - 30_000);
  stamped = applyNotification(
    stamped,
    quotaRetryFrame(9, { delayMs: sixHours, resumesAt: now + sixHours, groupElapsedMs: 0 }),
    now - 30_000,
  );
  expect(stamped.quotaWaitStartedAt).toBe(9);
  threadsStore.setState({ threads: new Map([["s1", stamped]]) });
  render(
    <LivenessLine
      lastFrameAt={now - 30_000}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={stamped.modelRetry}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("quota");
  // The measured zero is real, so it ADVANCES like any sample: 30s since
  // receipt is the elapsed fact. The countdown comes from the announcement's
  // own absolute instant measured at this render's now, not from any
  // client-clock runway. Assert both as computed values, never as the sentence.
  expect(el.getAttribute("data-elapsed-ms")).toBe(String(30_000));
  expect(el.getAttribute("data-remaining-ms")).toBe(String(sixHours));
});

// Scenario 11: a passed read deadline plus a late announcement from a
// daemon that predates the stamp. One time basis per wait: the deadline the
// stamped read set is the whole countdown, and past it the wait is pending.
// The late unstamped announcement installs nothing, so no receipt runway can
// leap the line back to a future that has already arrived.
test("a late unstamped announcement cannot leap a passed stamped deadline back to a countdown", () => {
  const now = 1_000_000;
  // A stamped read: the failure summary names the wait's stamp and instant,
  // so the model holds the deadline and its identity.
  const deadline = now + 60_000;
  const read = testThread({
    status: { type: "active" },
    evener: {
      failure: { title: "Usage limit reached", resumesAt: deadline, waitStartedAt: 1 },
    },
  });
  let model = hydrateThread({ thread: read }, "s1", now);
  expect(model.quotaResumesAt).toBeDefined();
  expect(model.quotaWaitStartedAt).toBe(1);
  // The read's deadline passed 30s ago.
  const past = now + 90_000;
  // An announcement from a daemon that predates the stamp arrives now with
  // six hours of its own delay. It installs no wait — no receipt runway
  // exists to choose between — so the model keeps the read's basis alone.
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
        delayMs: 6 * 60 * 60_000,
        errorClass: "quota_exceeded",
        groupElapsedMs: 5_000,
        attemptCap: 0,
      },
    },
    past,
  );
  expect(model.modelRetry).toBeUndefined();
  expect(model.quotaResumesAt).toBeDefined();
  // At the pane's clock the read's instant has passed: the wait is pending.
  expect(quotaWaitState(model.quotaResumesAt, past)).toEqual({ phase: "pending" });
  threadsStore.setState({ threads: new Map([["s1", model]]) });
  render(
    <LivenessLine
      lastFrameAt={now}
      now={past}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={model.modelRetry}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  // The pane held the read's basis: quota level, pending phase - asserted
  // structurally, never as the rendered sentence - and the late unstamped
  // frame contributed nothing.
  expect(el.getAttribute("data-level")).toBe("quota");
  expect(el.getAttribute("data-passed-reset")).toBe("true");
});

// Scenario 17's pane half: the wake's zero-delay announcement ends the wait,
// and the pane clears immediately - the resumed call's own retry is not a
// wait, so it drives ordinary retry-progress text (the "in progress" shape a
// delta-less zero-delay retry reads as), never the quota level again.
test("the wake's zero-delay announcement clears the pane's wait immediately", () => {
  const now = 1_000_000;
  const sixHours = 6 * 60 * 60_000;
  let model = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "s1", now - 5_000);
  model = applyNotification(
    model,
    quotaRetryFrame(7, {
      resumesAt: now + sixHours,
      delayMs: sixHours,
      model: "wait/model",
      groupElapsedMs: 14 * 60_000,
    }),
    now - 5_000,
  );
  expect(model.quotaResumesAt).toBeDefined();
  // The wake: the same stamp, no delay, no resume instant.
  model = applyNotification(model, quotaRetryFrame(7, { delayMs: 0, groupElapsedMs: 14 * 60_000 }), now);
  expect(model.quotaResumesAt).toBeUndefined();
  expect(model.quotaWaitStartedAt).toBeUndefined();
  threadsStore.setState({ threads: new Map([["s1", model]]) });

  render(
    <LivenessLine
      lastFrameAt={now - 5_000}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={model.modelRetry}
    />,
  );
  // No residue at all: the wait is gone, and a first-attempt retry inside
  // the quiet gate renders nothing - exactly the ordinary liveness decision
  // the pane would have made had no wait ever happened.
  const el = screen.queryByTestId("liveness-line");
  expect(el?.getAttribute("data-level") ?? "none").not.toBe("quota");
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
