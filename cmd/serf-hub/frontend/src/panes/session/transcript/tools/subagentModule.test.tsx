import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy, StrictMode } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { toolRendererFor } from "../toolRenderers";
import { classifyJobStatus, resolveRowKey } from "./subagentModule";
import { resetSubagentModuleStoreForTests } from "./subagentModuleStore";
import "./subagentModule";
import type { DockviewApi } from "dockview-core";
import type { ItemModel } from "../../../../protocol/model";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import { registerPane } from "../../../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../../stores/threads";

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

// --- classifyJobStatus / resolveRowKey (pure, unit-level) -----------------

test("classifyJobStatus: failed family", () => {
  for (const s of ["failed", "errored", "error", "exhausted"]) expect(classifyJobStatus(s)).toBe("failed");
});

test("classifyJobStatus: done family (cancelled/stopped count as done, not failed)", () => {
  for (const s of ["completed", "done", "cancelled", "stopped", "succeeded"]) expect(classifyJobStatus(s)).toBe("done");
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

// --- delegate descriptor: summary ----------------------------------------

test("delegate: summary leads with the clipped task", () => {
  const d = toolRendererFor("delegate");
  const args = JSON.stringify({ task: "Run the full test suite and report back" });
  expect(d.summary(delegateItem({ argumentsJSON: args }))).toBe("Delegated: Run the full test suite and report back");
});

test("delegate: a long task is clipped to 80 chars", () => {
  const d = toolRendererFor("delegate");
  const longTask = "x".repeat(100);
  const args = JSON.stringify({ task: longTask });
  expect(d.summary(delegateItem({ argumentsJSON: args }))).toBe(`Delegated: ${"x".repeat(80)}…`);
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

// --- row content ----------------------------------------------------------

test("a running row shows the task and a running status", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const running = delegateItem({
    id: "d_run",
    callId: "call_run",
    argumentsJSON: JSON.stringify({ task: "still working" }),
    output: JSON.stringify({ job_id: "job_r", status: "running", transcript_ref: "ref_r" }),
  });
  render(<Body item={running} live={false} />);
  const row = screen.getByTestId("subagent-row");
  expect(within(row).getByText("still working")).toBeTruthy();
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

test("a running row with a transcriptRef watches the child and shows a live Cadence indicator", async () => {
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
      status: { type: "active" },
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

  await screen.findByTestId("cadence-dot");
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
  await user.click(screen.getByRole("button", { name: /open transcript/i }));
  const panes = workspaceStore.getState().panes;
  // The DISTINCT read-only "transcript" pane (plan §Ambiguities #1 / PIN-A:
  // reachable via openBeside, never a URL) - opened against the child's own ref.
  const opened = panes.find((p) => p.type === "transcript");
  expect(opened?.params).toEqual({ ref: "ref_child_open" });
  expect(opened?.beside).toBeUndefined(); // no split on mobile
  // The row must no longer open a live SESSION pane for the child.
  expect(panes.some((p) => p.type === "session")).toBe(false);
});

test("open transcript splits the transcript pane BESIDE the focused pane (desktop host present)", async () => {
  registerDockviewApi(fakeApi());
  const anchor = workspaceStore.getState().openPane("transcript", { ref: "ref_parent_view" });
  const user = userEvent.setup();
  const Body = toolRendererFor("delegate").body!;
  render(<Body item={delegateWithTranscriptRef("ref_child_open")} live={false} />);
  await user.click(screen.getByRole("button", { name: /open transcript/i }));
  const opened = workspaceStore.getState().panes.find((p) => p.type === "transcript" && p.id !== anchor);
  expect(opened?.params).toEqual({ ref: "ref_child_open" });
  expect(opened?.beside).toBe(anchor); // split beside the pane that was focused
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
                { id: "item_msg", turnId: "turn_c1", type: "agentMessage", text: "all done", status: "completed" },
              ],
            },
          ]
        : [],
    },
  };
}

test("an expanded delegate card shows the Mandate, a live Activity feed, and the Summary", async () => {
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

  // Collapsed: no expanded body yet.
  expect(screen.queryByTestId("subagent-activity")).toBeNull();

  const user = userEvent.setup();
  await user.click(screen.getByText("audit the reducer")); // click the summary to expand

  // Mandate is the delegation task.
  const mandate = await screen.findByTestId("subagent-mandate");
  expect(within(mandate).getByText("audit the reducer")).toBeTruthy();

  // Activity feed maps the child's tool-call description/purpose fields.
  const activity = await screen.findByTestId("subagent-activity");
  expect(within(activity).getByText("step one")).toBeTruthy();
  expect(within(activity).getByText("step two")).toBeTruthy();

  // Summary is the child's last agentMessage.
  const summary = screen.getByTestId("subagent-summary");
  expect(within(summary).getByText("all done")).toBeTruthy();
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
  expect(within(row).getByText("failed")).toBeTruthy();
});
