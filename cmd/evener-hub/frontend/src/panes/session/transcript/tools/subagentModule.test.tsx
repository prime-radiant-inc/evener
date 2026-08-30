import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { ToolCallItem } from "../ToolCallItem";
import { toolRendererFor } from "../toolRenderers";
import { classifyJobStatus, resolveRowKey, rowFromDelegateItem } from "./subagentModule";
import { resetSubagentModuleStoreForTests } from "./subagentModuleStore";
import "./subagentModule";
import type { DockviewApi } from "dockview-core";
import { type ItemModel, SYSTEM_PRELUDE_TURN_ID, type ThreadModel, type TurnModel } from "../../../../protocol/model";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { EvenerDelegateInfo } from "../../../../protocol/types.gen";
import { registerPaneForTests } from "../../../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { SessionNowContext } from "../../liveness";

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
    row: { delegateId: "dlg_stable", transcriptRef: "local:sess_child" },
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

test("two delegate ToolCallItems each retain their own card and open action", () => {
  const turn: TurnModel = { id: "turn_two_delegates", status: "completed", items: [] };
  const first = delegateItem({
    id: "d1",
    turnId: turn.id,
    callId: "call_d1",
    description: "first delegate",
    argumentsJSON: JSON.stringify({ task: "first child" }),
    output: JSON.stringify({ delegate_id: "dlg_1", status: "running", transcript_ref: "ref_child_1" }),
  });
  const second = delegateItem({
    id: "d2",
    turnId: turn.id,
    callId: "call_d2",
    description: "second delegate",
    argumentsJSON: JSON.stringify({ task: "second child" }),
    output: JSON.stringify({ delegate_id: "dlg_2", status: "running", transcript_ref: "ref_child_2" }),
  });
  render(
    <>
      <ToolCallItem item={first} turn={turn} live={false} sessionRef="ref_parent" />
      <ToolCallItem item={second} turn={turn} live={false} sessionRef="ref_parent" />
    </>,
  );

  const toolRows = screen.getAllByTestId("tool-call-item");
  expect(toolRows).toHaveLength(2);
  expect(within(toolRows[0]!).getAllByTestId("subagent-row")).toHaveLength(1);
  expect(within(toolRows[1]!).getAllByTestId("subagent-row")).toHaveLength(1);
  expect(within(toolRows[0]!).getByRole("button", { name: "Open transcript" })).toBeTruthy();
  expect(within(toolRows[1]!).getByRole("button", { name: "Open transcript" })).toBeTruthy();
});

// --- row content ----------------------------------------------------------

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
});

// 3zf8: a child deliberately killed with job_stop (or reconciled to
// stopped/runtime_lost after a hub restart - agent/internal/jobstore/
// reconcile.go) must never render byte-identical to one that finished its
// task cleanly - same glyph, same tone, same label was the exact defect.
// Nothing broke, so it's still not "failed", but it is not "done" either.
test("3zf8: a cancelled child gets its own distinct stopped kind", () => {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  const stopped = delegateItem({
    id: "d_stopped",
    callId: "call_stopped",
    argumentsJSON: JSON.stringify({ task: "misbehaving, killed" }),
    output: JSON.stringify({ delegate_id: "job_stopped", status: "cancelled", transcript_ref: "ref_stopped" }),
  });
  render(<Body item={stopped} live={false} />);
  const row = screen.getByTestId("subagent-row");
  expect(row.dataset.kind).toBe("stopped");
  // The card is headless - no tag, no task text inside; identity lives on the
  // delegate's own tool row.
  expect(within(row).queryByTestId("subagent-tag")).toBeNull();
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
// with a `description` intent) plus a final agentMessage summary - but ONLY
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
                  // (ToolRow.tsx's statedIntentOf, shared by both surfaces).
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
  expect(quote.tagName).toBe("EM");
});

