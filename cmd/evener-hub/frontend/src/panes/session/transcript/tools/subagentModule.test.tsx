import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy, StrictMode } from "react";
import { afterAll, afterEach, beforeEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { ToolCallItem } from "../ToolCallItem";
import { toolRendererFor } from "../toolRenderers";
import { classifyJobStatus, resolveRowKey, rowFromDelegateItem, rowKindFromChildStatus } from "./subagentModule";
import { resetSubagentModuleStoreForTests, turnScopeKey, updateSubagentRowIfExists } from "./subagentModuleStore";
import "./subagentModule";
import type { DockviewApi } from "dockview-core";
import { type ItemModel, SYSTEM_PRELUDE_TURN_ID, type TurnModel } from "../../../../protocol/model";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { EvenerDelegateInfo } from "../../../../protocol/types.gen";
import { registerPaneForTests } from "../../../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import rawCssModule from "./subagentmodule.module.css";

// A minimal, test-only "session" pane registration - real registerPane/
// paneFor/openPane machinery, just without pulling in the actual
// panes/session module (a heavier, T1-owned dependency this test doesn't
// need: it only asserts openPane was called correctly, never that a real
// SessionPane renders).
afterAll(
  registerPaneForTests({
    id: "session",
    title: () => "test session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);

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
  quoteLive: requireClass(rawCssModule.quoteLive, "subagentmodule.module.css", "quoteLive"),
  quoteMsg: requireClass(rawCssModule.quoteMsg, "subagentmodule.module.css", "quoteMsg"),
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
  expect(resolveRowKey(undefined, undefined, "call_1")).toBe("call:call_1");
  expect(resolveRowKey(undefined, "job_1", "call_1")).not.toBe(resolveRowKey("dlg_1", "job_1", "call_1"));
  // The per-kind prefix is what stops a delegate id colliding with an unrelated
  // job id that happens to be the same raw string.
  expect(resolveRowKey("x", undefined, "f")).not.toBe(resolveRowKey(undefined, "x", "f"));
});

test("rowFromDelegateItem uses stable delegate_id and rejects activation-only job_id", () => {
  const stable = rowFromDelegateItem(
    delegateItem({
      callId: "call_stable",
      argumentsJSON: JSON.stringify({ task: "inspect" }),
      output: JSON.stringify({
        delegate_id: "dlg_stable",
        status: "running",
        transcript_ref: "local:sess_child",
      }),
    }),
  );
  expect(stable).toMatchObject({
    rowKey: "dlg:dlg_stable",
    row: { delegateId: "dlg_stable", transcriptRef: "local:sess_child", task: "inspect" },
  });
  expect(stable?.row).not.toHaveProperty("jobId");

  expect(
    rowFromDelegateItem(
      delegateItem({
        callId: "call_legacy",
        argumentsJSON: JSON.stringify({ task: "legacy" }),
        output: JSON.stringify({ job_id: "job_legacy", status: "running" }),
      }),
    ),
  ).toBeNull();
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
// evicted, orphaned, or lost to a hub restart (cmd/evener-hub/app_threadread.go's
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
    output: JSON.stringify({ delegate_id: "job_1", status: "running", transcript_ref: "ref_child_1" }),
  });
  const second = delegateItem({
    id: "d2",
    callId: "call_d2",
    argumentsJSON: JSON.stringify({ task: "second child" }),
    output: JSON.stringify({ delegate_id: "job_2", status: "running", transcript_ref: "ref_child_2" }),
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
  expect(screen.getAllByTestId("subagent-row")).toHaveLength(2);
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
    output: JSON.stringify({ delegate_id: "job_sess_a", status: "running" }),
  });
  const sessionB = delegateItem({
    id: "d_sess_b",
    turnId: "turn_21",
    callId: "call_sess_b",
    argumentsJSON: JSON.stringify({ task: "session B's own task" }),
    output: JSON.stringify({ delegate_id: "job_sess_b", status: "running" }),
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
});

// 78nj: the Disclosure id used a bare turnId, not turnScopeKey(sessionRef,
// turnId) - the same collision class as kata 8525 above, but on the
// disclosureStore's open/closed map rather than this store's rows. Both
// items below rely on the SAME defaults (id "item_1", turnId "turn_1", no
// callId) and neither output carries delegate_id/job_id, so both resolve to
// the identical fallback rowKey call:item_1 under the identical turnId -
// exactly the "a call that errors before minting any handle" scenario the
// kata describes. Only sessionRef tells the two rows apart.
test("78nj: one session's expanded card never opens another session's quote list (disclosure ids are session-scoped)", async () => {
  const user = userEvent.setup();
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
  expect(within(moduleA!).getAllByTestId("subagent-row")).toHaveLength(1);
  expect(within(moduleB!).getAllByTestId("subagent-row")).toHaveLength(1);

  // Both items rely on the SAME defaults (id "item_1", turnId "turn_1", no
  // callId, no delegate_id), so both resolve to the identical fallback rowKey
  // call:item_1 under the identical turnId - only sessionRef tells their
  // per-card disclosure ids apart.
  await user.click(within(moduleA!).getByRole("button", { name: /show recent activity/i }));
  expect(within(moduleA!).getByTestId("subagent-quotes")).toBeTruthy();
  expect(within(moduleB!).queryByTestId("subagent-quotes")).toBeNull();
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
    output: JSON.stringify({
      delegate_id: "job_collapsed_live",
      status: "running",
      transcript_ref: "ref_collapsed_live",
    }),
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
      delegate_id: "job_live_collapsed_initial",
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
  // Both rows persist in the store (it never evicts) - the follower's module
  // shows the unmounted leader's row beside its own.
  expect(screen.getAllByTestId("subagent-row")).toHaveLength(2);
});

test("a delegate row migrates fallback identity to stable delegate identity without losing its live overlay", () => {
  const Body = toolRendererFor("delegate").body!;
  const scopeKey = turnScopeKey(undefined, "turn_row_migrates");
  const beforeJob = delegateItem({
    id: "d_row_migrates",
    turnId: "turn_row_migrates",
    callId: "call_row_migrates",
    argumentsJSON: JSON.stringify({ task: "migrate this row" }),
    output: undefined,
  });
  const { rerender } = render(<Body item={beforeJob} live={false} />);

  act(() => {
    updateSubagentRowIfExists(scopeKey, "call:call_row_migrates", {
      liveKind: "failed",
      liveReason: "watch failed",
    });
  });

  const withJob = { ...beforeJob, output: JSON.stringify({ delegate_id: "job_row_migrates", status: "completed" }) };
  rerender(<Body item={withJob} live={false} />);

  const rows = screen.getAllByTestId("subagent-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]?.getAttribute("data-kind")).toBe("failed");
  // The failed card's folded quote is the live reason, verbatim, ✕-marked.
  expect(within(rows[0]!).getByTestId("subagent-quote").textContent).toBe("✕ watch failed");
});

// --- row content ----------------------------------------------------------

test("a running row in a multi-row stack shows its kind (identity lives on the delegate's own tool row)", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const running = delegateItem({
    id: "d_run",
    callId: "call_run",
    argumentsJSON: JSON.stringify({ task: "still working" }),
    output: JSON.stringify({ delegate_id: "job_r", status: "running", transcript_ref: "ref_r" }),
  });
  const done = delegateItem({
    id: "d_done_task_row",
    callId: "call_done_task_row",
    argumentsJSON: JSON.stringify({ task: "other task" }),
    output: JSON.stringify({ delegate_id: "job_d_row", status: "completed", transcript_ref: "ref_done" }),
  });
  render(
    <>
      <Body item={running} live={false} />
      <Body item={done} live={false} />
    </>,
  );
  const rows = screen.getAllByTestId("subagent-row");
  expect(rows).toHaveLength(2);
  // Worst-first: the running card sits above the done one.
  expect(rows.map((r) => r.getAttribute("data-kind"))).toEqual(["running", "done"]);
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
    output: JSON.stringify({ delegate_id: "job_d", status: "completed", transcript_ref: "ref_d" }),
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
      source: "evener",
      evener: { ref: (params as { ref: string }).ref, capabilities: {} as never, queue: { revision: 0 } },
    },
  }));
  connectionStore.getState().connect(fake);

  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const running = delegateItem({
    id: "d_watch",
    callId: "call_watch",
    argumentsJSON: JSON.stringify({ task: "watched task" }),
    output: JSON.stringify({ delegate_id: "job_w", status: "running", transcript_ref: "ref_watched_child" }),
  });
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await waitFor(() => expect(row.dataset.kind).toBe("failed"));
  expect(screen.getByTestId("subagent-module").querySelector('[data-kind="failed"]')).toBeTruthy();
});

