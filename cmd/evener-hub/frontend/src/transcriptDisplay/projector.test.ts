import { describe, expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../protocol/model";
import { makeTranscriptDisplayConfig, presetContent, type TranscriptDisplayConfigV1 } from "./config";
import { projectThread } from "./projector";

const BASE_THREAD = {
  ref: "ref:test",
  threadId: "thread:test",
  name: "fixture",
  status: { type: "idle" },
  modelProvider: "test",
  model: "test-model",
  askPending: false,
  pendingEscalations: [],
  activeTurnId: undefined,
  queue: null,
  tasks: null,
  jobsUpdatedAt: null,
  jobsTreeRevision: null,
  lastFrameAt: 0,
  capabilities: {},
  goal: null,
  contextUsed: 0,
  contextWindow: 100,
  contextPressure: 0,
  usage: null,
  workMillis: 0,
} as unknown as Omit<ThreadModel, "turns">;

function item(id: string, type: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "turn-1", type, text: "", status: "completed", ...overrides };
}

function turn(items: ItemModel[], overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn-1", status: "completed", items, ...overrides };
}

function threadWith(...items: ItemModel[]): ThreadModel {
  return { ...BASE_THREAD, turns: [turn(items)] } as unknown as ThreadModel;
}

function preset(level: "chat" | "intent" | "tools" | "activity" | "full", advanced = {}) {
  return makeTranscriptDisplayConfig({ kind: "preset", level }, advanced);
}

function custom(content: { toolIntent: boolean; toolCalls: boolean; reasoning: boolean; expandByDefault: boolean }) {
  return makeTranscriptDisplayConfig({ kind: "custom", ...content });
}

function entriesFor(model: ThreadModel, config: TranscriptDisplayConfigV1) {
  return projectThread(model, config).turns[0]?.entries ?? [];
}

