import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { GoalControl, resetGoalOverridesForTests } from "./GoalControl";

const here = dirname(fileURLToPath(import.meta.url));

test("goal CSS hides only the inline anchor below the full-row threshold", () => {
  const css = readFileSync(join(here, "goalcontrol.module.css"), "utf8");
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.anchor\s*\{[^}]*display:\s*none/);
});

const FULL_CAPABILITIES: ThreadCapabilities = {
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
    capabilities: FULL_CAPABILITIES,
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

// Setting a goal moved to the command palette's /goal builtin (the unified
// session menu deliberately carries no slash-command actions - see
// SessionMenu.tsx's header comment), so this component is display + clear
// only: no harness and no dialog tests remain.

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// Opens the goal chip's popover (status + iterations + Clear goal). The chip
// button's accessible name is "Goal: {status}" - the trailing colon keeps
// this from matching the popover's own "Clear goal" button.
async function openGoalPopover(user: ReturnType<typeof userEvent.setup>): Promise<HTMLElement> {
  await user.click(screen.getByRole("button", { name: /goal:/i }));
  return screen.getByTestId("goal-popover");
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  // Toasts are module state and outlive cleanup(); without this a toast from an
  // earlier test in this file is still on screen, and an assertion that a
  // message is ABSENT matches the stale one instead.
  resetToastStoreForTests();
  resetGoalOverridesForTests();
});

afterEach(() => {
  cleanup();
});

// --- display -----------------------------------------------------------------

test("renders nothing when the thread has no goal (the set-goal entry point is the palette's /goal now)", () => {
  const { container } = render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  expect(container.firstChild).toBeNull();
  expect(screen.queryByRole("button", { name: /goal/i })).toBeNull();
});

test("shows a chip once a goal is set; its popover carries the status and iteration count, singular for exactly one iteration", async () => {
  const user = userEvent.setup();
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 1 } })} />);
  expect(screen.getByRole("button", { name: /goal: active/i })).toBeTruthy();

  const popover = await openGoalPopover(user);
  expect(within(popover).getByText(/active/i)).toBeTruthy();
  expect(within(popover).getByText(/\b1 iteration\b/i)).toBeTruthy();
});

test("pluralizes iterations in the popover for anything other than exactly one", async () => {
  const user = userEvent.setup();
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 3 } })} />);

  const popover = await openGoalPopover(user);
  expect(within(popover).getByText(/3 iterations/i)).toBeTruthy();
});

test("offers Clear goal in the popover only when a goal is currently set", async () => {
  const user = userEvent.setup();
  const { rerender } = render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  // No goal: nothing rendered at all, so no chip to open and no Clear goal.
  expect(screen.queryByRole("button", { name: /clear goal/i })).toBeNull();

  rerender(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 0 } })} />);
  const popover = await openGoalPopover(user);
  expect(within(popover).getByRole("button", { name: /clear goal/i })).toBeTruthy();
});

test("disables Clear goal when the thread's goal capability is unavailable", async () => {
  const user = userEvent.setup();
  render(
    <GoalControl
      sessionRef="ref_a"
      model={testModel({
        goal: { status: "active", iterations: 0 },
        capabilities: { ...FULL_CAPABILITIES, goal: false },
      })}
    />,
  );
  const popover = await openGoalPopover(user);
  expect((within(popover).getByRole("button", { name: /clear goal/i }) as HTMLButtonElement).disabled).toBe(true);
});

// --- clearing a goal -----------------------------------------------------------

test("Clear goal calls setGoal with an empty objective directly, no confirmation dialog", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("goal/set", (params) => {
    called = params;
    return { started: false };
  });

  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 4 } })} />);
  const popover = await openGoalPopover(user);
  await user.click(within(popover).getByRole("button", { name: /clear goal/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", objective: "" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  // Cleared: the optimistic override is null, so the chip is gone.
  await waitFor(() => expect(screen.queryByRole("button", { name: /goal:/i })).toBeNull());
});

test("a failed Clear goal surfaces an error toast and leaves the goal displayed as it was", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("goal/set", () => {
    throw new Error("clear goal boom");
  });

  render(
    <>
      <GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 4 } })} />
      <Toast />
    </>,
  );
  const popover = await openGoalPopover(user);
  await user.click(within(popover).getByRole("button", { name: /clear goal/i }));

  await screen.findByText(/clear goal boom/i);
  // The popover stays open showing the unchanged goal.
  expect(within(screen.getByTestId("goal-popover")).getByText(/4 iterations/i)).toBeTruthy();
});
