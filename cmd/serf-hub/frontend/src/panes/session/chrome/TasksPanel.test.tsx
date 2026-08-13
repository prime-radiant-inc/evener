import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
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
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { STATUS_TONE, TasksPanel, TasksPanelBody } from "./TasksPanel";
import { absoluteTime } from "./taskTime";

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
  const { jobsTreeRevision = null, ...rest } = overrides;
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
    jobsUpdatedAt: null,
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
    ...rest,
    jobsTreeRevision,
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

const DATED_TASK_CREATED_AT = new Date(2026, 7, 8, 22, 3, 48).toISOString();

const DATED_TASKS = [
  {
    id: 1,
    type: "implement",
    description: "Implement artifact store",
    prompt: "",
    status: "done",
    depends_on: [14],
    reasoning_effort: "high",
    notes: ["Implemented secure artifact store in commits 9853cf561 and 162d0d41e."],
    created_at: DATED_TASK_CREATED_AT,
    updated_at: "2026-08-09T10:53:57-07:00",
    completed_at: "2026-08-09T10:53:57-07:00",
  },
  {
    id: 2,
    type: "implement",
    description: "Extend transcript API",
    prompt: "Execute Task 6.",
    status: "in_progress",
    created_at: "2026-08-08T22:03:48-07:00",
    updated_at: "2026-08-09T13:02:17-07:00",
  },
  {
    id: 3,
    type: "implement",
    description: "Transition to implementation plan",
    prompt: "",
    status: "cancelled",
    notes: ["Cancelled at the user's request."],
    created_at: "2026-08-08T20:25:33-07:00",
    updated_at: "2026-08-08T21:49:49-07:00",
  },
  {
    id: 4,
    type: "implement",
    description: "Prepare release notes",
    prompt: "",
    status: "open",
    notes: ["Captured the compatibility changes for the release notes."],
    created_at: "2026-08-09T11:00:00-07:00",
    updated_at: "2026-08-09T13:15:00-07:00",
  },
];

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  // The toast queue is module state and outlives cleanup(): without this, a
  // toast an earlier test pushed is still on screen when a later test asserts
  // a message is ABSENT, and the matcher finds the stale one.
  resetToastStoreForTests();
  // Row disclosure open/closed state lives in the shared disclosureStore
  // (module-level, survives cleanup()) - same reset every transcript
  // disclosure test performs, so an earlier test's expanded row can't leak
  // into a later test that expects to start collapsed.
  resetDisclosureStoreForTests();
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

test("rows group by status: in progress, then open, then the collapsed settled group; wire order holds within a group", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: TASKS_DATA }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(screen.getAllByTestId("task-row")).toHaveLength(2));
  const liveGroups = screen.getAllByTestId("task-group-live");
  expect(liveGroups.map((group) => group.getAttribute("data-status"))).toEqual(["in_progress", "open"]);
  expect(screen.getByTestId("task-settled-group").textContent).toContain("1");

  await user.click(screen.getByTestId("task-settled-group-summary"));
  await waitFor(() => expect(screen.getAllByTestId("task-row")).toHaveLength(3));
});

test("empty groups render nothing, so Open leads when there are no in-progress tasks", async () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [TASKS_DATA[2]] }));

  render(<TasksPanelBody sessionRef="ref_a" model={testModel()} />);

  const liveGroups = await screen.findAllByTestId("task-group-live");
  expect(liveGroups).toHaveLength(1);
  expect(liveGroups[0]?.getAttribute("data-status")).toBe("open");
  expect(screen.queryByText(/in progress/i)).toBeNull();
  expect(screen.queryByTestId("task-settled-group")).toBeNull();
});

