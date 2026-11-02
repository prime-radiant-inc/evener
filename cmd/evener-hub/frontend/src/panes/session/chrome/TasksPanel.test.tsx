import type { Task, TaskListResponse, ThreadModel } from "@evener/appwire-client";
import { absoluteTime, WireError } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test } from "vitest";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { readModuleCss, topRuleBlock } from "../../../styles/cssBlock";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../../threadTestUtils";
import { Toast } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { emptyStateReasons } from "../../../widgets/emptystate/emptyStateTestUtils";
import { requireClass } from "../../../widgets/internal/requireClass";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import visuallyHiddenStyles from "../../../widgets/visuallyhidden/visuallyhidden.module.css";
import { STATUS_TOUCH } from "../transcript/tools/taskCheck";
import { TasksPanel, TasksPanelBody } from "./TasksPanel";

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    contextAssumed: false,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...rest,
    jobsTreeRevision,
  };
}

// Wire-true fixture: the real daemon Task shape (agent/task/task_store.go:
// 54-79), same fixture family as the package's taskListData.test.ts.
const TASKS_DATA: [Task, Task, Task] = [
  { id: 1, type: "implement", description: "Wire up the status row", prompt: "", status: "done" },
  { id: 2, type: "implement", description: "Wire up session actions", prompt: "", status: "in_progress" },
  { id: 3, type: "verify", description: "Gate green", prompt: "", status: "open" },
];

const DATED_TASK_CREATED_AT = new Date(2026, 7, 8, 22, 3, 48).toISOString();

const DATED_TASKS: [Task, Task, Task, Task] = [
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
  resetConnectionStoreForTests();
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

function errorToasts(): string[] {
  return getToasts()
    .filter((toast) => toast.kind === "error")
    .map((toast) => toast.text);
}

// Every failed first-fetch notice on the page. A failure is an InlineNotice,
// never an EmptyState, so emptyStateReasons does not see it. The first fetch
// runs as the panel opens, so its failure is a polite status (role=status)
// rather than an alert that interrupts a screen reader.
function failedNotices(): Element[] {
  return [...document.querySelectorAll('[role="status"][data-reason="failed"]')];
}

// The failed first-fetch notice's heading and detail, joined the way a toast
// joins the same failure's headline and detail. The heading is the notice's
// title paragraph; the detail is the rest of the notice body beside it.
function failedStateSentence(): string {
  const [failed] = failedNotices();
  if (!failed) throw new Error("Missing failed notice");
  const heading = failed.querySelector("p");
  if (!heading?.parentElement) throw new Error("Failed notice has no heading");
  const detail = [...heading.parentElement.childNodes]
    .filter((node) => node !== heading)
    .map((node) => node.textContent)
    .join("");
  return [heading.textContent, detail].filter((part) => part !== "").join(": ");
}

// --- trigger badge: unchanged, still driven by the live-pushed aggregate ---
// (model.tasks stays the live signal per the plan's push-driven-plus-fetch-
// on-open model - it's cheap and already-live without opening anything.)

test("the trigger shows a bare 'Tasks' label when no aggregate has arrived yet", () => {
  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: null })} />);
  expect(screen.getByRole("button", { name: "Tasks" })).toBeTruthy();
});

test("the trigger shows what is left once the aggregate has arrived", () => {
  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 7, done: 3 } })} />);
  expect(screen.getByRole("button", { name: "4 of 7 tasks left" })).toBeTruthy();
});

test("outcome aggregates condense to what is left in the trigger and the body head", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [] }));
  const model = testModel({ tasks: { total: 7, done: 1, cancelled: 5, remaining: 1 } });

  render(
    <>
      <TasksPanel sessionRef="ref_a" model={model} />
      <TasksPanelBody sessionRef="ref_a" model={model} />
    </>,
  );

  expect(screen.getByRole("button", { name: "1 of 7 tasks left" })).toBeTruthy();
  await waitFor(() => expect(screen.getByTestId("tasks-body-head")).toBeTruthy());
  expect(screen.getByTestId("tasks-body-head").textContent).toContain("1 of 7 tasks left");
  expect(screen.queryByRole("meter")).toBeNull();
});

