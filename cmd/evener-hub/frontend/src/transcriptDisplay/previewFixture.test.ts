import { describe, expect, test } from "vitest";
import type { ItemModel } from "../protocol/model";
import { makeTranscriptDisplayConfig } from "./config";
import { makeTranscriptPreviewModel } from "./previewFixture";
import { projectThread } from "./projector";

const FIXED_TIMESTAMP = "2026-08-25T18:00:00.000Z";
const EXPECTED_CATEGORIES = [
  "userMessage",
  "commandExecution:success",
  "commandExecution:failure",
  "reasoning",
  "agentMessage",
  "systemMessage:environment",
  "systemMessage:system_prompt",
  "systemMessage:prompt_loaded",
  "systemMessage:hook_completed:success",
  "systemMessage:hook_completed:failure",
];

function items(): ItemModel[] {
  return makeTranscriptPreviewModel().turns.flatMap((turn) => turn.items);
}

function category(item: ItemModel): string {
  if (item.type === "commandExecution") return `commandExecution:${item.exitCode === 0 ? "success" : "failure"}`;
  if (item.type !== "systemMessage") return item.type;
  if (item.eventKind === "hook_completed") {
    return `systemMessage:hook_completed:${item.exitCode === 0 ? "success" : "failure"}`;
  }
  return `systemMessage:${item.eventKind}`;
}

describe("deterministic transcript preview fixture", () => {
  test("returns deterministic, deeply independent models", () => {
    const first = makeTranscriptPreviewModel();
    const second = makeTranscriptPreviewModel();

    expect(second).toEqual(first);
    expect(second).not.toBe(first);
    expect(second.turns).not.toBe(first.turns);
    expect(second.turns[0]?.items).not.toBe(first.turns[0]?.items);
    expect(second.turns[0]?.items[1]).not.toBe(first.turns[0]?.items[1]);

    const firstTool = first.turns[0]?.items[1];
    if (!firstTool) throw new Error("fixture is missing its successful tool");
    firstTool.argumentsJSON = "mutated";
    expect(second.turns[0]?.items[1]?.argumentsJSON).toBe('{"path":"src"}');
  });

  test("contains the exact structured scenario inventory", () => {
    expect(items().map(category)).toEqual(EXPECTED_CATEGORIES);

    const [
      user,
      successfulTool,
      failedTool,
      reasoning,
      agent,
      environment,
      systemPrompt,
      promptLoaded,
      hookOk,
      hookFailed,
    ] = items();
    expect(user).toMatchObject({ type: "userMessage", text: "Inspect the transcript display flow" });
    expect(successfulTool).toMatchObject({
      type: "commandExecution",
      description: "Inspect the transcript display flow",
      argumentsJSON: '{"path":"src"}',
      output: "src/transcriptDisplay",
      status: "completed",
      exitCode: 0,
    });
    expect(failedTool).toMatchObject({
      type: "commandExecution",
      description: "Run the focused checks",
      error: "exit status 1",
      status: "completed",
      exitCode: 1,
    });
    expect(reasoning).toMatchObject({ type: "reasoning", status: "completed" });
    expect(agent).toMatchObject({ type: "agentMessage", text: "The transcript display flow is ready." });
    expect(environment).toMatchObject({ type: "systemMessage", eventKind: "environment" });
    expect(systemPrompt).toMatchObject({ type: "systemMessage", eventKind: "system_prompt" });
    expect(promptLoaded).toMatchObject({ type: "systemMessage", eventKind: "prompt_loaded" });
    expect(hookOk).toMatchObject({ type: "systemMessage", eventKind: "hook_completed", exitCode: 0 });
    expect(hookFailed).toMatchObject({ type: "systemMessage", eventKind: "hook_completed", exitCode: 2 });
  });

  test("uses only fixed terminal timestamps and statuses", () => {
    const model = makeTranscriptPreviewModel();
    const turn = model.turns[0];

    expect(model.createdAt).toBe(FIXED_TIMESTAMP);
    expect(model.updatedAt).toBe(FIXED_TIMESTAMP);
    expect(turn?.startedAt).toBe(FIXED_TIMESTAMP);
    expect(turn?.completedAt).toBe(FIXED_TIMESTAMP);
    expect(turn?.status).toBe("completed");
    expect(model.activeTurnId).toBeUndefined();
    expect(model.lastFrameAt).toBe(1787680800000);
    expect(model.workMillis).toBe(2400);
    expect(turn?.durationMs).toBe(2400);
    expect(turn?.usage).toEqual({ inputTokens: 128, outputTokens: 64, totalTokens: 192 });
    expect(turn?.cost).toBe("0.0125");

    expect(JSON.stringify(model)).not.toContain("inProgress");
    expect(JSON.stringify(model)).not.toContain("active");
    expect(model.turns.flatMap((value) => value.items).every((item) => item.status === "completed")).toBe(true);
  });

  test.each(["chat", "intent", "tools", "activity", "full"] as const)(
    "projects through %s and representative advanced configurations",
    (level) => {
      const model = makeTranscriptPreviewModel();
      const config = makeTranscriptDisplayConfig(
        { kind: "preset", level },
        {
          roundTimings: true,
          tokenCounts: true,
          estimatedCost: true,
          systemEvents: true,
          promptEvents: true,
          hookExits: "all",
        },
      );
      const projection = projectThread(model, config);
      const entries = projection.turns[0]?.entries ?? [];

      expect(projection.turns).toHaveLength(1);
      expect(entries).not.toHaveLength(0);
      expect(projection.metadata).toEqual(config.advanced);
      expect(projection.anchors).toHaveLength(entries.length);
    },
  );

  test("supports a representative Custom vector without mutating the fixture", () => {
    const model = makeTranscriptPreviewModel();
    const before = makeTranscriptPreviewModel();
    const config = makeTranscriptDisplayConfig(
      {
        kind: "custom",
        toolIntent: true,
        toolCalls: false,
        reasoning: true,
        expandByDefault: true,
      },
      { systemEvents: true, promptEvents: true, hookExits: "successful" },
    );

    expect(() => projectThread(model, config)).not.toThrow();
    expect(model).toEqual(before);
  });
});
