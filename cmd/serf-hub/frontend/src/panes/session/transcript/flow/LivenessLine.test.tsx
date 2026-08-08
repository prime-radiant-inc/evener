import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { resetSubagentModuleStoreForTests, turnScopeKey, upsertSubagentRow } from "../tools/subagentModuleStore";
import { LivenessLine } from "./LivenessLine";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  resetSubagentModuleStoreForTests();
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
  expect(el.textContent).toContain("~30s");
});

test("renders the stalled phrase once the gap crosses the stall threshold, for an active turn tracking no children at all", () => {
  render(<LivenessLine lastFrameAt={0} now={185_000} active={true} sessionRef="s1" turnId="turn_0" />);
  const el = screen.getByTestId("liveness-line");
  expect(el.textContent!.toLowerCase()).toContain("stalled");
  expect(el.textContent).toContain("3m 5s");
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

// --- running children (kata aep5): counted via subagentModuleStore's own
// useSubagentRows, scoped by turnScopeKey(sessionRef, turnId) exactly as the
// subagent module itself scopes its rows - never a bare turn id, since turn
// ids restart at 0 per session (katas eptj/8525) and this store is a
// page-lifetime singleton that would otherwise collide two sessions' rows.

test("renders 'Waiting on 1 subagent' instead of the quiet phrase once a running child is tracked for the active turn", () => {
  upsertSubagentRow(turnScopeKey("s1", "turn_0"), { rowKey: "dlg:1", kind: "running", task: "t", resultPreview: "" });
  render(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  const el = screen.getByTestId("liveness-line");
  expect(el.textContent).toBe("Waiting on 1 subagent");
  expect(el.textContent!.toLowerCase()).not.toContain("quiet");
});

// Past the stall threshold the line stops taking the child's word for it and
// reports both facts - see describeLiveness's own comment for why a believed-
// running child must not be able to silence the stall report forever.
test("past the stall threshold, a tracked running child still surfaces the silence rather than hiding it", () => {
  upsertSubagentRow(turnScopeKey("s1", "turn_0"), { rowKey: "dlg:1", kind: "running", task: "t", resultPreview: "" });
  render(<LivenessLine lastFrameAt={0} now={185_000} active={true} sessionRef="s1" turnId="turn_0" />);
  const text = screen.getByTestId("liveness-line").textContent ?? "";
  expect(text).toContain("Waiting on 1 subagent");
  expect(text).toContain("no updates for");
});

test("renders 'Waiting on N subagents' (plural) once past the quiet threshold, pre-empting the quiet phrase too", () => {
  const scopeKey = turnScopeKey("s1", "turn_0");
  upsertSubagentRow(scopeKey, { rowKey: "dlg:1", kind: "running", task: "a", resultPreview: "" });
  upsertSubagentRow(scopeKey, { rowKey: "dlg:2", kind: "running", task: "b", resultPreview: "" });
  upsertSubagentRow(scopeKey, { rowKey: "dlg:3", kind: "running", task: "c", resultPreview: "" });
  render(<LivenessLine lastFrameAt={0} now={30_000} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").textContent).toBe("Waiting on 3 subagents");
});

test("a done/failed row alone does not explain a wait - falls back to the ordinary stalled decision", () => {
  const scopeKey = turnScopeKey("s1", "turn_0");
  upsertSubagentRow(scopeKey, { rowKey: "dlg:1", kind: "done", task: "a", resultPreview: "ok" });
  upsertSubagentRow(scopeKey, { rowKey: "dlg:2", kind: "failed", task: "b", resultPreview: "boom" });
  render(<LivenessLine lastFrameAt={0} now={185_000} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").textContent!.toLowerCase()).toContain("stalled");
});

test("prefers the live liveKind overlay over the frozen kind when counting running children (matches SubagentRowView's own displayKind)", () => {
  // The frozen tool-output kind says "done" but a faster live watch/
  // notification (dr7e) has already overlaid liveKind "running" - the row is
  // genuinely still running and must count.
  upsertSubagentRow(turnScopeKey("s1", "turn_0"), {
    rowKey: "dlg:1",
    kind: "done",
    liveKind: "running",
    task: "a",
    resultPreview: "",
  });
  // Below the stall threshold, so this asserts the overlay preference alone
  // rather than entangling it with the past-threshold wording above.
  render(<LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").textContent).toBe("Waiting on 1 subagent");
});

test("the inverse overlay also applies: a liveKind of 'done' over a frozen 'running' kind does not count as running", () => {
  upsertSubagentRow(turnScopeKey("s1", "turn_0"), {
    rowKey: "dlg:1",
    kind: "running",
    liveKind: "done",
    task: "a",
    resultPreview: "ok",
  });
  render(<LivenessLine lastFrameAt={0} now={185_000} active={true} sessionRef="s1" turnId="turn_0" />);
  expect(screen.getByTestId("liveness-line").textContent!.toLowerCase()).toContain("stalled");
});

test("an undefined turnId (no active turn yet) reads as zero running children even if rows exist for a real turn in the same session", () => {
  upsertSubagentRow(turnScopeKey("s1", "turn_0"), { rowKey: "dlg:1", kind: "running", task: "a", resultPreview: "" });
  render(<LivenessLine lastFrameAt={0} now={185_000} active={true} sessionRef="s1" turnId={undefined} />);
  expect(screen.getByTestId("liveness-line").textContent!.toLowerCase()).toContain("stalled");
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
  expect(el.textContent).toBe("Rate limited — retry 9 of 11, next in 60s — 5s on this call");
  expect(el.textContent!.toLowerCase()).not.toContain("quiet");
});

test("past the stall threshold a retry still surfaces the silence rather than hiding it", () => {
  render(
    <LivenessLine lastFrameAt={0} now={630_000} active={true} sessionRef="s1" turnId="turn_0" retry={rawRetry()} />,
  );
  const text = screen.getByTestId("liveness-line").textContent ?? "";
  expect(text).toContain("Rate limited — retry 9 of 11");
  expect(text).toContain("no updates for");
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
    "Rate limited — retry 3 of 4, next in 5s — 5s on this call",
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
    "Rate limited (gpt-5) — retry 1 of 4, next in 5s — 5s on this call",
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
    "Rate limited — retry 1 of 4, next in 5s — 5s on this call",
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
  expect(screen.getByTestId("liveness-line").textContent).toContain("14m on this call");
});

// Web-only difference from the TUI (task 11 brief): once a delta has landed
// since the retry was reported, the wait it described is over, so the line
// says "in progress" instead of leaving a stale "next in 60s" countdown
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
    "Rate limited — retry 9 of 11, in progress — 5s on this call",
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
    "Rate limited — retry 9 of 11, in progress — 5s on this call",
  );
});