test("a failed card carries the danger rail itself - there is no module chrome to average it away", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const failed = delegateItem({
    id: "d_fail",
    callId: "call_fail",
    argumentsJSON: JSON.stringify({ task: "will fail" }),
    output: JSON.stringify({ delegate_id: "job_f", status: "failed", transcript_ref: "ref_f", reason: "build error" }),
  });
  render(<Body item={failed} live={false} />);
  const row = screen.getByTestId("subagent-row");
  expect(row.dataset.kind).toBe("failed");
  // The folded quote IS the failure reason, verbatim, ✕-marked - the exception
  // earns the explanation without a click.
  expect(within(row).getByTestId("subagent-quote").textContent).toBe("✕ build error");

  // jsdom evaluates no cascade, so the rail's visual effect can only be
  // asserted at the declaration level (the same rowCss() idiom
  // toolRowGrammar.test.tsx uses). Without this half, the DOM assertion above
  // would keep passing even if subagentmodule.module.css lost the rule.
  const css = moduleCss();
  expect(css).toMatch(/\.card\[data-kind="failed"\]\s*\{[^}]*border-left-color:\s*var\(--danger\)/);
  expect(css).toMatch(/\.card\[data-kind="failed"\]\s*\{[^}]*background:\s*var\(--danger-bg\)/);
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
    output: JSON.stringify({ delegate_id: "job_stopped", status: "cancelled", transcript_ref: "ref_stopped" }),
  });
  render(<Body item={stopped} live={false} />);
  const module = screen.getByTestId("subagent-module");
  const row = screen.getByTestId("subagent-row");
  expect(row.dataset.kind).toBe("stopped");
  expect(within(module).queryByText(/1 stopped/)).toBeNull();
  expect(within(module).queryByText(/done/)).toBeNull();
  // The card is headless - no tag, no task text inside; identity lives on the
  // delegate's own tool row.
  expect(within(row).queryByTestId("subagent-tag")).toBeNull();
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
          output: JSON.stringify({ delegate_id: `job_done_${i}`, status: "completed", transcript_ref: `ref_${i}` }),
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
        output: JSON.stringify({ delegate_id: "job_running_extra", status: "running" }),
      })}
      live={false}
    />,
  );
  render(bodies);
  // 6 done rows visible + the running row always visible = 7 rows shown;
  // 2 done rows folded behind "+2 more".
  expect(screen.getAllByTestId("subagent-row")).toHaveLength(7);
  expect(screen.getAllByTestId("subagent-row").some((r) => r.getAttribute("data-kind") === "running")).toBe(true);
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
          output: JSON.stringify({ delegate_id: `job_fold_${i}`, status: "completed" }),
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
    output: JSON.stringify({ delegate_id: "job_ref", status: "running", transcript_ref: ref }),
  });
}

