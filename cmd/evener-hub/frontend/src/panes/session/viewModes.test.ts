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
    expect(SESSION_VIEW_MODES.map((mode) => mode.label)).toEqual(["Everything", "Intent"]);
    expect(SESSION_VIEW_MODES.map((mode) => mode.value)).toEqual(["everything", "intent"]);
  });

  test("collapses contiguous intents into a labeled action group", () => {
    expect(focusedEntries(fixtures)).toEqual([
      expect.objectContaining({ kind: "message", role: "user", id: "user-1" }),
      expect.objectContaining({
        kind: "action-group",
        count: 2,
        label: "2 actions",
        id: "actions:tool-1:tool-2",
        intents: [
          { id: "intent:tool-1", rationale: "Inspect the first input" },
          { id: "intent:tool-2", rationale: "Find the relevant references" },
        ],
      }),
      expect.objectContaining({ kind: "message", role: "agent", id: "agent-1" }),
    ]);
  });

  test("coalesces intents across adjacent turns", () => {
    const adjacent = [
      turn("turn-a", [item({ id: "tool-a", turnId: "turn-a", type: "commandExecution", description: "One" })]),
      turn("turn-b", [item({ id: "tool-b", turnId: "turn-b", type: "commandExecution", description: "Two" })]),
      turn("turn-c", [
        item({ id: "tool-c", turnId: "turn-c", type: "commandExecution", description: "Three" }),
        item({ id: "agent-c", turnId: "turn-c", type: "agentMessage", text: "done" }),
      ]),
    ];

    expect(focusedEntries(adjacent)).toEqual([
      expect.objectContaining({
        kind: "action-group",
        turnId: "turn-a",
        count: 3,
        label: "3 actions",
        id: "actions:tool-a:tool-c",
      }),
      expect.objectContaining({ kind: "message", role: "agent", id: "agent-c" }),
    ]);
  });

  test("a visible message splits action groups", () => {
    const split = [
      turn("turn-2", [item({ id: "tool-a", turnId: "turn-2", type: "commandExecution", description: "Solo" })]),
      turn("turn-3", [
        item({ id: "agent-a", turnId: "turn-3", type: "agentMessage", text: "between" }),
        item({ id: "tool-b", turnId: "turn-3", type: "commandExecution", description: "After" }),
      ]),
      turn("turn-4", [item({ id: "tool-c", turnId: "turn-4", type: "commandExecution", description: "Next" })]),
      turn("turn-5", [item({ id: "tool-d", turnId: "turn-5", type: "commandExecution", description: "Last" })]),
    ];

    expect(focusedEntries(split)).toEqual([
      expect.objectContaining({ kind: "action-group", count: 1, label: "1 action" }),
      expect.objectContaining({ kind: "message", role: "agent" }),
      expect.objectContaining({ kind: "action-group", count: 3, label: "3 actions" }),
    ]);
  });

  test("focused entries expose no raw tool fields", () => {
    for (const entry of focusedEntries(fixtures)) {
      expect(entry).not.toHaveProperty("toolName");
      expect(entry).not.toHaveProperty("argumentsJSON");
      expect(entry).not.toHaveProperty("output");
      expect(entry).not.toHaveProperty("tool");
    }
  });

  test("omits missing or blank rationales and drops empty groups", () => {
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

    expect(focusedEntries(sparse).map((entry) => entry.kind)).toEqual(["message", "message"]);
  });

  test("returns stable IDs without mutating source turns", () => {
    const before = structuredClone(fixtures);
    const first = focusedEntries(fixtures).map((entry) => entry.id);
    const second = focusedEntries(fixtures).map((entry) => entry.id);

    expect(second).toEqual(first);
    expect(fixtures).toEqual(before);
  });
});