describe("transcript projector", () => {
  test("uses an intent proxy until the real tool row supersedes it", () => {
    const model = threadWith(item("tool-1", "commandExecution", { description: "Inspect the tree" }));

    expect(entriesFor(model, preset("chat"))).toEqual([]);
    expect(entriesFor(model, preset("intent"))).toEqual([
      expect.objectContaining({ kind: "intent", id: "intent:tool-1", rationale: "Inspect the tree" }),
    ]);
    expect(entriesFor(model, preset("tools"))).toEqual([expect.objectContaining({ kind: "item", id: "tool-1" })]);
  });

  test.each([
    ["chat", ["user", "agent"]],
    ["intent", ["user", "intent:tool", "agent"]],
    ["tools", ["user", "tool", "agent"]],
    ["activity", ["user", "tool", "think", "agent"]],
    ["full", ["user", "tool", "think", "agent"]],
  ] as const)("projects the cumulative %s content vector", (level, expectedIds) => {
    const model = threadWith(
      item("user", "userMessage"),
      item("tool", "commandExecution", { description: "Inspect" }),
      item("think", "reasoning"),
      item("agent", "agentMessage"),
    );
    expect(entriesFor(model, preset(level)).map((entry) => entry.id)).toEqual(expectedIds);
  });

  test("preserves conversation, all current transcript item types, and unknown items in source order", () => {
    const model = threadWith(
      item("user", "userMessage", { text: "Question" }),
      item("agent", "agentMessage", { text: "Answer" }),
      item("tool", "commandExecution", { description: "Read a file", toolName: "read_file" }),
      item("think", "reasoning", { text: "Reasoning" }),
      item("system", "systemMessage", { eventKind: "unknown_event" }),
      item("steer", "steering", { text: "Steer" }),
      item("warning", "warning", { text: "Warning" }),
      item("future", "futureItem", { text: "Future" }),
    );

    const entries = entriesFor(model, preset("full"));
    expect(entries.map((entry) => entry.id)).toEqual([
      "user",
      "agent",
      "tool",
      "think",
      "system",
      "steer",
      "warning",
      "future",
    ]);
    expect(entries[0]).toMatchObject({ kind: "item", isMessage: true });
    expect(entries[1]).toMatchObject({ kind: "item", isMessage: true });
    expect(entries[2]).toMatchObject({ kind: "item", isMessage: false });
    expect(entries[3]).toMatchObject({ kind: "item", isMessage: false });
    expect(entries[4]).toMatchObject({ kind: "item", isMessage: false });
    expect(entries[5]).toMatchObject({ kind: "critical", sourceItemId: "steer" });
    expect(entries[6]).toMatchObject({ kind: "critical", sourceItemId: "warning" });
    expect(entries[7]).toMatchObject({ kind: "item", isMessage: false });
  });

  test.each(["chat", "intent", "tools", "activity", "full"] as const)(
    "keeps critical interaction and failure rows at the %s level",
    (level) => {
      const model = threadWith(
        item("ask", "commandExecution", { toolName: "ask_user", description: "Ask for a choice" }),
        item("failed-tool", "commandExecution", { toolName: "shell", description: "Run checks", error: "denied" }),
        item("active-tool", "commandExecution", {
          toolName: "shell",
          description: "Run checks",
          status: "inProgress",
        }),
        item("hook-failure", "systemMessage", {
          eventKind: "hook_completed",
          description: "Hook",
          exitCode: 7,
        }),
        item("warning", "warning", { text: "Warning" }),
        item("steer", "steering", { text: "Steer" }),
        item("turn-error", "systemMessage", { eventKind: "error", text: "Failure" }),
      );
      const entries = entriesFor(model, preset(level));
      expect(entries.map((entry) => entry.id)).toEqual([
        "ask",
        "failed-tool",
        "active-tool",
        "hook-failure",
        "warning",
        "steer",
        "turn-error",
      ]);
      expect(entries.find((entry) => entry.id === "ask")?.kind).toBe("critical");
      expect(entries.find((entry) => entry.id === "failed-tool")?.kind).toBe(
        level === "chat" || level === "intent" ? "critical" : "item",
      );
      expect(entries.find((entry) => entry.id === "active-tool")?.kind).toBe(
        level === "chat" || level === "intent" ? "critical" : "item",
      );
      expect(entries.find((entry) => entry.id === "hook-failure")?.kind).toBe("critical");
      for (const id of ["warning", "steer", "turn-error"]) {
        expect(entries.find((entry) => entry.id === id)?.kind).toBe("critical");
      }
    },
  );

  test("uses the neutral action summary for a blank tool purpose without dropping the action", () => {
    const model = threadWith(item("blank-tool", "commandExecution", { toolName: "shell", description: "   " }));

    expect(entriesFor(model, preset("intent"))).toEqual([
      expect.objectContaining({
        kind: "intent",
        id: "intent:blank-tool",
        sourceItemId: "blank-tool",
        rationale: "Action summary unavailable",
      }),
    ]);
    expect(entriesFor(model, preset("chat"))).toEqual([
      expect.objectContaining({ kind: "critical", id: "blank-tool", summary: "Action summary unavailable" }),
    ]);
    for (const level of ["tools", "activity", "full"] as const) {
      expect(entriesFor(model, preset(level))).toEqual([
        expect.objectContaining({ kind: "critical", id: "blank-tool", summary: "Action summary unavailable" }),
      ]);
    }
  });

  test("filters routine system events by typed event kind and keeps unknown events fail-open", () => {
    const model = threadWith(
      item("system", "systemMessage", { eventKind: "plugin_loaded" }),
      item("prompt", "systemMessage", { eventKind: "prompt_loaded" }),
      item("timing", "systemMessage", { eventKind: "round_timings" }),
      item("clean-hook", "systemMessage", { eventKind: "hook_completed", exitCode: 0 }),
      item("failed-hook", "systemMessage", { eventKind: "hook_completed", exitCode: 1 }),
      item("unknown", "systemMessage", { eventKind: "future_event" }),
    );

    expect(entriesFor(model, preset("chat")).map((entry) => entry.id)).toEqual(["failed-hook", "unknown"]);
    expect(
      entriesFor(
        model,
        preset("chat", { systemEvents: true, promptEvents: true, roundTimings: true, hookExits: "successful" }),
      ).map((entry) => entry.id),
    ).toEqual(["system", "prompt", "timing", "clean-hook", "failed-hook", "unknown"]);
    expect(entriesFor(model, preset("chat", { hookExits: "all" })).map((entry) => entry.id)).toEqual([
      "clean-hook",
      "failed-hook",
      "unknown",
    ]);
  });

  test("governs the environment event with Advanced systemEvents", () => {
    const model = threadWith(item("environment", "systemMessage", { eventKind: "environment" }));

    expect(entriesFor(model, preset("chat"))).toEqual([]);
    expect(entriesFor(model, preset("chat", { systemEvents: true }))).toEqual([
      expect.objectContaining({ kind: "item", id: "environment" }),
    ]);
  });

  test("covers every current event kind with Advanced diagnostics enabled", () => {
    const eventKinds = [
      "system_prompt",
      "plugin_loaded",
      "skill_activated",
      "hook_completed",
      "prompt_loaded",
      "context_compaction",
      "compaction",
      "turn_limit",
      "loop_detection",
      "goal_ended",
      "fork_summary",
      "round_timings",
      "tool_repair",
      "model_switch",
      "error",
      "environment",
    ] as const;
    const model = threadWith(
      ...eventKinds.map((eventKind, index) =>
        item(`event-${index}`, "systemMessage", {
          eventKind,
          exitCode: eventKind === "hook_completed" ? 0 : undefined,
        }),
      ),
    );

    expect(
      entriesFor(
        model,
        preset("chat", { systemEvents: true, promptEvents: true, roundTimings: true, hookExits: "all" }),
      ).map((entry) => entry.id),
    ).toEqual(eventKinds.map((_, index) => `event-${index}`));
  });

  test("keeps approval vocabulary and recovery events critical without parsing prose", () => {
    const model = threadWith(
      item("approval", "commandExecution", { toolName: "approval", text: "ordinary text" }),
      item("escalation", "commandExecution", { toolName: "sandbox_escalation", text: "ordinary text" }),
      item("repair", "systemMessage", { eventKind: "tool_repair", text: "ordinary text" }),
    );

    expect(entriesFor(model, preset("full")).map((entry) => entry.kind)).toEqual(["critical", "critical", "critical"]);
  });

  test("uses turn and item status fields rather than message prose for active and failed work", () => {
    const model = {
      ...threadWith(
        item("active", "reasoning", { status: "inProgress", text: "not an active marker" }),
        item("failed", "futureItem", { status: "failed", text: "not a failure marker" }),
      ),
      turns: [
        turn(
          [
            item("active", "reasoning", { status: "inProgress", text: "not an active marker" }),
            item("failed", "futureItem", { status: "failed", text: "not a failure marker" }),
          ],
          { status: "interrupted" },
        ),
      ],
    } as ThreadModel;

    expect(entriesFor(model, preset("chat"))).toEqual([
      expect.objectContaining({ kind: "critical", id: "active" }),
      expect.objectContaining({ kind: "item", id: "failed" }),
    ]);

    const turnStillOpening = {
      ...threadWith(item("status-only-active", "commandExecution", { toolName: "shell" })),
      turns: [turn([item("status-only-active", "commandExecution", { toolName: "shell" })], { status: "inProgress" })],
    } as ThreadModel;
    expect(entriesFor(turnStillOpening, preset("chat"))).toEqual([
      expect.objectContaining({ kind: "critical", id: "status-only-active" }),
    ]);
  });

  test("keeps the typed failure marker for failed and interrupted turns", () => {
    const model = {
      ...threadWith(),
      turns: [
        turn([item("failed-marker", "systemMessage", { eventKind: "error", status: "failed" })], {
          id: "failed-turn",
          status: "failed",
        }),
        turn([item("interrupted-steer", "steering", { steeringKind: "interrupted" })], {
          id: "interrupted-turn",
          status: "interrupted",
        }),
      ],
    } as ThreadModel;

    expect(projectThread(model, preset("chat")).turns.map((projected) => projected.entries[0]?.kind)).toEqual([
      "critical",
      "critical",
    ]);
  });

  test("projects failed reasoning and routine work from terminal turns at low detail", () => {
    const terminalTurn = turn(
      [
        item("failed-command", "commandExecution", { description: "Routine command", status: "completed" }),
        item("failed-reasoning", "reasoning", { status: "interrupted" }),
      ],
      { status: "failed", error: { message: "structured turn failure" } },
    );
    const model = { ...threadWith(), turns: [terminalTurn] } as ThreadModel;

    const projection = projectThread(model, preset("chat"));
    expect(projection.turns[0]?.entries.map((entry) => entry.id)).toEqual(["failed-command", "failed-reasoning"]);
    expect(projection.turns[0]?.entries.every((entry) => entry.kind === "critical")).toBe(true);
    expect(projection.turns[0]?.source).toBe(terminalTurn);
    expect(projection.turns[0]?.source.status).toBe("failed");
    expect(projection.turns[0]?.source.error).toEqual({ message: "structured turn failure" });
  });

  test("keeps a terminal turn renderable when all ordinary rows are hidden", () => {
    const terminalTurn = turn([item("hidden-system", "systemMessage", { eventKind: "plugin_loaded" })], {
      status: "interrupted",
      error: { message: "structured interruption" },
    });
    const model = { ...threadWith(), turns: [terminalTurn] } as ThreadModel;

    const projection = projectThread(model, preset("chat"));
    expect(projection.turns[0]?.entries).toEqual([
      expect.objectContaining({ kind: "critical", id: "hidden-system", sourceItemId: "hidden-system" }),
    ]);
    expect(projection.turns[0]?.visibleItems).toEqual([terminalTurn.items[0]]);
    expect(projection.turns[0]?.source).toBe(terminalTurn);
  });

  test("returns metadata directly from Advanced and identifies eligible disclosures", () => {
    const config = preset("full", {
      roundTimings: true,
      tokenCounts: true,
      estimatedCost: true,
      systemEvents: true,
      promptEvents: true,
      hookExits: "all",
    });
    const model = threadWith(
      item("tool", "commandExecution", { description: "Read" }),
      item("think", "reasoning"),
      item("system", "systemMessage", { eventKind: "plugin_loaded" }),
      item("prompt", "systemMessage", { eventKind: "system_prompt" }),
      item("timing", "systemMessage", { eventKind: "round_timings", raw: { roundTimings: { round: 1 } } }),
      item("hook", "systemMessage", { eventKind: "hook_completed", exitCode: 0 }),
    );

    const projection = projectThread(model, config);
    expect(projection.metadata).toEqual(config.advanced);
    expect(projection.eligibleDisclosureIds).toEqual(["tool", "think", "system", "prompt", "timing", "hook"]);
    expect(projection.turns[0]?.entries.find((entry) => entry.id === "timing")).toMatchObject({
      kind: "item",
      item: { raw: { roundTimings: { round: 1 } } },
    });
  });

  test("filters before projection indexes and preserves anchors in source coordinates", () => {
    const model = threadWith(
      item("user", "userMessage"),
      item("hidden", "systemMessage", { eventKind: "plugin_loaded" }),
      item("tool", "commandExecution", { description: "Do it" }),
      item("agent", "agentMessage"),
    );
    const projection = projectThread(model, preset("intent"));

    expect(projection.turns[0]?.entries.map((entry) => [entry.id, entry.sourceIndex])).toEqual([
      ["user", 0],
      ["intent:tool", 2],
      ["agent", 3],
    ]);
    expect(projection.anchors).toEqual([
      { id: "user", sourceIndex: 0, index: 0, isMessage: true },
      { id: "intent:tool", sourceIndex: 2, index: 1, isMessage: false },
      { id: "agent", sourceIndex: 3, index: 2, isMessage: true },
    ]);
    expect(projection.turns[0]?.visibleItems.map((item) => item.id)).toEqual(["user", "tool", "agent"]);
  });

  test("provides grouping input after filtering hidden tools and system rows", () => {
    const model = threadWith(
      item("user", "userMessage"),
      item("hidden-tool", "commandExecution", { description: "Routine hidden action" }),
      item("hidden-system", "systemMessage", { eventKind: "plugin_loaded" }),
      item("warning", "warning", { text: "Attention" }),
      item("agent", "agentMessage"),
    );
    const projected = projectThread(model, preset("chat")).turns[0];

    expect(projected?.source.items.map((item) => item.id)).toEqual([
      "user",
      "hidden-tool",
      "hidden-system",
      "warning",
      "agent",
    ]);
    expect(projected?.visibleItems.map((item) => item.id)).toEqual(["user", "warning", "agent"]);
    expect(projected?.entries.map((entry) => entry.id)).toEqual(["user", "warning", "agent"]);
  });

  test("preserves source turns and does not mutate the input model or items", () => {
    const sourceItem = item("tool", "commandExecution", { description: "Inspect" });
    const sourceTurn = turn([sourceItem]);
    const model = { ...threadWith(), turns: [sourceTurn] } as ThreadModel;
    const before = structuredClone(model);

    const projection = projectThread(model, preset("intent"));

    expect(projection.turns[0]?.source).toBe(sourceTurn);
    expect(projection.turns[0]?.visibleItems).toEqual([sourceItem]);
    expect(model).toEqual(before);
    expect(sourceTurn.items[0]).toBe(sourceItem);
  });

  test("supports representative Custom vectors without selecting a false preset", () => {
    const model = threadWith(item("tool", "commandExecution", { description: "Inspect" }), item("think", "reasoning"));

    const toolsWithoutIntent = projectThread(
      model,
      custom({ toolIntent: false, toolCalls: true, reasoning: false, expandByDefault: true }),
    );
    expect(toolsWithoutIntent.turns[0]?.entries.map((entry) => entry.id)).toEqual(["tool"]);
    expect(toolsWithoutIntent.turns[0]?.entries[0]).toMatchObject({ kind: "item", id: "tool" });

    const reasoningWithoutTools = projectThread(
      model,
      custom({ toolIntent: true, toolCalls: false, reasoning: true, expandByDefault: false }),
    );
    expect(reasoningWithoutTools.turns[0]?.entries.map((entry) => entry.id)).toEqual(["intent:tool", "think"]);
  });

  test("projects an equivalent Custom vector like its named preset without changing its kind", () => {
    const model = threadWith(
      item("user", "userMessage"),
      item("tool", "commandExecution", { description: "Inspect" }),
      item("think", "reasoning"),
      item("agent", "agentMessage"),
    );
    const customConfig = custom(presetContent("tools"));
    const presetConfig = preset("tools");

    expect(customConfig.content.kind).toBe("custom");
    expect(entriesFor(model, customConfig)).toEqual(entriesFor(model, presetConfig));
  });
});