test("outcome aggregates infer an omitted remaining for labels", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [] }));
  const cancelledOnly = testModel({ tasks: { total: 7, done: 1, cancelled: 5 } });
  const { unmount } = render(<TasksPanelBody sessionRef="ref_cancelled" model={cancelledOnly} />);

  await waitFor(() => expect(screen.getByTestId("tasks-body-head")).toBeTruthy());
  expect(screen.getByTestId("tasks-body-head").textContent).toContain("1 of 7 tasks left");
  expect(screen.queryByRole("meter")).toBeNull();
  unmount();

  const remainingOnly = testModel({ tasks: { total: 7, done: 1, remaining: 5 } });
  render(<TasksPanelBody sessionRef="ref_remaining" model={remainingOnly} />);
  await waitFor(() => expect(screen.getByTestId("tasks-body-head")).toBeTruthy());
  expect(screen.getByTestId("tasks-body-head").textContent).toContain("5 of 7 tasks left");
  expect(screen.queryByRole("meter")).toBeNull();
});

// --- STATUS_TOUCH: pinning test (review finding) -------------------------
// The panel shares the transcript card's TaskCheck glyph family (the
// 2026-09 task-rendering unification), so the status-to-glyph mapping is the
// one place the two surfaces could drift. The mapping keeps the cancellation
// rule the legacy chain already settled (renderer-format.js:496-506 +
// style.css:3324-3329): a cancelled task's glyph reads as the same dim
// neutral as pending's, never danger - in this design system's
// color-is-attention rule, danger-tinting a routine cancellation would make
// reprioritized work indistinguishable from a real failure. The ✕ mark
// alone carries the "won't happen" distinction.
test("pins each task status's TaskCheck touch - open renders the pending box, cancelled stays neutral", () => {
  expect(STATUS_TOUCH).toEqual({
    open: "pending",
    in_progress: "started",
    done: "done",
    cancelled: "cancelled",
  });
});

// --- fetch-on-open: the per-task row list --------------------------------

test("opening the panel fetches via listTasks(ref) and shows a loading state until it resolves", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  const box: { resolve: ((r: TaskListResponse) => void) | null } = { resolve: null };
  fake.on("evener/tasks/list", () => new Promise((resolve) => (box.resolve = resolve)));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  expect(await screen.findByText(/loading tasks/i)).toBeTruthy();
  const call = fake.calls.find((c) => c.method === "evener/tasks/list");
  expect(call?.params).toEqual({ ref: "ref_a" });

  await act(async () => {
    box.resolve?.({ data: [] });
  });
  await waitFor(() => expect(screen.queryByText(/loading tasks/i)).toBeNull());
});

test("rows group by status: the collapsed settled group first, then in progress, then open; wire order holds within a group", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(screen.getAllByTestId("task-row")).toHaveLength(2));
  const liveGroups = screen.getAllByTestId("task-group-live");
  expect(liveGroups.map((group) => group.getAttribute("data-status"))).toEqual(["in_progress", "open"]);
  const settled = screen.getByTestId("task-settled-group");
  expect(settled.textContent).toContain("1");
  // The folded-away settled group sits above the current work, not at the bottom.
  const body = settled.parentElement;
  expect(body).toBeTruthy();
  const settledIndex = Array.from(body!.children).indexOf(settled);
  const firstLiveIndex = Array.from(body!.children).indexOf(liveGroups[0]!);
  expect(settledIndex).toBeLessThan(firstLiveIndex);

  await user.click(screen.getByTestId("task-settled-group-summary"));
  await waitFor(() => expect(screen.getAllByTestId("task-row")).toHaveLength(3));
});

