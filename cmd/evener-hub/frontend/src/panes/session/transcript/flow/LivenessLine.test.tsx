import { compactDuration, STUCK_AFTER_MS } from "@evener/appwire-client";
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

// Re-renders the mounted element with a new `now`, the way Session's
// ticking clock would between renders.
function rerenderWith(mounted: ReturnType<typeof render>, quotaResumesAt: number, now: number) {
  mounted.rerender(
    <LivenessLine
      lastFrameAt={0}
      now={now}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      quotaResumesAt={quotaResumesAt}
    />,
  );
}

// A quota wait the thread snapshot carries (ThreadFailure.ResumesAt): the
// pane re-hydrating from a read has no modelRetry notification at all, so
// the resume instant alone must produce the line. It shows at once (a
// provider-named wait is not a stall guess) and keeps reading as a wait
// however long the silence runs, where an ordinary first retry would read
// "no updates for 3h" past the stuck threshold.
test("a quota wait known from the thread snapshot renders immediately and never reads as stalled", () => {
  const resumesAt = 30_000 + 90 * 60_000;
  const mounted = render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      quotaResumesAt={resumesAt}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("retrying");

  rerenderWith(mounted, resumesAt, STUCK_AFTER_MS * 6);
  const hoursIn = screen.getByTestId("liveness-line");
  expect(hoursIn.getAttribute("data-level")).toBe("retrying");
});

// A snapshot whose deadline has passed is still evidence of a pending wait:
// the daemon keeps the wait while the retry queues behind other work, so a
// pane opened during that interval (a reload, a cold visit) reads the
// expired instant as overdue rather than falling back to ordinary liveness
// - which would report a stall for a wait the snapshot itself names. The
// snapshot knows no elapsed time, so none renders. (Until round 7 the
// expired instant was discarded outright and the pane fell to quiet/stall.)
test("an expired snapshot deadline renders the overdue wait, never a stall", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={STUCK_AFTER_MS * 3}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      quotaResumesAt={10_000}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("retrying");
  // Overdue, not the silence-derived states: the line explains the wait the
  // snapshot names, distinct from every state derived from the gap.
  const overdueText = el.textContent ?? "";
  expect(overdueText).not.toBe(describeLiveness(STUCK_AFTER_MS * 3, true, 0).text);
  // No elapsed time: the snapshot alone cannot know one.
  expect(overdueText).not.toContain("on this call");
});

// A live quota wait keeps counting down while unrelated activity lands: a
// background job finishing advances lastFrameAt, but that frame is not the
// waiting call resuming, so the wait stays a wait and its elapsed time keeps
// advancing with the pane's clock (the announcement's own elapsed value plus
// the time since it landed).
test("a live quota wait stays a wait when unrelated activity advances lastFrameAt, and its elapsed time keeps advancing", () => {
  const retry = rawRetry({
    attempt: 1,
    attemptCap: 1,
    delayMs: 90 * 60_000,
    errorClass: "quota_exceeded",
    groupElapsedMs: 5 * 60_000,
    receivedAt: 10_000,
  });
  // An unrelated frame landed well after the wait was announced.
  render(
    <LivenessLine
      lastFrameAt={120_000}
      now={130_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={retry}
      quotaResumesAt={10_000 + 90 * 60_000}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.getAttribute("data-level")).toBe("retrying");
  // Still the pending countdown, with the wait's own elapsed value (5m) plus
  // the 120s since the announcement landed.
  expect(el.textContent).toContain(formatExactGap(5 * 60_000 + 120_000));
});

// The wake's zero-delay announcement is the request going out: the line
// describes the resumed request as motion, distinct from the pending
// countdown and carrying the computed elapsed time of the call.
test("a zero-delay quota announcement (the wake) renders the resumed request as motion", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={rawRetry({
        attempt: 1,
        attemptCap: 1,
        delayMs: 0,
        errorClass: "quota_exceeded",
        groupElapsedMs: 60 * 60_000,
        receivedAt: 30_000,
      })}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  // Motion, not a countdown: the wake carries no delay to count down.
  expect(el.textContent).not.toContain(compactDuration(60 * 60_000));
  expect(el.textContent).toContain(formatExactGap(60 * 60_000));
});

// The reset passing is not the request starting: the daemon may queue the
// retry behind other work for minutes past the reset, so the expired
// deadline reads as overdue - distinct from the countdown and from motion -
// while the wake is still pending.
test("a live quota wait whose reset has passed without a wake reads as overdue, not as motion", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={90 * 60_000 + 30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={rawRetry({
        attempt: 1,
        attemptCap: 1,
        delayMs: 90 * 60_000,
        errorClass: "quota_exceeded",
        groupElapsedMs: 5 * 60_000,
        receivedAt: 0,
      })}
      quotaResumesAt={90 * 60_000}
    />,
  );
  const overdue = screen.getByTestId("liveness-line");
  // The countdown has stopped: the passed reset renders nowhere, and the
  // line stays distinct from the resumed-request line the wake renders.
  expect(overdue.textContent).not.toContain(compactDuration(90 * 60_000));
  expect(overdue.textContent).toContain(formatExactGap(5 * 60_000 + 90 * 60_000 + 30_000));
  cleanup();
  const wake = render(
    <LivenessLine
      lastFrameAt={0}
      now={90 * 60_000 + 30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={rawRetry({
        attempt: 1,
        attemptCap: 1,
        delayMs: 0,
        errorClass: "quota_exceeded",
        groupElapsedMs: 5 * 60_000,
        receivedAt: 90 * 60_000 + 30_000,
      })}
    />,
  );
  expect(wake.getByTestId("liveness-line").textContent).not.toBe(overdue.textContent);
});

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