test("expanding a card lists recent quotes - intents plain, messages italic - each with its runtime and timestamp", async () => {
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
  // contributed no quote (the same statedIntentOf rule the old feed used).
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

  // Expanded activity retains source-specific treatment.
  expect(items[2]!.querySelector("em")?.textContent).toBe("all done");
  expect(items[0]!.querySelector("em")).toBeNull();
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

// mhcf: a child thread/read fixture with MANY intent-bearing steps.
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

// A timing annotation is not an action: round_timings items carry an intent-
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
  // is not a call; the blank-intent commandExecution still is).
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

test("a running card's stats line closes with a live elapsed clock from its start time", () => {
  const Body = toolRendererFor("delegate").body!;
  const now = 1_700_000_221_000;
  const running = delegateItem({
    id: "d_clock",
    callId: "call_clock",
    argumentsJSON: JSON.stringify({ task: "timing me" }),
    output: JSON.stringify({ delegate_id: "job_clock", status: "running" }),
    startedAt: new Date(now - 221_000).toISOString(),
  });
  render(
    <SessionNowContext.Provider value={now}>
      <Body item={running} live={false} />
    </SessionNowContext.Provider>,
  );
  const stats = within(screen.getByTestId("subagent-row")).getByTestId("subagent-stats");
  expect(stats.textContent).toContain("3m41s");
});

test("stable delegate attention and lifecycle own the card while child content status changes", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => childThreadRead(params, "active"));
  connectionStore.getState().connect(fake);

  const stable = {
    delegateId: "dlg_attention",
    status: "running",
    outcome: "running",
    terminal: false,
    resumable: true,
    needsAttention: true,
    projectionRevision: 1,
  } as EvenerDelegateInfo;
  threadsStore.setState((state) => ({
    threads: new Map(state.threads).set("ref_attention_parent", { delegates: [stable] } as ThreadModel),
  }));

  const turn: TurnModel = { id: "turn_attention", status: "completed", items: [] };
  const running = delegateItem({
    id: "d_await",
    turnId: turn.id,
    callId: "call_await",
    argumentsJSON: JSON.stringify({ task: "needs an answer" }),
    output: JSON.stringify({
      delegate_id: stable.delegateId,
      status: "running",
      transcript_ref: "ref_await_child",
    }),
  });
  render(<ToolCallItem item={running} turn={turn} live={false} sessionRef="ref_attention_parent" />);

  const row = screen.getByTestId("subagent-row");
  await waitFor(() => expect(row.dataset.attention).toBe("true"));
  expect(row.dataset.kind).toBe("running");
  expect(within(row).getByText("Status: needs attention")).toBeTruthy();
  expect(within(row).getByTestId("subagent-status-glyph").textContent).toBe("◆");

  for (const status of ["idle", "awaiting"]) {
    await act(async () => {
      fake.emitNotification({
        method: "thread/status/changed",
        params: { threadId: "thr_child", ref: "ref_await_child", status: { type: status } },
      } as never);
    });
    expect(row.dataset.attention).toBe("true");
    expect(row.dataset.kind).toBe("running");
    expect(within(row).getByText("Status: needs attention")).toBeTruthy();
    expect(within(row).getByTestId("subagent-status-glyph").textContent).toBe("◆");
  }

  act(() => {
    threadsStore.setState((state) => ({
      threads: new Map(state.threads).set("ref_attention_parent", {
        delegates: [{ ...stable, needsAttention: false, projectionRevision: 2 }],
      } as ThreadModel),
    }));
  });
  expect(row.dataset.attention).toBeUndefined();
  expect(row.dataset.kind).toBe("running");
  expect(within(row).getByText("Status: running")).toBeTruthy();
  expect(within(row).getByTestId("subagent-status-glyph").textContent).toBe("●");

  await act(async () => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_child", ref: "ref_await_child", status: { type: "active" } },
    } as never);
    threadsStore.setState((state) => ({
      threads: new Map(state.threads).set("ref_attention_parent", {
        delegates: [
          {
            ...stable,
            status: "idle",
            outcome: "completed",
            terminal: true,
            needsAttention: false,
            projectionRevision: 3,
          },
        ],
      } as ThreadModel),
    }));
  });
  expect(row.dataset.attention).toBeUndefined();
  expect(row.dataset.kind).toBe("done");
  expect(within(row).getByText("Status: done")).toBeTruthy();
  expect(within(row).getByTestId("subagent-status-glyph").textContent).toBe("✓");
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

test("a headless card exposes hidden identity and status, a visible status shape, and a controlled disclosure", async () => {
  const Body = toolRendererFor("delegate").body!;
  const user = userEvent.setup();
  render(
    <Body
      item={delegateItem({
        id: "d_accessible",
        callId: "call_accessible",
        argumentsJSON: JSON.stringify({ task: "accessible child" }),
        output: JSON.stringify({ delegate_id: "dlg_accessible", status: "completed" }),
      })}
      live={false}
      sessionRef="ref_accessible"
    />,
  );

  const row = screen.getByTestId("subagent-row");
  expect(within(row).getByText("Delegate dlg_accessible")).toBeTruthy();
  expect(within(row).getByText("Status: done")).toBeTruthy();
  const glyph = within(row).getByTestId("subagent-status-glyph");
  expect(glyph.textContent).not.toBe("");
  expect(glyph.getAttribute("aria-hidden")).toBe("true");

  const disclosure = within(row).getByRole("button", { name: /show recent activity/i });
  const regionId = disclosure.getAttribute("aria-controls");
  expect(regionId).toBeTruthy();
  expect(disclosure.getAttribute("aria-expanded")).toBe("false");
  await user.click(disclosure);
  expect(disclosure.getAttribute("aria-expanded")).toBe("true");
  expect(within(row).getByRole("region").id).toBe(regionId);
});

test("multiple cards schedule no intervals of their own", () => {
  const Body = toolRendererFor("delegate").body!;
  const setIntervalSpy = vi.spyOn(globalThis, "setInterval");
  render(
    <>
      <Body
        item={delegateItem({
          id: "d_clock_a",
          callId: "call_clock_a",
          argumentsJSON: JSON.stringify({ task: "clock a" }),
          output: JSON.stringify({ delegate_id: "dlg_clock_a", status: "running" }),
        })}
        live={false}
      />
      <Body
        item={delegateItem({
          id: "d_clock_b",
          callId: "call_clock_b",
          argumentsJSON: JSON.stringify({ task: "clock b" }),
          output: JSON.stringify({ delegate_id: "dlg_clock_b", status: "running" }),
        })}
        live={false}
      />
    </>,
  );

  expect(setIntervalSpy).not.toHaveBeenCalled();
  setIntervalSpy.mockRestore();
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
                  needsAttention: false,
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

  const turn: TurnModel = { id: "turn_hist", status: "completed", items: [] };
  render(
    <ToolCallItem
      item={delegateItem({
        id: "d_hist",
        turnId: turn.id,
        callId: "call_hist",
        argumentsJSON: JSON.stringify({ task: "historical child" }),
        output: JSON.stringify({ delegate_id: "dlg_hist", status: "running", transcript_ref: "ref_hist_child" }),
      })}
      turn={turn}
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