test("an all-settled list shows only the collapsed settled disclosure line", async () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [DATED_TASKS[0], DATED_TASKS[2]] }));

  render(<TasksPanelBody sessionRef="ref_a" model={testModel()} />);

  const settled = await screen.findByTestId("task-settled-group");
  expect(settled.textContent).toContain("Done · settled 2");
  expect(screen.queryByTestId("task-group-live")).toBeNull();
  expect(screen.queryByTestId("task-row")).toBeNull();
});

test("a live row shows its latest note inline; a settled row does not", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  expect((await screen.findByTestId("task-latest")).textContent).toContain(
    "Captured the compatibility changes for the release notes.",
  );

  await user.click(screen.getByTestId("task-settled-group-summary"));
  const settledRow = screen
    .getAllByTestId("task-row")
    .find((row) => row.textContent?.includes("Implement artifact store"));
  expect(settledRow?.querySelector("[data-testid='task-latest']")).toBeNull();
});

test("a cancelled row renders struck-through inside the settled group", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByTestId("task-settled-group-summary"));

  const cancelled = screen.getByText("Transition to implementation plan");
  expect(cancelled.getAttribute("data-struck")).toBe("true");
  expect(cancelled.closest("[data-testid='task-row']")?.textContent).toContain("✕");
});

test("a live row shows a relative updated time", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  const row = (await screen.findByText("Extend transcript API")).closest("[data-testid='task-row']");

  expect(row?.querySelector("[data-testid='task-row-time']")).toBeTruthy();
});

test("the settled group defaults to collapsed and remembers being opened per session", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: DATED_TASKS }));

  const first = render(<TasksPanelBody sessionRef="ref_remember" model={testModel({ ref: "ref_remember" })} />);
  await screen.findByTestId("task-settled-group");
  expect(screen.queryByText("Implement artifact store")).toBeNull();
  await user.click(screen.getByTestId("task-settled-group-summary"));
  expect(await screen.findByText("Implement artifact store")).toBeTruthy();

  first.unmount();
  render(<TasksPanelBody sessionRef="ref_remember" model={testModel({ ref: "ref_remember" })} />);
  expect(await screen.findByText("Implement artifact store")).toBeTruthy();
});

test("the body header shows the meter and count when the aggregate is known", async () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [TASKS_DATA[0]] }));

  render(
    <>
      <TasksPanelBody sessionRef="ref_a" model={testModel({ tasks: { total: 20, done: 16 } })} />
      <Toast />
    </>,
  );
  await waitFor(() => expect(screen.getByTestId("tasks-body-head")).toBeTruthy());
  expect(screen.getByTestId("tasks-body-head").textContent).toContain("16/20 done");
  expect(screen.getByRole("meter", { name: "Task progress: 16 of 20 complete" })).toBeTruthy();
});

test("the body header is absent while no aggregate has arrived", async () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [TASKS_DATA[0]] }));

  render(
    <>
      <TasksPanelBody sessionRef="ref_a" model={testModel({ tasks: null })} />
      <Toast />
    </>,
  );
  await waitFor(() => expect(screen.getByTestId("task-settled-group")).toBeTruthy());
  expect(screen.queryByTestId("tasks-body-head")).toBeNull();
});

// --- row disclosure: expand a task row to see its dense detail body ---------

const RICH_TASK = {
  id: 5,
  type: "implement",
  description: "Wire up expand/collapse",
  prompt: "Follow the transcript's existing disclosure idiom.",
  status: "in_progress",
  depends_on: [1, 3],
  reasoning_effort: "high",
  notes: ["started", "blocked on #1"],
};

test("a task row starts collapsed; clicking its summary expands its meta and updates", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [RICH_TASK] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  const summary = await screen.findByText("Wire up expand/collapse");

  expect(screen.queryByTestId("task-expanded")).toBeNull();

  await user.click(summary);

  const body = screen.getByTestId("task-expanded");
  expect(body.querySelector("[data-testid='task-meta']")?.textContent).toContain("implement");
  expect(body.querySelector("[data-testid='task-meta']")?.textContent).toContain("#1 #3");
  expect(body.querySelector("[data-testid='task-meta']")?.textContent).toContain("high");
  expect(body.querySelector("[data-testid='task-notes']")?.textContent).toContain("started");
  expect(body.querySelector("[data-testid='task-notes']")?.textContent).toContain("blocked on #1");
});

