import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { CurrentWork } from "./CurrentWork";

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
