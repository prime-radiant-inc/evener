import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { GoalControl, resetGoalOverridesForTests } from "./GoalControl";

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
    ...overrides,
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetGoalOverridesForTests();
});

afterEach(() => {
  cleanup();
});

// --- display -----------------------------------------------------------------

test("shows 'no goal set' and only a Set-goal action when the thread has no goal", () => {
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  expect(screen.getByText(/no goal set/i)).toBeTruthy();
  expect(screen.getByRole("button", { name: /set goal/i })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /clear goal/i })).toBeNull();
});

test("shows the goal's status and iteration count when one is set, singular for exactly one iteration", () => {
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 1 } })} />);
  expect(screen.getByText(/active/i)).toBeTruthy();
  expect(screen.getByText(/1 iteration\b/i)).toBeTruthy();
});

test("pluralizes iterations for anything other than exactly one", () => {
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 3 } })} />);
  expect(screen.getByText(/3 iterations/i)).toBeTruthy();
});

test("offers Clear goal only when a goal is currently set", () => {
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 0 } })} />);
  expect(screen.getByRole("button", { name: /clear goal/i })).toBeTruthy();
  expect(screen.getByRole("button", { name: /change goal/i })).toBeTruthy();
});

test("disables both actions when the thread's goal capability is unavailable", () => {
  render(
    <GoalControl
      sessionRef="ref_a"
      model={testModel({
        goal: { status: "active", iterations: 0 },
        capabilities: { ...FULL_CAPABILITIES, goal: false },
      })}
    />,
  );
  expect((screen.getByRole("button", { name: /change goal/i }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: /clear goal/i }) as HTMLButtonElement).disabled).toBe(true);
});

// --- setting a goal ------------------------------------------------------------

test("opens a dialog with an empty objective field", async () => {
  const user = userEvent.setup();
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  await user.click(screen.getByRole("button", { name: /set goal/i }));

  const dialog = await screen.findByRole("dialog");
  expect((within(dialog).getByRole("textbox") as HTMLTextAreaElement).value).toBe("");
});

test("Save is disabled while the objective is blank", async () => {
  const user = userEvent.setup();
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  await user.click(screen.getByRole("button", { name: /set goal/i }));
  const dialog = await screen.findByRole("dialog");

  expect((within(dialog).getByRole("button", { name: /save/i }) as HTMLButtonElement).disabled).toBe(true);
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  expect((within(dialog).getByRole("button", { name: /save/i }) as HTMLButtonElement).disabled).toBe(false);
});

test("saving calls setGoal with the objective and optimistically shows an active, zero-iteration goal with no live push needed", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("goal/set", (params) => {
    called = params;
    return { started: true };
  });

  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  await user.click(screen.getByRole("button", { name: /set goal/i }));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", objective: "ship the feature" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(screen.getByText(/active/i)).toBeTruthy();
  expect(screen.getByText(/0 iterations/i)).toBeTruthy();
});

test("a failed setGoal surfaces an error toast, leaves the dialog open, and does not change the displayed goal", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("goal/set", () => {
    throw new Error("goal boom");
  });

  render(
    <>
      <GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: /set goal/i }));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));

  await screen.findByText(/goal boom/i);
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByText(/no goal set/i)).toBeTruthy();
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
  await user.click(screen.getByRole("button", { name: /clear goal/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", objective: "" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  await waitFor(() => expect(screen.getByText(/no goal set/i)).toBeTruthy());
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
  await user.click(screen.getByRole("button", { name: /clear goal/i }));

  await screen.findByText(/clear goal boom/i);
  expect(screen.getByText(/4 iterations/i)).toBeTruthy();
});

// --- remount-safety + stale-override invalidation -----------------------------
//
// Binding constraint (every wave-5 task): "durable state ... lives in
// stores/localStorage, never component state that matters across a tab
// switch" (dockview unmounts an inactive pane's whole tree). There is no
// threads-store action to patch ThreadModel.goal locally (goal/set has no
// live push - protocol/model.ts's own doc comment), so the optimistic value
// lives in a small ref-keyed module cache instead of component state.

test("the optimistic override survives an unmount/remount of the SAME ref (a dockview tab switch)", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("goal/set", () => ({ started: true }));

  const first = render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  await user.click(screen.getByRole("button", { name: /set goal/i }));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));
  await waitFor(() => expect(screen.getByText(/active/i)).toBeTruthy());

  first.unmount(); // real dockview behavior: the pane's whole tree unmounts on a tab switch

  // Remount: the store's own model.goal is STILL null (no live push exists to
  // have updated it) - if this read plain model.goal, it would wrongly
  // revert to "no goal set".
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  expect(screen.getByText(/active/i)).toBeTruthy();
});

test("a genuinely fresh model.goal (e.g. a reconnect re-hydrate) supersedes a stale override instead of being masked by it forever", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("goal/set", () => ({ started: true }));

  const { rerender } = render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  await user.click(screen.getByRole("button", { name: /set goal/i }));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));
  await waitFor(() => expect(screen.getByText(/0 iterations/i)).toBeTruthy());

  // A fresh hydrate brings real, more-advanced server truth - the daemon's
  // goal loop has actually been running. The stale optimistic {active, 0}
  // must not keep shadowing it.
  rerender(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "complete", iterations: 5 } })} />);
  expect(screen.getByText(/complete/i)).toBeTruthy();
  expect(screen.getByText(/5 iterations/i)).toBeTruthy();
});