test("empty groups render nothing, so Open leads when there are no in-progress tasks", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [TASKS_DATA[2]] }));

  render(<TasksPanelBody sessionRef="ref_a" model={testModel()} />);

  const liveGroups = await screen.findAllByTestId("task-group-live");
  expect(liveGroups).toHaveLength(1);
  expect(liveGroups[0]?.getAttribute("data-status")).toBe("open");
  expect(document.querySelector('[data-testid="task-group-live"][data-status="in_progress"]')).toBeNull();
  expect(screen.queryByTestId("task-settled-group")).toBeNull();
});

test("an all-settled list shows only the collapsed settled disclosure line", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [DATED_TASKS[0], DATED_TASKS[2]] }));

  render(<TasksPanelBody sessionRef="ref_a" model={testModel()} />);

  const settled = await screen.findByTestId("task-settled-group");
  expect(settled.textContent).toContain("1 completed task");
  expect(settled.textContent).toContain("1 cancelled task");
  expect(screen.queryByTestId("task-group-live")).toBeNull();
  expect(screen.queryByTestId("task-row")).toBeNull();
});

test("a live row shows its latest note inline; a settled row does not", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  expect((await screen.findByTestId("task-latest")).textContent).toContain(
    "Captured the compatibility changes for the release notes.",
  );
  // The note hangs bare in the prose face, like the card's fresh note - the
  // old "latest" label chip is gone.
  expect(screen.queryByText("latest")).toBeNull();

  await user.click(screen.getByTestId("task-settled-group-summary"));
  const settledRow = screen
    .getAllByTestId("task-row")
    .find((row) => row.textContent?.includes("Implement artifact store"));
  expect(settledRow?.querySelector("[data-testid='task-latest']")).toBeNull();
});

test("a cancelled row renders struck-through inside the settled group", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByTestId("task-settled-group-summary"));

  const cancelled = screen.getByText("Transition to implementation plan");
  expect(cancelled.getAttribute("data-struck")).toBe("true");
  expect(
    cancelled
      .closest("[data-testid='task-row']")
      ?.querySelector('[data-testid="task-check"]')
      ?.getAttribute("data-touch"),
  ).toBe("cancelled");
});

test("every row leads with the shared TaskCheck glyph matching its task's status", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByTestId("task-settled-group-summary"));

  const expectTouch = async (description: string, touch: string) => {
    const row = (await screen.findByText(description)).closest("[data-testid='task-row']");
    expect(row?.querySelector('[data-testid="task-check"]')?.getAttribute("data-touch")).toBe(touch);
  };
  await expectTouch("Implement artifact store", "done");
  await expectTouch("Transition to implementation plan", "cancelled");
  await expectTouch("Extend transcript API", "started");
  await expectTouch("Prepare release notes", "pending");
});

// The VisuallyHidden widget's sr-only class, through requireClass so a
// renamed class fails loudly.
const VISUALLY_HIDDEN_CLASS = requireClass(
  visuallyHiddenStyles.visuallyHidden,
  "visuallyhidden.module.css",
  "visuallyHidden",
);

test("every row carries a visually hidden status word beside its aria-hidden glyph", async () => {
  // TaskCheck is a picture of state, deliberately aria-hidden; the row's
  // status rides along visually hidden instead (the same word the card's
  // rows carry), or a screen reader cannot tell done from cancelled.
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByTestId("task-settled-group-summary"));

  const expectWord = async (description: string, word: string) => {
    const row = (await screen.findByText(description)).closest("[data-testid='task-row']");
    const srWord = row?.querySelector(`.${VISUALLY_HIDDEN_CLASS}`);
    expect(srWord?.textContent).toBe(word);
  };
  await expectWord("Implement artifact store", "done");
  await expectWord("Transition to implementation plan", "cancelled");
  await expectWord("Extend transcript API", "started");
  await expectWord("Prepare release notes", "pending");
});

