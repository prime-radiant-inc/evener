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

// Below 560px the full text chip used to vanish outright, leaving phones with
// no goal affordance at all. It now swaps for a compact glyph-only trigger
// instead - the same container query, but toggling which of the two trigger
// elements is visible rather than hiding the whole anchor.
test("goal CSS shows the full chip and hides the compact trigger at/above the full-row threshold", () => {
  const css = readFileSync(join(here, "goalcontrol.module.css"), "utf8");
  expect(css).toMatch(/\.compactTrigger\s*\{[^}]*display:\s*none/);
});

test("goal CSS swaps to the compact trigger and hides the full chip below the full-row threshold", () => {
  const css = readFileSync(join(here, "goalcontrol.module.css"), "utf8");
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.chipButton\s*\{[^}]*display:\s*none/);
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.compactTrigger\s*\{[^}]*display:\s*(inline-flex|flex)/);
});

// Mobile touch-target floor (2026-07-30-mobile-session-layout-design.md,
// decision 4): the compact trigger is the ONLY way to reach the goal popover
// below 560px, so a coarse pointer needs the platform's 44px tap floor.
// jsdom has no layout, so this reads the CSS source directly - same approach
// as button.module.css's own mobile tap-floor test.
test("the compact trigger reaches the 44px tap floor under a coarse pointer", () => {
  const css = readFileSync(join(here, "goalcontrol.module.css"), "utf8");
  const coarse = css.match(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/);
  expect(coarse, "goalcontrol.module.css must have a (pointer: coarse) media block").not.toBeNull();
  const rule = coarse![1]!.match(/\.compactTrigger\s*\{([^}]*)\}/);
  expect(rule, "the coarse-pointer block must override .compactTrigger").not.toBeNull();
  expect(rule![1]).toContain("min-width: var(--tap-min)");
  expect(rule![1]).toContain("min-height: var(--tap-min)");
});

// The clipping regression (live-verified): a hand-rolled `position: absolute;
// bottom: calc(100% + ...)` popover, opening upward from an anchor inside the
// composer status row, was clipped entirely by that row's overflow: hidden
// container - correct geometry, opacity 1, never painted. The popover is now
// the portalled, viewport-positioned widgets/popover (same as ModelSwitch's
// own), so goalcontrol.module.css must never again declare the positioning
// that caused it - only content styling stays.
test("goalcontrol.module.css no longer hand-rolls popover positioning", () => {
  const css = readFileSync(join(here, "goalcontrol.module.css"), "utf8");
  const popoverRule = css.match(/\.popover\s*\{([^}]*)\}/);
  expect(popoverRule, "goalcontrol.module.css must still declare .popover for content styling").not.toBeNull();
  expect(popoverRule![1]).not.toMatch(/position:\s*absolute/);
  expect(popoverRule![1]).not.toMatch(/bottom:/);
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
  changeVisionModel: true,
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
    visionModel: "",
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

// Opens the goal chip's popover (status + iterations + Clear goal) through
// the full-width text chip. Below 560px a second, compact glyph-only trigger
// (data-testid="goal-compact-trigger") opens the identical popover - see the
// dedicated compact-trigger tests below - but both triggers carry the same
// "Goal: {status}" accessible name, so a role/name query can't disambiguate
// them; the testid picks the chip specifically.
async function openGoalPopover(user: ReturnType<typeof userEvent.setup>): Promise<HTMLElement> {
  await user.click(screen.getByTestId("goal-chip-trigger"));
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
  expect(screen.getByTestId("goal-chip-trigger").textContent).toMatch(/goal: active/i);

  const popover = await openGoalPopover(user);
  expect(within(popover).getByText(/active/i)).toBeTruthy();
  expect(within(popover).getByText(/\b1 iteration\b/i)).toBeTruthy();
});

// --- compact glyph trigger (< 560px) -----------------------------------------

test("the compact trigger's accessible name carries the same status text as the chip", () => {
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 1 } })} />);
  expect(screen.getByTestId("goal-compact-trigger").getAttribute("aria-label")).toMatch(/^goal: active$/i);
});

test("the compact trigger's accessible name tracks a complete goal too", () => {
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "complete", iterations: 3 } })} />);
  expect(screen.getByTestId("goal-compact-trigger").getAttribute("aria-label")).toMatch(/^goal: complete$/i);
});

test("opens the same popover through the compact glyph trigger", async () => {
  const user = userEvent.setup();
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 2 } })} />);
  await user.click(screen.getByTestId("goal-compact-trigger"));

  const popover = screen.getByTestId("goal-popover");
  expect(within(popover).getByText(/active/i)).toBeTruthy();
  expect(within(popover).getByRole("button", { name: /clear goal/i })).toBeTruthy();
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

// --- dismissal (mirrors ModelSwitch.test.tsx's own Popover-dismissal tests,
// since GoalControl's popover is now the same widget) --------------------

test("pressing Escape closes an open popover", async () => {
  const user = userEvent.setup();
  render(<GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 1 } })} />);
  await openGoalPopover(user);
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByTestId("goal-popover")).toBeNull());
});

test("a click outside the open popover closes it", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <button type="button" data-testid="outside">
        outside
      </button>
      <GoalControl sessionRef="ref_a" model={testModel({ goal: { status: "active", iterations: 1 } })} />
    </div>,
  );
  await openGoalPopover(user);
  await user.click(screen.getByTestId("outside"));

  await waitFor(() => expect(screen.queryByTestId("goal-popover")).toBeNull());
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
