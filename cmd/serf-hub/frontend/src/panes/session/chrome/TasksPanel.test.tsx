import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { TasksPanel } from "./TasksPanel";

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
    ...overrides,
  };
}

afterEach(() => {
  cleanup();
});

// model.tasks is the aggregate {total, done} pushed live by serf/task/
// updated (protocol/reducer.ts's own case) and seeded null at hydrate
// (never populated from a snapshot - protocol/reducer.ts's hydrateThread:
// `tasks: null`) - so a session with no live task-count push yet has
// nothing to show beyond an honest "no data" state. The per-task row list
// (taskData.ts's TaskRow[]) has no live data source at all this wave (no
// threads-store action fetches serf/tasks/list - see this stream's own
// report for the NEEDS_CONTEXT gap), so this panel deliberately shows the
// aggregate only.

test("the trigger shows a bare 'Tasks' label when no aggregate has arrived yet", () => {
  render(<TasksPanel model={testModel({ tasks: null })} />);
  expect(screen.getByRole("button", { name: "Tasks" })).toBeTruthy();
});

test("the trigger shows the done/total counts once the aggregate has arrived", () => {
  render(<TasksPanel model={testModel({ tasks: { total: 7, done: 3 } })} />);
  expect(screen.getByRole("button", { name: "Tasks 3/7" })).toBeTruthy();
});

test("opens a panel titled Tasks on click", async () => {
  const user = userEvent.setup();
  render(<TasksPanel model={testModel({ tasks: null })} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  const dialog = await screen.findByRole("dialog");
  expect(dialog.textContent).toContain("Tasks");
});

test("shows an honest empty state when no aggregate has arrived yet, not a false 'zero tasks' claim", async () => {
  const user = userEvent.setup();
  render(<TasksPanel model={testModel({ tasks: null })} />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  expect(await screen.findByText(/no task activity yet/i)).toBeTruthy();
});

test("shows the done/total summary once the aggregate has arrived", async () => {
  const user = userEvent.setup();
  render(<TasksPanel model={testModel({ tasks: { total: 5, done: 2 } })} />);
  await user.click(screen.getByRole("button", { name: "Tasks 2/5" }));

  expect(await screen.findByText(/2 of 5 done/i)).toBeTruthy();
});