test("a live row shows a relative updated time", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: DATED_TASKS }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  const row = (await screen.findByText("Extend transcript API")).closest("[data-testid='task-row']");

  expect(row?.querySelector("[data-testid='task-row-time']")).toBeTruthy();
});

test("the standalone task body does not opt into the sidebar's compact presentation", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: DATED_TASKS }));
  render(<TasksPanelBody sessionRef="ref_a" model={testModel()} />);
  const row = (await screen.findByText("Extend transcript API")).closest("[data-testid='task-row']");
  expect(row?.querySelector("[data-testid='task-row-time']")?.tagName).not.toBe("TIME");
  expect(row?.querySelector("[data-testid='task-row-time']")).toBeTruthy();
  expect(row?.querySelector("[data-activity-time]")).toBeNull();
});

test("the settled group defaults to collapsed and remembers being opened per session", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: DATED_TASKS }));

  const first = render(<TasksPanelBody sessionRef="ref_remember" model={testModel({ ref: "ref_remember" })} />);
  await screen.findByTestId("task-settled-group");
  expect(screen.queryByText("Implement artifact store")).toBeNull();
  await user.click(screen.getByTestId("task-settled-group-summary"));
  expect(await screen.findByText("Implement artifact store")).toBeTruthy();

  first.unmount();
  render(<TasksPanelBody sessionRef="ref_remember" model={testModel({ ref: "ref_remember" })} />);
  expect(await screen.findByText("Implement artifact store")).toBeTruthy();
});

test("the body header shows the count when the aggregate is known, with no progress bar", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [TASKS_DATA[0]] }));

  render(
    <>
      <TasksPanelBody sessionRef="ref_a" model={testModel({ tasks: { total: 20, done: 16 } })} />
      <Toast />
    </>,
  );
  await waitFor(() => expect(screen.getByTestId("tasks-body-head")).toBeTruthy());
  expect(screen.getByTestId("tasks-body-head").textContent).toContain("4 of 20 tasks left");
  expect(screen.queryByRole("meter")).toBeNull();
});

test("the body header is absent while no aggregate has arrived", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [TASKS_DATA[0]] }));

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

const RICH_TASK: Task = {
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
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [RICH_TASK] }));

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
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [RICH_TASK] }));

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

// jsdom computes no cascade, so the caption step is asserted against the
// stylesheet's own source - readModuleCss/topRuleBlock from styles/cssBlock,
// the technique the tap-floor test above uses - comments stripped once up
// front so an in-rule comment can never satisfy or defeat an assertion. The
// sizing rationale lives in the .promptSummary rule comment in
// taskspanel.module.css; this pins BODY text - headings and tables keep the
// widget's fixed ramp (see widgets/markdown).
test("the prompt disclosure renders its markdown body text at the caption step, not prose size", () => {
  const uncommented = readModuleCss(import.meta.url, "taskspanel.module.css");
  expect(uncommented).not.toContain("--prose-font-size");
  expect(topRuleBlock(uncommented, ".promptSummary")).toContain("font-size: var(--font-size-caption)");
  expect(topRuleBlock(uncommented, ".promptBody")).toContain("font-size: var(--font-size-caption)");
});

test("the prompt disclosure shows a one-line markdown preview collapsed and the full markdown body open", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({
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
  // The disclosure turns the shared Chevron icon, not a text triangle.
  expect(prompt.textContent).not.toContain("▸");
  expect(prompt.querySelector("summary svg")).toBeTruthy();

  await user.click(screen.getByTestId("task-prompt-summary"));

  const body = await screen.findByTestId("task-prompt-body");
  expect(body.querySelector("code")?.textContent).toBe("job/artifact");
});

test("a task with a blank prompt renders no prompt disclosure", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [{ ...RICH_TASK, prompt: "" }] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByText("Wire up expand/collapse"));

  expect(screen.queryByTestId("task-prompt")).toBeNull();
});

