import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy, StrictMode } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { ToolCallItem } from "../ToolCallItem";
import { toolRendererFor } from "../toolRenderers";
import { classifyJobStatus, resolveRowKey, rowKindFromChildStatus } from "./subagentModule";
import { resetSubagentModuleStoreForTests, turnScopeKey, updateSubagentRowIfExists } from "./subagentModuleStore";
import "./subagentModule";
import type { DockviewApi } from "dockview-core";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import { registerPane } from "../../../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../../stores/threads";
import rawCssModule from "./subagentmodule.module.css";

// A minimal, test-only "session" pane registration - real registerPane/
// paneFor/openPane machinery, just without pulling in the actual
// panes/session module (a heavier, T1-owned dependency this test doesn't
// need: it only asserts openPane was called correctly, never that a real
// SessionPane renders).
registerPane({
  id: "session",
  title: () => "test session",
  component: lazy(() => Promise.resolve({ default: () => null })),
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  resetSubagentModuleStoreForTests();
  resetDisclosureStoreForTests();
  resetWorkspaceStoreForTests();
  registerDockviewApi(null); // never leak a fake dockview host to another test
});

// A minimal DockviewApi stand-in: openBeside only asks "is there a host at all"
// (non-null) to decide split-vs-plain-open, so a bare object suffices (mirrors
// shell/paneActions.test.ts's own fakeApi).
function fakeApi(): DockviewApi {
  return {} as unknown as DockviewApi;
}

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

function delegateItem(overrides: Partial<ItemModel> = {}): ItemModel {
  return item({ toolName: "delegate", ...overrides });
}

// jsdom runs no cascade, so data-has-failure's own visual effect (3h80) can
// only be asserted at the declaration level - comments are stripped first, so
// a stylesheet grep that only matches its own comment prose asserts nothing.
// Same idiom as toolRowGrammar.test.tsx's own rowCss().
function moduleCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "subagentmodule.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

// jsdom evaluates no cascade, but a CSS Modules class NAME is real (a hashed
// string Vite's transform produces, not layout) - same idiom as chip.test.tsx's
// own rawStyles import, used below to prove the "latest" emphasis lands on the
// right <li> rather than just eyeballing a snapshot.
const styles = {
  activityLatest: requireClass(rawCssModule.activityLatest, "subagentmodule.module.css", "activityLatest"),
};

// --- classifyJobStatus / resolveRowKey (pure, unit-level) -----------------

test("classifyJobStatus: failed family", () => {
  for (const s of ["failed", "errored", "error", "exhausted"]) expect(classifyJobStatus(s)).toBe("failed");
});

test("classifyJobStatus: done family (a clean completion, not a stop/cancel)", () => {
  for (const s of ["completed", "done", "succeeded"]) expect(classifyJobStatus(s)).toBe("done");
});

// 3zf8: cancelled/stopped must not fold into "done" (nor "failed" - nothing
// broke) - they get their own distinct kind so a deliberately-killed child
// never renders byte-identical to one that finished cleanly.
test("classifyJobStatus: stopped family (cancelled/stopped are their own kind, not done and not failed)", () => {
  for (const s of ["cancelled", "stopped"]) expect(classifyJobStatus(s)).toBe("stopped");
});

test("classifyJobStatus: literal unknown maps to unknown", () => {
  expect(classifyJobStatus("unknown")).toBe("unknown");
});

test("classifyJobStatus: running, and anything undetermined (including undefined), maps to running", () => {
  expect(classifyJobStatus("running")).toBe("running");
  expect(classifyJobStatus(undefined)).toBe("running");
  expect(classifyJobStatus("some-future-status")).toBe("running");
});

test("resolveRowKey: prefers delegateId, then jobId, then the fallback", () => {
  expect(resolveRowKey("dlg_1", "job_1", "call_1")).toBe(resolveRowKey("dlg_1", "job_2", "call_2"));
  expect(resolveRowKey(undefined, "job_1", "call_1")).not.toBe(resolveRowKey(undefined, "job_2", "call_1"));
  expect(resolveRowKey(undefined, undefined, "call_1")).toBe(resolveRowKey(undefined, undefined, "call_1"));
  expect(resolveRowKey(undefined, "job_1", "call_1")).not.toBe(resolveRowKey("dlg_1", "job_1", "call_1"));
});

// --- rowKindFromChildStatus (pure, unit-level) -----------------------------

test("rowKindFromChildStatus: a live child (working/needs-you/idle) reads as running", () => {
  expect(rowKindFromChildStatus("active")).toBe("running");
  expect(rowKindFromChildStatus("awaiting")).toBe("running");
  expect(rowKindFromChildStatus("warning")).toBe("running");
  expect(rowKindFromChildStatus("idle")).toBe("running");
});

test("rowKindFromChildStatus: systemError reads as failed, closed reads as done", () => {
  expect(rowKindFromChildStatus("systemError")).toBe("failed");
  expect(rowKindFromChildStatus("closed")).toBe("done");
});

// g5kf: notLoaded means the child left the daemon's live roster entirely -
// evicted, orphaned, or lost to a hub restart (cmd/serf-hub/app_threadread.go's
// pastEntryThread stamps it) - not "still going". cadenceStateForStatus folds
// it into the same "idle" family a genuinely-idle-but-still-live child gets
// (deliberately, for the Cadence dot's own "nothing to animate" purposes - see
// liveness.ts's own doc comment and its test), so rowKindFromChildStatus can't
// tell the two apart once it only looks at that folded state. It must check
// the literal wire status before folding, the same way Composer.tsx/
// StatusRow.tsx each keep their own separate notLoaded check alongside
// cadenceStateForStatus rather than trusting its output alone.
test("g5kf: rowKindFromChildStatus reads notLoaded as unknown, never running forever", () => {
  expect(rowKindFromChildStatus("notLoaded")).toBe("unknown");
});

// --- delegate descriptor: summary ----------------------------------------