test("open transcript opens the read-only transcript pane (mobile / no dockview host): plain full-screen open, not a session pane", async () => {
  registerDockviewApi(null); // StackHost registers no api - the mobile signal
  const user = userEvent.setup();
  const turn: TurnModel = { id: "turn_open_mobile", status: "completed", items: [] };
  render(
    <ToolCallItem
      item={{ ...delegateWithTranscriptRef("ref_child_open"), turnId: turn.id }}
      turn={turn}
      live={false}
    />,
  );
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
  const turn: TurnModel = { id: "turn_open_desktop", status: "completed", items: [] };
  render(
    <ToolCallItem
      item={{ ...delegateWithTranscriptRef("ref_child_open"), turnId: turn.id }}
      turn={turn}
      live={false}
    />,
  );
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
  const turn: TurnModel = { id: "turn_open_parentref", status: "completed", items: [] };
  render(
    <ToolCallItem
      item={{ ...delegateWithTranscriptRef("ref_child_open"), turnId: turn.id }}
      turn={turn}
      live={false}
      sessionRef="ref_parent_session"
    />,
  );
  await user.click(screen.getByRole("button", { name: /open transcript/i }));
  const opened = workspaceStore.getState().panes.find((p) => p.type === "transcript");
  expect(opened?.params).toEqual({ ref: "ref_child_open", parentRef: "ref_parent_session" });
});

test("kata 0pzz: with no enclosing session ref available, parentRef is simply absent (no crash, still opens)", async () => {
  registerDockviewApi(fakeApi());
  const user = userEvent.setup();
  const turn: TurnModel = { id: "turn_open_noparent", status: "completed", items: [] };
  render(
    <ToolCallItem
      item={{ ...delegateWithTranscriptRef("ref_child_open"), turnId: turn.id }}
      turn={turn}
      live={false}
    />,
  );
  await user.click(screen.getByRole("button", { name: /open transcript/i }));
  const opened = workspaceStore.getState().panes.find((p) => p.type === "transcript");
  expect(opened?.params).toEqual({ ref: "ref_child_open" });
});

test("no open-transcript button when the row has no transcriptRef yet", () => {
  const noRef = delegateItem({
    id: "d_noref",
    callId: "call_noref",
    argumentsJSON: JSON.stringify({ task: "no ref yet" }),
    output: "",
  });
  const turn: TurnModel = { id: "turn_noref", status: "completed", items: [] };
  render(<ToolCallItem item={{ ...noRef, turnId: turn.id }} turn={turn} live={false} />);
  expect(screen.queryByRole("button", { name: /open transcript/i })).toBeNull();
});

// yt2q §4.4: the child-transcript link must be available while the subagent is
// still RUNNING, not gated on the child being done - the opened pane watches
// the live child thread.
test("a still-running row (with a transcriptRef) offers Open transcript, not gated on the child being done", () => {
  const running = delegateItem({
    id: "d_run_link",
    callId: "call_run_link",
    argumentsJSON: JSON.stringify({ task: "still running" }),
    output: JSON.stringify({ delegate_id: "job_rl", status: "running", transcript_ref: "ref_run_link" }),
  });
  const turn: TurnModel = { id: "turn_run_link", status: "inProgress", items: [] };
  render(<ToolCallItem item={{ ...running, turnId: turn.id }} turn={turn} live={true} />);
  const tool = screen.getByTestId("tool-call-item");
  expect(within(tool).getByRole("img", { name: "Working" })).toBeTruthy(); // genuinely still running
  expect(within(tool).getByRole("button", { name: /open transcript/i })).toBeTruthy();
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
      source: "evener",
      evener: { ref: (params as { ref: string }).ref, capabilities: {} as never, queue: { revision: 0 } },
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

test("a folded card quotes the child's newest own words from the full event stream - verbatim, no quote-mark dressing", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => childThreadRead(params, "active"));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_quote",
    callId: "call_quote",
    argumentsJSON: JSON.stringify({ task: "audit the reducer" }),
    output: JSON.stringify({ delegate_id: "job_q", status: "running", transcript_ref: "ref_quote_child" }),
  });
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  // The newest child-authored line overall is its final agent message. The
  // card quotes it verbatim: no added quotation marks, no paraphrase.
  const quote = await within(row).findByTestId("subagent-quote");
  expect(quote.textContent).toBe("all done");
  expect(quote.textContent).not.toMatch(/[“”"]/);
});