test("clicking an expanded row's summary again collapses it", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [RICH_TASK] }));

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
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

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
  expect(body.querySelector("[data-testid='task-notes-empty']")).not.toBeNull();
  expect(body.querySelector("[data-testid='task-notes']")).toBeNull();
});

test("an expanded task shows the meta strip and timestamps line", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: DATED_TASKS }));

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
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({
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
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({
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
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await user.click(await screen.findByTestId("task-settled-group-summary"));
  await user.click(await screen.findByText("Wire up the status row"));

  expect(screen.getAllByTestId("task-expanded")).toHaveLength(1);
});

test("the expanded row is a real native disclosure (<details open>), not just conditionally-rendered markup", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [RICH_TASK] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await screen.findByText("Wire up expand/collapse");

  const details = screen.getByRole("group") as HTMLDetailsElement;
  expect(details.open).toBe(false);

  await user.click(screen.getByText("Wire up expand/collapse"));

  expect(details.open).toBe(true);
});

test("a confirmed-empty fetch (real daemon, genuinely zero tasks) shows the definitive empty state", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [] }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["empty"]));
  expect(screen.queryByTestId("tasks-stale")).toBeNull();
});

test("re-opening after closing re-fetches (the plan's fetch-on-open model, not a one-time fetch)", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await screen.findAllByTestId("task-row");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await screen.findAllByTestId("task-row");

  expect(fake.calls.filter((c) => c.method === "evener/tasks/list")).toHaveLength(2);
});

test("re-fetches automatically when the live aggregate changes WHILE the panel stays open", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));
  const user = userEvent.setup();

  const { rerender } = render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 0 } })} />);
  await user.click(screen.getByRole("button", { name: "3 of 3 tasks left" }));
  await screen.findAllByTestId("task-row");
  expect(fake.calls.filter((c) => c.method === "evener/tasks/list")).toHaveLength(1);

  // A live evener/task/updated notification bumped the aggregate - the panel
  // is still open, so this should trigger a fresh fetch automatically
  // rather than leaving stale rows on screen.
  rerender(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />);

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/tasks/list")).toHaveLength(2));
});

test("does not fetch at all while the panel is closed, even if the aggregate changes", () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

  const { rerender } = render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 0 } })} />);
  rerender(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />);

  expect(fake.calls.filter((c) => c.method === "evener/tasks/list")).toHaveLength(0);
});

// --- source-backed unsupported state (actionUnavailable) ------------------

test("a source-backed actionUnavailable rejection shows the honest unsupported state, with no error toast (it's not a bug)", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => {
    throw new WireError("remote source does not expose evener tasks", -32014, { evenerErrorInfo: "actionUnavailable" });
  });

  render(
    <>
      <TasksPanel sessionRef="ref_remote" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["unsupported"]));
  expect(errorToasts()).toEqual([]);
  // No Try again: the source cannot answer this call at all.
  expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
});

// --- other failures: toast + inline error state ----------------------------

test("a generic fetch failure surfaces an error toast AND an inline error state", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => {
    throw new Error("tasks boom");
  });

  render(
    <>
      <TasksPanel sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(failedNotices()).toHaveLength(1));
  expect(emptyStateReasons(document.body)).toEqual([]);
  // The toast and the inline state report the same failure.
  expect(errorToasts()).toEqual([failedStateSentence()]);
});