test("delegate: summary is the human description", () => {
  const d = toolRendererFor("delegate");
  const args = JSON.stringify({ task: "Run the full test suite and report back" });
  expect(d.summary(delegateItem({ description: "Testing delegation", argumentsJSON: args }))).toBe(
    "Testing delegation",
  );
});

test("delegate: delegated task text is not used for the row summary", () => {
  const d = toolRendererFor("delegate");
  const longTask = "x".repeat(100);
  const args = JSON.stringify({ task: longTask });
  expect(d.summary(delegateItem({ description: "Testing delegation", argumentsJSON: args }))).toBe(
    "Testing delegation",
  );
});

// --- leader election: only the FIRST delegate item in a turn renders the
// module; later ones in the same turn render nothing of their own (their
// ToolCallItem summary line still shows independently - that's owned by
// T1's ToolCallItem, not this body) ---------------------------------------

test("the first delegate item in a turn renders the module; a second one in the SAME turn renders nothing", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const first = delegateItem({
    id: "d1",
    callId: "call_d1",
    argumentsJSON: JSON.stringify({ task: "first child" }),
    output: JSON.stringify({ job_id: "job_1", status: "running", transcript_ref: "ref_child_1" }),
  });
  const second = delegateItem({
    id: "d2",
    callId: "call_d2",
    argumentsJSON: JSON.stringify({ task: "second child" }),
    output: JSON.stringify({ job_id: "job_2", status: "running", transcript_ref: "ref_child_2" }),
  });
  render(
    <>
      <Body item={first} live={false} />
      <Body item={second} live={false} />
    </>,
  );
  expect(screen.getByTestId("subagent-module")).toBeTruthy();
  // Only ONE module rendered, but it shows BOTH rows (both delegate calls'
  // own data reached the shared store).
  expect(screen.getAllByTestId("subagent-module")).toHaveLength(1);
  expect(screen.getByText("first child")).toBeTruthy();
  expect(screen.getByText("second child")).toBeTruthy();
});

test("a delegate item in a DIFFERENT turn gets its own, separate module", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const turnA = delegateItem({
    id: "da",
    turnId: "turn_A",
    callId: "call_a",
    argumentsJSON: JSON.stringify({ task: "in turn A" }),
  });
  const turnB = delegateItem({
    id: "db",
    turnId: "turn_B",
    callId: "call_b",
    argumentsJSON: JSON.stringify({ task: "in turn B" }),
  });
  render(
    <>
      <Body item={turnA} live={false} />
      <Body item={turnB} live={false} />
    </>,
  );
  expect(screen.getAllByTestId("subagent-module")).toHaveLength(2);
});

// kata 8525: turn ids are minted per-session (they restart at "turn_1" for
// every fresh thread - internal/appprojector's own nextTurn counter), so the
// SAME turn_N string is not unique across sessions. Two delegate items from
// two DIFFERENT sessions that happen to land on the same turnId must still
// get their own, separate module/rows - the module store's key must include
// sessionRef, not turnId alone. Reproduced live (see kata) as an unrelated,
// already-abandoned session's rows bleeding into a brand-new session's own
// delegate block.
test("kata 8525: two sessions sharing the SAME turnId never bleed into each other's module", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const sessionA = delegateItem({
    id: "d_sess_a",
    turnId: "turn_21",
    callId: "call_sess_a",
    argumentsJSON: JSON.stringify({ task: "session A's own task" }),
    output: JSON.stringify({ job_id: "job_sess_a", status: "running" }),
  });
  const sessionB = delegateItem({
    id: "d_sess_b",
    turnId: "turn_21",
    callId: "call_sess_b",
    argumentsJSON: JSON.stringify({ task: "session B's own task" }),
    output: JSON.stringify({ job_id: "job_sess_b", status: "running" }),
  });
  render(
    <>
      <Body item={sessionA} live={false} sessionRef="session_a_ref" />
      <Body item={sessionB} live={false} sessionRef="session_b_ref" />
    </>,
  );
  // Two unrelated sessions, each with its own module - never one merged
  // "2 running" block.
  expect(screen.getAllByTestId("subagent-module")).toHaveLength(2);
  const [moduleA, moduleB] = screen.getAllByTestId("subagent-module");
  expect(within(moduleA!).getAllByTestId("subagent-row")).toHaveLength(1);
  expect(within(moduleB!).getAllByTestId("subagent-row")).toHaveLength(1);
  expect(screen.getByText("session A's own task")).toBeTruthy();
  expect(screen.getByText("session B's own task")).toBeTruthy();
});

// 78nj: the Disclosure id used a bare turnId, not turnScopeKey(sessionRef,
// turnId) - the same collision class as kata 8525 above, but on the
// disclosureStore's open/closed map rather than this store's rows. Both
// items below rely on the SAME defaults (id "item_1", turnId "turn_1", no
// callId) and neither output carries delegate_id/job_id, so both resolve to
// the identical fallback rowKey call:item_1 under the identical turnId -
// exactly the "a call that errors before minting any handle" scenario the
// kata describes. Only sessionRef tells the two rows apart.
test("78nj: one session's delegate row body never renders another session's mandate", async () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const sessionA = delegateItem({ argumentsJSON: JSON.stringify({ task: "session A's task" }), output: "" });
  const sessionB = delegateItem({ argumentsJSON: JSON.stringify({ task: "session B's task" }), output: "" });
  render(
    <>
      <Body item={sessionA} live={false} sessionRef="session_a_ref" />
      <Body item={sessionB} live={false} sessionRef="session_b_ref" />
    </>,
  );

  const [moduleA, moduleB] = screen.getAllByTestId("subagent-module");
  expect(within(moduleA!).getByTestId("subagent-mandate")).toBeTruthy();
  expect(within(moduleB!).getByTestId("subagent-mandate")).toBeTruthy();
  expect(within(moduleA!).getByText("session A's task")).toBeTruthy();
  expect(within(moduleB!).getByText("session B's task")).toBeTruthy();
});

