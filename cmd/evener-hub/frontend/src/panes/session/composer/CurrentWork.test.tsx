import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { CurrentWork } from "./CurrentWork";
import styles from "./currentwork.module.css";

function currentWorkCssRule(selector: string): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "currentwork.module.css");
  // Source assertions must not pass by matching prose in a CSS comment.
  const css = readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = new RegExp(`\\.${selector}\\s*\\{([^}]*)\\}`).exec(css)?.[1];
  if (!rule) throw new Error(`currentwork.module.css declares no .${selector} rule`);
  return rule;
}

const states = [
  {
    name: "task plus goal",
    task: "Inspect the current working directory",
    goal: "Keep the session focused",
    hasTask: true,
    hasGoal: true,
    hasDivider: true,
  },
  {
    name: "task only",
    task: "Inspect the current working directory",
    goal: undefined,
    hasTask: true,
    hasGoal: false,
    hasDivider: false,
  },
  {
    name: "goal only",
    task: undefined,
    goal: "Keep the session focused",
    hasTask: false,
    hasGoal: true,
    hasDivider: false,
  },
  {
    name: "empty",
    task: "   ",
    goal: "\n\t",
    hasTask: false,
    hasGoal: false,
    hasDivider: false,
  },
] as const;

// The live region's text, checked for what it announces rather than how it
// words it: each present value, in task-then-goal order, and nothing at all
// when neither is present.
function expectAnnounces(status: HTMLElement, values: string[]): void {
  const text = status.textContent ?? "";
  if (values.length === 0) {
    expect(text).toBe("");
    return;
  }
  const positions = values.map((value) => text.indexOf(value));
  for (const position of positions) expect(position).toBeGreaterThanOrEqual(0);
  expect(positions).toEqual([...positions].sort((a, b) => a - b));
}

function goalStatusWord(): HTMLElement {
  return screen.getByTestId("current-work-goal-status");
}

test.each(states)("renders the $name state", ({ task, goal, hasTask, hasGoal, hasDivider }) => {
  render(<CurrentWork task={task} goal={goal} onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />);

  const status = screen.getByRole("status");
  expect(status.getAttribute("aria-atomic")).toBe("true");
  expectAnnounces(status, [...(hasTask && task ? [task] : []), ...(hasGoal && goal ? [goal] : [])]);
  expect(screen.queryByTestId("current-work") !== null).toBe(hasTask || hasGoal);
  expect(screen.queryByTestId("current-work-task") !== null).toBe(hasTask);
  expect(screen.queryByTestId("current-work-goal") !== null).toBe(hasGoal);
  expect(screen.queryByTestId("current-work-divider") !== null).toBe(hasDivider);
});

test("keeps one text-content live region while task and goal change", () => {
  const view = render(
    <CurrentWork task="Inspect the diff" goal="Keep focus" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />,
  );
  const status = screen.getByRole("status");
  expectAnnounces(status, ["Inspect the diff", "Keep focus"]);

  view.rerender(<CurrentWork task="Ship it" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />);
  expect(screen.getByRole("status")).toBe(status);
  expectAnnounces(status, ["Ship it"]);
  expect(status.textContent).not.toContain("Keep focus");

  view.rerender(<CurrentWork task=" " goal={"\n"} onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />);
  expect(screen.getByRole("status")).toBe(status);
  expectAnnounces(status, []);
  expect(screen.queryByTestId("current-work")).toBeNull();

  view.rerender(<CurrentWork goal="Finish safely" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />);
  expect(screen.getByRole("status")).toBe(status);
  expectAnnounces(status, ["Finish safely"]);
  expect(status.textContent).not.toContain("Ship it");
});

test("renders the goal's status beside the objective, in the row and the live region", () => {
  render(
    <CurrentWork goal="Keep the session focused" goalStatus="active" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />,
  );

  const word = goalStatusWord();
  expect(word.dataset.state).toBe("active");
  expect(word.textContent).not.toBe("");
  expectAnnounces(screen.getByRole("status"), ["Keep the session focused", word.textContent ?? ""]);
});