// The store already rejects an obsolete completion (fetchID mismatch), so the
// stale request's failure must not toast either: an A-then-B overlap where the
// OLDER request fails after the newer one succeeded would otherwise show a
// failure banner over a list that is on screen and current.
test("a stale overlapping failure does not toast after a newer fetch succeeded", async () => {
  const fake = connectReadyFakeClient();
  const calls: Array<{ resolve: (value: TaskListResponse) => void; reject: (err: unknown) => void }> = [];
  fake.on(
    "evener/tasks/list",
    () =>
      new Promise<TaskListResponse>((resolve, reject) => {
        calls.push({ resolve, reject });
      }),
  );

  const { rerender } = render(
    <>
      <TasksPanelBody sessionRef="ref_stale" model={testModel({ ref: "ref_stale" })} />
      <Toast />
    </>,
  );
  // A tasks push refires the fetch effect while the first fetch is still in
  // flight: the store folds it into that run (one request at a time) and
  // reads again once the first settles, dropping the first fetch's answer.
  rerender(
    <>
      <TasksPanelBody sessionRef="ref_stale" model={testModel({ ref: "ref_stale", tasks: { total: 3, done: 1 } })} />
      <Toast />
    </>,
  );
  await waitFor(() => expect(calls).toHaveLength(1));

  await act(async () => calls[0]?.reject(new Error("tasks boom")));
  await waitFor(() => expect(calls).toHaveLength(2));
  expect(errorToasts()).toEqual([]);
  expect(failedNotices()).toEqual([]);

  await act(async () => calls[1]?.resolve({ data: TASKS_DATA }));
  await screen.findByText("Wire up session actions");
  expect(errorToasts()).toEqual([]);
  expect(screen.queryByTestId("tasks-stale")).toBeNull();
});

// The hub resumes a cold session before it can list anything
// (cmd/evener-hub/app_session_resume.go's withSessionResume). Both of this
// panel's two reports have to give way together: a toast blaming the daemon
// beside an inline heading blaming the task list is worse than either alone.
test("a failed auto-resume names the resume in BOTH the toast and the inline state", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => {
    throw new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" });
  });

  render(
    <>
      <TasksPanel sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(failedNotices()).toHaveLength(1));
  expect(emptyStateReasons(document.body)).toEqual([]);
  // The toast is exactly the inline heading and detail.
  expect(errorToasts()).toEqual([failedStateSentence()]);
  const resumeHeading = failedStateSentence().split(": ")[0];

  // The same rejection text without the hubLaunch discriminator is a plain
  // load failure, and gets the action's own heading instead.
  cleanup();
  resetToastStoreForTests();
  fake.on("evener/tasks/list", () => {
    throw new Error("evener launch-check timed out");
  });
  render(<TasksPanelBody sessionRef="ref_b" model={testModel({ ref: "ref_b" })} />);
  await waitFor(() => expect(failedNotices()).toHaveLength(1));
  expect(failedStateSentence().split(": ")[0]).not.toBe(resumeHeading);
});

// The toast drops the separator when a rejection carries no text of its own
// (protocol/errors.ts). The inline half has to drop the detail line for the
// same rejection, or one failure reads as a headline in the toast and a
// headline plus a blank line in the panel.
test("a rejection with no text of its own shows the headline alone, with no empty detail line", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => {
    throw new Error("");
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(failedNotices()).toHaveLength(1));
  const [failed] = failedNotices();
  const heading = failed?.querySelector("p");
  expect(failed?.querySelectorAll("p")).toHaveLength(1);
  expect(heading?.textContent).not.toBe("");
  // Nothing beside the heading: no empty detail line, no blank element.
  expect(heading?.parentElement?.textContent).toBe(heading?.textContent);
  expect([...(failed?.querySelectorAll("*") ?? [])].filter((element) => element.textContent === "")).toEqual([]);
});

// --- a failed LIVE re-fetch keeps the list it already has -------------------
//
// The panel re-fetches on every evener/task/updated push while it stays open.
// A momentary rejection on one of those fetches ("local daemon unavailable:
// broken pipe") used to replace the list the reader was mid-way through with
// an error state, though the previous page was still in hand. Nothing the
// reader did caused it, and nothing they could do brought it back.

// Scripts evener/tasks/list to answer `first` once and to reject every later
// call: the exact shape of a live re-fetch blipping under a reader.
function failAfterFirstFetch(fake: FakeClient, first: TaskListResponse["data"], err: unknown): void {
  let calls = 0;
  fake.on("evener/tasks/list", () => {
    calls += 1;
    if (calls === 1) return { data: first };
    throw err;
  });
}

