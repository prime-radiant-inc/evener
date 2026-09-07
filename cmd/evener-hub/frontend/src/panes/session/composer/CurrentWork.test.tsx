import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { CurrentWork } from "./CurrentWork";

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
    label: "Task: Inspect the current working directory. Goal: Keep the session focused",
    hasTask: true,
    hasGoal: true,
    hasDivider: true,
  },
  {
    name: "task only",
    task: "Inspect the current working directory",
    goal: undefined,
    label: "Task: Inspect the current working directory",
    hasTask: true,
    hasGoal: false,
    hasDivider: false,
  },
  {
    name: "goal only",
    task: undefined,
    goal: "Keep the session focused",
    label: "Goal: Keep the session focused",
    hasTask: false,
    hasGoal: true,
    hasDivider: false,
  },
  {
    name: "empty",
    task: "   ",
    goal: "\n\t",
    label: "",
    hasTask: false,
    hasGoal: false,
    hasDivider: false,
  },
] as const;

afterEach(cleanup);

test.each(states)("renders the $name state", ({ task, goal, label, hasTask, hasGoal, hasDivider }) => {
  render(<CurrentWork task={task} goal={goal} onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />);

  const status = screen.getByRole("status");
  expect(status.getAttribute("aria-atomic")).toBe("true");
  expect(status.textContent).toBe(label);
  expect(screen.queryByTestId("current-work") !== null).toBe(hasTask || hasGoal);
  expect(screen.queryByTestId("current-work-task") !== null).toBe(hasTask);
  expect(screen.queryByTestId("current-work-goal") !== null).toBe(hasGoal);
  expect(screen.queryByTestId("current-work-divider") !== null).toBe(hasDivider);
  if (hasTask) expect(screen.getByText("Task")).toBeTruthy();
  if (hasGoal) expect(screen.getByText("Goal")).toBeTruthy();
});

test("keeps one text-content live region while task and goal change", () => {
  const view = render(
    <CurrentWork task="Inspect the diff" goal="Keep focus" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />,
  );
  const status = screen.getByRole("status");
  expect(status.textContent).toBe("Task: Inspect the diff. Goal: Keep focus");

  view.rerender(<CurrentWork task="Ship it" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />);
  expect(screen.getByRole("status")).toBe(status);
  expect(status.textContent).toBe("Task: Ship it");

  view.rerender(<CurrentWork task=" " goal={"\n"} onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />);
  expect(screen.getByRole("status")).toBe(status);
  expect(status.textContent).toBe("");
  expect(screen.queryByTestId("current-work")).toBeNull();

  view.rerender(<CurrentWork goal="Finish safely" onOpenTasks={vi.fn()} onEditGoal={vi.fn()} />);
  expect(screen.getByRole("status")).toBe(status);
  expect(status.textContent).toBe("Goal: Finish safely");
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