// claimLeader/releaseLeader must stay symmetric across StrictMode's dev-only
// mount -> cleanup -> remount double-invoke of effects (React double-invokes
// mount effects once, in development, to surface exactly this class of bug -
// see Session.test.tsx's own StrictMode test and AppShell.tsx:55's comment
// for this codebase's existing precedent that StrictMode-safety is upheld
// even though production doesn't wrap in StrictMode today). Claiming inside
// a useState lazy initializer (render phase) while releasing inside a
// useLayoutEffect cleanup (effect phase) is asymmetric: the double-invoke's
// interim cleanup pass frees the store's leader slot while the leader
// component stays mounted and its own `isLeader` state is frozen forever, so
// nothing re-claims the slot on the double-invoke's remount pass - a THIRD
// delegate item mounting afterward, into the now-vacant slot, then also
// claims leadership.
test("StrictMode's mount-cleanup-remount double-invoke keeps leadership with the first item; a later-mounting delegate in the same turn must not also claim it", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const first = delegateItem({
    id: "ds1",
    callId: "call_ds1",
    argumentsJSON: JSON.stringify({ task: "strict first" }),
  });
  const second = delegateItem({
    id: "ds2",
    callId: "call_ds2",
    argumentsJSON: JSON.stringify({ task: "strict second" }),
  });
  const third = delegateItem({
    id: "ds3",
    callId: "call_ds3",
    argumentsJSON: JSON.stringify({ task: "strict third" }),
  });

  const { rerender } = render(
    <StrictMode>
      <Body item={first} live={false} />
      <Body item={second} live={false} />
    </StrictMode>,
  );
  expect(screen.getAllByTestId("subagent-module")).toHaveLength(1);

  // third mounts fresh, in the SAME turn, after the first two have already
  // been through StrictMode's double-invoke cycle once.
  rerender(
    <StrictMode>
      <Body item={first} live={false} />
      <Body item={second} live={false} />
      <Body item={third} live={false} />
    </StrictMode>,
  );
  expect(screen.getAllByTestId("subagent-module")).toHaveLength(1);
});

test("a collapsed live delegate updates its top-level status when the child settles", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => childThreadRead(params, "active"));
  connectionStore.getState().connect(fake);

  const turn: TurnModel = { id: "turn_collapsed_live", status: "completed", items: [] };
  const started = delegateItem({
    id: "d_collapsed_live",
    turnId: turn.id,
    callId: "call_collapsed_live",
    description: "Collapsed live delegate",
    argumentsJSON: JSON.stringify({ task: "wait for child" }),
    output: JSON.stringify({ job_id: "job_collapsed_live", status: "running", transcript_ref: "ref_collapsed_live" }),
  });
  render(<ToolCallItem item={started} turn={turn} live={false} />);

  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(true);
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();

  const user = userEvent.setup();
  await user.click(screen.getByTestId("tool-row"));
  expect(details.open).toBe(false);

  await act(async () => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_child", ref: "ref_collapsed_live", status: { type: "closed" } },
    } as never);
  });

  expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy();
});

test("a genuinely live collapsed delegate has a status row before its child settles", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => childThreadRead(params, "active"));
  connectionStore.getState().connect(fake);

  const turn: TurnModel = { id: "turn_live_collapsed_initial", status: "inProgress", items: [] };
  const liveItem = delegateItem({
    id: "d_live_collapsed_initial",
    turnId: turn.id,
    callId: "call_live_collapsed_initial",
    description: "Genuinely live delegate",
    argumentsJSON: JSON.stringify({ task: "keep working" }),
    output: JSON.stringify({
      job_id: "job_live_collapsed_initial",
      status: "running",
      transcript_ref: "ref_live_collapsed_initial",
    }),
  });
  render(<ToolCallItem item={liveItem} turn={turn} live={true} />);

  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1));

  await act(async () => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: {
        threadId: "thr_child",
        ref: "ref_live_collapsed_initial",
        status: { type: "closed" },
      },
    } as never);
  });

  expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy();
});

test("a mounted follower takes over when the current delegate leader unmounts", () => {
  const Body = toolRendererFor("delegate").body!;
  const first = delegateItem({
    id: "d_leader_unmounts",
    turnId: "turn_leader_unmounts",
    callId: "call_leader_unmounts",
    argumentsJSON: JSON.stringify({ task: "leader task" }),
  });
  const follower = delegateItem({
    id: "d_follower_takes_over",
    turnId: "turn_leader_unmounts",
    callId: "call_follower_takes_over",
    argumentsJSON: JSON.stringify({ task: "follower task" }),
  });

  const { rerender } = render(
    <>
      <Body key={first.id} item={first} live={false} />
      <Body key={follower.id} item={follower} live={false} />
    </>,
  );
  expect(screen.getAllByTestId("subagent-module")).toHaveLength(1);

  rerender(<Body key={follower.id} item={follower} live={false} />);

  expect(screen.getAllByTestId("subagent-module")).toHaveLength(1);
  expect(screen.getByText("follower task")).toBeTruthy();
});

test("a delegate row migrates fallback identity to job identity without losing its live overlay", () => {
  const Body = toolRendererFor("delegate").body!;
  const scopeKey = turnScopeKey(undefined, "turn_row_migrates");
  const beforeJob = delegateItem({
    id: "d_row_migrates",
    turnId: "turn_row_migrates",
    callId: "call_row_migrates",
    argumentsJSON: JSON.stringify({ task: "migrate this row" }),
    output: JSON.stringify({ status: "running" }),
  });
  const { rerender } = render(<Body item={beforeJob} live={false} />);

  act(() => {
    updateSubagentRowIfExists(scopeKey, "call:call_row_migrates", {
      liveKind: "failed",
      liveReason: "watch failed",
    });
  });

  const withJob = { ...beforeJob, output: JSON.stringify({ job_id: "job_row_migrates", status: "completed" }) };
  rerender(<Body item={withJob} live={false} />);

  const rows = screen.getAllByTestId("subagent-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]?.getAttribute("data-kind")).toBe("failed");
  expect(screen.getByText("watch failed")).toBeTruthy();
});

// --- row content ----------------------------------------------------------

test("a running row in a multi-row aggregate shows the task and running kind", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const running = delegateItem({
    id: "d_run",
    callId: "call_run",
    argumentsJSON: JSON.stringify({ task: "still working" }),
    output: JSON.stringify({ job_id: "job_r", status: "running", transcript_ref: "ref_r" }),
  });
  const done = delegateItem({
    id: "d_done_task_row",
    callId: "call_done_task_row",
    argumentsJSON: JSON.stringify({ task: "other task" }),
    output: JSON.stringify({ job_id: "job_d_row", status: "completed", transcript_ref: "ref_done" }),
  });
  render(
    <>
      <Body item={running} live={false} />
      <Body item={done} live={false} />
    </>,
  );
  const row = screen.getByText("still working").closest('[data-testid="subagent-row"]') as HTMLElement;
  expect(row.dataset.kind).toBe("running");
});

