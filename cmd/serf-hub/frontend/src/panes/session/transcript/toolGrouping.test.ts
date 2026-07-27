// @vitest-environment jsdom
import { expect, test } from "vitest";
import type { ItemModel } from "../../../protocol/model";
import { shouldGroup, toolRunFor } from "./toolGrouping";
import "./tools";

function item(id: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return {
    id,
    turnId: "turn_1",
    type: "commandExecution",
    text: "",
    toolName: "read_file",
    status: "completed",
    ...overrides,
  };
}

function suppressedTaskView(id: string): ItemModel {
  return item(id, { toolName: "task_list", argumentsJSON: '{"action":"view"}' });
}

test("an item not present or a non-tool item resolves to no run", () => {
  const items = [item("a"), item("prose", { type: "agentMessage" })];
  expect(toolRunFor(items, "missing")).toBeUndefined();
  expect(toolRunFor(items, "prose")).toBeUndefined();
});

test("adjacent eligible calls form one fresh run, and the first member owns it", () => {
  const items = [item("a"), item("b"), item("c"), item("answer", { type: "userMessage" })];
  expect(toolRunFor(items, "a")?.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
  expect(toolRunFor(items, "b")?.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
  expect(toolRunFor(items, "c")?.isFirst).toBe(false);
  expect(toolRunFor(items, "a")?.isLastActivity).toBe(false);
});

test("a non-tool item breaks adjacency and a run derives the current list each time", () => {
  const items = [item("a"), item("b"), item("prose", { type: "agentMessage" }), item("c"), item("d")];
  expect(toolRunFor(items, "a")?.items.map((i) => i.id)).toEqual(["a", "b"]);
  expect(toolRunFor(items, "c")?.items.map((i) => i.id)).toEqual(["c", "d"]);

  const extended = [...items, item("e")];
  expect(toolRunFor(extended, "c")?.items.map((i) => i.id)).toEqual(["c", "d", "e"]);
});

test("runs of one or two calls do not group, while exactly three do when followed by activity", () => {
  const one = [item("a"), item("reply", { type: "userMessage" })];
  const two = [item("a"), item("b"), item("reply", { type: "userMessage" })];
  const three = [item("a"), item("b"), item("c"), item("reply", { type: "userMessage" })];
  expect(shouldGroup(toolRunFor(one, "a")!)).toBe(false);
  expect(shouldGroup(toolRunFor(two, "a")!)).toBe(false);
  expect(shouldGroup(toolRunFor(three, "a")!)).toBe(true);
});

test("a run that is the turn's last activity never collapses", () => {
  const items = [item("a"), item("b"), item("c")];
  const run = toolRunFor(items, "b");
  expect(run?.isLastActivity).toBe(true);
  expect(shouldGroup(run!)).toBe(false);
});

test("a running call prevents its run from collapsing", () => {
  const items = [item("a"), item("b", { status: "inProgress" }), item("c"), item("reply", { type: "userMessage" })];
  const run = toolRunFor(items, "a");
  expect(run?.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
  expect(shouldGroup(run!)).toBe(false);
});

test("a generic failure stays standalone and breaks adjacent eligible calls", () => {
  const items = [
    item("a"),
    item("b", { status: "failed" }),
    item("c"),
    item("d"),
    item("reply", { type: "userMessage" }),
  ];
  expect(toolRunFor(items, "b")?.items.map((i) => i.id)).toEqual(["b"]);
  expect(shouldGroup(toolRunFor(items, "b")!)).toBe(false);
  expect(toolRunFor(items, "a")?.items.map((i) => i.id)).toEqual(["a"]);
  expect(toolRunFor(items, "c")?.items.map((i) => i.id)).toEqual(["c", "d"]);
});

test("a descriptor-specific shell failure is also a standalone boundary", () => {
  const items = [
    item("a"),
    item("failed-shell", { toolName: "shell", exitCode: 1 }),
    item("c"),
    item("d"),
    item("reply", { type: "userMessage" }),
  ];
  expect(toolRunFor(items, "failed-shell")?.items.map((i) => i.id)).toEqual(["failed-shell"]);
  expect(shouldGroup(toolRunFor(items, "failed-shell")!)).toBe(false);
  expect(toolRunFor(items, "c")?.items.map((i) => i.id)).toEqual(["c", "d"]);
});

test("ask_user is a conversational boundary, not a failure classification", () => {
  const items = [
    item("before-a"),
    item("before-b"),
    item("ask", { toolName: "ask_user" }),
    item("after-a"),
    item("after-b"),
    item("reply", { type: "userMessage" }),
  ];
  expect(toolRunFor(items, "before-a")?.items.map((i) => i.id)).toEqual(["before-a", "before-b"]);
  expect(toolRunFor(items, "ask")).toBeUndefined();
  expect(toolRunFor(items, "after-a")?.items.map((i) => i.id)).toEqual(["after-a", "after-b"]);
});

test("suppressed task_list views do not belong to an eligible run", () => {
  const items = [suppressedTaskView("view-a"), suppressedTaskView("view-b"), suppressedTaskView("view-c")];

  expect(toolRunFor(items, "view-a")).toBeUndefined();
  expect(toolRunFor(items, "view-b")).toBeUndefined();
  expect(toolRunFor(items, "view-c")).toBeUndefined();
});

test("suppressed calls are boundaries and cannot inflate a visible run", () => {
  const items = [
    item("before-a"),
    item("before-b"),
    suppressedTaskView("view"),
    item("after-a"),
    item("after-b"),
    item("reply", { type: "agentMessage" }),
  ];

  const before = toolRunFor(items, "before-a");
  const after = toolRunFor(items, "after-a");
  expect(before?.items.map((i) => i.id)).toEqual(["before-a", "before-b"]);
  expect(after?.items.map((i) => i.id)).toEqual(["after-a", "after-b"]);
  expect(shouldGroup(before!)).toBe(false);
  expect(shouldGroup(after!)).toBe(false);
});