test("expanding a card lists recent quotes - purposes plain, messages italic - each with its runtime and timestamp", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => {
    const base = childThreadRead(params, "active");
    if ((params as { includeTurns: boolean }).includeTurns) {
      const items = base.thread.turns[0]!.items as { startedAt?: string; completedAt?: string }[];
      // Step one: 6s starting at a fixed local wall-clock moment.
      items[0]!.startedAt = new Date(2026, 7, 20, 9, 41, 2).toISOString();
      items[0]!.completedAt = new Date(2026, 7, 20, 9, 41, 8).toISOString();
    }
    return base;
  });
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_quotes",
    callId: "call_quotes",
    argumentsJSON: JSON.stringify({ task: "audit the reducer" }),
    output: JSON.stringify({ delegate_id: "job_qs", status: "running", transcript_ref: "ref_quotes_child" }),
  });
  const user = userEvent.setup();
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await user.click(within(row).getByRole("button", { name: /show recent activity/i }));
  const quotes = await within(row).findByTestId("subagent-quotes");
  const items = within(quotes).getAllByRole("listitem") as HTMLLIElement[];
  // Two real steps plus the final message; the whitespace-only description
  // contributed no quote (the same statedPurposeOf rule the old feed used).
  expect(items).toHaveLength(3);
  expect(items.map((li) => li.value)).toEqual([1, 2, 3]);

  // Runtime + timestamp ride each stamped quote: "6s · HH:MM:SS" local. The
  // expected stamp is computed through the same Date parsing the formatter
  // uses, so the suite stays timezone-independent.
  const parsed = new Date(2026, 7, 20, 9, 41, 2);
  const stamp = `${String(parsed.getHours()).padStart(2, "0")}:${String(parsed.getMinutes()).padStart(2, "0")}:${String(parsed.getSeconds()).padStart(2, "0")}`;
  expect(items[0]!.textContent).toContain("step one");
  expect(items[0]!.textContent).toContain("6s");
  expect(items[0]!.textContent).toContain(stamp);
  // Unstamped quotes render no runtime segment rather than a guess.
  expect(items[1]!.textContent).toBe("step two");

  // The message renders with the quoteMsg (italic) class; purposes stay plain.
  expect(items[2]!.querySelector(`.${styles.quoteMsg}`)?.textContent).toBe("all done");
  expect(items[0]!.querySelector(`.${styles.quoteMsg}`)).toBeNull();
});

test("an expanded card with no child activity yet says so instead of rendering an empty list", async () => {
  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_empty_quotes",
    callId: "call_empty_quotes",
    argumentsJSON: JSON.stringify({ task: "just spawned" }),
    output: JSON.stringify({ delegate_id: "job_eq", status: "running" }),
  });
  const user = userEvent.setup();
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await user.click(within(row).getByRole("button", { name: /show recent activity/i }));
  const quotes = await within(row).findByTestId("subagent-quotes");
  expect(within(quotes).getByText(/no activity yet/i)).toBeTruthy();
  expect(within(quotes).queryAllByRole("listitem")).toHaveLength(0);
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
      source: "evener",
      evener: { ref: (params as { ref: string }).ref, capabilities: {} as never, queue: { revision: 0 } },
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
    output: JSON.stringify({ delegate_id: "job_cap", status: "running", transcript_ref: "ref_cap_child" }),
  });
  const user = userEvent.setup();
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await user.click(within(row).getByRole("button", { name: /show recent activity/i }));
  const activity = await screen.findByTestId("subagent-quotes");
  expect(within(activity).getAllByRole("listitem")).toHaveLength(5);
  // WHICH five: the most recent (16-20), never the first five.
  for (const n of [16, 17, 18, 19, 20]) expect(within(activity).getByText(`step ${n}`)).toBeTruthy();
  for (const n of [1, 2, 14, 15]) expect(within(activity).queryByText(`step ${n}`)).toBeNull();
});

test("mhcf: the capped window renders chronologically (most recent last), the live step is still (correctly) emphasized, and each <li> keeps its true ordinal", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => manyStepsThreadRead(params, 20));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_order",
    callId: "call_order",
    argumentsJSON: JSON.stringify({ task: "order audit" }),
    output: JSON.stringify({ delegate_id: "job_order", status: "running", transcript_ref: "ref_order_child" }),
  });
  const user = userEvent.setup();
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await user.click(within(row).getByRole("button", { name: /show recent activity/i }));
  const activity = await screen.findByTestId("subagent-quotes");
  const items = within(activity).getAllByRole("listitem") as HTMLLIElement[];

  // Chronological: step 16 leads, counting up to step 20 (the true latest)
  // last - the feed reads the way the child's own transcript does, top to
  // bottom, with the live step at the natural reading end.
  expect(items.map((li) => li.textContent)).toEqual(["step 16", "step 17", "step 18", "step 19", "step 20"]);

  // The live-step emphasis must land on the true latest step (step 20) by
  // CONTENT, not merely on whichever <li> a stale idx===length-1 formula
  // (written against the old uncapped array) would still hit.
  expect(items[4]!.classList.contains(styles.quoteLive)).toBe(true);
  for (const li of items.slice(0, 4)) expect(li.classList.contains(styles.quoteLive)).toBe(false);

  // list-style:decimal must read the TRUE step numbers (16 up to 20), not
  // "1." through "5." just because these are the five <li>s rendered -
  // that would understate how much the child has actually done.
  expect(items.map((li) => li.value)).toEqual([16, 17, 18, 19, 20]);
});