// Wire-true duration net: ItemModel.startedAt/completedAt are ISO strings the
// reducer produces via epochMsToISO from the wire's epoch-MILLISECONDS
// ThreadItem timestamps (reducer.ts:124-125; appwire/types.go stamps them via
// time.Time.UnixMilli). durationLabel diffs those two ISO instants, so a real
// 12-second span at a realistic ms epoch must read "12s" — reading the wire as
// seconds would place both instants ~12ms apart (1970-relative) and floor to
// "12ms". This locks the honest ms duration end to end at the consumer.
test("a settled delegate row renders an honest ms-scale duration", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const startedMs = 1_700_000_000_000; // 2023-11-14T22:13:20Z — a realistic epoch-ms
  const settled = delegateItem({
    id: "d_done",
    callId: "call_done",
    argumentsJSON: JSON.stringify({ task: "did the thing" }),
    output: JSON.stringify({ job_id: "job_d", status: "completed", transcript_ref: "ref_d" }),
    startedAt: new Date(startedMs).toISOString(),
    completedAt: new Date(startedMs + 12_000).toISOString(),
  });
  render(<Body item={settled} live={false} />);
  const row = screen.getByTestId("subagent-row");
  expect(within(row).getByText("12s")).toBeTruthy();
});

test("a running row with a transcriptRef watches the child and updates live status from thread state", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => ({
    thread: {
      id: "thr_child",
      sessionId: "sess_child",
      preview: "",
      ephemeral: false,
      modelProvider: "anthropic/claude-sonnet-4-5",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "systemError" },
      cwd: "/tmp",
      cliVersion: "1.0.0",
      source: "serf",
      serf: { ref: (params as { ref: string }).ref, capabilities: {} as never, queue: {} },
    },
  }));
  connectionStore.getState().connect(fake);

  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const running = delegateItem({
    id: "d_watch",
    callId: "call_watch",
    argumentsJSON: JSON.stringify({ task: "watched task" }),
    output: JSON.stringify({ job_id: "job_w", status: "running", transcript_ref: "ref_watched_child" }),
  });
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await waitFor(() => expect(row.dataset.kind).toBe("failed"));
  expect(screen.getByTestId("subagent-module").querySelector('[data-kind="failed"]')).toBeTruthy();
});

test("a failed row is flagged at module level via data-has-failure, not averaged away", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const failed = delegateItem({
    id: "d_fail",
    callId: "call_fail",
    argumentsJSON: JSON.stringify({ task: "will fail" }),
    output: JSON.stringify({ job_id: "job_f", status: "failed", transcript_ref: "ref_f", reason: "build error" }),
  });
  render(<Body item={failed} live={false} />);
  const module = screen.getByTestId("subagent-module");
  expect(module.dataset.hasFailure).toBe("true");
  const row = screen.getByTestId("subagent-row");
  expect(row.dataset.kind).toBe("failed");

  // 3h80: data-has-failure must actually paint something, not just sit on
  // the element - jsdom evaluates no cascade, so the only honest way to
  // prove that is to read the stylesheet directly, the same rowCss() idiom
  // toolRowGrammar.test.tsx uses. Without this half, the DOM assertion above
  // would keep passing even if subagentmodule.module.css were deleted
  // outright.
  const css = moduleCss();
  expect(css).toMatch(/\[data-has-failure="true"\]\s*\{[^}]*border-left:[^;]*var\(--danger\)/);
});

// 3zf8: a child deliberately killed with job_stop (or reconciled to
// stopped/runtime_lost after a hub restart - agent/internal/jobstore/
// reconcile.go) must never render byte-identical to one that finished its
// task cleanly - same glyph, same tone, same label was the exact defect.
// Nothing broke, so it's still not "failed", but it is not "done" either.
test("3zf8: a cancelled/stopped child gets its own distinct kind - never rendered (or tallied) as a clean 'done' success", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const stopped = delegateItem({
    id: "d_stopped",
    callId: "call_stopped",
    argumentsJSON: JSON.stringify({ task: "misbehaving, killed" }),
    output: JSON.stringify({ job_id: "job_stopped", status: "cancelled", transcript_ref: "ref_stopped" }),
  });
  render(<Body item={stopped} live={false} />);
  const module = screen.getByTestId("subagent-module");
  const row = screen.getByTestId("subagent-row");
  expect(row.dataset.kind).toBe("stopped");
  expect(within(module).queryByText(/1 stopped/)).toBeNull();
  expect(within(module).queryByText(/done/)).toBeNull();
  expect(screen.getAllByText("misbehaving, killed")).toHaveLength(1);
});

// --- fold beyond ~6 done rows (running/failed/unknown always visible) ----

