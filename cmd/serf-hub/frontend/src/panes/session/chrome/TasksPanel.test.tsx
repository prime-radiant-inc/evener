import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { WireError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { STATUS_TONE, TasksPanel } from "./TasksPanel";

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    lastFrameAt: 0,
    capabilities: CAPABILITIES,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...overrides,
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// Wire-true fixture: the real daemon Task shape (agent/task/task_store.go:
// 54-79), same fixture family as taskData.test.ts's own.
const TASKS_DATA = [
  { id: 1, type: "implement", description: "Wire up the status row", prompt: "", status: "done" },
  { id: 2, type: "implement", description: "Wire up session actions", prompt: "", status: "in_progress" },
  { id: 3, type: "verify", description: "Gate green", prompt: "", status: "open" },
];

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  // The toast queue is module state and outlives cleanup(): without this, a
  // toast an earlier test pushed is still on screen when a later test asserts
  // a message is ABSENT, and the matcher finds the stale one.
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
});

// --- trigger badge: unchanged, still driven by the live-pushed aggregate ---
// (model.tasks stays the live signal per the plan's push-driven-plus-fetch-
// on-open model - it's cheap and already-live without opening anything.)

test("the trigger shows a bare 'Tasks' label when no aggregate has arrived yet", () => {
  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: null })} />);
  expect(screen.getByRole("button", { name: "Tasks" })).toBeTruthy();
});

test("the trigger shows the done/total counts once the aggregate has arrived", () => {
  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 7, done: 3 } })} />);
  expect(screen.getByRole("button", { name: "Tasks 3/7" })).toBeTruthy();
});

// The command palette's "Toggle tasks panel" (/tasks) synthesizes a click on
// [data-tasks-trigger] (shell/palette/commands.ts clickTrigger). Without the
// attribute here the command is inert (W6-T3 punch item), so pin that the
// palette's own selector resolves to exactly this trigger.
test("the Tasks trigger carries data-tasks-trigger so the palette's /tasks command can reach it", () => {
  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: null })} />);
  const trigger = screen.getByRole("button", { name: "Tasks" });
  expect(document.querySelector("[data-tasks-trigger]")).toBe(trigger);
});

// --- STATUS_TONE: pinning test (review finding) --------------------------
// The mapping shipped entirely untested, which is how `cancelled: "danger"`
// slipped through: the legacy comment cited for that choice
// (renderer-format.js's planGlyphForStatus, "a plan item that will not
// happen reads the same as a failure") governs only the GLYPH shape, not
// color - the legacy's actual rendering chain (renderer-format.js:496-506
// planStateClass + style.css:3324-3329) colors a cancelled task's glyph
// `--ink-3` (the SAME dim neutral as pending's glyph) and its label
// `--ink-2` with a strikethrough - neutral/receding, never danger-red. In
// this design system's color-is-attention rule, danger-tinting a routine
// cancellation would make reprioritized work indistinguishable from a real
// failure. The ✕ glyph alone carries the "won't happen" distinction.
test("pins each task status's Chip tone - cancelled reads as neutral/receding, not danger", () => {
  expect(STATUS_TONE).toEqual({
    open: "neutral",
    in_progress: "alive",
    done: "neutral",
    cancelled: "neutral",
  });
});

// --- fetch-on-open: the per-task row list --------------------------------

test("opening the panel fetches via listTasks(ref) and shows a loading state until it resolves", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const box: { resolve: ((r: { data: unknown }) => void) | null } = { resolve: null };
  fake.on("serf/tasks/list", () => new Promise((resolve) => (box.resolve = resolve)));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  expect(await screen.findByText(/loading tasks/i)).toBeTruthy();
  const call = fake.calls.find((c) => c.method === "serf/tasks/list");
  expect(call?.params).toEqual({ ref: "ref_a" });

  await act(async () => {
    box.resolve?.({ data: [] });
  });
  await waitFor(() => expect(screen.queryByText(/loading tasks/i)).toBeNull());
});

test("renders every fetched task as a row, in the SAME order the wire returned them (no client-side re-sort)", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: TASKS_DATA }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  const rows = await screen.findAllByTestId("task-row");
  expect(rows).toHaveLength(3);
  expect(rows.map((r) => r.textContent)).toEqual([
    expect.stringContaining("Wire up the status row"),
    expect.stringContaining("Wire up session actions"),
    expect.stringContaining("Gate green"),
  ]);
});