// A timing annotation is not an action: round_timings items carry a purpose-
// like description ("Round timings") that would otherwise flood the feed -
// one per round - and crowd real steps out of the five-slot window. They are
// elided by eventKind, and the remaining steps keep contiguous true ordinals
// (the elided items never counted as steps at all).
test("the Activity feed elides round_timings items and ordinals count only real steps", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => {
    const base = manyStepsThreadRead(params, 6);
    const includeTurns = (params as { includeTurns: boolean }).includeTurns;
    if (includeTurns) {
      // Intersperse round_timings items the way the projector emits them:
      // a systemMessage with the round_timings eventKind and a "Round
      // timings" description (internal/appprojector/appwire_projection.go's
      // EventRoundTimings case). The fixture's items array is typed off the
      // tool-call shape manyStepsThreadRead pushes, so widen it for these
      // systemMessage entries.
      (base.thread.turns[0]!.items as object[]).splice(
        2,
        0,
        {
          id: "item_rt_1",
          turnId: "turn_c1",
          type: "systemMessage",
          eventKind: "round_timings",
          description: "Round timings",
          text: "Round 1 total=1.5s llm=1.2s",
          status: "completed",
        },
        {
          id: "item_rt_2",
          turnId: "turn_c1",
          type: "systemMessage",
          eventKind: "round_timings",
          description: "Round timings",
          text: "Round 2 total=1.4s llm=1.1s",
          status: "completed",
        },
      );
    }
    return base;
  });
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_rt",
    callId: "call_rt",
    argumentsJSON: JSON.stringify({ task: "timing audit" }),
    output: JSON.stringify({ delegate_id: "job_rt", status: "running", transcript_ref: "ref_rt_child" }),
  });
  const user = userEvent.setup();
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await user.click(within(row).getByRole("button", { name: /show recent activity/i }));
  const activity = await screen.findByTestId("subagent-quotes");
  expect(within(activity).queryByText("Round timings")).toBeNull();
  const items = within(activity).getAllByRole("listitem") as HTMLLIElement[];
  // Six real steps, capped to the five most recent (2-6); the two
  // round_timings items never entered the count, so ordinals run 2..6, not
  // 4..8.
  expect(items.map((li) => li.textContent)).toEqual(["step 2", "step 3", "step 4", "step 5", "step 6"]);
  expect(items.map((li) => li.value)).toEqual([2, 3, 4, 5, 6]);
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
    output: JSON.stringify({ delegate_id: "job_lp", status: "running", transcript_ref: "ref_live_pill_child" }),
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
    output: JSON.stringify({ delegate_id: "job_nl", status: "running", transcript_ref: "ref_notloaded_child" }),
  });
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await waitFor(() => expect(row.dataset.kind).toBe("unknown"));
  expect(within(row).queryByText("unknown")).toBeNull();
  expect(within(row).queryByText("running")).toBeNull();
});

// --- dr7e: evener/job/finished's own reason/resumable/exhaustion detail -----

test("the collapsed preview prefers a live overlay reason over the frozen tool-output reason", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const running = delegateItem({
    id: "d_livereason",
    callId: "call_livereason",
    argumentsJSON: JSON.stringify({ task: "still working" }),
    output: JSON.stringify({ delegate_id: "job_lr", status: "running", reason: "frozen reason" }),
  });
  render(<Body item={running} live={false} />);
  act(() =>
    updateSubagentRowIfExists(turnScopeKey(undefined, "turn_1"), "dlg:job_lr", { liveReason: "exhausted budget" }),
  );

  const row = screen.getByTestId("subagent-row");
  expect(within(row).getByText("exhausted budget")).toBeTruthy();
  expect(within(row).queryByText("frozen reason")).toBeNull();
});

test("an expanded card shows stable exhaustion budget, limit, and resumable evidence", async () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const settled = delegateItem({
    id: "d_exhaust",
    callId: "call_exhaust",
    argumentsJSON: JSON.stringify({ task: "long running task" }),
    output: JSON.stringify({ delegate_id: "job_ex", status: "exhausted" }),
  });
  const user = userEvent.setup();
  render(<Body item={settled} live={false} />);
  act(() =>
    updateSubagentRowIfExists(turnScopeKey(undefined, "turn_1"), "dlg:job_ex", {
      resumable: true,
      exhaustionBudget: "30m",
      exhaustionLimit: 60,
    }),
  );

  // The Job detail lives in the expanded region, behind the card's chevron.
  const row = screen.getByTestId("subagent-row");
  await user.click(within(row).getByRole("button", { name: /show recent activity/i }));
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
    output: JSON.stringify({ delegate_id: "job_noex", status: "completed" }),
  });
  const user = userEvent.setup();
  render(<Body item={settled} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await user.click(within(row).getByRole("button", { name: /show recent activity/i }));
  await within(row).findByTestId("subagent-quotes");
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
    argumentsJSON: JSON.stringify({ task: "Inspect one row, one task" }),
    output: JSON.stringify({ delegate_id: "job_evch", status: "running" }),
  });
  render(<ToolCallItem item={started} turn={turn} live={false} />);
  const tool = screen.getByTestId("tool-call-item");
  const module = screen.getByTestId("subagent-module");
  expect(screen.getAllByRole("img", { name: "Working" })).toHaveLength(1);
  expect(within(module).queryByText(/1 running/)).toBeNull(); // single-row mode omits the tally header
  // Exactly once: the delegate row's own summary. The card below is headless -
  // no tag, no second copy (the Rail × Quote redesign's dedup).
  expect(screen.getAllByText("Inspect one row, one task")).toHaveLength(1);
  expect(module.querySelectorAll("details > summary")).toHaveLength(0);
  expect(tool.querySelectorAll("details > summary")).toHaveLength(1);
  const statusRail = screen.getByTestId("tool-row-status");
  expect(statusRail.children).toHaveLength(1);
  expect(statusRail.firstElementChild?.getAttribute("role")).toBe("img");
});

test("a multi-row stack shows each task as its card's tag, with the old section chrome gone", () => {
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
          output: JSON.stringify({
            delegate_id: "job_multi_running",
            status: "running",
            transcript_ref: "ref_multi_run",
          }),
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
          output: JSON.stringify({
            delegate_id: "job_multi_done",
            status: "completed",
            transcript_ref: "ref_multi_done",
          }),
        })}
        live={false}
      />
    </>,
  );

  const rows = screen.getAllByTestId("subagent-row");
  expect(rows).toHaveLength(2);

  // The cards are headless: the tag/open head row is gone - identity and the
  // open control live on each delegate's own tool row (ToolCallItem).
  expect(screen.queryByTestId("subagent-tag")).toBeNull();
  expect(within(screen.getByTestId("subagent-module")).queryByRole("button", { name: /open transcript/i })).toBeNull();
  // The Mandate/Activity/Summary sections are gone; quote lists stay folded
  // behind each card's own chevron until asked for.
  expect(screen.queryByTestId("subagent-mandate")).toBeNull();
  expect(screen.queryByTestId("subagent-activity")).toBeNull();
  expect(screen.queryByTestId("subagent-summary")).toBeNull();
  expect(screen.queryByTestId("subagent-quotes")).toBeNull();
});

