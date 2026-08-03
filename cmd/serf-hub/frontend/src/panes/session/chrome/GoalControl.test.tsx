import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { WireError } from "../../../protocol/errors";
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
    ...overrides,
  };
}

// The set-goal Dialog is controlled by props (dialogOpen/onDialogOpenChange)
// owned by SessionChrome now, not GoalControl's own state - the "Set goal…"
// entry point lives in the session ⋯ menu (SessionActionsMenu), not this
// component. This harness stands in for that parent: it owns the open state
// and exposes a plain button as the external trigger, exactly the seam
// SessionChrome wires SessionActionsMenu's "Set goal…" item to. `initialOpen`
// lets a test render straight into the open-dialog state where the trigger
// itself isn't what's under test.
function GoalControlHarness({ model, initialOpen = false }: { model: ThreadModel; initialOpen?: boolean }) {
  const [open, setOpen] = useState(initialOpen);
  return (
    <>
      <button type="button" data-testid="open-goal-dialog" onClick={() => setOpen(true)}>
        open the set-goal dialog
      </button>
      <GoalControl sessionRef="ref_a" model={model} dialogOpen={open} onDialogOpenChange={setOpen} />
      <Toast />
    </>
  );
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// Opens the goal chip's popover (status + iterations + Clear goal). The chip
// button's accessible name is "Goal: {status}" - the trailing colon keeps
// this from matching the popover's own "Clear goal" button or the harness's
// external trigger.
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

test("renders nothing when the thread has no goal (the Set-goal entry point lives in the session ⋯ menu now)", () => {
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

// --- setting a goal ------------------------------------------------------------

test("the controlled dialog opens onto an empty objective field", async () => {
  const user = userEvent.setup();
  render(<GoalControlHarness model={testModel({ goal: null })} />);
  await user.click(screen.getByTestId("open-goal-dialog"));

  const dialog = await screen.findByRole("dialog");
  expect((within(dialog).getByRole("textbox") as HTMLTextAreaElement).value).toBe("");
});

test("Save is disabled while the objective is blank", async () => {
  const user = userEvent.setup();
  render(<GoalControlHarness model={testModel({ goal: null })} />);
  await user.click(screen.getByTestId("open-goal-dialog"));
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

  render(<GoalControlHarness model={testModel({ goal: null })} />);
  await user.click(screen.getByTestId("open-goal-dialog"));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", objective: "ship the feature" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  // The optimistic goal surfaces as the chip; its popover carries the
  // active/zero-iteration detail.
  expect(screen.getByRole("button", { name: /goal: active/i })).toBeTruthy();
  const popover = await openGoalPopover(user);
  expect(within(popover).getByText(/0 iterations/i)).toBeTruthy();
});

test("a failed setGoal surfaces an error toast, leaves the dialog open, and does not change the displayed goal", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("goal/set", () => {
    throw new Error("goal boom");
  });

  render(<GoalControlHarness model={testModel({ goal: null })} />);
  await user.click(screen.getByTestId("open-goal-dialog"));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));

  await screen.findByText(/goal boom/i);
  expect(screen.getByRole("dialog")).toBeTruthy();
  // Still no goal (no optimistic override was recorded), so still no chip.
  expect(screen.queryByRole("button", { name: /goal:/i })).toBeNull();
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

  const first = render(<GoalControlHarness model={testModel({ goal: null })} />);
  await user.click(screen.getByTestId("open-goal-dialog"));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));
  await waitFor(() => expect(screen.getByRole("button", { name: /goal: active/i })).toBeTruthy());

  first.unmount(); // real dockview behavior: the pane's whole tree unmounts on a tab switch

  // Remount: the store's own model.goal is STILL null (no live push exists to
  // have updated it) - if this read plain model.goal, it would wrongly render
  // nothing at all instead of the optimistic goal chip.
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: null })} />);
  expect(screen.getByRole("button", { name: /goal: active/i })).toBeTruthy();
});

test("a genuinely fresh model.goal (e.g. a reconnect re-hydrate) supersedes a stale override instead of being masked by it forever", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("goal/set", () => ({ started: true }));

  const { rerender } = render(<GoalControlHarness model={testModel({ goal: null })} />);
  await user.click(screen.getByTestId("open-goal-dialog"));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));
  await waitFor(() => expect(screen.getByRole("button", { name: /goal: active/i })).toBeTruthy());

  // A fresh hydrate brings real, more-advanced server truth - the daemon's
  // goal loop has actually been running. The stale optimistic {active, 0}
  // must not keep shadowing it.
  rerender(<GoalControlHarness model={testModel({ goal: { status: "complete", iterations: 5 } })} />);
  expect(screen.getByRole("button", { name: /goal: complete/i })).toBeTruthy();
  const popover = await openGoalPopover(user);
  expect(within(popover).getByText(/5 iterations/i)).toBeTruthy();
});

// goal/set resumes a cold session first (cmd/serf-hub/app_session_resume.go's
// setGoalWithResume). A failed resume is not a failed goal.
test("a setGoal that fails because the session would not start names the start, not the goal", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("goal/set", () => {
    throw new WireError("fork/exec serf: no such file", -32014, { serfErrorInfo: "hubLaunch" });
  });

  render(<GoalControlHarness model={testModel({ goal: null })} />);
  await user.click(screen.getByTestId("open-goal-dialog"));
  const dialog = await screen.findByRole("dialog");
  await user.type(within(dialog).getByRole("textbox"), "ship the feature");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));

  await screen.findByText("Couldn't start this session: fork/exec serf: no such file");
  expect(screen.queryByText(/couldn't set goal/i)).toBeNull();
});