test("a confirmed-empty fetch (real daemon, genuinely zero tasks) shows the definitive legacy empty-state copy", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  expect(await screen.findByText("No tasks yet")).toBeTruthy();
  expect(screen.getByText(/agent's task list is empty for this session/i)).toBeTruthy();
});

test("re-opening after closing re-fetches (the plan's fetch-on-open model, not a one-time fetch)", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: TASKS_DATA }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await screen.findAllByTestId("task-row");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await screen.findAllByTestId("task-row");

  expect(fake.calls.filter((c) => c.method === "serf/tasks/list")).toHaveLength(2);
});

test("re-fetches automatically when the live aggregate changes WHILE the panel stays open", async () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: TASKS_DATA }));
  const user = userEvent.setup();

  const { rerender } = render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 0 } })} />);
  await user.click(screen.getByRole("button", { name: "Tasks 0/3" }));
  await screen.findAllByTestId("task-row");
  expect(fake.calls.filter((c) => c.method === "serf/tasks/list")).toHaveLength(1);

  // A live serf/task/updated notification bumped the aggregate - the panel
  // is still open, so this should trigger a fresh fetch automatically
  // rather than leaving stale rows on screen.
  rerender(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />);

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "serf/tasks/list")).toHaveLength(2));
});

test("does not fetch at all while the panel is closed, even if the aggregate changes", () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: TASKS_DATA }));

  const { rerender } = render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 0 } })} />);
  rerender(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />);

  expect(fake.calls.filter((c) => c.method === "serf/tasks/list")).toHaveLength(0);
});

// --- Codex-source unsupported state (actionUnavailable) -------------------

test("a Codex-source actionUnavailable rejection shows the honest unsupported state, with no error toast (it's not a bug)", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => {
    throw new WireError("codex source does not expose serf tasks", -32014, { serfErrorInfo: "actionUnavailable" });
  });

  render(
    <>
      <TasksPanel sessionRef="ref_codex" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  expect(await screen.findByText(/task list isn.t available/i)).toBeTruthy();
  expect(screen.queryByText(/couldn.t load tasks/i)).toBeNull();
});

// --- other failures: toast + inline error state ----------------------------

test("a generic fetch failure surfaces an error toast AND an inline error state", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => {
    throw new Error("tasks boom");
  });

  render(
    <>
      <TasksPanel sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await screen.findByText(/couldn.t load tasks: tasks boom/i);
  // The toast and the inline state both surface the same failure - assert
  // the inline one separately from the toast region so this doesn't just
  // pass because the toast's own text happened to match.
  expect(screen.getAllByText(/couldn.t load tasks/i).length).toBeGreaterThanOrEqual(2);
});

// The hub resumes a cold session before it can list anything
// (cmd/serf-hub/app_session_resume.go's withSessionResume). Both of this
// panel's two reports have to give way together: a toast blaming the daemon
// beside an inline heading blaming the task list is worse than either alone.
test("a failed auto-resume names the resume in BOTH the toast and the inline state", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => {
    throw new WireError("serf launch-check timed out", -32014, { serfErrorInfo: "hubLaunch" });
  });

  render(
    <>
      <TasksPanel sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await screen.findByText("Couldn't start this session: serf launch-check timed out"); // the toast
  expect(screen.getByText("Couldn't start this session")).toBeTruthy(); // the inline heading
  expect(screen.getByText("serf launch-check timed out")).toBeTruthy(); // the inline detail
  // The action's own name is gone from both, not merely absent from one.
  expect(screen.queryByText(/couldn.t load tasks/i)).toBeNull();
});

// The toast drops the separator when a rejection carries no text of its own
// (protocol/errors.ts). The inline half has to drop the detail line for the
// same rejection, or one failure reads as a headline in the toast and a
// headline plus a blank line in the panel. No <Toast/> here: it would put a
// second "Couldn't load tasks" on screen and blunt the count below.
test("a rejection with no text of its own shows the headline alone, with no empty detail line", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => {
    throw new Error("");
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  const headline = await screen.findByText("Couldn't load tasks");
  expect(headline.parentElement?.querySelectorAll("p")).toHaveLength(1);
});

// --- a failed LIVE re-fetch keeps the list it already has -------------------
//
// The panel re-fetches on every serf/task/updated push while it stays open.
// A momentary rejection on one of those fetches ("local daemon unavailable:
// broken pipe") used to replace the list the reader was mid-way through with
// an error state, though the previous page was still in hand. Nothing the
// reader did caused it, and nothing they could do brought it back.

// Scripts serf/tasks/list to answer `first` once and to reject every later
// call: the exact shape of a live re-fetch blipping under a reader.
function failAfterFirstFetch(fake: FakeClient, first: unknown, err: unknown): void {
  let calls = 0;
  fake.on("serf/tasks/list", () => {
    calls += 1;
    if (calls === 1) return { data: first };
    throw err;
  });
}

