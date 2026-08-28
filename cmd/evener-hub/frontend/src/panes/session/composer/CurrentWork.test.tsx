import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
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
    label: "Working on: Inspect the current working directory. Goal: Keep the session focused",
    hasTask: true,
    hasGoal: true,
    hasDivider: true,
  },
  {
    name: "task only",
    task: "Inspect the current working directory",
    goal: undefined,
    label: "Working on: Inspect the current working directory",
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
    label: undefined,
    hasTask: false,
    hasGoal: false,
    hasDivider: false,
  },
] as const;

afterEach(cleanup);

test.each(states)("renders the $name state", ({ task, goal, label, hasTask, hasGoal, hasDivider }) => {
  render(<CurrentWork task={task} goal={goal} />);

  if (!label) {
    expect(screen.queryByTestId("current-work")).toBeNull();
    return;
  }

  expect(screen.getByRole("status", { name: label }).getAttribute("aria-atomic")).toBe("true");
  expect(screen.queryByTestId("current-work-task") !== null).toBe(hasTask);
  expect(screen.queryByTestId("current-work-goal") !== null).toBe(hasGoal);
  expect(screen.queryByTestId("current-work-divider") !== null).toBe(hasDivider);
  if (hasTask) expect(screen.getByText("Working on")).toBeTruthy();
  if (hasGoal) expect(screen.getByText("Goal")).toBeTruthy();
});

test("keeps long task and goal values in title attributes", () => {
  const task = "Task ".repeat(80);
  const goal = "Goal ".repeat(80);
  render(<CurrentWork task={task} goal={goal} />);

  expect(screen.getByTestId("current-work-task-value").getAttribute("title")).toBe(task);
  expect(screen.getByTestId("current-work-goal-value").getAttribute("title")).toBe(goal);
});

test("uses the approved semantic green ring and uppercase micro-label treatment", () => {
  const dot = currentWorkCssRule("dot");
  expect(dot).toContain("border: 1px solid var(--alive)");
  expect(dot).toContain("background: var(--alive-bg)");
  expect(dot).not.toContain("var(--accent)");

  const label = currentWorkCssRule("label");
  expect(label).toContain("text-transform: uppercase");
  expect(label).toContain("letter-spacing: var(--tracking-micro)");
});
