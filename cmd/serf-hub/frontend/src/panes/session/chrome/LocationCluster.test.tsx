import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { LocationCluster } from "./LocationCluster";

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
    cwd: "/home/jesse/project/worktrees/feature",
    ...overrides,
  };
}

afterEach(() => cleanup());

test("renders branch, project, and cwd - each keyed with its value - when all are present", () => {
  render(
    <LocationCluster
      model={testModel({ gitBranch: "feature/x", projectPath: "/home/jesse/project", cwd: "/home/jesse/project/wt" })}
    />,
  );
  expect(screen.getByText("branch")).toBeTruthy();
  expect(screen.getByText("project")).toBeTruthy();
  expect(screen.getByText("cwd")).toBeTruthy();
  expect(screen.getByText("feature/x")).toBeTruthy();
  expect(screen.getByText("/home/jesse/project")).toBeTruthy();
  expect(screen.getByText("/home/jesse/project/wt")).toBeTruthy();
});

test("renders the parts in branch -> project -> cwd order", () => {
  render(<LocationCluster model={testModel({ gitBranch: "main", projectPath: "/proj", cwd: "/proj/wt" })} />);
  const keys = screen.getAllByTestId("location-key").map((el) => el.textContent);
  expect(keys).toEqual(["branch", "project", "cwd"]);
});

test("each part carries a 'key value' title tooltip for the full value", () => {
  render(<LocationCluster model={testModel({ gitBranch: "main", cwd: "/very/long/path" })} />);
  expect(screen.getByTitle("branch main")).toBeTruthy();
  expect(screen.getByTitle("cwd /very/long/path")).toBeTruthy();
});

// Honest absence: a field the wire didn't provide is OMITTED, never shown as a
// placeholder dash (mirrors the legacy {{if .Branch}} guards,
// input_strip.html:7-9).
test("omits the branch part when gitBranch is absent - no placeholder", () => {
  render(<LocationCluster model={testModel({ gitBranch: undefined, projectPath: "/proj", cwd: "/proj/wt" })} />);
  expect(screen.queryByText("branch")).toBeNull();
  expect(screen.getByText("project")).toBeTruthy();
  expect(screen.getByText("cwd")).toBeTruthy();
});

test("omits the project part when projectPath is absent (pathless / unresolved project)", () => {
  render(<LocationCluster model={testModel({ gitBranch: "main", projectPath: undefined, cwd: "/wt" })} />);
  expect(screen.queryByText("project")).toBeNull();
  expect(screen.getByText("branch")).toBeTruthy();
  expect(screen.getByText("cwd")).toBeTruthy();
});

test("omits the cwd part when cwd is empty (pathless external thread)", () => {
  render(<LocationCluster model={testModel({ gitBranch: "main", projectPath: undefined, cwd: "" })} />);
  expect(screen.queryByText("cwd")).toBeNull();
  expect(screen.getByText("branch")).toBeTruthy();
});

test("renders nothing at all when no location fact is known", () => {
  const { container } = render(
    <LocationCluster model={testModel({ gitBranch: undefined, projectPath: undefined, cwd: "" })} />,
  );
  expect(container.innerHTML).toBe("");
});