test("notes render as a timeline with the latest note marked", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [RICH_TASK] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByText("Wire up expand/collapse"));

  const notes = screen.getByTestId("task-notes");
  const items = notes.querySelectorAll("li");
  expect(items).toHaveLength(2);
  expect(items[0]?.getAttribute("data-latest")).toBeNull();
  expect(items[1]?.getAttribute("data-latest")).toBe("true");
  expect(notes.textContent).toContain("Updates · 2");
});

// Touch target (UX fix): the prompt disclosure's own <summary> is a small
// clickable row (chevron + one-line preview), so a coarse pointer needs the
// platform's 44px tap floor - same treatment askdock/modelswitch/
// newcontentpill get, via (pointer: coarse) since this isn't a
// widgets/button consumer.
test("the prompt disclosure summary reaches the tap floor on a coarse pointer", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "taskspanel.module.css"), "utf8");
  const coarse = css.match(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/);
  expect(coarse, "taskspanel.module.css must have a (pointer: coarse) media block").not.toBeNull();
  const rule = coarse![1]!.match(/\.promptSummary\s*\{([^}]*)\}/);
  expect(rule, "the coarse-pointer block must override .promptSummary").not.toBeNull();
  expect(rule![1]).toContain("min-height: var(--tap-min)");
});

test("the prompt disclosure shows a one-line markdown preview collapsed and the full markdown body open", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({
    data: [
      {
        ...RICH_TASK,
        prompt: "Execute **Task 6** from the plan:\nread_transcript `job/artifact` API and errors.",
      },
    ],
  }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByText("Wire up expand/collapse"));

  const prompt = screen.getByTestId("task-prompt");
  expect(prompt.querySelector(".promptPreview strong, [class*='promptPreview'] strong")).toBeTruthy();
  expect(prompt.textContent).not.toContain("**");

  await user.click(screen.getByTestId("task-prompt-summary"));

  const body = await screen.findByTestId("task-prompt-body");
  expect(body.querySelector("code")?.textContent).toBe("job/artifact");
});

test("a task with a blank prompt renders no prompt disclosure", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [{ ...RICH_TASK, prompt: "" }] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByText("Wire up expand/collapse"));

  expect(screen.queryByTestId("task-prompt")).toBeNull();
});

test("clicking an expanded row's summary again collapses it", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [RICH_TASK] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  const summary = await screen.findByText("Wire up expand/collapse");
  await user.click(summary);
  expect(screen.getByTestId("task-expanded")).toBeTruthy();

  await user.click(summary);

  expect(screen.queryByTestId("task-expanded")).toBeNull();
});

test("an expanded bare task shows only the type meta and 'No updates yet.'", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: TASKS_DATA }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  // TASKS_DATA's first row: type "implement", prompt "", and no timestamps,
  // dependsOn, notes, or reasoningEffort.
  await user.click(await screen.findByTestId("task-settled-group-summary"));
  await user.click(await screen.findByText("Wire up the status row"));

  const body = screen.getByTestId("task-expanded");
  expect(body.querySelector("[data-testid='task-meta']")?.textContent).toContain("implement");
  expect(body.querySelector("[data-testid='task-meta']")?.textContent).not.toContain("reasoning");
  expect(body.querySelector("[data-testid='task-times']")).toBeNull();
  expect(body.querySelector("[data-testid='task-prompt']")).toBeNull();
  expect(body.textContent).toContain("No updates yet.");
});

