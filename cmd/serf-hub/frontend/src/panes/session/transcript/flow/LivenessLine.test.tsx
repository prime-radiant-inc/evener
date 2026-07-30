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

test("renders the retry cause and wait instead of the quiet phrase", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={30_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={{ attempt: 9, maxAttempts: 11, delayMs: 60_000, errorClass: "rate_limit" }}
    />,
  );
  const el = screen.getByTestId("liveness-line");
  expect(el.textContent).toBe("Rate limited — retry 9 of 11, next in 60s");
  expect(el.textContent!.toLowerCase()).not.toContain("quiet");
});

test("past the stall threshold a retry still surfaces the silence rather than hiding it", () => {
  render(
    <LivenessLine
      lastFrameAt={0}
      now={630_000}
      active={true}
      sessionRef="s1"
      turnId="turn_0"
      retry={{ attempt: 9, maxAttempts: 11, delayMs: 60_000, errorClass: "rate_limit" }}
    />,
  );
  const text = screen.getByTestId("liveness-line").textContent ?? "";
  expect(text).toContain("Rate limited — retry 9 of 11");
  expect(text).toContain("no updates for");
});