test("renders a blocked goal's status word in the attention ink", () => {
  render(
    <CurrentWork goal="Keep the session focused" goalStatus="blocked" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />,
  );

  const word = goalStatusWord();
  expect(word.dataset.state).toBe("blocked");
  expect(word.textContent).not.toBe("");
  expect(word.className).toContain(styles.statusBlocked);
  expectAnnounces(screen.getByRole("status"), ["Keep the session focused", word.textContent ?? ""]);
});

test("renders an unknown status as written", () => {
  render(
    <CurrentWork goal="Keep the session focused" goalStatus="in review" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />,
  );

  expect(goalStatusWord().textContent).toBe("in review");
  expectAnnounces(screen.getByRole("status"), ["Keep the session focused", "in review"]);
});

test("renders prototype-named statuses as written, not inherited Object.prototype values", () => {
  for (const goalStatus of ["__proto__", "constructor"] as const) {
    const { unmount } = render(
      <CurrentWork
        goal="Keep the session focused"
        goalStatus={goalStatus}
        onOpenTasks={vi.fn()}
        onEditGoal={vi.fn()}
      />,
    );

    expect(goalStatusWord().textContent).toBe(goalStatus);
    expectAnnounces(screen.getByRole("status"), ["Keep the session focused", goalStatus]);
    unmount();
  }
});

test.each([
  { name: "none", goalStatus: undefined },
  { name: "whitespace only", goalStatus: "  " },
])("renders no status word when the goal carries $name", ({ goalStatus }) => {
  render(
    <CurrentWork goal="Keep the session focused" goalStatus={goalStatus} onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />,
  );

  expect(screen.queryByTestId("current-work-goal-status")).toBeNull();
  // The goal is the last thing announced: no status follows it.
  expect(screen.getByRole("status").textContent?.endsWith("Keep the session focused")).toBe(true);
});

test("gives the status word the row's containment contract", () => {
  const status = currentWorkCssRule("status");
  // The wire status is a free string: a long one must ellipsize like the
  // objective instead of pushing the row past the pane, and it must render
  // as written rather than case-mangled by a text transform.
  expect(status).toContain("min-width: 0");
  expect(status).toContain("overflow: hidden");
  expect(status).toContain("text-overflow: ellipsis");
  expect(status).not.toContain("text-transform: uppercase");

  expect(currentWorkCssRule("statusBlocked")).toContain("color: var(--attention-ink)");
});

test("renders task and goal values as keyboard-accessible link actions with full-text titles", async () => {
  const user = userEvent.setup();
  const task = "Task ".repeat(80);
  const goal = "Goal ".repeat(80);
  const onOpenTasks = vi.fn();
  const onEditGoal = vi.fn();
  render(<CurrentWork task={task} goal={goal} onOpenTasks={onOpenTasks} onEditGoal={onEditGoal} />);

  const taskLink = screen.getByRole("button", { name: `Open tasks: ${task.trim()}` });
  const goalLink = screen.getByRole("button", { name: `Edit goal: ${goal.trim()}` });
  expect(taskLink.getAttribute("title")).toBe(task);
  expect(goalLink.getAttribute("title")).toBe(goal);

  await user.click(taskLink);
  await user.click(goalLink);
  expect(onOpenTasks).toHaveBeenCalledOnce();
  expect(onEditGoal).toHaveBeenCalledOnce();
});

test("left-justifies both mobile rows without indenting the goal", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "currentwork.module.css"), "utf8");
  const compactRules = /@container \(max-width: 559px\)\s*\{([\s\S]*)\}\s*$/.exec(css)?.[1] ?? "";
  expect(compactRules).not.toMatch(/\.goal\s*\{[^}]*padding-left/);
  expect(compactRules).toMatch(/\.task,\s*\.goal\s*\{[^}]*justify-content:\s*flex-start/);
});

test("uses the approved semantic green ring and uppercase micro-label treatment", () => {
  const dot = currentWorkCssRule("dot");
  expect(dot).toContain("border: 1px solid var(--alive)");
  expect(dot).toContain("background: var(--alive-bg)");
  expect(dot).not.toContain("var(--accent)");

  const label = currentWorkCssRule("label");
  expect(label).toContain("text-transform: uppercase");
  expect(label).toContain("letter-spacing: var(--tracking-eyebrow)");
});