test("beyond 6 done rows, the extras fold behind a '+N more' control; running/failed rows are never folded", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const bodies = [];
  for (let i = 0; i < 8; i++) {
    bodies.push(
      <Body
        key={i}
        item={delegateItem({
          id: `d_done_${i}`,
          callId: `call_done_${i}`,
          argumentsJSON: JSON.stringify({ task: `done task ${i}` }),
          output: JSON.stringify({ job_id: `job_done_${i}`, status: "completed", transcript_ref: `ref_${i}` }),
        })}
        live={false}
      />,
    );
  }
  bodies.push(
    <Body
      key="running"
      item={delegateItem({
        id: "d_running_extra",
        callId: "call_running_extra",
        argumentsJSON: JSON.stringify({ task: "still running extra" }),
        output: JSON.stringify({ job_id: "job_running_extra", status: "running" }),
      })}
      live={false}
    />,
  );
  render(bodies);
  // 6 done rows visible + the running row always visible = 7 rows shown;
  // 2 done rows folded behind "+2 more".
  expect(screen.getAllByTestId("subagent-row")).toHaveLength(7);
  expect(screen.getByText("still running extra")).toBeTruthy();
  expect(screen.getByText(/\+2 more/)).toBeTruthy();
});

test("clicking '+N more' expands to show every done row, and offers a collapse control back", async () => {
  const user = userEvent.setup();
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const bodies = [];
  for (let i = 0; i < 8; i++) {
    bodies.push(
      <Body
        key={i}
        item={delegateItem({
          id: `d_fold_${i}`,
          callId: `call_fold_${i}`,
          argumentsJSON: JSON.stringify({ task: `fold task ${i}` }),
          output: JSON.stringify({ job_id: `job_fold_${i}`, status: "completed" }),
        })}
        live={false}
      />,
    );
  }
  render(bodies);
  expect(screen.getAllByTestId("subagent-row")).toHaveLength(6);

  await user.click(screen.getByRole("button", { name: /\+2 more/i }));
  expect(screen.getAllByTestId("subagent-row")).toHaveLength(8);
  expect(screen.getByRole("button", { name: /collapse/i })).toBeTruthy();

  await user.click(screen.getByRole("button", { name: /collapse/i }));
  expect(screen.getAllByTestId("subagent-row")).toHaveLength(6);
});

// --- open-transcript action -----------------------------------------------

function delegateWithTranscriptRef(ref: string): ItemModel {
  return delegateItem({
    id: "d_ref",
    callId: "call_ref",
    argumentsJSON: JSON.stringify({ task: "has a transcript" }),
    output: JSON.stringify({ job_id: "job_ref", status: "running", transcript_ref: ref }),
  });
}

test("open transcript opens the read-only transcript pane (mobile / no dockview host): plain full-screen open, not a session pane", async () => {
  registerDockviewApi(null); // StackHost registers no api - the mobile signal
  const user = userEvent.setup();
  const Body = toolRendererFor("delegate").body!;
  render(<Body item={delegateWithTranscriptRef("ref_child_open")} live={false} />);
  const button = screen.getByRole("button", { name: "Open transcript" });
  expect(button.textContent).toContain("open");
  expect(button.querySelector("svg")).toBeTruthy();
  expect(button.getAttribute("aria-label")).toBe("Open transcript");
  await user.click(button);
  const panes = workspaceStore.getState().panes;
  // The DISTINCT read-only "transcript" pane (plan §Ambiguities #1 / PIN-A:
  // reachable via openBeside, never a URL) - opened against the child's own ref.
  const opened = panes.find((p) => p.type === "transcript");
  expect(opened?.params).toEqual({ ref: "ref_child_open" });
  // Slots are desktop geometry; StackHost has no groups and shows the focused
  // pane full-screen, so the first-pane-takes-main rule is all that applies.
  expect(opened?.slot).toBe("main");
  // The row must no longer open a live SESSION pane for the child.
  expect(panes.some((p) => p.type === "session")).toBe(false);
});

test("open transcript opens the transcript pane in the secondary group, not the main one (desktop host present)", async () => {
  registerDockviewApi(fakeApi());
  const anchor = workspaceStore.getState().openPane("transcript", { ref: "ref_parent_view" });
  const user = userEvent.setup();
  const Body = toolRendererFor("delegate").body!;
  render(<Body item={delegateWithTranscriptRef("ref_child_open")} live={false} />);
  await user.click(screen.getByRole("button", { name: /open transcript/i }));
  const opened = workspaceStore.getState().panes.find((p) => p.type === "transcript" && p.id !== anchor);
  expect(opened?.params).toEqual({ ref: "ref_child_open" });
  // The main slot already holds the pane being read, so this lands beside it
  // rather than replacing it - which is what "open transcript" means here.
  expect(opened?.slot).toBe("secondary");
});

test("kata 0pzz: open transcript carries the enclosing session ref as parentRef, so the reader can return", async () => {
  registerDockviewApi(fakeApi());
  const user = userEvent.setup();
  const Body = toolRendererFor("delegate").body!;
  render(<Body item={delegateWithTranscriptRef("ref_child_open")} live={false} sessionRef="ref_parent_session" />);
  await user.click(screen.getByRole("button", { name: /open transcript/i }));
  const opened = workspaceStore.getState().panes.find((p) => p.type === "transcript");
  expect(opened?.params).toEqual({ ref: "ref_child_open", parentRef: "ref_parent_session" });
});

test("kata 0pzz: with no enclosing session ref available, parentRef is simply absent (no crash, still opens)", async () => {
  registerDockviewApi(fakeApi());
  const user = userEvent.setup();
  const Body = toolRendererFor("delegate").body!;
  render(<Body item={delegateWithTranscriptRef("ref_child_open")} live={false} />);
  await user.click(screen.getByRole("button", { name: /open transcript/i }));
  const opened = workspaceStore.getState().panes.find((p) => p.type === "transcript");
  expect(opened?.params).toEqual({ ref: "ref_child_open" });
});

test("no open-transcript button when the row has no transcriptRef yet", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const noRef = delegateItem({
    id: "d_noref",
    callId: "call_noref",
    argumentsJSON: JSON.stringify({ task: "no ref yet" }),
    output: "",
  });
  render(<Body item={noRef} live={false} />);
  expect(screen.queryByRole("button", { name: /open transcript/i })).toBeNull();
});