test("no tally header; rows sit worst-first, so a live failure surfaces above a clean done", async () => {
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
            delegate_id: "job_multi_live_failed",
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
            delegate_id: "job_multi_live_done",
            status: "running",
            transcript_ref: "ref_multi_live_done",
          }),
        })}
        live={false}
      />
    </>,
  );

  const module = screen.getByTestId("subagent-module");
  await waitFor(() => {
    const kinds = within(module)
      .getAllByTestId("subagent-row")
      .map((row) => row.getAttribute("data-kind"));
    expect(kinds).toEqual(["failed", "done"]);
  });
  // The tally header is deleted chrome: the cards themselves carry the state.
  expect(module.textContent).not.toMatch(/\d+ failed/);
  expect(module.textContent).not.toMatch(/\d+ running/);
});

test("the card stylesheet declares the rail-quote anatomy: square corners, state rails, ellipsis quote, chromeless module, no head/tag", () => {
  const css = moduleCss();
  // Square corners were an explicit design amendment (no curved borders).
  expect(css).toMatch(/\.card\s*\{[^}]*border-radius:\s*0/);
  // The 2px left rail carries state colour; needs-you earns the attention wash.
  expect(css).toMatch(/\.card\[data-kind="running"\]\s*\{[^}]*border-left-color:\s*var\(--alive\)/);
  expect(css).toMatch(/\.card\[data-attention="true"\]\s*\{[^}]*border-left-color:\s*var\(--attention\)/);
  expect(css).toMatch(/\.card\[data-attention="true"\]\s*\{[^}]*background:\s*var\(--attention-bg\)/);
  // Quote: single line, ellipsized.
  expect(css).toMatch(/\.quote\s*\{[^}]*text-overflow:\s*ellipsis/);
  // The head/tag row is deleted - the card opens on the child's quote.
  expect(css).not.toMatch(/\.tag\s*\{/);
  expect(css).not.toMatch(/\.head\s*\{/);
  // The module itself is chromeless - no card box of its own.
  expect(css).not.toMatch(/\.module\s*\{[^}]*border:/);
});

// --- stats line: turns · calls · tokens · clock ------------------------------

test("the stats line counts the child's turns and tool calls from the full-turns watch, excluding the synthetic prelude turn", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => {
    const base = childThreadRead(params, "active");
    if ((params as { includeTurns: boolean }).includeTurns) {
      // The system prelude is not a turn the child took.
      base.thread.turns.unshift({
        id: SYSTEM_PRELUDE_TURN_ID,
        status: "completed",
        itemsView: "full",
        items: [],
      } as never);
    }
    return base;
  });
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_counts",
    callId: "call_counts",
    argumentsJSON: JSON.stringify({ task: "count my work" }),
    output: JSON.stringify({ delegate_id: "job_counts", status: "running", transcript_ref: "ref_counts_child" }),
  });
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  const stats = await within(row).findByTestId("subagent-stats");
  // 1 real turn (the prelude excluded), 3 tool calls (the final agent message
  // is not a call; the blank-purpose commandExecution still is).
  await waitFor(() => {
    expect(stats.textContent).toContain("1 turn");
    expect(stats.textContent).toContain("3 calls");
  });
});

test("the stats line singularizes a single turn and a single call", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => {
    const base = childThreadRead(params, "active");
    if ((params as { includeTurns: boolean }).includeTurns) {
      base.thread.turns = [
        {
          id: "turn_c1",
          status: "inProgress",
          itemsView: "full",
          items: [
            {
              id: "item_only",
              turnId: "turn_c1",
              type: "commandExecution",
              toolName: "shell",
              callId: "only",
              description: "only step",
              status: "running",
            },
          ],
        },
      ] as never;
    }
    return base;
  });
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_single_counts",
    callId: "call_single_counts",
    argumentsJSON: JSON.stringify({ task: "count one thing" }),
    output: JSON.stringify({
      delegate_id: "job_single_counts",
      status: "running",
      transcript_ref: "ref_single_counts",
    }),
  });
  render(<Body item={running} live={false} />);

  const stats = await within(screen.getByTestId("subagent-row")).findByTestId("subagent-stats");
  await waitFor(() => {
    expect(stats.textContent).toContain("1 turn");
    expect(stats.textContent).toContain("1 call");
  });
  expect(stats.textContent).not.toContain("1 calls");
  expect(stats.textContent).not.toContain("1 turns");
});

test("the stats line shows the delegate's projected token usage, and omits the segment entirely when there is none", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const withUsage = delegateItem({
    id: "d_tok",
    callId: "call_tok",
    argumentsJSON: JSON.stringify({ task: "tokened" }),
    output: JSON.stringify({ delegate_id: "job_tok", status: "running" }),
  });
  const withoutUsage = delegateItem({
    id: "d_notok",
    callId: "call_notok",
    argumentsJSON: JSON.stringify({ task: "untokened" }),
    output: JSON.stringify({ delegate_id: "job_notok", status: "running" }),
  });
  render(
    <>
      <Body item={withUsage} live={false} />
      <Body item={withoutUsage} live={false} />
    </>,
  );
  act(() =>
    updateSubagentRowIfExists(turnScopeKey(undefined, "turn_1"), "dlg:job_tok", {
      stable: { status: "running", usage: { inputTokens: 41200, outputTokens: 6100 } } as unknown as EvenerDelegateInfo,
    }),
  );

  const rows = screen.getAllByTestId("subagent-row");
  // Both running, so spawn order holds: the usage-patched card is first.
  const [tokRow, noTokRow] = rows;
  if (!tokRow || !noTokRow) throw new Error("expected two subagent cards");
  expect(within(tokRow!).getByTestId("subagent-stats").textContent).toContain("↑41k ↓6k");
  // The no-usage rule (the thread model's own usage-null precedent): absent
  // data renders as no segment, never a misleading ↑0 ↓0.
  expect(within(noTokRow!).getByTestId("subagent-stats").textContent).not.toContain("↑");
  expect(within(noTokRow!).getByTestId("subagent-stats").textContent).not.toContain("↓");
});