// Opens the panel, waits for the first fetch to land, then bumps the live
// aggregate the way a evener/task/updated push does - which is the only thing
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
  await user.click(screen.getByRole("button", { name: "3 of 3 tasks left" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/tasks/list")).toHaveLength(1));

  rerender(panel(1));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/tasks/list")).toHaveLength(2));
  return user;
}

test("a re-fetch that fails keeps the rows already on screen", async () => {
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
  failAfterFirstFetch(fake, TASKS_DATA, new Error("local daemon unavailable: broken pipe"));

  await openThenPush(fake);

  const stale = await screen.findByTestId("tasks-stale");
  expect(within(stale).getByRole("alert").textContent).not.toBe("");
  // The notice sits above the retained rows, so it is read before them.
  const firstRow = screen.getAllByTestId("task-row")[0];
  if (!firstRow) throw new Error("Missing retained task row");
  expect(stale.compareDocumentPosition(firstRow) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

// The other direction of the same confusion. "No tasks yet" is the panel's
// one definitive statement about the session - it claims the agent's list is
// empty - so a retained empty list has to be marked stale just as a retained
// populated one does, or a failed refresh passes for a confirmed answer.
test("a retained EMPTY list under a failed refresh is marked stale, not left as a confirmed answer", async () => {
  const fake = connectReadyFakeClient();
  failAfterFirstFetch(fake, [], new Error("local daemon unavailable: broken pipe"));

  await openThenPush(fake);

  await screen.findByTestId("tasks-stale");
  expect(emptyStateReasons(document.body)).toEqual(["empty"]);
});

// bv13's invariant, carried onto the retained-list path: the toast and the
// inline report are the SAME sentence, so a failed session resume takes over
// both or neither.
test("the stale notice and the toast report a re-fetch failure in the same words", async () => {
  const fake = connectReadyFakeClient();
  failAfterFirstFetch(
    fake,
    TASKS_DATA,
    new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" }),
  );

  await openThenPush(fake);

  const stale = await screen.findByTestId("tasks-stale");
  await waitFor(() => expect(errorToasts()).toHaveLength(1));
  // The notice's heading is the failure sentence; its body and Try again
  // follow beneath it.
  expect(errorToasts()).toEqual([within(stale).getByRole("alert").querySelector("p")?.textContent]);
});

// evener/task/updated is event-driven, never scheduled (internal/appprojector/
// appwire_projection.go's EventTaskUpdated case): a session whose agent has
// stopped touching its task list emits no further pushes, so a failed fetch
// is stuck until the reader asks again. Try again is that ask.
test("Try again re-fetches the list and clears the stale notice once it succeeds", async () => {
  const fake = connectReadyFakeClient();
  let calls = 0;
  fake.on("evener/tasks/list", () => {
    calls += 1;
    if (calls === 2) throw new Error("local daemon unavailable: broken pipe");
    return { data: TASKS_DATA };
  });

  const user = await openThenPush(fake);
  await screen.findByTestId("tasks-stale");

  await user.click(screen.getByRole("button", { name: "Try again" }));

  await waitFor(() => expect(screen.queryByTestId("tasks-stale")).toBeNull());
  expect(fake.calls.filter((c) => c.method === "evener/tasks/list")).toHaveLength(3);
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
  return new WireError("thread not found: thr_a", -32014, { evenerErrorInfo: "sessionUnavailable" });
}

test("thread-not-found on a session that never had a live aggregate still shows the honest 'No tasks yet' (unchanged)", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => {
    throw threadNotFoundError();
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: null })} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["empty"]));
  expect(screen.queryByTestId("tasks-daemon-gone")).toBeNull();
});

test("closing then re-opening after the daemon exits keeps the rows already shown, with a terminal notice instead of blanking them", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  let calls = 0;
  fake.on("evener/tasks/list", () => {
    calls += 1;
    if (calls === 1) return { data: TASKS_DATA };
    throw threadNotFoundError();
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />);
  await user.click(screen.getByRole("button", { name: "2 of 3 tasks left" }));
  await screen.findAllByTestId("task-row");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  await user.click(screen.getByRole("button", { name: "2 of 3 tasks left" }));

  await screen.findByTestId("tasks-daemon-gone");
  expect(screen.getAllByTestId("task-row")).toHaveLength(2);
  expect(screen.getByTestId("task-settled-group").textContent).toContain("1");
  expect(emptyStateReasons(document.body)).toEqual([]);
});

test("that terminal notice offers no Try again - the hub never tracked this session, so asking again fails identically forever", async () => {
  const fake = connectReadyFakeClient();
  failAfterFirstFetch(fake, TASKS_DATA, threadNotFoundError());

  await openThenPush(fake);

  await screen.findByTestId("tasks-daemon-gone");
  expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
});

test("thread-not-found with a known aggregate but no rows ever fetched shows a terminal message, not 'No tasks yet'", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => {
    throw threadNotFoundError();
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />);
  await user.click(screen.getByRole("button", { name: "2 of 3 tasks left" }));

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["ended"]));
  expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
});