// yt2q §4.4: the child-transcript link must be available while the subagent is
// still RUNNING, not gated on the child being done - the opened pane watches
// the live child thread.
test("a still-running row (with a transcriptRef) offers Open transcript, not gated on the child being done", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const running = delegateItem({
    id: "d_run_link",
    callId: "call_run_link",
    argumentsJSON: JSON.stringify({ task: "still running" }),
    output: JSON.stringify({ job_id: "job_rl", status: "running", transcript_ref: "ref_run_link" }),
  });
  render(<Body item={running} live={false} />);
  const row = screen.getByTestId("subagent-row");
  expect(row.dataset.kind).toBe("running"); // genuinely still running
  expect(within(row).getByRole("button", { name: /open transcript/i })).toBeTruthy();
});

// --- expanded card: disclosure + Mandate / Activity / Summary (qb8e, tv5k) -

// A child thread/read that carries a real Activity feed (two tool-call items
// with a `description` purpose) plus a final agentMessage summary - but ONLY
// when the caller asked for turns. A lean (includeTurns:false) read returns
// none, so a passing Activity/Summary assertion proves the expanded card's
// watch upgraded to { includeTurns: true } (Task 9's Option B), not that the
// lean row-dot watch happened to carry them.
function childThreadRead(params: unknown, childStatus: string) {
  const includeTurns = (params as { includeTurns: boolean }).includeTurns;
  return {
    thread: {
      id: "thr_child",
      sessionId: "sess_child",
      preview: "",
      ephemeral: false,
      modelProvider: "anthropic/claude-sonnet-4-5",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: childStatus },
      cwd: "/tmp",
      cliVersion: "1.0.0",
      source: "serf",
      serf: { ref: (params as { ref: string }).ref, capabilities: {} as never, queue: {} },
      turns: includeTurns
        ? [
            {
              id: "turn_c1",
              status: "completed",
              itemsView: "full",
              items: [
                {
                  id: "item_act_1",
                  turnId: "turn_c1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "ca1",
                  description: "step one",
                  status: "completed",
                },
                {
                  id: "item_act_2",
                  turnId: "turn_c1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "ca2",
                  description: "step two",
                  status: "completed",
                },
                {
                  // A whitespace-only description is ABSENCE, not a step - the
                  // same rule the main transcript's tool row applies
                  // (ToolRow.tsx's statedPurposeOf, shared by both surfaces).
                  // A truthiness filter here would count it and the two
                  // surfaces would disagree about whether it exists.
                  id: "item_act_blank",
                  turnId: "turn_c1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "ca3",
                  description: "   ",
                  status: "completed",
                },
                { id: "item_msg", turnId: "turn_c1", type: "agentMessage", text: "all done", status: "completed" },
              ],
            },
          ]
        : [],
    },
  };
}

test("a delegate card body shows the Mandate, a live Activity feed, and the Summary", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => childThreadRead(params, "active"));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_expand",
    callId: "call_expand",
    argumentsJSON: JSON.stringify({ task: "audit the reducer" }),
    output: JSON.stringify({ job_id: "job_e", status: "running", transcript_ref: "ref_expand_child" }),
  });
  render(<Body item={running} live={false} />);

  // Mandate is the delegation task.
  const mandate = await screen.findByTestId("subagent-mandate");
  expect(within(mandate).getByText("audit the reducer")).toBeTruthy();

  // Activity feed maps the child's tool-call description/purpose fields.
  const activity = await screen.findByTestId("subagent-activity");
  expect(within(activity).getByText("step one")).toBeTruthy();
  expect(within(activity).getByText("step two")).toBeTruthy();
  // The whitespace-only description contributed no step: exactly the two real
  // ones. (See the fixture's own note - both surfaces share statedPurposeOf.)
  expect(within(activity).getAllByRole("listitem")).toHaveLength(2);

  // Summary is the child's last agentMessage.
  const summary = screen.getByTestId("subagent-summary");
  expect(within(summary).getByText("all done")).toBeTruthy();
});

// mhcf: a child thread/read fixture with MANY purpose-bearing steps.
// childThreadRead above hard-codes exactly two real steps - useful for the
// Mandate/Activity/Summary shape, but far too few to exercise a cap. `status`
// is fixed "active" (childRunning: true) so the same fixture also exercises
// the live-step emphasis.
function manyStepsThreadRead(params: unknown, stepCount: number) {
  const includeTurns = (params as { includeTurns: boolean }).includeTurns;
  const items = [];
  for (let n = 1; n <= stepCount; n++) {
    items.push({
      id: `item_step_${n}`,
      turnId: "turn_c1",
      type: "commandExecution",
      toolName: "shell",
      callId: `ca${n}`,
      description: `step ${n}`,
      status: "completed",
    });
  }
  return {
    thread: {
      id: "thr_child",
      sessionId: "sess_child",
      preview: "",
      ephemeral: false,
      modelProvider: "anthropic/claude-sonnet-4-5",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "active" },
      cwd: "/tmp",
      cliVersion: "1.0.0",
      source: "serf",
      serf: { ref: (params as { ref: string }).ref, capabilities: {} as never, queue: {} },
      turns: includeTurns ? [{ id: "turn_c1", status: "completed", itemsView: "full", items }] : [],
    },
  };
}

test("mhcf: the Activity feed caps to the 5 most recent steps, not the first 5", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => manyStepsThreadRead(params, 20));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_cap",
    callId: "call_cap",
    argumentsJSON: JSON.stringify({ task: "long running audit" }),
    output: JSON.stringify({ job_id: "job_cap", status: "running", transcript_ref: "ref_cap_child" }),
  });
  render(<Body item={running} live={false} />);

  const activity = await screen.findByTestId("subagent-activity");
  expect(within(activity).getAllByRole("listitem")).toHaveLength(5);
  // WHICH five: the most recent (16-20), never the first five.
  for (const n of [16, 17, 18, 19, 20]) expect(within(activity).getByText(`step ${n}`)).toBeTruthy();
  for (const n of [1, 2, 14, 15]) expect(within(activity).queryByText(`step ${n}`)).toBeNull();
});