test("a running card's stats line closes with a live elapsed clock from its start time", () => {
  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_clock",
    callId: "call_clock",
    argumentsJSON: JSON.stringify({ task: "timing me" }),
    output: JSON.stringify({ delegate_id: "job_clock", status: "running" }),
    startedAt: new Date(Date.now() - 221_000).toISOString(),
  });
  render(<Body item={running} live={false} />);
  const stats = within(screen.getByTestId("subagent-row")).getByTestId("subagent-stats");
  // 221s elapsed reads "3m41s" (allow the second to tick over mid-test).
  expect(stats.textContent).toMatch(/3m4\ds/);
});

test("a child awaiting input earns the attention rail while staying kind=running", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => childThreadRead(params, "awaiting"));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_await",
    callId: "call_await",
    argumentsJSON: JSON.stringify({ task: "needs an answer" }),
    output: JSON.stringify({ delegate_id: "job_await", status: "running", transcript_ref: "ref_await_child" }),
  });
  render(<Body item={running} live={false} />);

  const row = screen.getByTestId("subagent-row");
  await waitFor(() => expect(row.dataset.attention).toBe("true"));
  // Needs-you is a cadence overlay, not a new row kind: the row still sorts
  // and reports as running.
  expect(row.dataset.kind).toBe("running");
});

// The card's head (tag + open) duplicated the delegate tool row it sits under.
// Both are gone from the card: the row carries identity and the open control.
test("the card is headless: no tag, no open button inside it", () => {
  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_headless",
    callId: "call_headless",
    argumentsJSON: JSON.stringify({ task: "identity is above me" }),
    output: JSON.stringify({ delegate_id: "job_headless", status: "running", transcript_ref: "ref_headless" }),
  });
  render(<Body item={running} live={false} />);
  const row = screen.getByTestId("subagent-row");
  expect(within(row).queryByTestId("subagent-tag")).toBeNull();
  expect(within(row).queryByRole("button", { name: /open transcript/i })).toBeNull();
});

// When only some stats segments have data (counts but no tokens, no clock),
// the line must join what it has - a trailing "·" advertises a segment that
// never came.
test("the stats line joins its present segments - never a dangling separator", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => childThreadRead(params, "active"));
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const running = delegateItem({
    id: "d_seps",
    callId: "call_seps",
    argumentsJSON: JSON.stringify({ task: "counts only, no tokens, no clock" }),
    output: JSON.stringify({ delegate_id: "job_seps", status: "running", transcript_ref: "ref_seps_child" }),
  });
  render(<Body item={running} live={false} />);

  const stats = await within(screen.getByTestId("subagent-row")).findByTestId("subagent-stats");
  await waitFor(() => expect(stats.textContent).toContain("3 calls"));
  expect(stats.textContent).not.toMatch(/·\s*$/);
});

// A historical session read ships its delegates' stable projections inside
// the snapshot (evener.diagnostics.delegates) - terminal kind, reason, usage,
// and the child's real run window (runStartedAt/runEndedAt). The card must
// hydrate from it: without this path a read-back session renders every card
// from the frozen spawn-call output alone (status stuck "running", no tokens,
// and the spawn's seconds posing as the child's runtime).
test("a historical session read hydrates a card's kind, tokens, and clock from the snapshot's stable projection", async () => {
  const fake = new FakeClient("ready");
  const t0 = 1_700_000_000_000;
  fake.on("thread/read", (params) => {
    const p = params as { ref: string; includeTurns?: boolean };
    if (p.ref === "ref_parent_hist") {
      return {
        thread: {
          id: "thr_parent_hist",
          sessionId: "sess_parent_hist",
          preview: "",
          ephemeral: false,
          modelProvider: "anthropic/claude-sonnet-4-5",
          createdAt: 1000,
          updatedAt: 1000,
          status: { type: "closed" },
          cwd: "/tmp",
          cliVersion: "1.0.0",
          source: "evener",
          evener: {
            ref: p.ref,
            capabilities: {} as never,
            queue: { revision: 0 },
            diagnostics: {
              delegates: [
                {
                  delegateId: "dlg_hist",
                  ownerSessionId: "sess_parent_hist",
                  rootSessionId: "sess_parent_hist",
                  childSessionId: "sess_hist_child",
                  transcriptRef: "ref_hist_child",
                  type: "delegate",
                  lifecycle: "idle",
                  phase: "idle",
                  status: "idle",
                  outcome: "completed",
                  terminal: true,
                  resumable: true,
                  projectionRevision: 3,
                  // No originTurnId - deliberately. Real stored projections
                  // can lack it (the delegate descriptor records
                  // origin_tool_call_id, and older daemons never recorded a
                  // turn id at all), and the card must still hydrate.
                  runStartedAt: new Date(t0).toISOString(),
                  runEndedAt: new Date(t0 + 22 * 60_000).toISOString(),
                  usage: { inputTokens: 41200, outputTokens: 6100 },
                },
              ],
            },
          },
          turns: [],
        },
      };
    }
    return childThreadRead(params, "closed");
  });
  connectionStore.getState().connect(fake);
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_parent_hist");
  });

  const Body = toolRendererFor("delegate").body!;
  render(
    <Body
      item={delegateItem({
        id: "d_hist",
        turnId: "turn_hist",
        callId: "call_hist",
        argumentsJSON: JSON.stringify({ task: "historical child" }),
        output: JSON.stringify({ delegate_id: "dlg_hist", status: "running", transcript_ref: "ref_hist_child" }),
      })}
      live={false}
      sessionRef="ref_parent_hist"
    />,
  );

  const row = await screen.findByTestId("subagent-row");
  // Terminal kind, real window, and tokens - all from the read, none from the
  // frozen "running" spawn output.
  await waitFor(() => expect(row.dataset.kind).toBe("done"));
  const stats = within(row).getByTestId("subagent-stats");
  expect(stats.textContent).toContain("↑41k ↓6k");
  expect(stats.textContent).toContain("22m00s");
});