test("no toast for a dead-daemon rejection - it's not a bug, it's an expected terminal state", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => {
    throw threadNotFoundError();
  });

  render(
    <>
      <TasksPanel sessionRef="ref_a" model={testModel({ tasks: { total: 3, done: 1 } })} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "2 of 3 tasks left" }));

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["ended"]));
  expect(errorToasts()).toEqual([]);
});

// The first fetch has no previous page to keep, so it still blanks - but it
// is stuck in exactly the same way, and needs the same way out.
test("a first fetch that fails offers Try again, which fetches again", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  let calls = 0;
  fake.on("evener/tasks/list", () => {
    calls += 1;
    if (calls === 1) throw new Error("tasks boom");
    return { data: TASKS_DATA };
  });

  render(<TasksPanel sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));
  await waitFor(() => expect(failedNotices()).toHaveLength(1));
  expect(failedStateSentence()).toContain("tasks boom");
  expect(screen.queryAllByTestId("task-row")).toHaveLength(0);

  await user.click(screen.getByRole("button", { name: "Try again" }));

  expect(await screen.findAllByTestId("task-row")).toHaveLength(2);
  expect(screen.getByTestId("task-settled-group").textContent).toContain("1");
  expect(failedNotices()).toEqual([]);
  expect(emptyStateReasons(document.body)).toEqual([]);
});

test("cancelled history never claims completion and preserves every current and remaining task", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/tasks/list", () => ({
    data: [
      { id: 1, type: "implement", description: "cancelled work", prompt: "", status: "cancelled" },
      { id: 2, type: "implement", description: "current first", prompt: "", status: "in_progress" },
      { id: 3, type: "implement", description: "remaining first", prompt: "", status: "open" },
      { id: 4, type: "implement", description: "current second", prompt: "", status: "in_progress" },
      { id: 5, type: "implement", description: "remaining second", prompt: "", status: "open" },
    ],
  }));
  render(<TasksPanelBody sessionRef="ref_a" model={testModel()} />);
  const summary = await screen.findByTestId("task-settled-group-summary");
  expect(summary.textContent).toBe("1 cancelled task");
  expect(screen.queryByText("cancelled work")).toBeNull();
  expect(screen.getAllByTestId("task-row").map((row) => row.textContent)).toEqual([
    expect.stringContaining("current first"),
    expect.stringContaining("current second"),
    expect.stringContaining("remaining first"),
    expect.stringContaining("remaining second"),
  ]);
  await user.click(summary);
  expect(screen.getByText("cancelled work").getAttribute("data-struck")).toBe("true");
});