test("mhcf: the capped window renders newest-first, the live step is still (correctly) emphasized, and each <li> keeps its true ordinal", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => manyStepsThreadRead(params, 20));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_order",
    callId: "call_order",
    argumentsJSON: JSON.stringify({ task: "order audit" }),
    output: JSON.stringify({ job_id: "job_order", status: "running", transcript_ref: "ref_order_child" }),
  });
  render(<Body item={running} live={false} />);

  const activity = await screen.findByTestId("subagent-activity");
  const items = within(activity).getAllByRole("listitem") as HTMLLIElement[];

  // Newest-first: step 20 (the true latest) leads, counting down to step 16 -
  // "reachable without scrolling" regardless of section height (mhcf).
  expect(items.map((li) => li.textContent)).toEqual(["step 20", "step 19", "step 18", "step 17", "step 16"]);

  // The live-step emphasis must land on the true latest step (step 20) by
  // CONTENT, not merely on whichever <li> a stale idx===length-1 formula
  // (written against the old oldest-first, uncapped array) would still hit.
  expect(items[0]!.classList.contains(styles.activityLatest)).toBe(true);
  for (const li of items.slice(1)) expect(li.classList.contains(styles.activityLatest)).toBe(false);

  // list-style:decimal must read the TRUE step numbers (20 down to 16), not
  // "1." through "5." just because these are the first five <li>s rendered -
  // that would understate how much the child has actually done.
  expect(items.map((li) => li.value)).toEqual([20, 19, 18, 17, 16]);
});

test("the collapsed pill reads the LIVE watched status, not the frozen tool-output value", async () => {
  const fake = new FakeClient("ready");
  // Frozen delegate output says running; the live child thread reports a
  // systemError - the pill must follow the live status (yd16 write-back).
  fake.on("thread/read", (params) => childThreadRead(params, "systemError"));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_live_pill",
    callId: "call_live_pill",
    argumentsJSON: JSON.stringify({ task: "will break" }),
    output: JSON.stringify({ job_id: "job_lp", status: "running", transcript_ref: "ref_live_pill_child" }),
  });
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await waitFor(() => expect(row.dataset.kind).toBe("failed")); // running -> live failed
  expect(row.dataset.kind).toBe("failed");
});

// g5kf: the honest-clock bug. A foreground_timeout freezes the delegate's own
// tool output at status:"running" forever (agent/job_delegate.go's mainline
// path for any non-trivial delegate, not an edge case), so the watched
// child's live thread status is the ONLY remaining signal - and once that
// child leaves the daemon's live roster entirely, it must demote off
// "running" rather than reading as though it were still genuinely working.
test("g5kf: a child that leaves the live roster (notLoaded) demotes off running to unknown, not stuck running forever", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => childThreadRead(params, "notLoaded"));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_notloaded",
    callId: "call_notloaded",
    argumentsJSON: JSON.stringify({ task: "orphaned by a hub restart" }),
    output: JSON.stringify({ job_id: "job_nl", status: "running", transcript_ref: "ref_notloaded_child" }),
  });
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await waitFor(() => expect(row.dataset.kind).toBe("unknown"));
  expect(within(row).queryByText("unknown")).toBeNull();
  expect(within(row).queryByText("running")).toBeNull();
});

// --- dr7e: serf/job/finished's own reason/resumable/exhaustion detail -----

test("dr7e: the collapsed preview prefers a job-notification liveReason over the frozen tool-output reason", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const running = delegateItem({
    id: "d_livereason",
    callId: "call_livereason",
    argumentsJSON: JSON.stringify({ task: "still working" }),
    output: JSON.stringify({ job_id: "job_lr", status: "running", reason: "frozen reason" }),
  });
  render(<Body item={running} live={false} />);
  act(() =>
    updateSubagentRowIfExists(turnScopeKey(undefined, "turn_1"), "job:job_lr", { liveReason: "exhausted budget" }),
  );

  const row = screen.getByTestId("subagent-row");
  expect(within(row).getByText("exhausted budget")).toBeTruthy();
  expect(within(row).queryByText("frozen reason")).toBeNull();
});

test("dr7e: an expanded card shows exhaustion budget/limit and resumable once a serf/job/finished notification sets them", async () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const settled = delegateItem({
    id: "d_exhaust",
    callId: "call_exhaust",
    argumentsJSON: JSON.stringify({ task: "long running task" }),
    output: JSON.stringify({ job_id: "job_ex", status: "exhausted" }),
  });
  render(<Body item={settled} live={false} />);
  act(() =>
    updateSubagentRowIfExists(turnScopeKey(undefined, "turn_1"), "job:job_ex", {
      resumable: true,
      exhaustionBudget: "30m",
      exhaustionLimit: 60,
    }),
  );

  const detail = await screen.findByTestId("subagent-job-detail");
  expect(within(detail).getByText("Exhaustion budget: 30m of 60")).toBeTruthy();
  expect(within(detail).getByText("Resumable")).toBeTruthy();
});

test("dr7e: no Job detail section renders when neither resumable nor exhaustion fields are set", async () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const settled = delegateItem({
    id: "d_noexhaust",
    callId: "call_noexhaust",
    argumentsJSON: JSON.stringify({ task: "quick task" }),
    output: JSON.stringify({ job_id: "job_noex", status: "completed" }),
  });
  render(<Body item={settled} live={false} />);

  await screen.findByTestId("subagent-mandate");
  expect(screen.queryByTestId("subagent-job-detail")).toBeNull();
});