test("an expanded task shows the meta strip and timestamps line", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByTestId("task-settled-group-summary"));
  await user.click(await screen.findByText("Implement artifact store"));

  const meta = screen.getByTestId("task-meta");
  expect(meta.textContent).toContain("implement");
  expect(meta.textContent).toContain("high");
  expect(meta.textContent).toContain("#14");
  const times = screen.getByTestId("task-times");
  expect(times.textContent).toContain(`created ${absoluteTime(DATED_TASK_CREATED_AT)}`);
  expect(times.textContent).toContain("updated");
  expect(times.textContent).toContain("completed");
});

test("the timestamps line omits updated when it equals created", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({
    data: [
      {
        id: 6,
        type: "verify",
        description: "Check unchanged timestamps",
        prompt: "",
        status: "open",
        created_at: "2026-08-09T12:00:00-07:00",
        updated_at: "2026-08-09T12:00:00-07:00",
      },
    ],
  }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByText("Check unchanged timestamps"));

  const times = screen.getByTestId("task-times");
  expect(times.textContent).toContain("created");
  expect(times.textContent).not.toContain("updated");
  expect(times.textContent).not.toContain("completed");
});

test("the timestamps line omits completed for a non-done task even when completed_at is present", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({
    data: [
      {
        id: 7,
        type: "verify",
        description: "Ignore stale completed timestamp",
        prompt: "",
        status: "cancelled",
        created_at: "2026-08-09T12:00:00-07:00",
        completed_at: "2026-08-09T12:30:00-07:00",
      },
    ],
  }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByTestId("task-settled-group-summary"));
  await user.click(await screen.findByText("Ignore stale completed timestamp"));

  const times = screen.getByTestId("task-times");
  expect(times.textContent).toContain("created");
  expect(times.textContent).not.toContain("completed");
});

test("each row's expand state is independent - opening one row leaves its siblings collapsed", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: TASKS_DATA }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByTestId("task-settled-group-summary"));
  await user.click(await screen.findByText("Wire up the status row"));

  expect(screen.getAllByTestId("task-expanded")).toHaveLength(1);
});

test("the expanded row is a real native disclosure (<details open>), not just conditionally-rendered markup", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [RICH_TASK] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await screen.findByText("Wire up expand/collapse");

  const details = screen.getByRole("group") as HTMLDetailsElement;
  expect(details.open).toBe(false);

  await user.click(screen.getByText("Wire up expand/collapse"));

  expect(details.open).toBe(true);
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

// The store already rejects an obsolete completion (fetchID mismatch), so the
// stale request's failure must not toast either: an A-then-B overlap where the
// OLDER request fails after the newer one succeeded would otherwise show a
// failure banner over a list that is on screen and current.
test("a stale overlapping failure does not toast after a newer fetch succeeded", async () => {
  const fake = connectFakeClient();
  const calls: Array<{ resolve: (value: { data: unknown }) => void; reject: (err: unknown) => void }> = [];
  fake.on(
    "serf/tasks/list",
    () =>
      new Promise<{ data: unknown }>((resolve, reject) => {
        calls.push({ resolve, reject });
      }),
  );

  const { rerender } = render(
    <>
      <TasksPanelBody sessionRef="ref_stale" model={testModel({ ref: "ref_stale" })} />
      <Toast />
    </>,
  );
  // A tasks push refires the fetch effect, starting a second overlapping fetch.
  rerender(
    <>
      <TasksPanelBody sessionRef="ref_stale" model={testModel({ ref: "ref_stale", tasks: { total: 3, done: 1 } })} />
      <Toast />
    </>,
  );
  await waitFor(() => expect(calls).toHaveLength(2));

  await act(async () => calls[1]?.resolve({ data: TASKS_DATA }));
  await screen.findByText("Wire up session actions");
  await act(async () => calls[0]?.reject(new Error("tasks boom")));

  expect(screen.queryByText(/couldn.t load tasks/i)).toBeNull();
  expect(screen.getByText("Wire up session actions")).toBeTruthy();
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
  expect(screen.getAllByTestId("task-row")).toHaveLength(2);
  expect(screen.getByTestId("task-settled-group").textContent).toContain("1");
  expect(screen.getByText("Wire up session actions")).toBeTruthy();
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
  expect(screen.getAllByTestId("task-row")).toHaveLength(2);
  expect(screen.getByTestId("task-settled-group").textContent).toContain("1");
});

// --- dead-daemon "thread not found": must not contradict the trigger -------
//
// isThreadNotFound fires ONLY for entryForRef finding no rendezvous entry at
// all (local_daemon.go:551/isDeadSessionError) - the hub tries a
// persisted past-index fallback first (app_tasks.go's hubTasksList) and only
// reaches this branch when THAT also comes up empty, i.e. a session the hub
// never tracked and therefore can never resume (withSessionResume gates on
// hubKnowsRef). So unlike the "local daemon unavailable" blip above, there is
// no schedule and no possible recovery to wait for here.
function threadNotFoundError(): WireError {
  return new WireError("thread not found: thr_a", -32014, { serfErrorInfo: "sessionUnavailable" });
}

test("thread-not-found on a session that never had a live aggregate still shows the honest 'No tasks yet' (unchanged)", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => {
    throw threadNotFoundError();
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: null })} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  expect(await screen.findByText("No tasks yet")).toBeTruthy();
  expect(screen.queryByTestId("tasks-daemon-gone")).toBeNull();
});