// --- live-data lessons (2026-08-20): real delegates exposed three fixture-blind
// spots ---------------------------------------------------------------------

// A foreground_timeout delegate's ITEM timestamps bracket the spawn
// round-trip (seconds), not the child's run (minutes). The stable
// projection's runStartedAt/runEndedAt is the child's real window and must
// win whenever it exists.
test("the clock shows the child's run window from the stable projection, not the spawn call's seconds", () => {
  const Body = toolRendererFor("delegate").body!;
  const t0 = 1_700_000_000_000;
  const settled = delegateItem({
    id: "d_runwindow",
    callId: "call_runwindow",
    argumentsJSON: JSON.stringify({ task: "long child, quick spawn" }),
    output: JSON.stringify({ delegate_id: "job_rw", status: "running" }),
    startedAt: new Date(t0).toISOString(),
    completedAt: new Date(t0 + 4_000).toISOString(), // the spawn round-trip
  });
  render(<Body item={settled} live={false} />);
  act(() =>
    updateSubagentRowIfExists(turnScopeKey(undefined, "turn_1"), "dlg:job_rw", {
      stable: {
        status: "completed",
        terminal: true,
        outcome: "completed",
        runStartedAt: new Date(t0).toISOString(),
        runEndedAt: new Date(t0 + 22 * 60_000).toISOString(),
      } as unknown as EvenerDelegateInfo,
    }),
  );
  const stats = within(screen.getByTestId("subagent-row")).getByTestId("subagent-stats");
  expect(stats.textContent).toContain("22m00s");
  expect(stats.textContent).not.toContain("4s");
});

// With no stable projection in play (a historical session read, where no
// delegate notifications arrive), the next-best honest window is the child
// transcript's own turn span - the card already holds the full-turns watch.
// Only the item's own spawn-call stamps remain otherwise, and those bracket
// seconds of RPC, not the child's work.
test("the clock falls back to the child transcript's turn span when no stable run window exists", async () => {
  const fake = new FakeClient("ready");
  const t0 = new Date(2026, 7, 19, 15, 0, 0);
  fake.on("thread/read", (params) => {
    const base = childThreadRead(params, "closed");
    if ((params as { includeTurns: boolean }).includeTurns) {
      const turn = base.thread.turns[0] as { startedAt?: string; completedAt?: string };
      turn.startedAt = t0.toISOString();
      turn.completedAt = new Date(t0.getTime() + 22 * 60_000).toISOString();
    }
    return base;
  });
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const done = delegateItem({
    id: "d_childspan",
    callId: "call_childspan",
    argumentsJSON: JSON.stringify({ task: "historical child" }),
    output: JSON.stringify({ delegate_id: "job_span", status: "completed", transcript_ref: "ref_span_child" }),
    startedAt: new Date(t0).toISOString(),
    completedAt: new Date(t0.getTime() + 4_000).toISOString(), // spawn round-trip
  });
  render(<Body item={done} live={false} />);

  const stats = await within(screen.getByTestId("subagent-row")).findByTestId("subagent-stats");
  await waitFor(() => expect(stats.textContent).toContain("22m00s"));
});

// A final report is markdown: "## Summary\n\nFixed 9 files…". Flattened raw,
// the heading markers are noise in a one-line quote. The quote lands on the
// first substantive plain-text line - skipping blank and heading-only lines,
// stripped of inline emphasis markers.
test("the folded quote of a markdown final report lands on its first substantive line, plain text", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => {
    const base = childThreadRead(params, "closed");
    if ((params as { includeTurns: boolean }).includeTurns) {
      base.thread.turns[0]!.items.push({
        id: "item_report",
        turnId: "turn_c1",
        type: "agentMessage",
        text: "## Summary\n\n**Fixed 9 test files** across 9 commits. All tests pass.\n\n## Files Fixed\n\n- a.go\n- b.go",
        status: "completed",
      } as never);
    }
    return base;
  });
  connectionStore.getState().connect(fake);

  const Body = toolRendererFor("delegate").body!;
  const done = delegateItem({
    id: "d_md_quote",
    callId: "call_md_quote",
    argumentsJSON: JSON.stringify({ task: "fix the findings" }),
    output: JSON.stringify({ delegate_id: "job_md", status: "completed", transcript_ref: "ref_md_child" }),
  });
  render(<Body item={done} live={false} />);

  const quote = await within(screen.getByTestId("subagent-row")).findByTestId("subagent-quote");
  expect(quote.textContent).toBe("Fixed 9 test files across 9 commits. All tests pass.");
});