// --- evch: the module must be visible without a click ----------------------
//
// A tool-call row with a body starts collapsed by default (ToolCallItem.tsx).
// A delegate call announces itself with a module row regardless of a click,
// and the child watch that drives live status in that row is not gated behind
// any explicit disclosure toggle - evch's exact complaint.
// Mirrors task_list's own `autoExpand: () => true` (a status card, not a
// fold-to-open row): a delegate is always worth seeing without a click.
test("a single delegate has one top-level disclosure, one status rail, and one visible task", () => {
  const turn: TurnModel = { id: "turn_evch", status: "completed", items: [] };
  const started = delegateItem({
    id: "d_evch",
    turnId: "turn_evch",
    callId: "call_evch",
    description: "Single delegation",
    argumentsJSON: JSON.stringify({ task: "Inspect one row, one task" }),
    output: JSON.stringify({ job_id: "job_evch", status: "running" }),
  });
  render(<ToolCallItem item={started} turn={turn} live={false} />);
  const tool = screen.getByTestId("tool-call-item");
  const module = screen.getByTestId("subagent-module");
  expect(screen.getAllByRole("img", { name: "Working" })).toHaveLength(1);
  expect(within(module).queryByText(/1 running/)).toBeNull(); // single-row mode omits the tally header
  expect(screen.getAllByText("Inspect one row, one task")).toHaveLength(1);
  expect(module.querySelectorAll("details > summary")).toHaveLength(0);
  expect(tool.querySelectorAll("details > summary")).toHaveLength(1);
  const statusRail = screen.getByTestId("tool-row-status");
  expect(statusRail.children).toHaveLength(1);
  expect(statusRail.firstElementChild?.getAttribute("role")).toBe("img");
});

test("a multi-row aggregate shows task text in row summary and suppresses duplicate mandate sections", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;

  render(
    <>
      <Body
        item={delegateItem({
          id: "d_multi_running",
          turnId: "turn_multi",
          callId: "call_multi_running",
          description: "running purpose",
          argumentsJSON: JSON.stringify({ task: "Running mandate text" }),
          output: JSON.stringify({ job_id: "job_multi_running", status: "running", transcript_ref: "ref_multi_run" }),
        })}
        live={false}
      />
      <Body
        item={delegateItem({
          id: "d_multi_done",
          turnId: "turn_multi",
          callId: "call_multi_done",
          description: "done purpose",
          argumentsJSON: JSON.stringify({ task: "Done mandate text" }),
          output: JSON.stringify({ job_id: "job_multi_done", status: "completed", transcript_ref: "ref_multi_done" }),
        })}
        live={false}
      />
    </>,
  );

  const rows = screen.getAllByTestId("subagent-row");
  expect(rows).toHaveLength(2);

  const runningRow = rows.find((row) => row.getAttribute("data-kind") === "running");
  const doneRow = rows.find((row) => row.getAttribute("data-kind") === "done");
  expect(runningRow).toBeTruthy();
  expect(doneRow).toBeTruthy();

  expect(within(runningRow!).queryByText("Running mandate text")).toBeTruthy();
  expect(within(runningRow!).queryByTestId("subagent-mandate")).toBeNull();
  expect(within(doneRow!).queryByText("Done mandate text")).toBeTruthy();
  expect(within(doneRow!).queryByTestId("subagent-mandate")).toBeNull();
});

test("a multi-row aggregate updates its tally from the watched live kind", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => {
    const ref = (params as { ref: string }).ref;
    return childThreadRead(params, ref === "ref_multi_live_failed" ? "systemError" : "closed");
  });
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  render(
    <>
      <Body
        item={delegateItem({
          id: "d_multi_live_failed",
          turnId: "turn_multi_live",
          callId: "call_multi_live_failed",
          argumentsJSON: JSON.stringify({ task: "live failure" }),
          output: JSON.stringify({
            job_id: "job_multi_live_failed",
            status: "running",
            transcript_ref: "ref_multi_live_failed",
          }),
        })}
        live={false}
      />
      <Body
        item={delegateItem({
          id: "d_multi_live_done",
          turnId: "turn_multi_live",
          callId: "call_multi_live_done",
          argumentsJSON: JSON.stringify({ task: "live completion" }),
          output: JSON.stringify({
            job_id: "job_multi_live_done",
            status: "running",
            transcript_ref: "ref_multi_live_done",
          }),
        })}
        live={false}
      />
    </>,
  );

  const module = screen.getByTestId("subagent-module");
  await waitFor(() => expect(within(module).getByText("1 failed · 1 done")).toBeTruthy());
  const failedRow = within(module)
    .getAllByTestId("subagent-row")
    .find((row) => row.getAttribute("data-kind") === "failed");
  expect(failedRow).toBeTruthy();
});

// --- 7f7c: the Mandate must not be clipped ----------------------------------
//
// The one-line row summary clips the task to TASK_CLIP (80 chars) - correct
// for a single line - but rowFromDelegateItem fed that SAME clipped string
// into the Mandate section, so even the "read more" affordance never showed
// the rest of the delegated prompt. The Mandate is the deliberate expanded
// view; it must carry the full, untruncated task text.
test("7f7c: the Mandate section shows the full, unclipped task text past the 80-char summary clip", async () => {
  const Body = toolRendererFor("delegate").body!;
  const longTask =
    "Please test delegation by inspecting the current working directory. Report the directory contents without changing any files.";
  expect(longTask.length).toBeGreaterThan(80);
  const running = delegateItem({
    id: "d_full_mandate",
    callId: "call_full_mandate",
    argumentsJSON: JSON.stringify({ task: longTask }),
    output: JSON.stringify({ job_id: "job_fm", status: "running" }),
  });
  render(<Body item={running} live={false} />);
  const mandate = await screen.findByTestId("subagent-mandate");
  expect(within(mandate).getByText(longTask)).toBeTruthy();
});

test("the Mandate keeps task line breaks in sans prose with safe wrapping", () => {
  const mandate = /\.mandate\s*\{([^}]*)\}/.exec(moduleCss());
  expect(mandate).not.toBeNull();
  expect(mandate![1]).toMatch(/font-family:\s*var\(--font-sans\)/);
  expect(mandate![1]).toMatch(/white-space:\s*pre-wrap/);
  expect(mandate![1]).toMatch(/overflow-wrap:\s*anywhere/);
});