test("closing then re-opening after the daemon exits keeps the rows already shown, with a terminal notice instead of blanking them", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("serf/tasks/list", () => {
    calls += 1;
    if (calls === 1) return { data: TASKS_DATA };
    throw threadNotFoundError();
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />);
  await user.click(screen.getByRole("button", { name: "Tasks 1/3" }));
  await screen.findAllByTestId("task-row");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  await user.click(screen.getByRole("button", { name: "Tasks 1/3" }));

  await screen.findByTestId("tasks-daemon-gone");
  expect(screen.getAllByTestId("task-row")).toHaveLength(2);
  expect(screen.getByTestId("task-settled-group").textContent).toContain("1");
  expect(screen.queryByText("No tasks yet")).toBeNull();
});

test("that terminal notice offers no Try again - the hub never tracked this session, so asking again fails identically forever", async () => {
  const fake = connectFakeClient();
  failAfterFirstFetch(fake, TASKS_DATA, threadNotFoundError());

  await openThenPush(fake);

  await screen.findByTestId("tasks-daemon-gone");
  expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
});

test("thread-not-found with a known aggregate but no rows ever fetched shows a terminal message, not 'No tasks yet'", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => {
    throw threadNotFoundError();
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />);
  await user.click(screen.getByRole("button", { name: "Tasks 1/3" }));

  await screen.findByText("This session has ended");
  expect(screen.queryByText("No tasks yet")).toBeNull();
});

test("no toast for a dead-daemon rejection - it's not a bug, it's an expected terminal state", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => {
    throw threadNotFoundError();
  });

  render(
    <>
      <TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Tasks 1/3" }));

  await screen.findByText("This session has ended");
  expect(screen.queryByText(/couldn.t load tasks/i)).toBeNull();
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
  // Headline and detail are asserted separately here, unlike the stale-notice
  // tests above: with no previous page to keep, this path renders EmptyState,
  // whose title and hint are two elements. The stale notice renders the same
  // failure as one sentence in one element. Same words either way - a combined
  // matcher just cannot span the EmptyState pair.
  await screen.findByText("Couldn't load tasks");
  await screen.findByText("tasks boom");

  await user.click(screen.getByRole("button", { name: "Try again" }));

  expect(await screen.findAllByTestId("task-row")).toHaveLength(2);
  expect(screen.getByTestId("task-settled-group").textContent).toContain("1");
  expect(screen.queryByText(/couldn.t load tasks/i)).toBeNull();
});