// Opens the panel, waits for the first fetch to land, then bumps the live
// aggregate the way a serf/task/updated push does - which is the only thing
// that re-fetches while the panel stays open.
async function openThenPush(fake: FakeClient): Promise<ReturnType<typeof userEvent.setup>> {
  const user = userEvent.setup();
  const panel = (done: number) => (
    <>
      <TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done } })} />
      <Toast />
    </>
  );
  const { rerender } = render(panel(0));
  await user.click(screen.getByRole("button", { name: "Tasks 0/3" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "serf/tasks/list")).toHaveLength(1));

  rerender(panel(1));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "serf/tasks/list")).toHaveLength(2));
  return user;
}

test("a re-fetch that fails keeps the rows already on screen", async () => {
  const fake = connectFakeClient();
  failAfterFirstFetch(fake, TASKS_DATA, new Error("local daemon unavailable: broken pipe"));

  await openThenPush(fake);

  await screen.findByTestId("tasks-stale");
  expect(screen.getAllByTestId("task-row")).toHaveLength(3);
  expect(screen.getByText("Wire up the status row")).toBeTruthy();
});

// A retained list is one push behind by construction - the push is what
// triggered the fetch that failed - and falls further behind for as long as
// the daemon stays unreachable. Keeping it silently would be worse than the
// blank it replaces: the reader would take a frozen list for a live one.
test("the retained list says it is out of date, so a failed refresh doesn't read as current", async () => {
  const fake = connectFakeClient();
  failAfterFirstFetch(fake, TASKS_DATA, new Error("local daemon unavailable: broken pipe"));

  await openThenPush(fake);

  const stale = await screen.findByTestId("tasks-stale");
  expect(stale.textContent).toContain("Showing the last list that loaded");
});

// The other direction of the same confusion. "No tasks yet" is the panel's
// one definitive statement about the session - it claims the agent's list is
// empty - so a retained empty list has to be marked stale just as a retained
// populated one does, or a failed refresh passes for a confirmed answer.
test("a retained EMPTY list under a failed refresh is marked stale, not left as a confirmed answer", async () => {
  const fake = connectFakeClient();
  failAfterFirstFetch(fake, [], new Error("local daemon unavailable: broken pipe"));

  await openThenPush(fake);

  await screen.findByTestId("tasks-stale");
  expect(screen.getByText("No tasks yet")).toBeTruthy();
});

// bv13's invariant, carried onto the retained-list path: the toast and the
// inline report are the SAME sentence, so a failed session resume takes over
// both or neither.
test("the stale notice and the toast report a re-fetch failure in the same words", async () => {
  const fake = connectFakeClient();
  failAfterFirstFetch(
    fake,
    TASKS_DATA,
    new WireError("serf launch-check timed out", -32014, { serfErrorInfo: "hubLaunch" }),
  );

  await openThenPush(fake);

  const sentence = "Couldn't start this session: serf launch-check timed out";
  await waitFor(() => expect(screen.getAllByText(sentence)).toHaveLength(2));
  // The action's own name is gone from both, not merely absent from one.
  expect(screen.queryByText(/couldn.t load tasks/i)).toBeNull();
});

// serf/task/updated is event-driven, never scheduled (internal/appprojector/
// appwire_projection.go's EventTaskUpdated case): a session whose agent has
// stopped touching its task list emits no further pushes, so a failed fetch
// is stuck until the reader asks again. Try again is that ask.
test("Try again re-fetches the list and clears the stale notice once it succeeds", async () => {
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("serf/tasks/list", () => {
    calls += 1;
    if (calls === 2) throw new Error("local daemon unavailable: broken pipe");
    return { data: TASKS_DATA };
  });

  const user = await openThenPush(fake);
  await screen.findByTestId("tasks-stale");

  await user.click(screen.getByRole("button", { name: "Try again" }));

  await waitFor(() => expect(screen.queryByTestId("tasks-stale")).toBeNull());
  expect(fake.calls.filter((c) => c.method === "serf/tasks/list")).toHaveLength(3);
  expect(screen.getAllByTestId("task-row")).toHaveLength(3);
});

// The first fetch has no previous page to keep, so it still blanks - but it
// is stuck in exactly the same way, and needs the same way out.
test("a first fetch that fails offers Try again, which fetches again", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("serf/tasks/list", () => {
    calls += 1;
    if (calls === 1) throw new Error("tasks boom");
    return { data: TASKS_DATA };
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await screen.findByText(/couldn.t load tasks: tasks boom/i);

  await user.click(screen.getByRole("button", { name: "Try again" }));

  expect(await screen.findAllByTestId("task-row")).toHaveLength(3);
  expect(screen.queryByText(/couldn.t load tasks/i)).toBeNull();
});
