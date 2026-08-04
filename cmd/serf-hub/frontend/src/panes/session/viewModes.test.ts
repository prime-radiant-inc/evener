import { describe, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../protocol/model";
import { focusedEntries, SESSION_VIEW_MODES } from "./viewModes";

function item(overrides: Partial<ItemModel> & Pick<ItemModel, "id" | "type">): ItemModel {
  return {
    turnId: "turn-1",
    text: "",
    ...overrides,
  };
}

function turn(id: string, items: ItemModel[]): TurnModel {
  return { id, status: "completed", items };
}

const fixtures: readonly TurnModel[] = [
  turn("turn-1", [
    item({ id: "user-1", type: "userMessage", text: "Please inspect this" }),
    item({
      id: "tool-1",
      type: "commandExecution",
      toolName: "read_file",
      argumentsJSON: '{"path":"one"}',
      output: "raw result one",
      description: "Inspect the first input",
    }),
    item({
      id: "tool-2",
      type: "commandExecution",
      toolName: "grep_files",
      argumentsJSON: '{"pattern":"needle"}',
      output: "raw result two",
      description: "Find the relevant references",
    }),
    item({ id: "agent-1", type: "agentMessage", text: "Here is what I found" }),
  ]),
];

describe("session view modes", () => {
  test("exposes the approved modes in order", () => {
    expect(SESSION_VIEW_MODES.map((mode) => mode.label)).toEqual(["Everything", "Conversation", "Intent"]);
    expect(SESSION_VIEW_MODES.map((mode) => mode.value)).toEqual(["everything", "conversation", "intent"]);
  });

  test("conversation preserves messages and collapses contiguous tool calls", () => {
    expect(focusedEntries(fixtures, "conversation")).toEqual([
      expect.objectContaining({ kind: "message", role: "user", id: "user-1" }),
      expect.objectContaining({
        kind: "tool-count",
        count: 2,
        label: "2 tool calls",
        id: "tools:tool-1:tool-2",
      }),
      expect.objectContaining({ kind: "message", role: "agent", id: "agent-1" }),
    ]);
  });

  test("a visible message splits tool-call groups", () => {
    const split = [
      turn("turn-2", [
        item({ id: "tool-a", turnId: "turn-2", type: "commandExecution" }),
        item({ id: "agent-a", turnId: "turn-2", type: "agentMessage", text: "between" }),
        item({ id: "tool-b", turnId: "turn-2", type: "commandExecution" }),
        item({ id: "tool-c", turnId: "turn-2", type: "commandExecution" }),
        item({ id: "tool-d", turnId: "turn-2", type: "commandExecution" }),
      ]),
    ];

    expect(focusedEntries(split, "conversation")).toEqual([
      expect.objectContaining({ kind: "tool-count", count: 1, label: "1 tool call" }),
      expect.objectContaining({ kind: "message", role: "agent" }),
      expect.objectContaining({ kind: "tool-count", count: 3, label: "3 tool calls" }),
    ]);
  });

  test("intent replaces tools with non-empty rationales in source order", () => {
    const entries = focusedEntries(fixtures, "intent");

    expect(entries.map((entry) => entry.kind)).toEqual(["message", "intent", "intent", "message"]);
    expect(entries).toEqual([
      expect.objectContaining({ kind: "message", role: "user", id: "user-1" }),
      expect.objectContaining({
        kind: "intent",
        id: "intent:tool-1",
        rationale: "Inspect the first input",
      }),
      expect.objectContaining({
        kind: "intent",
        id: "intent:tool-2",
        rationale: "Find the relevant references",
      }),
      expect.objectContaining({ kind: "message", role: "agent", id: "agent-1" }),
    ]);
  });

  test("focused entries expose no raw tool fields", () => {
    for (const mode of ["conversation", "intent"] as const) {
      for (const entry of focusedEntries(fixtures, mode)) {
        expect(entry).not.toHaveProperty("toolName");
        expect(entry).not.toHaveProperty("argumentsJSON");
        expect(entry).not.toHaveProperty("output");
        expect(entry).not.toHaveProperty("tool");
      }
    }
  });

  test("omits missing or blank rationales and tolerates missing tool metadata", () => {
    const sparse = [
      turn("turn-3", [
        item({ id: "user-3", turnId: "turn-3", type: "userMessage", text: "Keep me" }),
        item({ id: "tool-no-metadata", turnId: "turn-3", type: "commandExecution" }),
        item({
          id: "tool-blank-rationale",
          turnId: "turn-3",
          type: "commandExecution",
          description: "  ",
        }),
        item({ id: "agent-3", turnId: "turn-3", type: "agentMessage", text: "And me" }),
      ]),
    ];

    expect(focusedEntries(sparse, "conversation")).toEqual([
      expect.objectContaining({ kind: "message", role: "user" }),
      expect.objectContaining({ kind: "tool-count", count: 2 }),
      expect.objectContaining({ kind: "message", role: "agent" }),
    ]);
    expect(focusedEntries(sparse, "intent").map((entry) => entry.kind)).toEqual(["message", "message"]);
  });

  test("returns stable IDs without mutating source turns", () => {
    const before = structuredClone(fixtures);
    const first = focusedEntries(fixtures, "conversation").map((entry) => entry.id);
    const second = focusedEntries(fixtures, "conversation").map((entry) => entry.id);
    const intentFirst = focusedEntries(fixtures, "intent").map((entry) => entry.id);
    const intentSecond = focusedEntries(fixtures, "intent").map((entry) => entry.id);

    expect(second).toEqual(first);
    expect(intentSecond).toEqual(intentFirst);
    expect(fixtures).toEqual(before);
  });
});
